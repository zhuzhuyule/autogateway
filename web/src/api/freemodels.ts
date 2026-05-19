// FreeModels Registry — consumes /api/freemodels/registry (backend proxy
// of zhuzhuyule/FreeModels). Data is cached in a module-level Vue ref so
// any computed/watch reading via `isFree()` etc. auto-refreshes once the
// registry lands. Falls back to localStorage on cold reload, then to the
// bundled static FREE_MODELS list (in freeProviders.ts) on full miss.
import { computed, ref } from "vue";
import http from "@/utils/http";

// FreeQuotaMeta 来自上游 free_quota 字段 ({notes: string} | null).
// 缺时 undefined; 含值时至少 notes 非空.
export interface FreeQuotaMeta {
  notes?: string;
}

export interface FreeModelMeta {
  provider: string;
  modelId: string;
  name: string;
  /** 短描述, e.g. "语言模型". 645/697 模型有, 缺时空串. */
  description?: string;
  /** Unix ts, 上游模型创建时间. 排序 / "新" 标签用. */
  created?: number;
  isFree: boolean;
  billingMode: string; // "free" | "trial" | "paid"
  freeTier: string; // "full" | "trial" | ""
  freeKind: string; // "permanent" | "rate-limited" | ...
  /** 上游 2026-05 起 free_quota 改为 dict 形式. */
  freeQuota?: FreeQuotaMeta;
  contextSize: number;
  contextLabel: string;
  tier: string; // "small" | "medium" | "large"
  speed: string; // "fast" | "standard" | "premium"
  /** 上游 2026-05 起新增. "entry" | "mid" | "high". 697/697 覆盖. */
  performanceLevel?: string;
  /** "< 1s" | "1-3s" | "3-10s". 697/697 覆盖. */
  estimatedLatency?: string;
  isReasoning: boolean;
  isMultimodal: boolean;
  hasToolUse: boolean;
  /**
   * 能力枚举: "chat" / "text-generation" / "tool-use" / "vision" / "reasoning"
   * / "code-generation" / "function-calling" / "embeddings" / "rerank" / 等
   * 29 种. 比 isReasoning / isMultimodal / hasToolUse 三个 bool 表达力强;
   * 路由器决策建议直接读这里.
   */
  capabilities?: string[];
  /** "conversation" / "q&a" / "code-completion" / "math" / 等. 30 种. */
  useCase?: string[];
  /** 上游给出的参数量 (e.g. 8e9 表示 8B). 261/697 覆盖. */
  parameterCount?: number;
  modelVariant?: string;
  quantization?: string;
  priceInput: number;
  priceOutput: number;
  modelFamily: string;
  aliases: string[]; // 跨 provider 同名,e.g. ["groq/llama-3.3-70b-versatile"]
  tags: string[];
}

/**
 * Provider 元数据 — 跟模型列表分开存, per-provider 唯一字段都聚在这里.
 * 让 api-center 能从 FreeModels 直接拿 baseUrl / channelType, 不再在
 * 本地 freeProviders.ts 重复维护 (本地表降级为 fallback).
 */
export interface FreeProviderMeta {
  name: string;
  displayName: string;
  website?: string;
  logoUrl?: string;
  /** 推荐 API base URL, 派生 host 用于反查关联用户分组 upstream. */
  apiBaseUrl?: string;
  /** 决定下游网关用哪种 channel 适配. */
  channelType?: "openai" | "anthropic" | "gemini";
  priceCurrency?: "USD" | "CNY";
  priceUnit?: "per_million_tokens";
}

export interface FreeModelsEnvelope {
  view: string;
  updatedAt: string;
  totalModels: number;
  models: FreeModelMeta[];
  /** 后端 2026-05 起会下发, 旧 cache 没有此字段时降级走本地 fallback. */
  providerMeta?: Record<string, FreeProviderMeta>;
}

const STORAGE_KEY = "freemodels-registry-v1";
const STORAGE_TTL_MS = 24 * 60 * 60 * 1000; // 24h — backend refreshes every 6h, this is just a cold-start helper

interface CachedEnvelope {
  envelope: FreeModelsEnvelope;
  fetchedAt: number;
}

// Reactive store. Components reading it via computed will re-run when the
// envelope is replaced (i.e. after the network fetch completes).
export const freeModelsRef = ref<FreeModelsEnvelope | null>(null);

