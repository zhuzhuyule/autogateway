// 别名页的数据层: 加载 + 派生, 不含任何渲染。
//
// 为什么在模块作用域持有 ref 而不是在 setup 里: 别名数据有两个入口(别名页、
// 密钥页的模型卡), 两边看到的必须是同一份状态 —— 一处改完另一处不该等到刷新。
// 仓库没有 pinia, services/ 下 auth.ts / version.ts 已经是同一套 module-level
// ref 的做法, 这里跟着走。
import { computed, ref } from "vue";
import {
  aliasesApi,
  RESERVED_ALIASES,
  routingSettingsApi,
  type AliasSuggestion,
  type ModelAliasRow,
  type RoutingSettings,
} from "@/api/aliases";
import {
  getModelTimings,
  getModelTraffic,
  type ModelTiming,
  type ModelTrafficRow,
} from "@/api/dashboard";
import { keysApi } from "@/api/keys";
import type { Group } from "@/types/models";
import type {
  AliasView,
  AutoTier,
  CandidateRowView,
  CandidateState,
} from "@/components/aliases/types";
import { getGroupDisplayName } from "@/utils/display";

/** 新建候选的默认值 —— 所有入口(别名页 / 密钥页弹窗)共用, 不要再各写一份。 */
const DEFAULT_WEIGHT = 100;
/** 与后端 createOnDB 的 nonZero(req.Priority, 100) 对齐: 这里写 0 会被替换成 100, UI 与库里不一致。 */
const DEFAULT_PRIORITY = 100;

const loading = ref(false);
const rows = ref<ModelAliasRow[]>([]);
const groups = ref<Group[]>([]);
const settings = ref<RoutingSettings>({
  Enabled: true,
  SimpleThreshold: 2000,
  ComplexThreshold: 8000,
});
/** 按**请求名**索引 —— 别名路由下请求名就是别名, 这才是别名级指标的正确键。 */
const timingsByName = ref<Record<string, ModelTiming>>({});
/** `${group_id}::${请求名}` -> 24h 调用数。 */
const trafficByKey = ref<Record<string, number>>({});
/** 一次 refresh 里 traffic 是否真的取到了(取不到时整列实测占比要隐藏)。 */
const trafficAvailable = ref(false);

export interface GroupInfo {
  mode: string;
  exposed: Set<string>;
  blocked: Set<string>;
  channelType: string;
}

/**
 * 解析 datatypes.JSON 列(可能是真数组, 也可能是 JSON 字符串)。
 *
 * 全应用只此一份: 之前在别名页写了 parseModelArray + 一处内联重复, 三个视图各有
 * 一套, 于是同一个字段在不同页面被解析出不同结果。
 */
export function parseStringList(raw: unknown): string[] {
  if (Array.isArray(raw)) {
    return raw.filter((m): m is string => typeof m === "string");
  }
  if (typeof raw === "string" && raw.trim()) {
    try {
      const j = JSON.parse(raw);
      if (Array.isArray(j)) {
        return j.filter((m): m is string => typeof m === "string");
      }
    } catch {
      /* 老数据里存在非法 JSON, 按空处理 */
    }
  }
  return [];
}

const groupInfoById = computed<Record<number, GroupInfo>>(() => {
  const out: Record<number, GroupInfo> = {};
  for (const g of groups.value) {
    if (!g.id) {
      continue;
    }
    out[g.id] = {
      mode: g.model_routing_mode || "passthrough",
      exposed: new Set(parseStringList(g.exposed_models)),
      blocked: new Set(parseStringList(g.blocked_models)),
      channelType: g.channel_type || "",
    };
  }
  return out;
});

const groupNameById = computed<Record<number, string>>(() => {
  const m: Record<number, string> = {};
  for (const g of groups.value) {
    if (g.id) {
      m[g.id] = getGroupDisplayName(g);
    }
  }
  return m;
});

