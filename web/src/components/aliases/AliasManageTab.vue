<script setup lang="ts">
import {
  aliasesApi,
  RESERVED_ALIASES,
  routingSettingsApi,
  type AliasSuggestion,
  type ModelAliasRow,
  type RoutingSettings,
} from "@/api/aliases";
import { keysApi } from "@/api/keys";
import {
  getModelTimings,
  getModelTraffic,
  type ModelTiming,
  type ModelTrafficRow,
} from "@/api/dashboard";
import AliasEditDrawer from "@/components/aliases/AliasEditDrawer.vue";
import type { Group } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import { findProviderByUpstreams, isFree } from "@/data/freeProviders";
import AliasCandidateList from "@/components/aliases/AliasCandidateList.vue";
import AliasTableView from "@/components/aliases/AliasTableView.vue";
import AliasSplitView from "@/components/aliases/AliasSplitView.vue";
import AliasHealthView from "@/components/aliases/AliasHealthView.vue";
import type { AliasView } from "@/components/aliases/types";
// inferProvider 用它做 provider 目录查找 —— 只有 pavClass 是随编辑弹窗一起废弃的。
import { V3_PROVIDER_DIR } from "@/data/v3Catalog";
import { copy } from "@/utils/clipboard";
import {
  AddOutline,
  AlbumsOutline,
  BanOutline,
  BulbOutline,
  CheckmarkCircle,
  ChevronDownOutline,
  ChevronUpOutline,
  CloseOutline,
  HelpCircleOutline,
  LockClosedOutline,
  PulseOutline,
  RefreshOutline,
  SettingsOutline,
} from "@vicons/ionicons5";
import {
  NDrawer,
  NDrawerContent,
  NIcon,
  NInput,
  NModal,
  NSlider,
  NSpin,
  NSwitch,
  NTooltip,
  useDialog,
  useMessage,
} from "naive-ui";
import AliasQuickSetupTab from "@/components/aliases/AliasQuickSetupTab.vue";
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";

// i18n 约定: 一律直接写 t("key")。
//
// 不要写 t("key") || "中文兜底" —— vue-i18n 在 key 缺失时返回的是 **key 本身**
// (非空字符串), || 永远不会触发, 兜底是死代码。真缺 key 时用户看到的是
// "v3.aliasXxx" 这种原始键名, 而不是兜底文案 —— 兜底反而掩盖了问题。
// 缺 key 就去补 locale 文件 (zh-CN / en-US / ja-JP 三份都要)。
// 兼容老链接 ?tab=quick —— 由视图层传进来, 决定要不要一进来就打开"按家族整理"。
const props = withDefaults(defineProps<{ initialFamilyOpen?: boolean }>(), {
  initialFamilyOpen: false,
});

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();
const route = useRoute();
const router = useRouter();
const highlightAlias = ref<string | null>(null);

watch(
  () => route.query.highlight,
  raw => {
    const a = typeof raw === "string" ? raw : null;
    if (!a) {
      return;
    }
    highlightAlias.value = a;
    setTimeout(() => {
      highlightAlias.value = null;
      const { highlight: _drop, ...rest } = route.query;
      router.replace({ query: rest });
    }, 1500);
  },
  { immediate: true }
);

const loading = ref(false);
const rows = ref<ModelAliasRow[]>([]);
const groups = ref<Group[]>([]);
const settings = ref<RoutingSettings>({
  Enabled: true,
  SimpleThreshold: 2000,
  ComplexThreshold: 8000,
});
// 阈值区默认折叠: 它是"配一次基本不动"的东西, 不该常驻占版面。
const threshOpen = ref(false);

const DEFAULT_WEIGHT = 100;
// 与后端 createOnDB 的 nonZero(req.Priority, 100) 对齐: 之前这里写 0, 后端把 0
// 当"未设置"替换成 100, 于是 UI 显示 0、库里存 100, 来回不一致。
const DEFAULT_PRIORITY = 100;

const groupNameById = computed<Record<number, string>>(() => {
  const m: Record<number, string> = {};
  for (const g of groups.value) {
    if (g.id) {
      m[g.id] = getGroupDisplayName(g);
    }
  }
  return m;
});

// 分组 id -> 该分组下可选的真实模型。
//
// 口径必须与"模型"标签页一致, 否则用户在这里看到的列表和那边对不上:
//   specified 模式 → exposed_models; passthrough → available_models;
//   specified 下 exposed 为空时降级用 available 兜底, 避免空列表。
// 模型选择器(picker)和编辑抽屉都从这里取, 保证两处口径不会漂移。
const modelsByGroup = computed<Record<number, string[]>>(() => {
  const out: Record<number, string[]> = {};
  for (const g of groups.value) {
    if (!g.id) {
      continue;
    }
    const gg = g as unknown as {
      model_routing_mode?: string;
      exposed_models?: unknown;
      available_models?: unknown;
    };
    const mode = gg.model_routing_mode || "passthrough";
    let src =
      mode === "specified"
        ? parseModelArray(gg.exposed_models)
        : parseModelArray(gg.available_models);
    if (mode === "specified" && src.length === 0) {
      src = parseModelArray(gg.available_models);
    }
    out[g.id] = src;
  }
  return out;
});

// === Custom Picker Logic ===
const pickerOpen = ref(false);
const pickerSearch = ref("");
const pickerActiveGroupId = ref<number | null>(null);
const pickerTargetAlias = ref<string>("");

function openPicker(alias: string, seedPicks?: PendingPick[]) {
  pickerTargetAlias.value = alias;
  pickerSearch.value = "";
  // Stage seeds BEFORE flipping pickerOpen — the open-watcher resets
  // pendingPicks on every open, so seed-after-open would be wiped out.
  pendingPicksSeed.value = seedPicks || null;
  pickerOpen.value = true;
  if (!pickerActiveGroupId.value && groups.value.length) {
    pickerActiveGroupId.value = groups.value.find(g => g.group_type !== "aggregate")?.id || null;
  }
}

// 点击别名标题快捷复制 — 外部 client 用这个名字作为 chat completions 的 model 字段。
async function copyAlias(alias: string) {
  await copy(alias);
  message.success(t("v3.aliasCopied", { alias }));
}

function parseModelArray(raw: unknown): string[] {
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
      /* ignore */
    }
  }
  return [];
}

interface PickerModelView {
  id: string;
  isFree: boolean;
  alreadyBound: boolean;
  blocked: boolean;
}

const filteredPickerModels = computed<PickerModelView[]>(() => {
  if (!pickerActiveGroupId.value) {
    return [];
  }
  const g = groups.value.find(gr => gr.id === pickerActiveGroupId.value);
  if (!g) {
    return [];
  }

  // 1. 可选模型列表统一走 modelsByGroup —— 口径与"模型"标签页 / 编辑抽屉一致
  const source: string[] = modelsByGroup.value[g.id as number] || [];

  // 2. provider 上下文 — 用于免费检测
  const providerId = findProviderByUpstreams(g.upstreams || [])?.id;

  // 3. 已为当前 alias 在该 group 绑定的 real_model 集合
  const aliasName = pickerTargetAlias.value;
  const boundSet = new Set(
    rows.value.filter(r => r.alias === aliasName && r.group_id === g.id).map(r => r.real_model)
  );
  // 黑名单 - 这些选了也不会被路由
  const blockedSet = new Set(
    parseModelArray((g as unknown as { blocked_models?: unknown }).blocked_models)
  );

  // 4. 搜索过滤
  const q = pickerSearch.value.toLowerCase().trim();
  const filtered = q ? source.filter(m => m.toLowerCase().includes(q)) : source;

  // 5. 增广 + 排序:免费优先,字母序
  return filtered
    .map(id => ({
      id,
      isFree: isFree(providerId, id) === true,
      alreadyBound: boundSet.has(id),
      blocked: blockedSet.has(id),
    }))
    .sort((a, b) => {
      if (a.isFree !== b.isFree) {
        return a.isFree ? -1 : 1;
      }
      return a.id.localeCompare(b.id);
    });
});

// 暂存区:点 + 加入,确认时一次性批量创建
interface PendingPick {
  groupId: number;
  groupName: string;
  modelId: string;
}
const pendingPicks = ref<PendingPick[]>([]);
const pendingPicksSeed = ref<PendingPick[] | null>(null);
const pickerSubmitting = ref(false);

const pendingKeySet = computed(
  () => new Set(pendingPicks.value.map(p => `${p.groupId}:${p.modelId}`))
);

function isModelPending(modelId: string): boolean {
  if (!pickerActiveGroupId.value) {
    return false;
  }
  return pendingKeySet.value.has(`${pickerActiveGroupId.value}:${modelId}`);
}

function togglePendingPick(modelId: string) {
  if (!pickerActiveGroupId.value) {
    return;
  }
  const groupId = pickerActiveGroupId.value;
  const groupName =
    groupNameById.value[groupId] || groups.value.find(g => g.id === groupId)?.name || "";
  const key = `${groupId}:${modelId}`;
  const idx = pendingPicks.value.findIndex(p => `${p.groupId}:${p.modelId}` === key);
  if (idx >= 0) {
    pendingPicks.value.splice(idx, 1);
  } else {
    pendingPicks.value.push({ groupId, groupName, modelId });
  }
}

function removePending(p: PendingPick) {
  const key = `${p.groupId}:${p.modelId}`;
  pendingPicks.value = pendingPicks.value.filter(x => `${x.groupId}:${x.modelId}` !== key);
}