// FreeModels CDN 的 modelId 格式不统一:
//   groq/cerebras/bigmodel/openrouter/xinghuo/xingchen/longcat → 加 "<provider>/" 前缀
//     groq/minimax-m2.5, bigmodel/glm-4-flash, groq/qwen/qwen3-vl-32b
//   gitee/nvidia/google → 直接用上游 raw modelId, 不加前缀
//     jina-clip-v1 (gitee), bytedance/seed-oss-36b-instruct (nvidia 上托管的 ByteDance 模型)
//
// 而我们 group.available_models 是上游 /v1/models 真实返回的裸 id, 总不带 provider 前缀.
// 对齐方法: 只在 modelId 第一段 === provider 名时剥, 否则保留原样.
//   - "groq/minimax-m2.5"          + provider="groq"   → "minimax-m2.5"           (剥)
//   - "groq/qwen/qwen3-vl-32b"     + provider="groq"   → "qwen/qwen3-vl-32b"      (剥, 保留作者前缀)
//   - "bytedance/seed-oss-36b-instruct" + provider="nvidia" → 不剥 (第一段 != "nvidia")
//   - "jina-clip-v1"               + provider="gitee"  → "jina-clip-v1"           (无 /)
function bareModelId(idWithMaybePrefix: string, provider?: string): string {
  const slash = idWithMaybePrefix.indexOf("/");
  if (slash <= 0) {
    return idWithMaybePrefix;
  }
  const head = idWithMaybePrefix.slice(0, slash).toLowerCase();
  // 仅当首段等于 provider 名 (含 alias) 时才视为前缀
  if (provider) {
    const providerAliases = expandProviderAliases(provider);
    if (providerAliases.includes(head)) {
      return idWithMaybePrefix.slice(slash + 1);
    }
    return idWithMaybePrefix; // 首段是模型作者名 (e.g. "bytedance/", "google/") — 保留
  }
  // 不知 provider, 保守:仅当首段命中桥接表时剥
  if (isKnownProviderId(head)) {
    return idWithMaybePrefix.slice(slash + 1);
  }
  return idWithMaybePrefix;
}

// =============================================================================
// 10 家 FreeModels 上游 provider id, 跟本地 freeProviders.ts 已统一为同名:
// bigmodel / cerebras / gitee / google / groq / longcat / nvidia / openrouter /
// xinghuo (讯飞星火 Spark API) / xingchen (讯飞星辰 MaaS).
// 因此不需要别名映射表,id 直通即可.
// =============================================================================
const KNOWN_FREEMODELS_PROVIDERS = new Set([
  "bigmodel",
  "cerebras",
  "gitee",
  "google",
  "groq",
  "longcat",
  "nvidia",
  "openrouter",
  "xinghuo",
  "xingchen",
]);
function isKnownProviderId(s: string): boolean {
  return KNOWN_FREEMODELS_PROVIDERS.has(s.toLowerCase());
}

/**
 * Provider ID 对齐 — registry 与本地 freeProviders.ts 的 ID 不完全一致时,
 * 通过别名让两边互查能命中. byProvMod 索引时会把每个 alias 都注册一份.
 *
 * 已知不一致 (2026-05-19 ofind schema):
 *   - registry 'github' ↔ 本地 'github-models' (Azure 托管的 GitHub Models)
 *
 * 其他 ID (groq/cerebras/openrouter/...) 双方已对齐, alias = [自身].
 */
const PROVIDER_ALIAS_PAIRS: Array<[string, string]> = [["github", "github-models"]];
const PROVIDER_ALIAS_MAP: Record<string, string[]> = (() => {
  const out: Record<string, string[]> = {};
  for (const [a, b] of PROVIDER_ALIAS_PAIRS) {
    out[a] = [a, b];
    out[b] = [b, a];
  }
  return out;
})();

export function expandProviderAliases(providerId: string): string[] {
  const key = providerId.toLowerCase();
  return PROVIDER_ALIAS_MAP[key] || [key];
}