// 候选模型列表的口径必须与"模型"页一致, 否则同一个分组在两处给出不同列表:
// specified → exposed_models(为空时降级用 available), passthrough → available_models。
const modelsByGroup = computed<Record<number, string[]>>(() => {
  const out: Record<number, string[]> = {};
  for (const g of groups.value) {
    if (!g.id) {
      continue;
    }
    const info = groupInfoById.value[g.id];
    const available = parseStringList(g.available_models);
    let src = info.mode === "specified" ? [...info.exposed] : available;
    if (info.mode === "specified" && src.length === 0) {
      src = available;
    }
    out[g.id] = src;
  }
  return out;
});

function candidateState(row: ModelAliasRow): CandidateState {
  // 保留别名的占位行(group_id=0)不是候选。
  if (row.is_reserved && row.group_id === 0) {
    return "usable";
  }
  if (!row.enabled) {
    return "disabled";
  }
  const info = groupInfoById.value[row.group_id];
  // blocked_models 命中时无视 routing mode 直接拒绝(见 router_engine.filterByExposed)。
  if (info?.blocked.has(row.real_model)) {
    return "blocked";
  }
  if (info && info.mode === "specified" && !info.exposed.has(row.real_model)) {
    return "unexposed";
  }
  return "usable";
}

function tierOf(alias: string): AutoTier {
  return (RESERVED_ALIASES as readonly string[]).includes(alias) ? (alias as AutoTier) : "";
}

/** 成本 chip 文案: 有成本显示折算价格, 免费但有用量显示 token 数, 否则空。 */
function costChip(t?: ModelTiming): string {
  if (!t) {
    return "";
  }
  if (t.cost_usd > 0) {
    return t.cost_usd < 1 ? `$${t.cost_usd.toFixed(4)}` : `$${t.cost_usd.toFixed(2)}`;
  }
  if (t.tokens > 0) {
    const tok = t.tokens >= 1000 ? `${(t.tokens / 1000).toFixed(1)}K` : `${t.tokens}`;
    return `${tok} tok`;
  }
  return "";
}