watch(pickerOpen, open => {
  if (open) {
    // Seed-on-open path lets family suggestions pre-populate the cart
    // without the user clicking each model. Seed is consumed once.
    pendingPicks.value = pendingPicksSeed.value ? [...pendingPicksSeed.value] : [];
    pendingPicksSeed.value = null;
  }
});

// createAliasCandidates 是**所有**"给别名添加候选"入口的唯一实现。
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
async function createAliasCandidates(
  alias: string,
  picks: PendingPick[]
): Promise<{ ok: number; fail: number }> {
  if (!alias || !picks.length) {
    return { ok: 0, fail: 0 };
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
    const info = groupExposureById.value[p.groupId];
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
      const info = groupExposureById.value[gid];
      const next = [...Array.from(info.exposed), ...models];
      return keysApi.updateGroup(gid, { exposed_models: next } as Partial<Group>).catch(() => null);
    })
  );

  return { ok, fail };
}

async function commitPendingPicks() {
  if (!pickerTargetAlias.value || !pendingPicks.value.length) {
    return;
  }
  pickerSubmitting.value = true;
  const { ok, fail } = await createAliasCandidates(pickerTargetAlias.value, pendingPicks.value);
  pickerSubmitting.value = false;
  if (ok > 0) {
    message.success(t("v5.maCreated", { ok, fail }));
    pickerOpen.value = false;
    await loadAll();
  } else if (fail > 0) {
    message.error(t("v5.maAllFailed"));
  } else {
    // ok=0 且 fail=0: 选中的候选全都已存在 —— 无事可做。静默关闭即可,
    // 报"全部失败"是错的。
    pickerOpen.value = false;
  }
}

// === Provider Inference ===
function inferProvider(row: ModelAliasRow): string {
  const gName = groupNameById.value[row.group_id] || "";
  const lowerG = gName.toLowerCase();
  const lowerM = (row.real_model || "").toLowerCase();
  for (const p of Object.keys(V3_PROVIDER_DIR)) {
    if (lowerG.includes(p) || lowerM.includes(p)) {
      return p;
    }
  }
  if (lowerG.includes("google") || lowerM.includes("gemini")) {
    return "google";
  }
  if (lowerG.includes("azure") || lowerM.includes("gpt")) {
    return "openai";
  }
  return "default";
}

// providerLogos.ts 的 KEYWORD_TO_KEY 是子串匹配, 把 group name 和 real model
// 拼一起当 hint, 命中范围比 inferProvider (仅 V3_PROVIDER_DIR) 大得多 — 智谱
// (bigmodel/glm) / 讯飞 (xinghuo/xingchen) / longcat / siliconflow 等都能识别。
function aliasLogoHint(row: ModelAliasRow): string {
  const gName = groupNameById.value[row.group_id] || "";
  return `${gName} ${row.real_model || ""}`;
}

// model-timings: { real_model -> avg_ms } 24h 平均请求耗时,用于 chip 旁挂个 "≈ X ms"
const timingMap = ref<Record<string, number>>({});
// { real_model -> {cost, tokens} } 24h 折算成本/用量,挂成本 chip (①成本可观测性).
const costMap = ref<Record<string, { cost: number; tokens: number }>>({});

async function loadTimings() {
  try {
    const r = await getModelTimings("24h");
    const list: ModelTiming[] = (r as unknown as { data: ModelTiming[] }).data || [];
    const map: Record<string, number> = {};
    const cmap: Record<string, { cost: number; tokens: number }> = {};
    for (const t of list) {
      if (t?.model) {
        map[t.model] = t.avg_ms || 0;
        cmap[t.model] = { cost: t.cost_usd || 0, tokens: t.tokens || 0 };
      }
    }
    timingMap.value = map;
    costMap.value = cmap;
  } catch {
    /* swallow — chip is purely decorative */
  }
}

// `${group_id}::${real_model}` -> 24h 实际调用数。
//
// 日志里存的是分组的**原始 name**(keypool/validator.go 里 `GroupName: group.Name`),
// 而前端 UI 显示的是 display name, 所以先用 name -> id 映射一次, 避免两个名字
// 体系对不上导致实际占比永远为空。
const trafficByGroupModel = ref<Record<string, number>>({});

async function loadTraffic() {
  try {
    const r = await getModelTraffic("24h");
    const list = (r as unknown as { data: ModelTrafficRow[] }).data || [];
    const nameToId: Record<string, number> = {};
    for (const g of groups.value) {
      if (g.id && g.name) {
        nameToId[g.name] = g.id;
      }
    }
    const map: Record<string, number> = {};
    for (const t of list) {
      const gid = nameToId[t.group_name];
      if (!gid) {
        continue;
      }
      map[`${gid}::${t.model}`] = t.calls;
    }
    trafficByGroupModel.value = map;
  } catch {
    /* 纯展示数据 —— 取不到就不显示"实际占比", 不影响主流程 */
  }
}

function avgMsFor(modelId: string): number {
  return timingMap.value[modelId] || 0;
}

// 成本 chip 文案: 有成本显示折算价格, 免费但有用量显示 token 数, 否则空。
function costChipFor(modelId: string): string {
  const c = costMap.value[modelId];
  if (!c) {
    return "";
  }
  if (c.cost > 0) {
    return c.cost < 1 ? `$${c.cost.toFixed(4)}` : `$${c.cost.toFixed(2)}`;
  }
  if (c.tokens > 0) {
    const tok = c.tokens >= 1000 ? `${(c.tokens / 1000).toFixed(1)}K` : `${c.tokens}`;
    return `${tok} tok`;
  }
  return "";
}

// === 别名编辑抽屉 ===
//
// 编辑单位从"一行"改成"一个别名": 候选池的构成/顺序/占比是相互关联的, 一次只改
// 一行看不出全貌。抽屉里一次改完, 保存走整体替换接口(事务内), 不会留半改状态。
const editorOpen = ref(false);
const editorAlias = ref("");

function openAliasEditor(alias: string) {
  editorAlias.value = alias;
  editorOpen.value = true;
}

/** 某别名当前的候选行(group_id=0 的占位行已被 grouped 过滤掉)。 */
function membersOf(alias: string): ModelAliasRow[] {
  return grouped.value.find(g => g.alias === alias)?.members || [];
}

interface GroupedAlias {
  alias: string;
  isReserved: boolean;
  members: ModelAliasRow[];
}

const grouped = computed<GroupedAlias[]>(() => {
  const map = new Map<string, GroupedAlias>();
  for (const r of rows.value) {
    const cur = map.get(r.alias) || {
      alias: r.alias,
      isReserved: r.is_reserved,
      members: [],
    };
    cur.isReserved = cur.isReserved || r.is_reserved;
    if (!(r.is_reserved && r.group_id === 0)) {
      cur.members.push(r);
    }
    map.set(r.alias, cur);
  }
  for (const g of map.values()) {
    g.members.sort((a, b) => {
      if (a.enabled === b.enabled) {
        return a.real_model.localeCompare(b.real_model);
      }
      return a.enabled ? -1 : 1;
    });
  }
  return Array.from(map.values()).sort((a, b) => {
    const ai = RESERVED_ALIASES.indexOf(a.alias as (typeof RESERVED_ALIASES)[number]);
    const bi = RESERVED_ALIASES.indexOf(b.alias as (typeof RESERVED_ALIASES)[number]);
    if (ai !== -1 && bi !== -1) {
      return ai - bi;
    }
    if (ai !== -1) {
      return -1;
    }
    if (bi !== -1) {
      return 1;
    }
    return a.alias.localeCompare(b.alias);
  });
});

const totalMappings = computed(
  () => rows.value.filter(r => !(r.is_reserved && r.group_id === 0)).length
);

// === alias 失效判定:目标 group 处于 specified 模式且 real_model 不在 exposed_models 里 ===
// 与 "enabled=false" 不同 — 这是因为 group 端配置导致 alias 静默失效,
// 路由层会跳过此 alias (见 router_engine.filterByExposed).
const groupExposureById = computed<Record<number, { mode: string; exposed: Set<string> }>>(() => {
  const out: Record<number, { mode: string; exposed: Set<string> }> = {};
  for (const g of groups.value) {
    if (!g.id) {
      continue;
    }
    const raw = (g as unknown as { exposed_models?: unknown }).exposed_models;
    let arr: string[] = [];
    if (Array.isArray(raw)) {
      arr = raw.filter((m): m is string => typeof m === "string");
    } else if (typeof raw === "string" && raw.trim()) {
      try {
        const j = JSON.parse(raw);
        if (Array.isArray(j)) {
          arr = j.filter((m): m is string => typeof m === "string");
        }
      } catch {
        /* ignore */
      }
    }
    out[g.id] = {
      mode: g.model_routing_mode || "passthrough",
      exposed: new Set(arr),
    };
  }
  return out;
});

// 失效 alias 一键修复: 跳到 Keys 页对应 group + 高亮该 real_model,
// 让 admin 立刻看到 model 卡片右上角"+加入"按钮 (V3GroupDetail 内置的 addToExposed).
// P8.5+P8.11: 强制 tab=models (model card 渲染在 v-else-if="tab === 'models'"
// 块, 不是 keys tab). 之前写成 keys 导致跳过去只看到 API key 列表, 找不到 model.
function goFixAlias(row: ModelAliasRow): void {
  if (!row.group_id) {
    return;
  }
  router.push({
    name: "keys",
    query: { groupId: row.group_id, tab: "models", highlight: row.real_model },
  });
}

