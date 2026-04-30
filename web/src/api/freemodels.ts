// FreeModels Registry — consumes /api/freemodels/registry (backend proxy
// of zhuzhuyule/FreeModels). Data is cached in a module-level Vue ref so
// any computed/watch reading via `isFree()` etc. auto-refreshes once the
// registry lands. Falls back to localStorage on cold reload, then to the
// bundled static FREE_MODELS list (in freeProviders.ts) on full miss.
import { computed, ref } from "vue";
import http from "@/utils/http";

export interface FreeModelMeta {
  provider: string;
  modelId: string;
  name: string;
  isFree: boolean;
  billingMode: string; // "free" | "trial" | "paid"
  freeTier: string; // "full" | "trial" | ""
  freeKind: string; // "permanent" | "rate-limited" | ...
  contextSize: number;
  contextLabel: string;
  tier: string; // "small" | "medium" | "large"
  speed: string; // "fast" | "balanced" | "slow"
  isReasoning: boolean;
  isMultimodal: boolean;
  hasToolUse: boolean;
  priceInput: number;
  priceOutput: number;
  modelFamily: string;
  aliases: string[]; // 跨 provider 同名,e.g. ["groq/llama-3.3-70b-versatile"]
  tags: string[];
}

export interface FreeModelsEnvelope {
  view: string;
  updatedAt: string;
  totalModels: number;
  models: FreeModelMeta[];
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
//   groq/cerebras/bigmodel/openrouter/xunfei/longcat → 加 "<provider>/" 前缀
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
  if (isKnownProviderId(head) || LOCALID_TO_BRIDGE.has(head)) {
    return idWithMaybePrefix.slice(slash + 1);
  }
  return idWithMaybePrefix;
}

// =============================================================================
// PROVIDER_BRIDGE — 上游 FreeModels 9 家 ↔ 本地 freeProviders.ts ↔ upstream hosts
// =============================================================================
// 单一可信源:每个 entry 同时把 FreeModels (上游) provider id、我们本地
// freeProviders id、以及上游 host (用于反查) 绑在一起。 expandProviderAliases /
// findFreeModelsProvider / isKnownProviderId 都从此表派生, 不再各自维护别名表。
//
// 9 家 FreeModels 项目收录的 provider, 跟我们 freeProviders.ts 一一对应:
//   bigmodel    ↔ zhipu             ↔ open.bigmodel.cn
//   cerebras    ↔ cerebras          ↔ api.cerebras.ai
//   gitee       ↔ gitee-ai          ↔ ai.gitee.com
//   google      ↔ google-aistudio   ↔ generativelanguage.googleapis.com
//   groq        ↔ groq              ↔ api.groq.com
//   longcat     ↔ longcat           ↔ api.longcat.chat
//   nvidia      ↔ nvidia-nim        ↔ integrate.api.nvidia.com
//   openrouter  ↔ openrouter        ↔ openrouter.ai
//   xunfei      ↔ xfyun             ↔ maas-api.cn-huabei-1.xf-yun.com /
//                                     spark-api-open.xf-yun.com
//
// 同名 4 家:cerebras / groq / longcat / openrouter
// 异名 5 家:bigmodel ↔ zhipu / gitee ↔ gitee-ai / google ↔ google-aistudio /
//          nvidia ↔ nvidia-nim / xunfei ↔ xfyun
interface ProviderBridge {
  /** FreeModels 项目用的 id (registry.provider 字段值) */
  fmId: string;
  /** 我们 freeProviders.ts 用的 id (group 反查命中此 id) */
  localId: string;
  /** 上游真实 hostname, 用于通过 group.upstreams[0].url 反查 fmId */
  hosts: string[];
}

const PROVIDER_BRIDGE: ProviderBridge[] = [
  { fmId: "bigmodel", localId: "zhipu", hosts: ["open.bigmodel.cn"] },
  { fmId: "cerebras", localId: "cerebras", hosts: ["api.cerebras.ai"] },
  { fmId: "gitee", localId: "gitee-ai", hosts: ["ai.gitee.com"] },
  { fmId: "google", localId: "google-aistudio", hosts: ["generativelanguage.googleapis.com"] },
  { fmId: "groq", localId: "groq", hosts: ["api.groq.com"] },
  { fmId: "longcat", localId: "longcat", hosts: ["api.longcat.chat"] },
  { fmId: "nvidia", localId: "nvidia-nim", hosts: ["integrate.api.nvidia.com", "api.nvcf.nvidia.com"] },
  { fmId: "openrouter", localId: "openrouter", hosts: ["openrouter.ai"] },
  { fmId: "xunfei", localId: "xfyun", hosts: ["maas-api.cn-huabei-1.xf-yun.com", "spark-api-open.xf-yun.com"] },
];

const FMID_TO_BRIDGE = new Map(PROVIDER_BRIDGE.map(b => [b.fmId.toLowerCase(), b]));
const LOCALID_TO_BRIDGE = new Map(PROVIDER_BRIDGE.map(b => [b.localId.toLowerCase(), b]));

function isKnownProviderId(s: string): boolean {
  return FMID_TO_BRIDGE.has(s.toLowerCase());
}

/** 通过 hostname 反查 FreeModels provider 桥接信息. host 大小写不敏感, 子串匹配. */
export function findBridgeByHost(host: string | undefined): ProviderBridge | null {
  if (!host) {
    return null;
  }
  const lower = host.toLowerCase();
  for (const b of PROVIDER_BRIDGE) {
    if (b.hosts.some(h => lower.includes(h))) {
      return b;
    }
  }
  return null;
}

/** 通过 provider id (任意一边) 反查桥接信息. */
export function findBridgeById(providerId: string | undefined): ProviderBridge | null {
  if (!providerId) {
    return null;
  }
  const lower = providerId.toLowerCase();
  return FMID_TO_BRIDGE.get(lower) || LOCALID_TO_BRIDGE.get(lower) || null;
}

/**
 * 把任意 provider id 展开成它跨命名空间的所有候选 (含自身), 用于 lookup 时
 * 同时尝试 FreeModels id 和本地 id. 不在桥接表里的 (e.g. together / mistral)
 * 返回 [自身].
 */
export function expandProviderAliases(providerId: string): string[] {
  const lower = providerId.toLowerCase();
  const bridge = findBridgeById(lower);
  if (bridge) {
    return [bridge.fmId, bridge.localId];
  }
  return [lower];
}

// Indexed lookup tables — 必须是 Vue computed 才能让所有 caller (render / watch /
// 其它 computed) 在 freeModelsRef 改变时正确重算。 用 module-level mutable Map
// 时 Vue 看不到 Map 内容变化, 即使触发了 ref 依赖也用的是旧索引。
// byProvMod:  "<freemodels-provider>/<bareId>" 和 "<aliased-provider>/<bareId>"
// byBareModel: 裸 modelId → meta list (不知 provider 时回退)
const indexCache = computed(() => {
  const byProvMod = new Map<string, FreeModelMeta>();
  const byBareModel = new Map<string, FreeModelMeta[]>();
  const env = freeModelsRef.value;
  if (!env || !env.models) {
    return { byProvMod, byBareModel };
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
  return { byProvMod, byBareModel };
});

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