// Indexed lookup tables — 必须是 Vue computed 才能让所有 caller (render / watch /
// 其它 computed) 在 freeModelsRef 改变时正确重算。 用 module-level mutable Map
// 时 Vue 看不到 Map 内容变化, 即使触发了 ref 依赖也用的是旧索引。
// byProvMod:  "<freemodels-provider>/<bareId>" 和 "<aliased-provider>/<bareId>"
// byBareModel: 裸 modelId → meta list (不知 provider 时回退)
// byHost:     host (lowercase) → providerId, 从 providerMeta.apiBaseUrl 派生
const indexCache = computed(() => {
  const byProvMod = new Map<string, FreeModelMeta>();
  const byBareModel = new Map<string, FreeModelMeta[]>();
  const byHost = new Map<string, string>();
  const env = freeModelsRef.value;
  if (!env || !env.models) {
    return { byProvMod, byBareModel, byHost };
  }
  for (const m of env.models) {
    const bare = bareModelId(m.modelId, m.provider).toLowerCase();
    for (const alias of expandProviderAliases(m.provider)) {
      byProvMod.set(`${alias}/${bare}`, m);
    }
    byProvMod.set(m.modelId.toLowerCase(), m);

    if (!byBareModel.has(bare)) {
      byBareModel.set(bare, []);
    }
    byBareModel.get(bare)!.push(m);
  }
  if (env.providerMeta) {
    for (const [providerId, meta] of Object.entries(env.providerMeta)) {
      const host = extractHostFromUrl(meta.apiBaseUrl);
      if (host) {
        byHost.set(host, providerId);
      }
    }
  }
  return { byProvMod, byBareModel, byHost };
});

/** 从 URL 提 host (lowercase). 解析失败返回 "". */
function extractHostFromUrl(url: string | undefined): string {
  if (!url) {
    return "";
  }
  try {
    return new URL(url).host.toLowerCase();
  } catch {
    return "";
  }
}

/**
 * 用 host (e.g. "api.groq.com") 反查 FreeModels providerId (e.g. "groq").
 * 让 freeProviders.ts.findProviderByUpstreamUrl 能优先走 registry,
 * 不必在本地表持续维护 upstreamHosts[].
 *
 * 数据未到位时返回 undefined, caller 走本地 fallback.
 */
export function lookupProviderIdByHost(host: string): string | undefined {
  if (!host) {
    return undefined;
  }
  const { byHost } = indexCache.value;
  if (!freeModelsRef.value) {
    void loadFreeModelsRegistry();
  }
  return byHost.get(host.toLowerCase());
}

/** 拿到 provider 的推荐 API base URL (建组流程默认填充 / 展示用). */
export function getProviderApiBaseUrl(providerId: string): string | undefined {
  if (!providerId) {
    return undefined;
  }
  const env = freeModelsRef.value;
  return env?.providerMeta?.[providerId]?.apiBaseUrl;
}

/** 拿 provider 的完整元数据 (channelType / displayName / etc.). */
export function getProviderMeta(providerId: string): FreeProviderMeta | undefined {
  if (!providerId) {
    return undefined;
  }
  const env = freeModelsRef.value;
  return env?.providerMeta?.[providerId];
}

export function lookupRegistry(provider: string | undefined, modelId: string): FreeModelMeta | null {
  if (!modelId) {
    return null;
  }
  const { byProvMod, byBareModel } = indexCache.value;
  // 懒加载: 如果 registry 还没填, 触发一次 fetch (不阻塞当前调用,
  // reactive computed 数据到位后自动重算).
  if (!freeModelsRef.value) {
    void loadFreeModelsRegistry();
  }
  const bare = bareModelId(modelId, provider).toLowerCase();
  if (provider) {
    for (const alias of expandProviderAliases(provider)) {
      const hit = byProvMod.get(`${alias}/${bare}`);
      if (hit) {
        return hit;
      }
    }
  }
  const direct = byProvMod.get(modelId.toLowerCase());
  if (direct) {
    return direct;
  }
  const list = byBareModel.get(bare);
  if (list && list.length) {
    return list[0];
  }
  return null;
}

/**
 * 三态返回:
 *   true   — 注册表标记为免费
 *   false  — 注册表标记为付费 (billingMode="paid")
 *   null   — 注册表无信息,调用方 fallback 静态清单或保持未知
 */
export function isFreeFromRegistry(provider: string | undefined, modelId: string): boolean | null {
  if (!modelId) {
    return null;
  }
  const { byProvMod, byBareModel } = indexCache.value;
  if (!freeModelsRef.value) {
    void loadFreeModelsRegistry();
  }
  const bare = bareModelId(modelId, provider).toLowerCase();
  if (provider) {
    for (const alias of expandProviderAliases(provider)) {
      const hit = byProvMod.get(`${alias}/${bare}`);
      if (hit) {
        return hit.isFree;
      }
    }
  }
  const list = byBareModel.get(bare);
  if (list && list.length) {
    if (list.some(m => m.isFree)) {
      return true;
    }
    return false;
  }
  return null;
}