// === 候选状态 ===
//
// 一个候选(一行 model_alias)在运行时可能因四种原因不参与选路。之前 UI 只用
// opacity+grayscale 把"无效"整体压暗 —— 结果是模型名看不清, 而且分不出**为什么**
// 无效(unexposed 和 disabled 长得几乎一样)。这里把它归一成显式状态, 每种状态
// 有自己的文案和颜色, 并且"可用/不可用"在卡片里分组展示。
type CandidateState = "usable" | "disabled" | "unexposed" | "blocked";

// blocked_models 命中时无视 routing mode 直接拒绝(见 router_engine.filterByExposed)。
const blockedModelsByGroup = computed<Record<number, Set<string>>>(() => {
  const out: Record<number, Set<string>> = {};
  for (const g of groups.value) {
    if (!g.id) {
      continue;
    }
    out[g.id] = new Set(
      parseModelArray((g as unknown as { blocked_models?: unknown }).blocked_models)
    );
  }
  return out;
});

function candidateState(row: ModelAliasRow): CandidateState {
  // 保留别名的占位行(group_id=0)不是候选, 不参与状态判定。
  if (row.is_reserved && row.group_id === 0) {
    return "usable";
  }
  if (!row.enabled) {
    return "disabled";
  }
  if (blockedModelsByGroup.value[row.group_id]?.has(row.real_model)) {
    return "blocked";
  }
  const info = groupExposureById.value[row.group_id];
  if (info && info.mode === "specified" && !info.exposed.has(row.real_model)) {
    return "unexposed";
  }
  return "usable";
}

// 把候选行 + 所有展示需要的派生信息一次算好, 交给 AliasCandidateList 渲染。
// 子组件只负责画, 不关心 exposure / blocked / 计时数据从哪来。
// (状态 -> 文案的映射在子组件里, 父组件不需要。)
function candidateRowsFor(members: ModelAliasRow[]) {
  // 配置占比按 weight 归一化。SWRR 只关心比例, 所以 100/1/1 与 98/1/1 等价,
  // 直接展示百分比比展示裸 weight 好读。
  const total = members.reduce((s, m) => s + Math.max(m.weight, 0), 0);
  return members.map(m => ({
    row: m,
    provider: inferProvider(m),
    logoHint: aliasLogoHint(m),
    groupName: groupNameById.value[m.group_id] || inferProvider(m),
    avgMs: avgMsFor(m.real_model),
    cost: costChipFor(m.real_model),
    share:
      total > 0
        ? Math.round((Math.max(m.weight, 0) / total) * 100)
        : Math.round(100 / Math.max(1, members.length)),
    calls: trafficByGroupModel.value[`${m.group_id}::${m.real_model}`] || 0,
    state: candidateState(m),
  }));
}

// === 视图模式 ===
//
// 同一个数据, 四种呈现, 各自服务不同的动作:
//   cards  — 总览: 一个别名里有什么, 点开就编辑
//   table  — 审计: 所有候选打平, 可排序筛选, 找"谁在吃流量 / 谁坏了"
//   split  — 调优: 左列表右详情, 连续切换, 行内即时改
//   health — 排障: 按问题类型分组, 集中处理失效候选
// 选择持久化 —— 每个人习惯一种, 不该每次进来重选。
type ViewMode = "cards" | "table" | "split" | "health";
const VIEW_MODES: readonly ViewMode[] = ["cards", "table", "split", "health"];
const VIEW_MODE_KEY = "alias-view-mode";

function loadViewMode(): ViewMode {
  const saved = localStorage.getItem(VIEW_MODE_KEY);
  return saved && (VIEW_MODES as readonly string[]).includes(saved) ? (saved as ViewMode) : "cards";
}
const viewMode = ref<ViewMode>(loadViewMode());
function setViewMode(m: ViewMode): void {
  viewMode.value = m;
  localStorage.setItem(VIEW_MODE_KEY, m);
}

const viewModeOptions = computed(() =>
  VIEW_MODES.map(m => ({ value: m, label: t(`aliases.view_${m}`) }))
);

// 三个新视图共用的数据: 每个别名 + 它的候选行(含状态/占比/实际调用)。
const aliasViews = computed<AliasView[]>(() =>
  grouped.value.map(g => {
    const rows = candidateRowsFor(g.members);
    const usable = rows.filter(r => r.state === "usable").length;
    return {
      alias: g.alias,
      isReserved: g.isReserved,
      rows,
      usable,
      unusable: rows.length - usable,
      total: rows.length,
      calls: rows.reduce((s, r) => s + r.calls, 0),
    };
  })
);

// === 行内快捷操作 ===
// 启停 / 移除走单行接口即时生效; 改结构(增删候选、调权重、排序)才需要打开
// 完整编辑抽屉 —— 那类改动要一次事务保存, 不适合逐行即时生效。
// 两个行内操作都做乐观更新: 启停/移除是很轻的动作, 没必要为此重拉
// (aliases + groups + settings + 建议×2 + timings + traffic) 一整套接口 ——
// 那样每次点一下都会整页闪一下。这里直接改本地数据, grouped / aliasViews /
// candidateState 都是 computed, 会自动重算; 只有失败才回滚并全量刷新兜底。
async function toggleCandidate(row: ModelAliasRow): Promise<void> {
  const next = !row.enabled;
  const target = rows.value.find(r => r.id === row.id);
  if (target) {
    target.enabled = next;
  }
  try {
    await aliasesApi.update(row.id, { enabled: next });
  } catch {
    if (target) {
      target.enabled = !next;
    }
    message.error(t("common.requestFailed"));
    await refreshAll();
  }
}

function removeCandidate(row: ModelAliasRow): void {
  dialog.warning({
    title: t("v3.aliasDeleteTitle"),
    content: t("v3.aliasDeleteConfirm", { alias: row.alias, model: row.real_model }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      const before = rows.value;
      rows.value = rows.value.filter(r => r.id !== row.id);
      try {
        await aliasesApi.remove(row.id);
      } catch {
        rows.value = before;
        message.error(t("common.requestFailed"));
      }
    },
  });
}