const aliasViews = computed<AliasView[]>(() => {
  const byName = new Map<string, ModelAliasRow[]>();
  const reserved = new Map<string, boolean>();
  for (const r of rows.value) {
    const list = byName.get(r.alias) || [];
    // 占位行不是候选, 但它的存在让空档位在列表里可见。
    if (!(r.is_reserved && r.group_id === 0)) {
      list.push(r);
    }
    byName.set(r.alias, list);
    reserved.set(r.alias, reserved.get(r.alias) || false || r.is_reserved);
  }
  for (const name of RESERVED_ALIASES) {
    if (!byName.has(name)) {
      byName.set(name, []);
      reserved.set(name, true);
    }
  }

  // 同一 (group, model) 出现在多个档位 → 跨档位复用, 要显示出来而不是藏起来。
  const tierKeys = new Map<string, Set<string>>();
  for (const [alias, list] of byName.entries()) {
    if (!tierOf(alias)) {
      continue;
    }
    tierKeys.set(alias, new Set(list.map(r => `${r.group_id}:${r.real_model}`)));
  }

  const out: AliasView[] = [];
  for (const [alias, members] of byName.entries()) {
    const weightTotal = members.reduce((s, m) => s + Math.max(m.weight, 0), 0);
    const groupHits: Record<number, number> = {};
    for (const m of members) {
      groupHits[m.group_id] = (groupHits[m.group_id] || 0) + 1;
    }
    const isTier = !!tierOf(alias);
    let crossTier = false;
    for (const m of members) {
      for (const [otherTier, keys] of tierKeys) {
        // 只在档位别名之间判定"跨档位复用": 自定义别名与档位共用一条候选是
        // 常态, 不是需要提醒的问题。
        if (isTier && otherTier !== alias && keys.has(`${m.group_id}:${m.real_model}`)) {
          crossTier = true;
        }
      }
    }
    const timing = timingsByName.value[alias];
    const derived: CandidateRowView[] = members.map(m => {
      const info = groupInfoById.value[m.group_id];
      const name = groupNameById.value[m.group_id] || String(m.group_id);
      const channel = info?.channelType || "";
      return {
        row: m,
        groupName: name,
        providerLabel: channel || name,
        providerIsFallback: !channel,
        avgMs: timing?.avg_ms || 0,
        cost: costChip(timing),
        // 配置占比按 weight 归一化。SWRR 只关心比例, 所以 100/1/1 与 98/1/1 等价,
        // 直接展示百分比比展示裸 weight 好读。
        share:
          weightTotal > 0
            ? Math.round((Math.max(m.weight, 0) / weightTotal) * 100)
            : Math.round(100 / Math.max(1, members.length)),
        calls: trafficByKey.value[`${m.group_id}::${alias}`] || 0,
        callsSharedGroup: (groupHits[m.group_id] || 0) > 1,
        state: candidateState(m),
      };
    });
    const usable = derived.filter(r => r.state === "usable").length;
    let problem: AliasView["problem"] = "";
    if (!derived.length) {
      problem = "no-candidates";
    } else if (derived.some(r => r.state === "unexposed")) {
      problem = "unexposed";
    } else if (derived.some(r => r.state === "blocked")) {
      problem = "blocked";
    } else if (derived.some(r => r.state === "disabled")) {
      problem = "disabled";
    }
    out.push({
      alias,
      isReserved: reserved.get(alias) || false,
      tier: tierOf(alias),
      // 可用在前, 同状态按模型名 —— 排障时想看的总是坏的那些, 但它们不该把列表撑乱。
      rows: derived.sort((a, b) =>
        a.state === b.state
          ? a.row.real_model.localeCompare(b.row.real_model)
          : a.state === "usable"
            ? -1
            : 1
      ),
      usable,
      unusable: derived.length - usable,
      total: derived.length,
      calls: timing?.calls || 0,
      errors: timing?.errors || 0,
      errorRate: timing?.error_rate || 0,
      avgMs: timing?.avg_ms || 0,
      costUsd: timing?.cost_usd || 0,
      crossTier,
      problem,
    });
  }
  return out.sort((a, b) => {
    const ai = RESERVED_ALIASES.indexOf(a.tier as (typeof RESERVED_ALIASES)[number]);
    const bi = RESERVED_ALIASES.indexOf(b.tier as (typeof RESERVED_ALIASES)[number]);
    if (ai !== -1 && bi !== -1) {
      return ai - bi;
    }
    if (ai !== -1) {
      return -1;
    }
    if (bi !== -1) {
      return 1;
    }
    return b.calls - a.calls || a.alias.localeCompare(b.alias);
  });
});

const totalCandidates = computed(() => aliasViews.value.reduce((s, a) => s + a.total, 0));

// === 建议(logs-driven + registry-driven) ===
const suggestions = ref<AliasSuggestion[]>([]);
// P8.4: 单条 dismiss, localStorage 持久化避免每次进页面又出现。
const dismissedFamilies = ref<Set<string>>(loadDismissedFamilies());

function loadDismissedFamilies(): Set<string> {
  try {
    const raw = localStorage.getItem("alias-suggest-dismissed-families");
    return new Set(raw ? (JSON.parse(raw) as string[]) : []);
  } catch {
    return new Set();
  }
}

const visibleSuggestions = computed(() =>
  // family 类型按 dismissedFamilies 过滤; single 类型按 model 名过滤(复用同套)。
  suggestions.value.filter(s => {
    const key = s.kind === "family" ? s.family : s.model;
    return key && !dismissedFamilies.value.has(key);
  })
);
const suggestionCount = computed(() => visibleSuggestions.value.length);

function dismissSuggestion(key: string): void {
  if (!key) {
    return;
  }
  const s = new Set(dismissedFamilies.value);
  s.add(key);
  dismissedFamilies.value = s;
  try {
    localStorage.setItem(
      "alias-suggest-dismissed-families",
      JSON.stringify(Array.from(dismissedFamilies.value))
    );
  } catch {
    /* 隐私模式下的 localStorage 写入失败不该影响功能 */
  }
}