/**
 * 三态免费身份(细化 isFree):
 *   "full"  — 完全免费 (freeTier=full, e.g. openrouter :free, gitee 完全免费)
 *   "trial" — 体验模式 (freeTier=trial, 目前主要是 gitee 145 个体验模型)
 *   "paid"  — 注册表确定付费
 *   null    — 注册表未收录,fallback 上层判断
 */
export function getFreeStatus(
  provider: string | undefined,
  modelId: string
): "full" | "trial" | "paid" | null {
  const meta = lookupRegistry(provider, modelId);
  if (!meta) {
    return null;
  }
  if (!meta.isFree) {
    return "paid";
  }
  return meta.freeTier === "trial" ? "trial" : "full";
}

/** registry 直出 speed (fast/balanced/slow), 找不到返回 null. */
export function getModelSpeed(
  provider: string | undefined,
  modelId: string
): "fast" | "balanced" | "slow" | null {
  const meta = lookupRegistry(provider, modelId);
  if (!meta || !meta.speed) {
    return null;
  }
  if (meta.speed === "fast" || meta.speed === "balanced" || meta.speed === "slow") {
    return meta.speed;
  }
  return null;
}

/** registry 直出 tier (small/medium/large), 找不到返回 null. */
export function getModelTier(
  provider: string | undefined,
  modelId: string
): "small" | "medium" | "large" | null {
  const meta = lookupRegistry(provider, modelId);
  if (!meta || !meta.tier) {
    return null;
  }
  if (meta.tier === "small" || meta.tier === "medium" || meta.tier === "large") {
    return meta.tier;
  }
  return null;
}

function setEnvelope(env: FreeModelsEnvelope): void {
  // 索引由 indexCache (computed) 自动维护, 改 freeModelsRef 即可。
  freeModelsRef.value = env;
}

function loadFromStorage(): boolean {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) {
      return false;
    }
    const cached: CachedEnvelope = JSON.parse(raw);
    if (!cached.envelope || Date.now() - cached.fetchedAt > STORAGE_TTL_MS) {
      return false;
    }
    setEnvelope(cached.envelope);
    return true;
  } catch {
    return false;
  }
}

function saveToStorage(env: FreeModelsEnvelope): void {
  try {
    const payload: CachedEnvelope = { envelope: env, fetchedAt: Date.now() };
    localStorage.setItem(STORAGE_KEY, JSON.stringify(payload));
  } catch {
    /* quota / private mode — ignore */
  }
}

let pending: Promise<void> | null = null;

/**
 * 启动时调用一次。先用 localStorage 给一个即时值,再异步从后端拉最新覆盖。
 * 重复调用会复用同一个 in-flight promise。
 *
 * 后端 system setting `use_freemodels_registry` (默认 true) 控制总开关:
 * 关闭时清空 registry, isFree() 等查询自然降级到本地静态清单 / /api/models.
 */
export function loadFreeModelsRegistry(): Promise<void> {
  if (pending) {
    return pending;
  }
  // 先看 settings 开关
  const enabled = localStorage.getItem("freemodels_registry_enabled");
  if (enabled === "false") {
    // 用户/管理员关掉了,清空 registry 让所有 reactive 依赖回退
    setEnvelope({ view: "free", updatedAt: "", totalModels: 0, models: [] });
    return Promise.resolve();
  }
  loadFromStorage();
  pending = (async () => {
    try {
      // 后端 freemodels_handler.go 直接 c.JSON(snap), 没有 {code,message,data} 信封,
      // 所以 axios interceptor 拿到的 response.data 就是 envelope 本身.
      const r = await http.get<FreeModelsEnvelope>("/freemodels/registry", { hideMessage: true });
      const env = r as unknown as FreeModelsEnvelope;
      if (env && Array.isArray(env.models)) {
        setEnvelope(env);
        saveToStorage(env);
      }
    } catch {
      // backend may be down or registry not yet populated; keep storage
      // value (if any) and fall back to static lists upstream
    } finally {
      pending = null;
    }
  })();
  return pending;
}

/** 设置开关并立即生效. 通常 Settings 页面在 saveSettings 后调用. */
export function setFreeModelsRegistryEnabled(enabled: boolean): void {
  localStorage.setItem("freemodels_registry_enabled", enabled ? "true" : "false");
  if (!enabled) {
    setEnvelope({ view: "free", updatedAt: "", totalModels: 0, models: [] });
    try {
      localStorage.removeItem(STORAGE_KEY);
    } catch {
      /* ignore */
    }
  } else {
    pending = null; // 强制下次 loadFreeModelsRegistry 重新拉
    loadFreeModelsRegistry();
  }
}