async function loadAll() {
  loading.value = true;
  try {
    const [r, g, s] = await Promise.all([
      aliasesApi.list(),
      keysApi.getGroups(),
      routingSettingsApi.get(),
    ]);
    rows.value = (r as unknown as { data: ModelAliasRow[] }).data || [];
    groups.value = (g || []).filter(gr => gr.id);
    settings.value = (s as unknown as { data: RoutingSettings }).data || settings.value;
  } catch (e) {
    console.error(e);
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
}

const suggestions = ref<AliasSuggestion[]>([]);
// 建议从"常驻横幅"改成"抽屉": 默认收起、不占版面, 想看才点开。
// 于是 dismiss-all 和折叠状态都不再需要 —— 只保留按 family 的单条忽略。
const suggestDrawerOpen = ref(false);
// "按家族整理" —— 原"快速整理"tab 的入口, 现在收进主 tab 的一个弹窗。
const familyModalOpen = ref(false);
// P8.4: 单条 dismiss, localStorage 持久化避免每次进页面又出现.
const dismissedFamilies = ref<Set<string>>(loadDismissedFamilies());
function loadDismissedFamilies(): Set<string> {
  try {
    const raw = localStorage.getItem("alias-suggest-dismissed-families");
    return new Set(raw ? (JSON.parse(raw) as string[]) : []);
  } catch {
    return new Set();
  }
}
function saveDismissedFamilies(): void {
  localStorage.setItem(
    "alias-suggest-dismissed-families",
    JSON.stringify(Array.from(dismissedFamilies.value))
  );
}
function dismissOneFamily(family: string): void {
  if (!family) {
    return;
  }
  const s = new Set(dismissedFamilies.value);
  s.add(family);
  dismissedFamilies.value = s;
  saveDismissedFamilies();
}
const visibleSuggestions = computed(() => {
  // family 类型按 dismissedFamilies 过滤; single 类型按 model 名过滤 (复用同套)
  return suggestions.value.filter(s => {
    const key = s.kind === "family" ? s.family : s.model;
    return key && !dismissedFamilies.value.has(key);
  });
});
async function loadSuggestions() {
  try {
    // 同时拉两个数据源: 现有 logs-driven (reactive) + P4.2 registry-driven
    // (proactive 主动建议跨 sub-group family). 所有 aggregate group 都查一遍,
    // 合并去重 (按 family 名).
    const [logsRes, aggResp] = await Promise.all([
      aliasesApi.suggestions().catch(() => ({ data: [] })),
      // 拿所有 group, 过滤 aggregate, 并行问每个 P4.2 registry-driven 建议
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

function onClickSuggestion(s: AliasSuggestion) {
  if (s.kind === "family") {
    onClickFamilySuggestion(s);
    return;
  }
  if (s.model) {
    openPicker(s.model);
  }
}

/**
 * Family suggestion → open picker pre-populated with every (group, model)
 * pair the backend told us about. We use `existing_alias` as the target
 * name when present (== "append to existing"); otherwise the family name
 * becomes a new alias.
 */
function onClickFamilySuggestion(s: AliasSuggestion) {
  const alias = (s.existing_alias || s.family || "").trim();
  if (!alias || !s.models?.length) {
    return;
  }
  const seeds: PendingPick[] = [];
  const seen = new Set<string>();
  for (const fm of s.models) {
    const ids = fm.in_group_ids || [];
    for (const gid of ids) {
      const groupName =
        groupNameById.value[gid] || groups.value.find(g => g.id === gid)?.name || "";
      // Don't seed picks that would duplicate a row that already exists
      // for this alias+group+model — the API treats those as conflicts.
      const dup = rows.value.some(
        r => r.alias === alias && r.group_id === gid && r.real_model === fm.name
      );
      if (dup) {
        continue;
      }
      const key = `${gid}:${fm.name}`;
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);
      seeds.push({ groupId: gid, groupName, modelId: fm.name });
    }
  }
  openPicker(alias, seeds);
}

// P7: family suggestion 一键采纳 -- 不开 picker, 直接批量建候选。
//
// 具体的"建行 + 补 exposed"逻辑统一在 createAliasCandidates 里 (见那里的注释),
// 这里只负责把 suggestion 的 (group, model) 摊平成 picks —— 与 picker 路径共用
// 同一实现, 保证两条路径的副作用一致。
async function quickAdoptFamilySuggestion(s: AliasSuggestion) {
  const alias = (s.existing_alias || s.family || "").trim();
  if (!alias || !s.models?.length) {
    return;
  }
  const picks: PendingPick[] = [];
  const seen = new Set<string>();
  for (const fm of s.models) {
    for (const gid of fm.in_group_ids || []) {
      const key = `${gid}:${fm.name}`;
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);
      picks.push({
        groupId: gid,
        groupName: groupNameById.value[gid] || groups.value.find(g => g.id === gid)?.name || "",
        modelId: fm.name,
      });
    }
  }
  if (!picks.length) {
    return;
  }
  await createAliasCandidates(alias, picks);
  await Promise.all([loadAll(), loadSuggestions()]);
}

async function refreshAll() {
  // 先 loadAll: loadTraffic 要用 groups 把日志里的分组 name 映射成 id,
  // 并行跑会拿到空 groups, 实际占比就永远是空的。
  await loadAll();
  await Promise.all([loadSuggestions(), loadTimings(), loadTraffic()]);
}

onMounted(() => {
  if (props.initialFamilyOpen) {
    familyModalOpen.value = true;
  }
  return refreshAll();
});

// === 卡片视图的数据 ===
//
// 直接从 aliasViews 切两片, 不再从 `grouped` 二次推导。之前这里是两套并行的
// 推导逻辑(卡片走 grouped->tiers, 另外三个视图走 aliasViews), 同一个别名有
// 两个"长什么样"的来源; 而且模板里现算候选行会**每次
// 渲染都重算** N 个别名 × M 个候选。现在四个视图共用同一份预计算结果。
// 注意别用 `as const` —— 那样会推导成元组类型, 下面 `indexOf(x.id)` 传 string
// 过不了类型检查。这里要的就是 readonly string[]。
const RESERVED_TIERS: readonly string[] = ["simple", "medium", "complex"];

function isReservedTier(alias: string): boolean {
  return RESERVED_TIERS.includes(alias);
}

/** 智能路由的三档池, 按 simple/medium/complex 固定顺序 + 各档色带。 */
const tierViews = computed(() =>
  aliasViews.value
    .filter(a => isReservedTier(a.alias))
    .map(a => ({
      ...a,
      id: a.alias,
      title: t(`v3.${a.alias}`),
      class: `v3-tier--${a.alias}`,
    }))
    .sort((x, y) => RESERVED_TIERS.indexOf(x.id) - RESERVED_TIERS.indexOf(y.id))
);

const customAliasViews = computed(() => aliasViews.value.filter(a => !isReservedTier(a.alias)));

// === New alias creation ===
const newCardOpen = ref(false);
const newCardName = ref("");
function openNewCard() {
  newCardOpen.value = true;
  newCardName.value = "";
}
function cancelNewCard() {
  newCardOpen.value = false;
}
async function commitNewCard() {
  const name = newCardName.value.trim();
  if (!name) {
    message.warning(t("v5.alNameRequired"));
    return;
  }
  // Close dialog first, then open picker to add the first member
  cancelNewCard();
  openPicker(name);
}

// === Settings ===
const SLIDER_MAX = 32000;
const rangeValue = computed({
  get: () => [settings.value.SimpleThreshold, settings.value.ComplexThreshold] as [number, number],
  set: (val: [number, number]) => {
    settings.value.SimpleThreshold = val[0];
    settings.value.ComplexThreshold = val[1];
  },
});
const presetList = [
  { label: "Economy", simple: 500, complex: 2000, color: "var(--v3-ok)" },
  { label: "Balanced", simple: 2000, complex: 8000, color: "var(--v3-warn)" },
  { label: "Performance", simple: 4000, complex: 16000, color: "var(--v3-danger)" },
];
async function applyPreset(p: (typeof presetList)[0]) {
  settings.value.SimpleThreshold = p.simple;
  settings.value.ComplexThreshold = p.complex;
  await saveSettings(true); // 预设是显式动作,弹 toast
}

// 静默保存:slider 拖动 / 点轨道 / 键盘调整都用此函数,无 toast 干扰
async function saveSettings(notify = false) {
  if (settings.value.SimpleThreshold >= settings.value.ComplexThreshold) {
    settings.value.ComplexThreshold = settings.value.SimpleThreshold + 100;
  }
  try {
    const r = await routingSettingsApi.save({
      enabled: settings.value.Enabled,
      simple_threshold: settings.value.SimpleThreshold,
      complex_threshold: settings.value.ComplexThreshold,
    });
    settings.value = (r as unknown as { data: RoutingSettings }).data;
    if (notify) {
      message.success(t("common.operationSuccess"));
    }
  } catch {
    message.error(t("common.requestFailed"));
  }
}

// 节流:连续拖动时只保存最后一次
let saveTimer: ReturnType<typeof setTimeout> | null = null;
function saveSettingsThrottled() {
  if (saveTimer) {
    clearTimeout(saveTimer);
  }
  saveTimer = setTimeout(() => {
    saveSettings();
    saveTimer = null;
  }, 400);
}
</script>

<template>
  <div class="v3-page-aliases">
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.aliases") }}</div>
      <div class="v3-viewhead__actions">
        <!-- 同一份数据四种呈现: 卡片看全貌 / 表格做审计 / 分栏连续调优 / 健康排障。
             选择持久化 —— 习惯一种就不该每次重选。 -->
        <div class="v3-viewswitch">
          <button
            v-for="m in viewModeOptions"
            :key="m.value"
            class="v3-viewswitch__btn"
            :class="{ 'v3-viewswitch__btn--active': viewMode === m.value }"
            @click="setViewMode(m.value)"
          >
            {{ m.label }}
          </button>
        </div>
        <button
          v-if="visibleSuggestions.length"
          class="v3-btn"
          :title="t('v5.suggestionsTitle')"
          @click="suggestDrawerOpen = true"
        >
          <n-icon :component="BulbOutline" :size="12" />
          {{ t("v5.suggestionsTitle") }}
          <span class="v5-suggest-banner__count">{{ visibleSuggestions.length }}</span>
        </button>
        <button class="v3-btn" @click="familyModalOpen = true">
          <n-icon :component="AlbumsOutline" :size="12" />
          {{ t("aliases.browseFamily") }}
        </button>
        <button class="v3-btn" @click="refreshAll">
          <n-icon :component="RefreshOutline" :size="12" />
          {{ t("v3.refresh") }}
        </button>
        <button class="v3-btn v3-btn--accent" @click="openNewCard">
          <n-icon :component="AddOutline" :size="12" />
          {{ t("v5.alNewAlias") }}
        </button>
      </div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("v3.aliasesTitle") }}
      <n-tooltip trigger="hover">
        <template #trigger>
          <n-icon
            :component="HelpCircleOutline"
            :size="15"
            style="margin-left: 6px; cursor: help; color: var(--v3-ink-3)"
          />
        </template>
        {{ t("v3.aliasesDesc") }}
      </n-tooltip>
      <span class="v3-viewtitle__meta">
        {{ t("v5.alMappings", { n: totalMappings }) }}
      </span>
    </h1>

    <!-- 建议改成抽屉: 横幅会常驻占版面, 而建议是"有空才看"的辅助信息。 -->
    <NDrawer v-model:show="suggestDrawerOpen" :width="420" placement="right">
      <NDrawerContent :title="t('v5.suggestionsTitle')" closable :native-scrollbar="false">
        <div v-if="!visibleSuggestions.length" class="v5-suggest-empty">
          {{ t("v5.suggestionsEmpty") }}
        </div>
        <div class="v5-suggest-banner__list">
          <template
            v-for="(s, idx) in visibleSuggestions"
            :key="s.kind === 'family' ? `f:${s.family}` : `s:${s.model}-${idx}`"
          >
            <!-- Family suggestion: one click pre-fills the picker with every
               (group, model) sibling the backend found, target alias = family
               name (or existing_alias when an alias by that name already
               exists → "append" rather than "create"). -->
            <n-tooltip v-if="s.kind === 'family'" trigger="hover" placement="top">
              <template #trigger>
                <span class="v5-suggest-chip-wrap">
                  <!-- 主行: 默认行为 = 一键采纳 (跳过 picker, 直接 batch create alias).
                     admin 信任 backend 推荐时 1 click 完成. 想审查就用旁边"审查" 按钮. -->
                  <button
                    class="v5-suggest-chip v5-suggest-chip--family"
                    @click="quickAdoptFamilySuggestion(s)"
                  >
                    <span class="v5-suggest-chip__family">{{ s.family }}</span>
                    <span class="v5-suggest-chip__sub">
                      {{ t("v5.suggestFamilyMeta", { n: (s.models || []).length, hits: s.count }) }}
                    </span>
                    <span
                      class="v5-suggest-chip__pill"
                      :class="
                        s.existing_alias
                          ? 'v5-suggest-chip__pill--append'
                          : 'v5-suggest-chip__pill--new'
                      "
                    >
                      {{
                        s.existing_alias ? t("v5.suggestFamilyAppend") : t("v5.suggestFamilyCreate")
                      }}
                    </span>
                  </button>
                  <!-- 次要操作: 打开 picker 让 admin 检查/调整候选. -->
                  <button
                    class="v5-suggest-chip__zap"
                    :title="t('v5.suggestFamilyReview')"
                    @click.stop="onClickSuggestion(s)"
                  >
                    {{ t("v5.suggestFamilyReviewBtn") }}
                  </button>
                  <!-- P8.4: 单条 dismiss ✕, localStorage 持久化, 不影响其他 family. -->
                  <button
                    class="v5-suggest-chip__dismiss"
                    :title="t('v5.suggestionsDismissOne')"
                    @click.stop="dismissOneFamily(s.family || '')"
                  >
                    <n-icon :component="CloseOutline" :size="10" />
                  </button>
                </span>
              </template>
              <div style="font: 500 11px var(--v3-sans); margin-bottom: 4px">
                {{
                  s.existing_alias
                    ? t("v5.suggestFamilyTooltipAppend", { alias: s.existing_alias })
                    : t("v5.suggestFamilyTooltipCreate", { alias: s.family })
                }}
              </div>
              <div style="display: flex; flex-direction: column; gap: 2px">
                <div v-for="m in s.models || []" :key="m.name" style="font: 11px var(--v3-mono)">
                  <span>{{ m.name }}</span>
                  <span v-if="m.count" style="color: var(--v3-ink-4); margin-left: 4px">
                    ×{{ m.count }}
                  </span>
                  <span
                    v-if="(m.in_group_ids || []).length"
                    style="color: var(--v3-accent); margin-left: 4px"
                  >
                    · {{ t("v5.suggestFamilyInGroups", { n: (m.in_group_ids || []).length }) }}
                  </span>
                  <span v-else-if="!m.from_logs" style="color: var(--v3-ink-4); margin-left: 4px">
                    ·
                  </span>
                </div>
              </div>
            </n-tooltip>

            <!-- Single suggestion: legacy chip. -->
            <button
              v-else
              class="v5-suggest-chip"
              @click="onClickSuggestion(s)"
              :title="s.last_seen ? `Last seen ${s.last_seen}` : ''"
            >
              {{ s.model }}
              <span class="v5-suggest-count">×{{ s.count }}</span>
            </button>
          </template>
        </div>
      </NDrawerContent>
    </NDrawer>

    <!-- 智能路由阈值: 折叠成一行状态条。
         阈值是"配一次基本不动"的东西, 之前它常驻占掉半屏, 把真正的别名列表挤到
         下面 —— 打开页面第一眼看到的应该是别名, 不是滑块。 -->
    <div class="v3-thresh-card v3-thresh-card--compact">
      <div class="v3-thresh-bar">
        <n-icon :component="SettingsOutline" :size="14" />
        <span class="v3-thresh-bar__label">{{ t("v3.complexityThresholds") }}</span>
        <n-tooltip trigger="hover">
          <template #trigger>
            <n-icon
              :component="HelpCircleOutline"
              :size="13"
              style="cursor: help; color: var(--v3-ink-4)"
            />
          </template>
          {{ t("v3.complexityThresholdsSub") }}
        </n-tooltip>

        <span class="v3-thresh-bar__summary">
          {{ t("v3.simple") }} &lt; {{ settings.SimpleThreshold.toLocaleString() }} ·
          {{ t("v3.medium") }} · {{ t("v3.complex") }} ≥
          {{ settings.ComplexThreshold.toLocaleString() }}
        </span>

        <button
          class="v3-btn v3-btn--sm v3-thresh-bar__expand"
          :title="threshOpen ? t('common.collapse') : t('common.expand')"
          @click="threshOpen = !threshOpen"
        >
          <n-icon :component="threshOpen ? ChevronUpOutline : ChevronDownOutline" :size="12" />
        </button>

        <span class="v3-thresh-bar__toggle">
          <span class="v3-thresh-bar__state">
            {{ settings.Enabled ? t("v3.routingActive") : t("v3.routingPassthrough") }}
          </span>
          <n-switch v-model:value="settings.Enabled" size="small" @update:value="saveSettings" />
        </span>
      </div>

      <div v-if="threshOpen" class="v3-thresh-body">
        <div class="v5-qs__quickbtns">
          <button
            v-for="p in presetList"
            :key="p.label"
            class="v3-btn v3-btn--sm"
            style="font-size: 10.5px; padding: 3px 8px; border-radius: 4px"
            :style="{
              borderColor:
                settings.SimpleThreshold === p.simple && settings.ComplexThreshold === p.complex
                  ? p.color
                  : undefined,
              background:
                settings.SimpleThreshold === p.simple && settings.ComplexThreshold === p.complex
                  ? p.color + '10'
                  : undefined,
            }"
            @click="applyPreset(p)"
          >
            {{ p.label }} · {{ p.simple }}/{{ p.complex }}
          </button>
        </div>

        <div class="v3-custom-slider">
          <div class="v3-slider-inner">
            <div class="v3-slider-rail">
              <div
                class="v3-slider-seg v3-slider-seg--fast"
                :style="{ width: (settings.SimpleThreshold / SLIDER_MAX) * 100 + '%' }"
              />
              <div
                class="v3-slider-seg v3-slider-seg--balanced"
                :style="{
                  left: (settings.SimpleThreshold / SLIDER_MAX) * 100 + '%',
                  width:
                    ((settings.ComplexThreshold - settings.SimpleThreshold) / SLIDER_MAX) * 100 +
                    '%',
                }"
              />
              <div
                class="v3-slider-seg v3-slider-seg--flagship"
                :style="{
                  left: (settings.ComplexThreshold / SLIDER_MAX) * 100 + '%',
                  width: (1 - settings.ComplexThreshold / SLIDER_MAX) * 100 + '%',
                }"
              />
            </div>
            <div
              class="v3-slider-zone"
              :style="{ left: (settings.SimpleThreshold / SLIDER_MAX) * 50 + '%' }"
            >
              FAST
            </div>
            <div
              class="v3-slider-zone"
              :style="{
                left:
                  ((settings.SimpleThreshold + settings.ComplexThreshold) / SLIDER_MAX) * 50 + '%',
              }"
            >
              BALANCED
            </div>
            <div
              class="v3-slider-zone"
              :style="{ left: ((settings.ComplexThreshold + SLIDER_MAX) / SLIDER_MAX) * 50 + '%' }"
            >
              COMPLEX
            </div>
            <div
              class="v3-slider-val"
              :style="{ left: (settings.SimpleThreshold / SLIDER_MAX) * 100 + '%' }"
            >
              {{ settings.SimpleThreshold.toLocaleString() }}
            </div>
            <div
              class="v3-slider-val"
              :style="{ left: (settings.ComplexThreshold / SLIDER_MAX) * 100 + '%' }"
            >
              {{ settings.ComplexThreshold.toLocaleString() }}
            </div>
            <n-slider
              v-model:value="rangeValue"
              range
              :min="1"
              :max="SLIDER_MAX"
              :step="100"
              class="v3-slider-overlay"
              @update:value="saveSettingsThrottled"
              @dragend="saveSettings"
            />
            <div class="v3-slider-legend" style="left: 0">0</div>
            <div class="v3-slider-legend" style="right: 0">{{ SLIDER_MAX / 1000 }}K</div>
          </div>
        </div>
      </div>
    </div>

    <!-- 四种视图共用同一份 aliasViews 数据。cards 直接内联(它的样式在本组件
         scoped 作用域里), 另外三种是独立组件。 -->
    <template v-if="viewMode === 'cards'">
      <!-- Tier Board (Grid Mode) -->
      <div class="v5-alias-grid" style="margin-bottom: 20px">
        <div
          v-for="tier in tierViews"
          :key="tier.id"
          :class="['v3-tier', tier.class, { 'v5-alias-card--glow': tier.id === highlightAlias }]"
        >
          <div class="v3-tier__band" />
          <div class="v3-tier__head">
            <div class="v3-tier__head-main" style="align-items: center">
              <div
                class="v3-tier__title v3-tier__title--copyable"
                role="button"
                tabindex="0"
                :title="t('v3.aliasClickToCopy')"
                @click="copyAlias(tier.id)"
                @keydown.enter.prevent="copyAlias(tier.id)"
                @keydown.space.prevent="copyAlias(tier.id)"
              >
                {{ tier.title }}
              </div>
              <div class="v3-tier__rule" style="margin-top: 0">
                <span style="opacity: 0.7; font-weight: 500; margin-right: 4px">{{ tier.id }}</span>
                · {{ tier.usable }}/{{ tier.total }}
                {{ t("v3.aliasStateUsable") }}
              </div>
            </div>
            <button
              class="v3-btn v3-btn--accent v3-btn--icon v3-btn--sm"
              style="width: 24px; height: 24px"
              @click="openPicker(tier.id)"
            >
              <n-icon :component="AddOutline" :size="13" />
            </button>
          </div>

          <div class="v3-tier__body">
            <AliasCandidateList
              :rows="tier.rows"
              @edit="() => openAliasEditor(tier.id)"
              @fix="goFixAlias"
            />
            <div v-if="!tier.total" class="v3-tier__empty-hint">
              {{ t("v3.noMappings") }}
            </div>
          </div>
        </div>
      </div>

      <div class="v5-section-title">
        <h3>
          {{ t("v5.aggregates") }}
          <n-tooltip trigger="hover">
            <template #trigger>
              <n-icon
                :component="HelpCircleOutline"
                :size="14"
                style="cursor: help; color: var(--v3-ink-4)"
              />
            </template>
            {{ t("v5.aggregatesSub") }}
          </n-tooltip>
        </h3>
      </div>

      <n-spin :show="loading">
        <div class="v5-alias-grid">
          <div
            v-for="grp in customAliasViews"
            :key="grp.alias"
            class="v3-tier"
            :class="{
              'v5-alias-card--reserved': grp.isReserved,
              'v5-alias-card--glow': grp.alias === highlightAlias,
            }"
          >
            <div class="v3-tier__band" />
            <div class="v3-tier__head">
              <div class="v3-tier__head-main" style="align-items: center">
                <div
                  class="v3-tier__title v3-tier__title--copyable"
                  role="button"
                  tabindex="0"
                  :title="t('v3.aliasClickToCopy')"
                  @click="copyAlias(grp.alias)"
                  @keydown.enter.prevent="copyAlias(grp.alias)"
                  @keydown.space.prevent="copyAlias(grp.alias)"
                >
                  {{ grp.alias }}
                </div>
                <div class="v3-tier__rule" style="margin-top: 0">
                  <span
                    v-if="grp.isReserved"
                    style="
                      margin-right: 4px;
                      color: var(--v3-warn);
                      display: flex;
                      align-items: center;
                    "
                  >
                    <n-icon :component="LockClosedOutline" :size="10" />
                  </span>
                  {{ grp.usable }}/{{ grp.total }}
                  {{ t("v3.aliasStateUsable") }}
                </div>
              </div>
              <button
                class="v3-btn v3-btn--accent v3-btn--icon v3-btn--sm"
                style="width: 24px; height: 24px"
                @click="openPicker(grp.alias)"
              >
                <n-icon :component="AddOutline" :size="13" />
              </button>
            </div>

            <div class="v3-tier__body">
              <AliasCandidateList
                :rows="grp.rows"
                @edit="() => openAliasEditor(grp.alias)"
                @fix="goFixAlias"
              />
              <div v-if="!grp.total" class="v3-tier__empty-hint">
                {{ t("v3.noMappings") }}
              </div>
            </div>
          </div>

          <div
            v-if="!customAliasViews.length && !newCardOpen"
            class="v5-empty"
            style="grid-column: 1 / -1"
          >
            <div class="v5-empty__icon"><n-icon :component="HelpCircleOutline" :size="22" /></div>
            <div class="v5-empty__title">{{ t("v5.alEmpty") }}</div>
            <div class="v5-empty__sub">{{ t("v5.alEmptySub") }}</div>
            <button class="v3-btn v3-btn--accent" style="margin-top: 8px" @click="openNewCard">
              <n-icon :component="AddOutline" :size="12" />
              {{ t("v5.alNewAlias") }}
            </button>
          </div>
        </div>
      </n-spin>
    </template>

    <AliasTableView
      v-else-if="viewMode === 'table'"
      :aliases="aliasViews"
      :loading="loading"
      @edit="openAliasEditor"
      @fix="goFixAlias"
      @toggle="toggleCandidate"
      @remove="removeCandidate"
    />
    <AliasSplitView
      v-else-if="viewMode === 'split'"
      :aliases="aliasViews"
      :loading="loading"
      @edit="openAliasEditor"
      @fix="goFixAlias"
      @toggle="toggleCandidate"
      @remove="removeCandidate"
      @copy="copyAlias"
      @add="openPicker"
    />
    <AliasHealthView
      v-else
      :aliases="aliasViews"
      :loading="loading"
      @edit="openAliasEditor"
      @fix="goFixAlias"
      @toggle="toggleCandidate"
      @copy="copyAlias"
      @add="openPicker"
    />

    <!-- Two-Pane Model Picker Dialog (批量选择) -->
    <n-modal
      v-model:show="pickerOpen"
      preset="card"
      style="width: 840px"
      :title="t('v3.aliasAddMember')"
    >
      <!-- 已选暂存区 -->
      <div class="v3-picker-pending">
        <div class="v3-picker-pending__head">
          <span class="v3-picker-pending__lbl">
            {{ t("v3.aliasPendingTitle") }} ({{ pendingPicks.length }})
          </span>
          <span v-if="!pendingPicks.length" class="v3-picker-pending__hint">
            {{ t("v3.aliasPendingHint") }}
          </span>
        </div>
        <div v-if="pendingPicks.length" class="v3-picker-pending__list">
          <div
            v-for="p in pendingPicks"
            :key="`${p.groupId}:${p.modelId}`"
            class="v3-picker-pending__chip"
          >
            <span class="v3-picker-pending__chip-grp">{{ p.groupName }}</span>
            <span class="v3-picker-pending__chip-sep">·</span>
            <span class="v3-picker-pending__chip-mod">{{ p.modelId }}</span>
            <button
              class="v3-picker-pending__chip-x"
              :title="t('common.delete')"
              @click="removePending(p)"
            >
              <n-icon :component="CloseOutline" :size="12" />
            </button>
          </div>
        </div>
      </div>

      <div
        style="
          display: flex;
          height: 460px;
          gap: 1px;
          background: var(--v3-line);
          border: 1px solid var(--v3-line);
          border-radius: 6px;
          overflow: hidden;
        "
      >
        <!-- Left: Groups -->
        <div
          style="
            width: 280px;
            background: var(--v3-surface-2);
            display: flex;
            flex-direction: column;
            min-height: 0;
          "
        >
          <div
            style="
              padding: 14px;
              font: 700 11px var(--v3-mono);
              color: var(--v3-ink-3);
              text-transform: uppercase;
              border-bottom: 1px solid var(--v3-line);
              flex-shrink: 0;
            "
          >
            {{ t("keys.groupManagement") }}
          </div>
          <div class="scroll" style="flex: 1; overflow-y: auto; min-height: 0">
            <div
              v-for="g in groups"
              :key="g.id"
              class="v3-picker-group-row"
              :class="{ 'v3-picker-group-row--active': pickerActiveGroupId === g.id }"
              @click="pickerActiveGroupId = g.id as number"
            >
              <div style="font-weight: 700; font-size: 13px">{{ getGroupDisplayName(g) }}</div>
              <div style="font-size: 10px; color: var(--v3-ink-4); margin-top: 2px">
                {{ g.name }}
              </div>
            </div>
          </div>
        </div>
        <!-- Right: Models -->
        <div
          style="
            flex: 1;
            background: var(--v3-surface);
            display: flex;
            flex-direction: column;
            min-height: 0;
          "
        >
          <div style="padding: 12px; border-bottom: 1px solid var(--v3-line); flex-shrink: 0">
            <n-input
              v-model:value="pickerSearch"
              :placeholder="t('v3.filterModels')"
              clearable
              size="small"
            >
              <template #prefix><n-icon :component="PulseOutline" /></template>
            </n-input>
          </div>
          <div class="scroll" style="flex: 1; overflow-y: auto; padding: 12px; min-height: 0">
            <div
              v-if="!filteredPickerModels.length"
              style="padding: 60px; text-align: center; color: var(--v3-ink-4)"
            >
              {{ t("modelcatalog.noData") }}
            </div>
            <div
              v-else
              style="
                display: grid;
                grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
                gap: 10px;
              "
            >
              <button
                v-for="m in filteredPickerModels"
                :key="m.id"
                class="v3-picker-model-btn"
                :class="{
                  'v3-picker-model-btn--picked': isModelPending(m.id),
                  'v3-picker-model-btn--bound': m.alreadyBound,
                  'v3-picker-model-btn--blocked': m.blocked,
                }"
                :disabled="m.alreadyBound || m.blocked"
                :title="
                  m.blocked
                    ? t('v3.aliasBlockedTip')
                    : m.alreadyBound
                      ? t('v3.aliasAlreadyBound')
                      : m.id
                "
                @click="!m.alreadyBound && !m.blocked && togglePendingPick(m.id)"
              >
                <span
                  v-if="m.isFree"
                  class="v3-picker-model-btn__free"
                  :title="t('modelcatalog.freeTag')"
                >
                  🆓
                </span>
                <span class="v3-picker-model-btn__id">{{ m.id }}</span>
                <n-icon v-if="m.blocked" :component="BanOutline" class="v3-picker-model-btn__add" />
                <n-icon
                  v-else-if="m.alreadyBound"
                  :component="LockClosedOutline"
                  class="v3-picker-model-btn__add"
                />
                <n-icon
                  v-else-if="isModelPending(m.id)"
                  :component="CheckmarkCircle"
                  class="v3-picker-model-btn__add"
                />
                <n-icon v-else :component="AddOutline" class="v3-picker-model-btn__add" />
              </button>
            </div>
          </div>
        </div>
      </div>

      <template #footer>
        <div style="display: flex; justify-content: flex-end; gap: 8px">
          <n-button @click="pickerOpen = false">{{ t("common.cancel") }}</n-button>
          <n-button
            type="primary"
            :loading="pickerSubmitting"
            :disabled="!pendingPicks.length"
            @click="commitPendingPicks"
          >
            {{ t("v3.aliasPendingConfirm", { n: pendingPicks.length }) }}
          </n-button>
        </div>
      </template>
    </n-modal>

    <!-- New Alias Name Modal -->
    <n-modal
      v-model:show="newCardOpen"
      preset="dialog"
      :title="t('v5.alNewAlias')"
      style="width: 400px"
    >
      <div style="padding-top: 16px; display: flex; flex-direction: column; gap: 16px">
        <n-input
          v-model:value="newCardName"
          :placeholder="t('v3.aliasNamePlaceholder')"
          @keyup.enter="commitNewCard"
        />
        <div style="display: flex; justify-content: flex-end; gap: 12px">
          <button class="v3-btn" @click="cancelNewCard">{{ t("common.cancel") }}</button>
          <button class="v3-btn v3-btn--accent" @click="commitNewCard">
            {{ t("common.save") }}
          </button>
        </div>
      </div>
    </n-modal>

    <!-- 别名编辑抽屉: 编辑单位是"整个别名", 不再是"一行一个弹窗"。 -->
    <AliasEditDrawer
      v-model:show="editorOpen"
      :alias="editorAlias"
      :rows="membersOf(editorAlias)"
      :groups="groups"
      :group-name-by-id="groupNameById"
      :models-by-group="modelsByGroup"
      :traffic="trafficByGroupModel"
      :is-reserved="isReservedTier(editorAlias)"
      @saved="refreshAll"
    />

    <!-- 按家族整理: 原"快速整理"tab 的入口, 现在收进主 tab 的一个弹窗,
         页面不再有两个并列的 tab / 两套心智模型。 -->
    <n-modal
      v-model:show="familyModalOpen"
      preset="card"
      :title="t('aliases.browseFamily')"
      style="width: 1100px"
    >
      <AliasQuickSetupTab />
    </n-modal>
  </div>