// === 建候选的唯一入口 ===
//
// 集中在一处, 是因为建一行候选有两个副作用必须同时发生 —— 分开写就会漂移,
// 而且这个漂移已经真实发生过:
//   1. 用统一的默认 weight/priority 建行。picker 曾传 weight=100、family 建议
//      传 weight=1, 同一件事两条路径落库的权重不同。
//   2. specified 模式的分组要把 real_model 补进 exposed_models。
//      router_engine.filterByExposed 只放行暴露过的模型, 漏了这步建出来的别名
//      是"静默失效"状态 —— 卡片上标"失效", 但不报错, 请求会被跳过。
//      之前只有 family 建议路径做了这步, picker 路径没做。
//
// 返回 ok/fail 供调用方决定提示文案; 已存在的 (group, model) 直接跳过,
// 不发注定 409 的请求。
export async function createAliasCandidates(
  alias: string,
  picks: Array<{ groupId: number; modelId: string }>
): Promise<{ ok: number; fail: number }> {
  if (!alias || !picks.length) {
    return { ok: 0, fail: 0 };
  }
  // 密钥页的模型卡可以在没进过别名页的情况下直接建候选 —— 那时 groups 还是空的,
  // 下面的 exposed 补丁会静默跳过, 建出来的别名就是"失效"状态。先补一次加载。
  if (!groups.value.length) {
    await loadCore().catch(() => null);
  }
  const existing = new Set(
    rows.value.filter(r => r.alias === alias).map(r => `${r.group_id}:${r.real_model}`)
  );
  const todo = picks.filter(p => !existing.has(`${p.groupId}:${p.modelId}`));
  if (!todo.length) {
    return { ok: 0, fail: 0 };
  }

  let ok = 0;
  let fail = 0;
  for (const p of todo) {
    try {
      await aliasesApi.create({
        alias,
        group_id: p.groupId,
        real_model: p.modelId,
        weight: DEFAULT_WEIGHT,
        priority: DEFAULT_PRIORITY,
        enabled: true,
      });
      ok += 1;
    } catch {
      fail += 1;
    }
  }

  // 按 groupId 聚合暴露补丁, 避免同一分组被 PUT 多次。
  const exposureAdds = new Map<number, string[]>();
  for (const p of todo) {
    const info = groupInfoById.value[p.groupId];
    if (!info || info.mode !== "specified") {
      continue;
    }
    if (info.exposed.has(p.modelId)) {
      continue;
    }
    const arr = exposureAdds.get(p.groupId) || [];
    if (!arr.includes(p.modelId)) {
      arr.push(p.modelId);
    }
    exposureAdds.set(p.groupId, arr);
  }
  await Promise.all(
    Array.from(exposureAdds.entries()).map(([gid, models]) => {
      const info = groupInfoById.value[gid];
      const next = [...Array.from(info.exposed), ...models];
      return keysApi.updateGroup(gid, { exposed_models: next } as Partial<Group>).catch(() => null);
    })
  );

  return { ok, fail };
}

async function loadSuggestions(): Promise<void> {
  try {
    // 同时拉两个数据源: 现有 logs-driven (reactive) + P4.2 registry-driven
    // (proactive 主动建议跨 sub-group family)。所有 aggregate group 都查一遍,
    // 合并去重 (按 family 名)。
    const [logsRes, aggResp] = await Promise.all([
      aliasesApi.suggestions().catch(() => ({ data: [] })),
      (async () => {
        try {
          const http = (await import("@/utils/http")).default;
          const resp = await http.get<Array<{ id: number; group_type?: string }>>("/groups");
          const grps =
            (resp as unknown as { data: Array<{ id: number; group_type?: string }> | null }).data ||
            [];
          const aggregates = grps.filter(g => g.group_type === "aggregate");
          const calls = aggregates.map(g =>
            aliasesApi.suggestionsFromRegistry(g.id).catch(() => ({ data: [] }))
          );
          const results = await Promise.all(calls);
          const merged: AliasSuggestion[] = [];
          for (const r of results) {
            const arr = (r as unknown as { data: AliasSuggestion[] | null }).data || [];
            merged.push(...arr);
          }
          return { data: merged };
        } catch {
          return { data: [] };
        }
      })(),
    ]);
    const logsArr = (logsRes as unknown as { data: AliasSuggestion[] | null }).data || [];
    const aggArr = (aggResp as unknown as { data: AliasSuggestion[] | null }).data || [];
    // 按 family 名 dedupe (logs-driven 优先, 因为有 last_seen / count 信息更丰富)
    const seenFamilies = new Set<string>();
    for (const s of logsArr) {
      if (s.kind === "family" && s.family) {
        seenFamilies.add(s.family.toLowerCase());
      }
    }
    const out = [...logsArr];
    for (const s of aggArr) {
      if (s.kind === "family" && s.family && !seenFamilies.has(s.family.toLowerCase())) {
        out.push(s);
        seenFamilies.add(s.family.toLowerCase());
      }
    }
    suggestions.value = out;
  } catch {
    suggestions.value = [];
  }
}

