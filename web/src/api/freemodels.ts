// FreeModels Registry — consumes /api/freemodels/registry (backend proxy
// of zhuzhuyule/FreeModels). Data is cached in a module-level Vue ref so
// any computed/watch reading via `isFree()` etc. auto-refreshes once the
// registry lands. Falls back to localStorage on cold reload, then to the
// bundled static FREE_MODELS list (in freeProviders.ts) on full miss.
import { ref } from "vue";
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

// Indexed lookup tables — rebuilt every time freeModelsRef changes.
let byProvMod: Map<string, FreeModelMeta> = new Map();
let byModelOnly: Map<string, FreeModelMeta[]> = new Map();

function rebuildIndex(env: FreeModelsEnvelope | null): void {
  byProvMod = new Map();
  byModelOnly = new Map();
  if (!env) {
    return;
  }
  for (const m of env.models) {
    const key = `${m.provider.toLowerCase()}/${m.modelId.toLowerCase()}`;
    byProvMod.set(key, m);
    const bare = m.modelId.toLowerCase();
    const list = byModelOnly.get(bare) || [];
    list.push(m);
    byModelOnly.set(bare, list);
  }
}

export function lookupRegistry(provider: string | undefined, modelId: string): FreeModelMeta | null {
  // Touch the ref so reactivity tracking kicks in for callers inside
  // computed/watch — value itself isn't used.
  void freeModelsRef.value;
  if (!modelId) {
    return null;
  }
  const lower = modelId.toLowerCase();
  if (provider) {
    const hit = byProvMod.get(`${provider.toLowerCase()}/${lower}`);
    if (hit) {
      return hit;
    }
  }
  const list = byModelOnly.get(lower);
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
  void freeModelsRef.value;
  if (!modelId) {
    return null;
  }
  const lower = modelId.toLowerCase();
  if (provider) {
    const hit = byProvMod.get(`${provider.toLowerCase()}/${lower}`);
    if (hit) {
      return hit.isFree;
    }
  }
  const list = byModelOnly.get(lower);
  if (list && list.length) {
    // 任一 provider 标 free 就视为 free
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
  freeModelsRef.value = env;
  rebuildIndex(env);
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
 */
export function loadFreeModelsRegistry(): Promise<void> {
  if (pending) {
    return pending;
  }
  loadFromStorage(); // 即时呈现 (若有);失败也无所谓
  pending = (async () => {
    try {
      const r = await http.get<FreeModelsEnvelope>("/freemodels/registry", { hideMessage: true });
      const env = (r as unknown as { data: FreeModelsEnvelope }).data;
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