</template>

<style scoped>
.v5-al-tinylbl {
  font: 500 10px/1 var(--v3-mono);
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--v3-ink-3);
  margin-bottom: 6px;
}

/* Custom Slider Styles */
.v3-custom-slider {
  position: relative;
  height: 85px;
  margin-top: 35px;
  padding: 0 10px;
}
.v3-slider-inner {
  position: relative;
  width: 100%;
  height: 100%;
}
.v3-slider-rail {
  position: absolute;
  top: 40px;
  left: 0;
  right: 0;
  height: 8px;
  background: var(--v3-surface-3);
  border-radius: 4px;
  overflow: hidden;
}
.v3-slider-seg {
  position: absolute;
  top: 0;
  height: 100%;
}
.v3-slider-seg--fast {
  background: var(--v3-ok);
}
.v3-slider-seg--balanced {
  background: var(--v3-warn);
}
.v3-slider-seg--flagship {
  background: var(--v3-danger);
}
.v3-slider-zone {
  position: absolute;
  top: 58px;
  transform: translateX(-50%);
  font: 700 9.5px var(--v3-mono);
  color: var(--v3-ink-3);
  letter-spacing: 0.08em;
  pointer-events: none;
  white-space: nowrap;
}
.v3-slider-val {
  position: absolute;
  top: 2px;
  transform: translateX(-50%);
  background: var(--v3-chrome);
  color: var(--v3-chrome-ink);
  padding: 2px 6px;
  border-radius: 4px;
  font: 700 11px var(--v3-mono);
  pointer-events: none;
  z-index: 15;
}
.v3-slider-val::after {
  content: "";
  position: absolute;
  bottom: -4px;
  left: 50%;
  transform: translateX(-50%);
  border-left: 4px solid transparent;
  border-right: 4px solid transparent;
  border-top: 4px solid var(--v3-chrome);
}
.v3-slider-overlay {
  position: absolute;
  top: 33px;
  left: 0;
  right: 0;
  z-index: 20;
}
:deep(.n-slider-rail),
:deep(.n-slider-rail__fill),
:deep(.n-slider-handle),
.v3-slider-seg,
.v3-slider-zone,
.v3-slider-val {
  transition: none !important;
}
:deep(.n-slider-rail) {
  background-color: transparent !important;
}
:deep(.n-slider-rail__fill) {
  background-color: transparent !important;
}
:deep(.n-slider-handle) {
  border: 2px solid var(--v3-chrome) !important;
  background-color: #fff !important;
  box-shadow: var(--v3-shadow-md) !important;
}
:deep(.v3-slider-overlay .n-slider-handle:nth-child(1)) {
  border-color: var(--v3-ok) !important;
}
:deep(.v3-slider-overlay .n-slider-handle:nth-child(2)) {
  border-color: var(--v3-warn) !important;
}
.v3-slider-legend {
  position: absolute;
  top: 38px;
  font: 500 9px var(--v3-mono);
  color: var(--v3-ink-4);
  pointer-events: none;
}
.v3-slider-legend:first-of-type {
  left: -6px;
}
.v3-slider-legend:last-of-type {
  right: -6px;
}