async function loadCore(): Promise<void> {
  const [r, g, s] = await Promise.all([
    aliasesApi.list(),
    keysApi.getGroups(),
    routingSettingsApi.get(),
  ]);
  rows.value = (r as unknown as { data: ModelAliasRow[] }).data || [];
  groups.value = (g || []).filter(gr => gr.id);
  settings.value = (s as unknown as { data: RoutingSettings }).data || settings.value;
}

async function loadMetrics(): Promise<void> {
  try {
    const res = await getModelTimings("24h");
    const list = (res as unknown as { data: ModelTiming[] }).data || [];
    const map: Record<string, ModelTiming> = {};
    for (const t of list) {
      if (t?.model) {
        map[t.model] = t;
      }
    }
    timingsByName.value = map;
  } catch {
    /* 指标是装饰性的 —— 取不到就不显示, 不影响主流程 */
  }
  try {
    const res = await getModelTraffic("24h");
    const list = (res as unknown as { data: ModelTrafficRow[] }).data || [];
    // 日志里存的是分组**原始 name**(keypool/validator.go 里 GroupName: group.Name),
    // UI 显示的是 display name, 先映射一次, 否则两个名字体系对不上、实测永远为空。
    const nameToId: Record<string, number> = {};
    for (const g of groups.value) {
      if (g.id && g.name) {
        nameToId[g.name] = g.id;
      }
    }
    const map: Record<string, number> = {};
    for (const t of list) {
      const gid = nameToId[t.group_name];
      if (gid) {
        map[`${gid}::${t.model}`] = t.calls;
      }
    }
    trafficByKey.value = map;
    trafficAvailable.value = list.length > 0;
  } catch {
    trafficAvailable.value = false;
  }
}

let inflight: Promise<void> | null = null;

/** 刷新。并发调用共用同一次请求。 */
function refresh(): Promise<void> {
  if (inflight) {
    return inflight;
  }
  loading.value = true;
  // 顺序不能反: loadMetrics 要用 groups 把日志里的分组 name 换成 id。
  inflight = loadCore()
    .then(() => Promise.all([loadMetrics(), loadSuggestions()]))
    .then(() => undefined)
    .catch(e => {
      console.error(e);
      throw e;
    })
    .finally(() => {
      loading.value = false;
      inflight = null;
    });
  return inflight;
}

/** 候选增删改之后只重拉别名行 + 分组(比重拉整套轻)。 */
async function reload(): Promise<void> {
  await loadCore();
}

/** 保存智能路由阈值; 返回最新配置, 失败时返回 null 且不改动本地状态。 */
export async function saveRoutingSettings(payload: {
  enabled: boolean;
  simple_threshold: number;
  complex_threshold: number;
}): Promise<RoutingSettings | null> {
  try {
    const r = await routingSettingsApi.save(payload);
    const next = (r as unknown as { data: RoutingSettings }).data;
    if (next) {
      settings.value = next;
    }
    return next || null;
  } catch {
    return null;
  }
}

export function useAliasData() {
  return {
    loading,
    rows,
    groups,
    settings,
    trafficByKey,
    trafficAvailable,
    aliasViews,
    totalCandidates,
    groupInfoById,
    groupNameById,
    modelsByGroup,
    visibleSuggestions,
    suggestionCount,
    dismissSuggestion,
    refresh,
    reload,
  };
}