/* One-line Card Layout */
.v5-alias-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(30%, 1fr));
  gap: 12px;
  grid-auto-rows: auto;
}
.v3-tier {
  background: var(--v3-surface);
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius-md);
  padding: 8px 12px 10px;
  display: flex;
  flex-direction: column;
  position: relative;
  transition: all 120ms;
  height: auto;
  min-height: 0;
}
.v3-tier:hover {
  border-color: var(--v3-line-strong);
  box-shadow: var(--v3-shadow-sm);
}
.v3-tier__band {
  position: absolute;
  top: -1px;
  left: -1px;
  bottom: -1px;
  width: 4px;
  background: var(--v3-line);
  border-radius: var(--v3-radius-md) 0 0 var(--v3-radius-md);
  opacity: 0.5;
}
.v3-tier--simple .v3-tier__band {
  background: var(--v3-ok);
  opacity: 1;
}
.v3-tier--medium .v3-tier__band {
  background: var(--v3-warn);
  opacity: 1;
}
.v3-tier--complex .v3-tier__band {
  background: var(--v3-danger);
  opacity: 1;
}

.v3-tier__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 6px;
}
.v3-tier__head-main {
  flex: 1;
  display: flex;
  align-items: baseline;
  gap: 6px;
  overflow: hidden;
}
.v3-tier__title {
  font: 700 13.5px var(--v3-sans);
  color: var(--v3-ink);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.v3-tier__title--copyable {
  cursor: pointer;
  user-select: none;
  border-radius: 4px;
  padding: 1px 4px;
  margin: -1px -4px;
  transition:
    background 0.15s ease,
    color 0.15s ease;
}
.v3-tier__title--copyable:hover {
  background: var(--v3-ink-7, rgba(127, 127, 127, 0.08));
  color: var(--v3-accent, var(--v3-ink));
}
.v3-tier__title--copyable:focus-visible {
  outline: 2px solid var(--v3-accent, currentColor);
  outline-offset: 2px;
}
.v3-tier__rule {
  font: 600 10px var(--v3-mono);
  color: var(--v3-ink-4);
}

.v3-tier__body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.v3-tier__empty-hint {
  font-size: 11px;
  color: var(--v3-ink-4);
  font-style: italic;
  padding: 2px 0;
}

/* P8.4: suggestion banner head 按钮化 + count chip + 单条 dismiss ✕ */
.v5-suggest-banner__title--toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: transparent;
  border: none;
  padding: 2px 6px;
  margin: -2px -6px;
  border-radius: 4px;
  cursor: pointer;
  color: inherit;
  font: inherit;
  transition: background 0.1s;
}
.v5-suggest-banner__title--toggle:hover {
  background: var(--v3-line-soft, oklch(0.95 0 0));
}
.v5-suggest-banner__count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 18px;
  height: 16px;
  padding: 0 5px;
  border-radius: 8px;
  background: var(--v3-accent-soft, oklch(0.96 0.05 230));
  color: var(--v3-accent, oklch(0.55 0.15 230));
  font: 600 10px var(--v3-mono);
  margin-left: 2px;
}
.v5-suggest-chip__dismiss {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  padding: 0;
  border: 1px solid var(--v3-line);
  border-left: none;
  border-top-right-radius: 6px;
  border-bottom-right-radius: 6px;
  background: var(--v3-surface);
  color: var(--v3-ink-4);
  cursor: pointer;
  transition:
    background 0.1s,
    color 0.1s;
}
.v5-suggest-chip__dismiss:hover {
  background: oklch(from var(--v3-warn, oklch(0.7 0.16 60)) l c h / 0.15);
  color: var(--v3-warn, oklch(0.55 0.18 60));
}
/* 既有 zap (审查) 按钮在中间, 取消右下圆角让 dismiss 接圆 */
.v5-suggest-chip-wrap .v5-suggest-chip__zap {
  border-top-right-radius: 0;
  border-bottom-right-radius: 0;
}
/* P8.15: alias 编辑 modal 内的预览块, 跟 chip 双行布局保持视觉一致 */
/* Custom Picker Styles */
.v3-picker-group-row {
  padding: 12px 16px;
  cursor: pointer;
  transition: all 120ms;
  border-left: 3px solid transparent;
}
.v3-picker-group-row:hover {
  background: var(--v3-surface-3);
}
.v3-picker-group-row--active {
  background: var(--v3-surface);
  border-left-color: var(--v3-info);
}
.v3-picker-model-btn {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 14px;
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  cursor: pointer;
  transition: all 100ms;
  text-align: left;
}
.v3-picker-model-btn:hover {
  border-color: var(--v3-info);
  background: var(--v3-surface);
  box-shadow: var(--v3-shadow-sm);
}
.v3-picker-model-btn__id {
  font: 600 12.5px var(--v3-mono);
  color: var(--v3-ink-2);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}
.v3-picker-model-btn:hover .v3-picker-model-btn__id {
  color: var(--v3-ink);
}
.v3-picker-model-btn__add {
  color: var(--v3-ink-4);
  opacity: 0.5;
}
.v3-picker-model-btn--picked {
  border-color: var(--v3-ok);
  background: var(--v3-ok-soft);
}
.v3-picker-model-btn--picked .v3-picker-model-btn__id {
  color: var(--v3-ok);
}
.v3-picker-model-btn--picked .v3-picker-model-btn__add {
  color: var(--v3-ok);
  opacity: 1;
}
.v3-picker-model-btn--bound,
.v3-picker-model-btn--blocked {
  cursor: not-allowed;
  opacity: 0.55;
  border-style: dashed;
  background: var(--v3-surface);
}
.v3-picker-model-btn--blocked {
  border-color: var(--v3-danger);
}
.v3-picker-model-btn--blocked .v3-picker-model-btn__id {
  color: var(--v3-danger);
  text-decoration: line-through;
}
.v3-picker-model-btn--blocked .v3-picker-model-btn__add {
  color: var(--v3-danger);
}
.v3-picker-model-btn--bound:hover {
  border-color: var(--v3-line);
  box-shadow: none;
}
.v3-picker-model-btn--bound .v3-picker-model-btn__id {
  color: var(--v3-ink-3);
  text-decoration: line-through;
  text-decoration-thickness: 1px;
  text-decoration-color: var(--v3-ink-4);
}
.v3-picker-model-btn--bound .v3-picker-model-btn__add {
  color: var(--v3-ink-4);
}
.v3-picker-model-btn__free {
  font-size: 13px;
  line-height: 1;
  flex-shrink: 0;
  margin-right: 6px;
}

/* Pending picks 暂存区 */
.v3-picker-pending {
  margin-bottom: 10px;
  padding: 10px 12px;
  background: var(--v3-accent-soft);
  border: 1px dashed var(--v3-accent);
  border-radius: 6px;
}
.v3-picker-pending__head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
.v3-picker-pending__lbl {
  font: 700 11px/1 var(--v3-mono);
  color: var(--v3-accent);
  letter-spacing: 0.04em;
  text-transform: uppercase;
}
.v3-picker-pending__hint {
  font: 400 11.5px var(--v3-sans);
  color: var(--v3-ink-3);
}
.v3-picker-pending__list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  max-height: 80px;
  overflow-y: auto;
}
.v3-picker-pending__chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 8px 3px 10px;
  background: var(--v3-bg);
  border: 1px solid var(--v3-accent);
  border-radius: 999px;
  font: 500 11px var(--v3-mono);
}
.v3-picker-pending__chip-grp {
  color: var(--v3-ink-3);
}
.v3-picker-pending__chip-sep {
  color: var(--v3-ink-4);
}
.v3-picker-pending__chip-mod {
  color: var(--v3-ink);
}
.v3-picker-pending__chip-x {
  border: 0;
  background: transparent;
  cursor: pointer;
  padding: 2px;
  border-radius: 3px;
  color: var(--v3-ink-4);
  display: inline-flex;
  align-items: center;
  margin-left: 2px;
}
.v3-picker-pending__chip-x:hover {
  color: var(--v3-danger);
  background: var(--v3-danger-soft);
}

.v3-picker-model-btn:hover .v3-picker-model-btn__add {
  color: var(--v3-info);
  opacity: 1;
}

.v5-alias-card--reserved {
  border-color: oklch(from var(--v3-warn) l c h / 0.2);
  background: oklch(from var(--v3-warn) l c h / 0.02);
}
.v5-alias-card__name-input {
  border: 0;
  outline: none;
  background: transparent;
  color: var(--v3-ink);
  width: 200px;
}
.v5-keycard__iconbtn {
  background: transparent;
  border: 0;
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  color: var(--v3-ink-3);
  display: flex;
  align-items: center;
}
.v5-keycard__iconbtn:hover {
  background: var(--v3-surface-3);
  color: var(--v3-ink);
}
.v5-suggest-banner {
  padding: 12px 14px;
  margin-bottom: 16px;
  background: var(--v3-bg-soft, var(--v3-bg));
  border: 1px solid var(--v3-rule);
  border-radius: var(--v3-radius);
}
.v5-suggest-banner__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}
.v5-suggest-banner__title {
  font: 500 12px/1.4 var(--v3-sans);
  color: var(--v3-ink-2);
}
.v5-suggest-banner__close {
  border: none;
  background: transparent;
  color: var(--v3-ink-3);
  cursor: pointer;
  padding: 2px 4px;
  border-radius: 3px;
  display: flex;
  align-items: center;
}
.v5-suggest-banner__close:hover {
  background: var(--v3-surface-3, var(--v3-bg));
  color: var(--v3-ink);
}
.v5-suggest-banner__list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.v5-suggest-chip {
  padding: 4px 8px;
  border: 1px solid var(--v3-rule);
  border-radius: 4px;
  background: var(--v3-bg);
  cursor: pointer;
  font: 500 11px/1 var(--v3-mono);
  color: var(--v3-ink);
}
.v5-suggest-chip:hover {
  border-color: var(--v3-accent);
}
.v5-suggest-count {
  margin-left: 4px;
  color: var(--v3-ink-3);
}
.v5-suggest-chip--family {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  padding: 5px 10px;
  border-color: oklch(from var(--v3-accent) l c h / 0.4);
  background: oklch(from var(--v3-accent) l c h / 0.04);
}
.v5-suggest-chip--family:hover {
  border-color: var(--v3-accent);
  background: oklch(from var(--v3-accent) l c h / 0.08);
}
.v5-suggest-chip__family {
  font: 600 12px/1 var(--v3-mono);
  color: var(--v3-accent);
}
.v5-suggest-chip__sub {
  font: 500 10.5px/1 var(--v3-mono);
  color: var(--v3-ink-3);
}
.v5-suggest-chip__pill {
  font: 600 9.5px/1 var(--v3-sans);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 2px 5px;
  border-radius: 3px;
}
.v5-suggest-chip__pill--new {
  color: var(--v3-ok);
  background: oklch(from var(--v3-ok) l c h / 0.12);
}
.v5-suggest-chip__pill--append {
  color: var(--v3-warn);
  background: oklch(from var(--v3-warn) l c h / 0.12);
}
/* P7: family chip 容器 + ⚡ 一键采纳按钮 */
.v5-suggest-chip-wrap {
  display: inline-flex;
  align-items: stretch;
}
.v5-suggest-chip-wrap .v5-suggest-chip {
  border-top-right-radius: 0;
  border-bottom-right-radius: 0;
  border-right-width: 0;
}
.v5-suggest-chip__zap {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0 10px;
  border: 1px solid var(--v3-line);
  border-left: 1px solid var(--v3-line-soft, var(--v3-line));
  border-top-right-radius: 6px;
  border-bottom-right-radius: 6px;
  background: var(--v3-surface);
  color: var(--v3-ink-4);
  cursor: pointer;
  font: 500 11px var(--v3-sans);
  line-height: 1;
  transition:
    background 0.1s,
    color 0.1s;
}
.v5-suggest-chip__zap:hover {
  background: oklch(from var(--v3-accent) l c h / 0.15);
}
.v5-suggest-chip__zap:active {
  background: oklch(from var(--v3-accent) l c h / 0.28);
}
@keyframes v5-alias-glow {
  0% {
    box-shadow: 0 0 0 0 var(--v3-accent);
  }
  50% {
    box-shadow: 0 0 0 6px var(--v3-accent-soft);
  }
  100% {
    box-shadow: 0 0 0 0 transparent;
  }
}
.v5-alias-card--glow {
  animation: v5-alias-glow 1.5s ease-out 1;
  border-color: var(--v3-accent) !important;
}
/* === 折叠后的智能路由状态条 ===
   阈值是"配一次基本不动"的东西, 常驻展开会把别名列表挤到首屏之外。 */
.v3-thresh-card--compact {
  padding: 8px 12px;
  margin-bottom: 16px;
}
.v3-thresh-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.v3-thresh-bar__label {
  font: 600 12.5px var(--v3-sans);
  color: var(--v3-ink);
  white-space: nowrap;
}
.v3-thresh-bar__summary {
  font: 500 10.5px var(--v3-mono);
  color: var(--v3-ink-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}
.v3-thresh-bar__expand {
  margin-left: auto;
  flex-shrink: 0;
}
.v3-thresh-bar__toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.v3-thresh-bar__state {
  font: 500 10.5px var(--v3-mono);
  color: var(--v3-ink-3);
  white-space: nowrap;
}
.v3-thresh-body {
  margin-top: 14px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
/* 建议抽屉的空态 */
.v5-suggest-empty {
  font: 400 12px var(--v3-sans);
  color: var(--v3-ink-4);
  font-style: italic;
  padding: 8px 2px;
}
/* === 视图模式切换 === */
.v3-viewswitch {
  display: inline-flex;
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  overflow: hidden;
}
.v3-viewswitch__btn {
  font: 600 10.5px var(--v3-mono);
  letter-spacing: 0.03em;
  padding: 4px 10px;
  border: none;
  border-right: 1px solid var(--v3-line);
  background: var(--v3-surface);
  color: var(--v3-ink-3);
  cursor: pointer;
  transition:
    background 100ms,
    color 100ms;
}
.v3-viewswitch__btn:last-child {
  border-right: none;
}
.v3-viewswitch__btn:hover {
  background: var(--v3-surface-2);
  color: var(--v3-ink);
}
.v3-viewswitch__btn--active {
  background: var(--v3-accent);
  color: white;
}
</style>
