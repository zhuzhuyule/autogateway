<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { aliasesApi, type ModelAliasRow } from "@/api/aliases";
import KeyCreateDialog from "@/components/keys/KeyCreateDialog.vue";
import KeyDeleteDialog from "@/components/keys/KeyDeleteDialog.vue";
import GroupCopyModal from "@/components/keys/GroupCopyModal.vue";
import GroupFormModal from "@/components/keys/GroupFormModal.vue";
import ModelAliasModal from "@/components/keys/ModelAliasModal.vue";
import V3SubGroupTable from "@/components/v3/V3SubGroupTable.vue";
import { findFreeModel, findProviderByUpstreams, isFree, isRecommended } from "@/data/freeProviders";
import type { APIKey, Group, GroupStatsResponse, KeyStatus, SubGroupInfo } from "@/types/models";
import { appState, triggerSyncOperationRefresh } from "@/utils/app-state";
import { copy as copyToClipboard } from "@/utils/clipboard";
import { getGroupDisplayName, maskKey } from "@/utils/display";
import {
  AddOutline,
  Checkmark,
  CheckmarkCircle,
  CloseOutline,
  CopyOutline,
  CubeOutline,
  DownloadOutline,
  FlashOutline,
  HelpCircleOutline,
  KeyOutline,
  LinkOutline,
  LockClosedOutline,
  OpenOutline,
  PencilOutline,
  PulseOutline,
  RefreshOutline,
  RemoveCircleOutline,
  SearchOutline,
  SettingsOutline,
  Trash,
} from "@vicons/ionicons5";
import { NIcon, NPagination, NSpin, NTooltip, useDialog, useMessage } from "naive-ui";
import FreeBadge from "@/components/common/FreeBadge.vue";
import SpeedBadge from "@/components/common/SpeedBadge.vue";
import CapabilityIcons from "@/components/common/CapabilityIcons.vue";
import ProviderLogo from "@/components/common/ProviderLogo.vue";
import { freeModelsRef, getFreeStatus, lookupRegistry } from "@/api/freemodels";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";

const { t } = useI18n();

interface Props {
  group: Group;
  allGroups?: Group[];
}

interface Emits {
  (e: "refresh"): void;
  (e: "select-group", id: number): void;
}

const props = withDefaults(defineProps<Props>(), { allGroups: () => [] });
const emit = defineEmits<Emits>();

const isAggregate = computed(() => props.group?.group_type === "aggregate");

const message = useMessage();
const dialog = useDialog();
const router = useRouter();
const route = useRoute();

// P8.2: alias 失效项跳来 (?highlight=MODEL) → scroll 到 model card + 高亮 1.5s.
// 用 nextTick + setTimeout(0) 等 v-for 渲染 + filter 应用完, 才能 querySelector
// 到. DOM selector 用 data-model-id 属性 (template 给所有 model card 加).
// P8.3: 单击 model name 复制 ID 到剪贴板, 弹 toast 提示.
// P8.14: 提示简化为 "已复制" 不含 id (长 id 会撑大 toast 且冗余).
async function copyModelId(modelId: string) {
  try {
    await copyToClipboard(modelId);
    message.success(t("v3.modelCopied"));
  } catch {
    message.error(t("common.copyFailed") || "复制失败");
  }
}

// P8.6 修跳转失效 + P8.8 修 retry 重设 tab:
// 之前 retry 每 200ms 都重新 set tab.value="keys", 用户手动切 tab 立刻
// 被拉回, UI 看起来"切不动". 改为仅 isFirstAttempt 时设 tab/filter,
// retry 只查 DOM 不动 UI 状态.
let highlightRetryTimer: ReturnType<typeof setTimeout> | null = null;
function highlightModelOnRoute(retriesLeft = 10, isFirstAttempt = true) {
  const target = route.query.highlight;
  if (!target || typeof target !== "string") return;
  // 只在 first attempt 时改 UI 状态. 否则 retry 会反复覆盖用户的 tab 切换.
  if (isFirstAttempt) {
    if (highlightRetryTimer) {
      clearTimeout(highlightRetryTimer);
      highlightRetryTimer = null;
    }
    // P8.11: 切到 models tab (model card 在这里渲染), 不是 keys.
    // standard group: tab="models" → 走 v-else-if="tab === 'models'" 块
    // aggregate group: 同上 (aggregate 的 models tab 也走 models 块)
    tab.value = "models";
    modelSearch.value = "";
    if (availFilter.value !== "all") {
      availFilter.value = "all";
    }
  }
  const el = document.querySelector(`[data-model-id="${CSS.escape(target)}"]`);
  if (!el) {
    if (retriesLeft > 0) {
      highlightRetryTimer = setTimeout(
        () => highlightModelOnRoute(retriesLeft - 1, false),
        200
      );
    } else {
      logrus_warn(`highlight model "${target}" not found in current group`);
    }
    return;
  }
  el.scrollIntoView({ behavior: "smooth", block: "center" });
  el.classList.add("v5-modelcard--highlight");
  setTimeout(() => (el as HTMLElement).classList.remove("v5-modelcard--highlight"), 2400);
  // 清 highlight query 避免下次切 tab 又 trigger, 保留 groupId
  router.replace({
    name: "keys",
    query: { groupId: props.group?.id || "" },
  });
}

function logrus_warn(msg: string) {
  // eslint-disable-next-line no-console
  console.warn("[V3GroupDetail]", msg);
}

const stats = ref<GroupStatsResponse | null>(null);
const statsLoading = ref(false);

type KeyRow = APIKey;

const keys = ref<KeyRow[]>([]);
const keysLoading = ref(false);
const search = ref("");
const statusFilter = ref<"all" | "active" | "invalid">("all");
const page = ref(1);
const pageSize = ref(24);
const total = ref(0);
const totalPages = ref(0);
const showAddKey = ref(false);
const showDeleteKey = ref(false);
const showEditGroup = ref(false);
const showCopyGroup = ref(false);
const showAliasModal = ref(false);
const aliasModalModel = ref<string>("");
// Tab 选择跨分组持久化: 用户在 group A 切到"模型",换到 group B 默认仍是"模型".
// 存 localStorage; 非法值直接降级到 "keys".
type GroupDetailTab = "keys" | "models" | "settings";
const TAB_STORAGE_KEY = "v3-group-detail-tab";
function isValidTab(v: unknown): v is GroupDetailTab {
  return v === "keys" || v === "models" || v === "settings";
}
// P8.10: tab 改为 URL-driven computed. URL.query.tab 是 single source of truth,
// 切 tab → setter 写 router.replace + localStorage, URL 改变自动反向更新 tab.value
// (computed get reactive 依赖 route.query.tab). 切 provider (sidebar) 只改 groupId,
// tab 保留. alias 跳转 ?tab=keys&highlight=MODEL 强制激活 keys.
// 优先级: URL.query.tab > localStorage > 'keys'.
function resolveTabFromRoute(): GroupDetailTab {
  const q = (route.query.tab as string | undefined) || "";
  if (isValidTab(q)) return q;
  try {
    const raw = localStorage.getItem(TAB_STORAGE_KEY);
    if (isValidTab(raw)) return raw;
  } catch {
    /* ignore */
  }
  return "keys";
}
const tab = computed<GroupDetailTab>({
  get() {
    return resolveTabFromRoute();
  },
  set(v: GroupDetailTab) {
    if (!isValidTab(v)) return;
    try {
      localStorage.setItem(TAB_STORAGE_KEY, v);
    } catch {
      /* ignore */
    }
    // 保留其他 query (groupId / highlight / ...), 只覆盖 tab.
    // 当前已经是 v 就不 replace, 避免 router 重复 navigation 报警告.
    if (route.query.tab === v) return;
    router.replace({
      name: "keys",
      query: { ...route.query, tab: v },
    });
  },
});
const faviconFailed = ref(false);

// Aggregate-specific
const subGroups = ref<SubGroupInfo[]>([]);
const subGroupsLoading = ref(false);

// Per-key UI state
// 用 Set 记录所有"正在测试/验证中"的 key —— 批量验证(测试全部)时每一把都转圈,
// 而非只显示最后一个(单值 ref 的老 bug)。
const testingKeyIds = ref<Set<number>>(new Set());
const confirmingDeleteId = ref<number | null>(null);
let confirmDeleteTimer: number | undefined;
// 复制成功后短暂把复制图标切成对勾, 给即时反馈 (toast 之外)
const copiedKeyId = ref<number | null>(null);
let copiedTimer: number | undefined;
// 测试完成后卡片轻微高亮一下, 让"刚更新"可感知
const flashKeyId = ref<number | null>(null);
let flashTimer: number | undefined;
function flashKey(id: number) {
  flashKeyId.value = id;
  if (flashTimer) {
    clearTimeout(flashTimer);
  }
  flashTimer = window.setTimeout(() => {
    if (flashKeyId.value === id) {
      flashKeyId.value = null;
    }
  }, 900);
}

// Aliases (loaded once for the Models tab)
const aliases = ref<ModelAliasRow[]>([]);
async function loadAliases() {
  try {
    const res = await aliasesApi.list();
    aliases.value = (res.data as unknown as ModelAliasRow[]) || [];
  } catch (e) {
    console.error("load aliases failed", e);
    aliases.value = [];
  }
}

function aliasesFor(modelId: string): ModelAliasRow[] {
  if (!props.group?.id) {
    return [];
  }
  return aliases.value
    .filter(a => a.group_id === props.group.id && a.real_model === modelId && a.enabled)
    .sort((a, b) => b.weight - a.weight);
}

function goToAliases(aliasName?: string) {
  router.push({ name: "aliases", query: aliasName ? { alias: aliasName } : undefined });
}

function addAliasFor(modelId: string) {
  aliasModalModel.value = modelId;
  showAliasModal.value = true;
}

function onAliasCreated() {
  showAliasModal.value = false;
  loadAliases();
}

async function loadSubGroups() {
  if (!isAggregate.value || !props.group?.id) {
    subGroups.value = [];
    return;
  }
  subGroupsLoading.value = true;
  try {
    subGroups.value = await keysApi.getSubGroups(props.group.id);
  } catch (e) {
    console.error("load sub-groups failed", e);
    subGroups.value = [];
  } finally {
    subGroupsLoading.value = false;
  }
}

// P6: aggregate sub-group 健康度 (latency EWMA / effective_weight / cooldown / 熔断)
// 10s poll, 仅 aggregate group + sub-group tab 时拉取.
type SubGroupHealthRow = Awaited<ReturnType<typeof keysApi.getGroupHealth>>["sub_groups"];
const healthRows = ref<NonNullable<SubGroupHealthRow>>([]);
const healthLoading = ref(false);
let healthPollTimer: number | null = null;
async function loadHealth() {
  if (!isAggregate.value || !props.group?.id) {
    healthRows.value = [];
    return;
  }
  healthLoading.value = true;
  try {
    const r = await keysApi.getGroupHealth(props.group.id);
    healthRows.value = r.sub_groups || [];
  } catch (e) {
    console.error("load health failed", e);
  } finally {
    healthLoading.value = false;
  }
}
function startHealthPoll() {
  stopHealthPoll();
  if (!isAggregate.value) return;
  void loadHealth();
  healthPollTimer = window.setInterval(() => void loadHealth(), 10000);
}
function stopHealthPoll() {
  if (healthPollTimer !== null) {
    window.clearInterval(healthPollTimer);
    healthPollTimer = null;
  }
}
onMounted(() => {
  startHealthPoll();
  // P8.9 修 TDZ: 之前 watch immediate:true 在 setup 早期跑, 访问还没声明
  // 的 modelSearch / availFilter ref → ReferenceError. 改 onMounted 时
  // 调一次 (setup 已完成所有 ref init), 后续靠下面的 non-immediate watch
  // 处理 group/highlight 变化.
  if (props.group?.id && route.query.highlight) {
    highlightModelOnRoute();
  }
});

// P8.6: group 注入后或 query.highlight 变化时, 尝试 highlight + scroll.
// 不用 immediate (TDZ), onMounted 兜底初次调用.
watch(
  () => [props.group?.id, route.query.highlight] as const,
  ([gid, hl]) => {
    if (gid && hl) highlightModelOnRoute();
  }
);
onBeforeUnmount(stopHealthPoll);
watch(
  () => [props.group?.id, isAggregate.value] as [number | undefined, boolean],
  () => {
    startHealthPoll();
  }
);

function formatLatency(ms: number): string {
  if (!ms || ms <= 0) return "—";
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}
function cooldownRemain(until?: string): string {
  if (!until) return "";
  const ms = new Date(until).getTime() - Date.now();
  if (ms <= 0) return "";
  if (ms < 60000) return `${Math.ceil(ms / 1000)}s`;
  return `${Math.ceil(ms / 60000)}m`;
}

const matchedProvider = computed(() => findProviderByUpstreams(props.group?.upstreams || []));

const proxyUrl = computed(() => {
  if (!props.group) {
    return "";
  }
  const channel = props.group.channel_type;
  const path =
    channel === "anthropic"
      ? "/anthropic/v1/messages"
      : channel === "gemini"
        ? `/gemini/v1beta/models/${props.group.test_model || "gemini-2.5-flash"}:generateContent`
        : "/openai/v1/chat/completions";
  if (props.group.is_system) {
    return `/proxy${path}`;
  }
  return `/proxy/${props.group.name}${path}`;
});

// Short SDK base_url shown in the hero — drops channel-specific suffix
// (e.g. /chat/completions). Matches v5 banner pattern.
const sdkBaseUrl = computed(() => {
  if (!props.group) {
    return "";
  }
  const ch = props.group.channel_type;
  const apiRoot = ch === "gemini" ? "v1beta" : "v1";
  const host = `${window.location.protocol}//${window.location.host}`;
  if (props.group.is_system) {
    return `${host}/proxy/${ch}/${apiRoot}`;
  }
  return `${host}/proxy/${props.group.name}/${apiRoot}`;
});

const friendlyHint = computed(() => {
  const ch = props.group?.channel_type;
  if (isAggregate.value) {
    if (props.group?.is_system) {
      if (ch === "anthropic") {
        return t("v5.hintSystemAnthropic");
      }
      if (ch === "gemini") {
        return t("v5.hintSystemGemini");
      }
      return t("v5.hintSystemOpenAI");
    }
    return t("v5.hintAggregate");
  }
  if (ch === "anthropic") {
    return t("v5.hintAnthropic");
  }
  if (ch === "gemini") {
    return t("v5.hintGemini");
  }
  return t("v5.hintOpenAI");
});

function extractHost(url?: string): string | null {
  if (!url) {
    return null;
  }
  try {
    return new URL(url).hostname;
  } catch {
    return null;
  }
}

const providerHint = computed(() => {
  const g = props.group;
  if (!g) {
    return "";
  }
  return [
    g.system_role || "",
    g.name || "",
    extractHost(g.upstreams?.[0]?.url) || "",
  ]
    .filter(Boolean)
    .join(" ");
});

watch(
  () => props.group?.id,
  () => {
    faviconFailed.value = false;
  }
);

async function loadStats() {
  if (!props.group?.id) {
    stats.value = null;
    return;
  }
  statsLoading.value = true;
  try {
    stats.value = await keysApi.getGroupStats(props.group.id);
  } catch (e) {
    console.error(e);
  } finally {
    statsLoading.value = false;
  }
}

async function loadKeys() {
  if (!props.group?.id) {
    keys.value = [];
    return;
  }
  keysLoading.value = true;
  try {
    const res = await keysApi.getGroupKeys({
      group_id: props.group.id,
      page: page.value,
      page_size: pageSize.value,
      status: statusFilter.value === "all" ? undefined : (statusFilter.value as KeyStatus),
      key_value: search.value.trim() || undefined,
    });
    keys.value = (res.items || []) as KeyRow[];
    total.value = res.pagination.total_items;
    totalPages.value = res.pagination.total_pages;
  } finally {
    keysLoading.value = false;
  }
}

watch(
  () => props.group?.id,
  () => {
    page.value = 1;
    statusFilter.value = "all";
    search.value = "";
    // 不重置 tab — 保留用户上一次选中的 tab (跨 group 持久化, 见 readPersistedTab).
    confirmingDeleteId.value = null;
    loadStats();
    loadAliases();
    if (isAggregate.value) {
      keys.value = [];
      total.value = 0;
      totalPages.value = 0;
      loadSubGroups();
    } else {
      subGroups.value = [];
      loadKeys();
    }
  },
  { immediate: true }
);

watch([page, statusFilter], () => loadKeys());

watch(
  () => appState.groupDataRefreshTrigger,
  () => {
    if (appState.lastCompletedTask && props.group?.name === appState.lastCompletedTask.groupName) {
      testingKeyIds.value.clear(); // 批量验证任务完成 → 清除所有 per-key loading
      loadStats();
      loadKeys();
    }
  }
);

let searchTimer: number | undefined;
function onSearchInput() {
  clearTimeout(searchTimer);
  searchTimer = window.setTimeout(() => {
    page.value = 1;
    loadKeys();
  }, 250);
}

function refreshAll() {
  loadStats();
  loadKeys();
  message.success(t("common.refresh"));
}

// === Models tab ===
function parseAvailableModels(raw: unknown): string[] {
  if (Array.isArray(raw)) {
    return raw.filter((m): m is string => typeof m === "string");
  }
  if (typeof raw === "string" && raw.trim().length > 0) {
    try {
      const arr = JSON.parse(raw);
      return Array.isArray(arr) ? arr.filter((m): m is string => typeof m === "string") : [];
    } catch {
      return [];
    }
  }
  return [];
}

// 上游 registry 知道但用户 group 没拉到的同 provider 模型 — 取并集.
// 上游 /v1/models 通常只返回 key 已订阅 / 推荐的少量模型 (如智谱只返回旗舰,
// 不返回 GLM-Flash 系列), 但 FreeModels Registry 里有完整名单. 合并显示让
// 用户能看到 registry 标了"免费"的模型并加入暴露/别名.
function mergeWithRegistry(localList: string[]): string[] {
  const env = freeModelsRef.value;
  const fmId = matchedProvider.value?.id;
  if (!env?.models || !fmId) {
    return localList;
  }
  const set = new Set<string>(localList);
  for (const m of env.models) {
    if (m.provider !== fmId) {
      continue;
    }
    // registry modelId 多带 "<provider>/" 前缀 — 剥掉跟 group available_models
    // 的裸 id 对齐.
    const slash = m.modelId.indexOf("/");
    const bare =
      slash > 0 && m.modelId.slice(0, slash).toLowerCase() === fmId.toLowerCase()
        ? m.modelId.slice(slash + 1)
        : m.modelId;
    set.add(bare);
  }
  return Array.from(set).sort();
}

// 子分组对聚合的真实贡献:
//   - exposed_models 非空 → 视为白名单 (无视 mode), 与 GroupInfoCard 行为一致
//   - 否则用 available_models
//   - 都减 blocked_models
function effectiveModelsFor(g: Group): string[] {
  const exposed = parseAvailableModels(
    (g as unknown as { exposed_models?: unknown }).exposed_models
  );
  const list = exposed.length > 0
    ? exposed
    : parseAvailableModels((g as unknown as { available_models?: unknown }).available_models);
  const blocked = new Set(
    parseAvailableModels((g as unknown as { blocked_models?: unknown }).blocked_models)
  );
  return blocked.size === 0 ? list : list.filter(m => !blocked.has(m));
}

// 系统聚合 (default-openai/anthropic/gemini) 走 chat completions, 子分组上游
// /v1/models 把 image/audio/embedding/rerank/upscale/tts 等也缓存进了
// available_models — 在聚合视图里展示就是误导, 客户端调过去 100% 报错.
// 这里用关键词启发式过滤. unknown 默认放行避免误伤.
const NON_CHAT_PATTERNS: RegExp[] = [
  /(^|[\W_-])tts([\W_-]|$)/i,
  /(^|[\W_-])asr([\W_-]|$)/i,
  /(^|[\W_-])stt([\W_-]|$)/i,
  /whisper/i,
  /embedding/i,
  /(^|[\W_-])embed([\W_-]|$)/i,
  /rerank/i,
  /upscal/i,
  /(^|[\W_-])(4x|8x)[\W_-]/i,
  /esrgan/i,
  /animesharp/i,
  /(^|[\W_-])sd([\W_-]|$)/i,
  /sdxl/i,
  /flux/i,
  /dall[\W_-]?e/i,
  /stable[\W_-]?diffusion/i,
  /controlnet/i,
  /animatediff/i,
  /(^|[\W_-])lcm([\W_-]|$)/i,
  /kandinsky/i,
  /kolors/i,
  /hunyuan[\W_-]?image/i,
  /qwen[\W_-]?image/i,
  /cogview/i,
  /(^|[\W_-])wan[\W_-]/i,
  /playground[\W_-]?v/i,
  /musicgen/i,
  /audiogen/i,
  /audiofly/i,
  /animefly/i,
  /ace[\W_-]?step/i,
  /(^|[\W_-])suno([\W_-]|$)/i,
  /kokoro/i,
  /(^|[\W_-])vits([\W_-]|$)/i,
  /sadtalker/i,
  /clip[\W_-]/i,
  /(^|[\W_-])moderation([\W_-]|$)/i,
];

function isLikelyChatModel(id: string): boolean {
  for (const p of NON_CHAT_PATTERNS) {
    if (p.test(id)) {
      return false;
    }
  }
  return true;
}

const isSystemAggregate = computed<boolean>(() => {
  const role = (props.group as unknown as { system_role?: string } | null | undefined)
    ?.system_role;
  return role === "default-openai" || role === "default-anthropic" || role === "default-gemini";
});

const groupModels = computed<string[]>(() => {
  if (isAggregate.value) {
    const chatOnly = isSystemAggregate.value;
    // key = trim + lowercase 归一化, value 保留首个出现的原始 ID 作展示;
    // 同一 model id 大小写或带空白不一致也合并成一行.
    const map = new Map<string, string>();
    for (const sg of subGroups.value) {
      for (const raw of effectiveModelsFor(sg.group)) {
        const id = (raw || "").trim();
        if (!id) {
          continue;
        }
        if (chatOnly && !isLikelyChatModel(id)) {
          continue;
        }
        const key = id.toLowerCase();
        if (!map.has(key)) {
          map.set(key, id);
        }
      }
    }
    return Array.from(map.values()).sort();
  }
  const raw = (props.group as unknown as { available_models?: unknown })?.available_models;
  const cached = parseAvailableModels(raw);
  let base = cached;
  if (!base.length) {
    // Fallback:无 /v1/models 端点的 provider (e.g. LongCat),老分组 cache 为空时
    // 用 provider 静态清单兜底,避免空 UI。
    const p = matchedProvider.value;
    if (p?.staticModels) {
      base = [...p.models, ...(p.paidModels ?? [])];
    }
  }
  return mergeWithRegistry(base);
});

// Aggregate-only stats
const aggKeyTotal = computed(() =>
  subGroups.value.reduce((s: number, sg: SubGroupInfo) => s + (sg.active_keys || 0), 0)
);
const aggKeyInvalid = computed(() =>
  subGroups.value.reduce((s: number, sg: SubGroupInfo) => s + (sg.invalid_keys || 0), 0)
);

// Notification when sub-group operations succeed inside V3SubGroupTable
function onSubGroupRefresh() {
  loadStats();
  loadSubGroups();
}

function onSubGroupSelect(id: number) {
  emit("select-group", id);
}

const modelsRefreshedAtDisplay = computed(() => {
  const at = (props.group as unknown as { models_refreshed_at?: string | null })
    ?.models_refreshed_at;
  if (!at) {
    return "";
  }
  try {
    return new Date(at).toLocaleString();
  } catch {
    return at;
  }
});

// === 刷新模型 ===
const modelsRefreshing = ref(false);

async function refreshGroupModels() {
  if (!props.group?.id || modelsRefreshing.value) {
    return;
  }
  modelsRefreshing.value = true;
  try {
    const res = await keysApi.refreshModels(props.group.id);
    const n = res?.count ?? res?.models?.length ?? 0;
    message.success(t("keys.refreshModelsSuccess", { n }));
    emit("refresh");
  } catch (e) {
    message.error((e as Error).message || t("keys.refreshModelsFail"));
  } finally {
    modelsRefreshing.value = false;
  }
}

// 速率档位优先取自 FreeModels Registry (无客户端推断)。
// registry miss 时(模型未收录) → 内置清单 → 关键字推断 → 默认 balanced.
const FAST_KEYWORDS = ["flash", "haiku", "8b", "instant", "mini", "small"];
const SLOW_KEYWORDS = ["pro", "opus", "sonnet", "70b", "405b", "4o", "o1", "r1"];

interface SpeedInference {
  speed: "fast" | "balanced" | "slow";
  source: "registry" | "preset" | "keyword" | "default";
  reason: string;
}

function inferSpeed(modelId: string): SpeedInference {
  // Tier 0: registry 直读 (FreeModels Hub 数据源, 单一可信源)
  const reg = lookupRegistry(matchedProvider.value?.id, modelId);
  if (reg?.speed && (reg.speed === "fast" || reg.speed === "balanced" || reg.speed === "slow")) {
    return {
      speed: reg.speed,
      source: "registry",
      reason: `tier=${reg.tier || "?"}${reg.contextLabel ? " · " + reg.contextLabel : ""}${reg.isReasoning ? " · reasoning" : ""}`,
    };
  }
  // Tier 1: 内置静态清单 (preset.tier 是老 fast/balanced/max 三档)
  const preset = findFreeModel(modelId);
  if (preset?.tier) {
    const speed: "fast" | "balanced" | "slow" =
      preset.tier === "fast" ? "fast" : preset.tier === "max" ? "slow" : "balanced";
    return {
      speed,
      source: "preset",
      reason: preset.notes || `内置清单 (${preset.providerId})`,
    };
  }
  // Tier 2: 关键字推断 (兜底)
  const id = modelId.toLowerCase();
  for (const kw of FAST_KEYWORDS) {
    if (id.includes(kw)) {
      return { speed: "fast", source: "keyword", reason: `名称含 "${kw}" → fast` };
    }
  }
  for (const kw of SLOW_KEYWORDS) {
    if (id.includes(kw)) {
      return { speed: "slow", source: "keyword", reason: `名称含 "${kw}" → slow` };
    }
  }
  return { speed: "balanced", source: "default", reason: "无信息,默认 balanced" };
}

function speedForModel(modelId: string): "fast" | "balanced" | "slow" {
  return inferSpeed(modelId).speed;
}

function speedReasonFor(modelId: string): string {
  const inf = inferSpeed(modelId);
  const sourceLabel =
    inf.source === "registry"
      ? t("v3.tierFromRegistry")
      : inf.source === "preset"
        ? t("v3.tierFromPreset")
        : inf.source === "keyword"
          ? t("v3.tierFromKeyword")
          : t("v3.tierDefault");  return `${sourceLabel}: ${inf.reason}`;
}

function isFreeModel(modelId: string): boolean {
  return isFree(matchedProvider.value?.id, modelId) === true;
}

// ⭐推荐徽标:单分组按 matchedProvider 判;聚合分组遍历子分组, 任一 sub 的 provider
// 推荐该 model 即视为推荐 (model card 上会带 ⭐ 徽标).
function isRecommendedModel(modelId: string): boolean {
  if (isAggregate.value) {
    for (const sg of subGroups.value) {
      const p = findProviderByUpstreams(sg.group?.upstreams || []);
      if (p?.id && isRecommended(p.id, modelId)) return true;
    }
    return false;
  }
  const pid = matchedProvider.value?.id;
  if (!pid) return false;
  return isRecommended(pid, modelId);
}

// 注: freeVariantFor 已被简化的 FreeBadge 替代 (v3.free 单一显示),
// 但 isFreeModel 仍用于过滤 (free/paid 列表). 保留下行 export 给可能的
// 其它消费者; 当前文件内部不再调用 (避免 TS6133 unused 错).

// === Per-model 连通性测试 ===
// 调用 POST /api/groups/:id/test-model, 后端用一个活跃 key 跑一次最小载荷的
// ValidateKey (aggregate 会先解析到 sub-group). 测试不会标记 key invalid.
const testingModels = ref<Set<string>>(new Set());
type ModelTestState = {
  ok: boolean;
  ms: number;
  statusCode: number;
  error?: string;
  resolved?: string;
  viaAgg?: boolean;
  // localStorage 持久化时间戳, 让 hover tooltip 能显示 "5 分钟前测过"
  testedAt?: number;
};
const modelTestResults = ref<Record<string, ModelTestState>>({});

// localStorage key: 按 group_id 隔离, 切换分组互不干扰
function testResultsKey(gid: number | undefined): string {
  return `model_test_results_${gid ?? "none"}`;
}

// 切换 group 时从 localStorage 读上次结果. 没数据就空 map, 用户重测.
watch(
  () => props.group?.id,
  (newId) => {
    if (!newId) {
      modelTestResults.value = {};
      return;
    }
    try {
      const raw = localStorage.getItem(testResultsKey(newId));
      modelTestResults.value = raw ? (JSON.parse(raw) as Record<string, ModelTestState>) : {};
    } catch {
      modelTestResults.value = {};
    }
  },
  { immediate: true },
);

function persistTestResults() {
  const gid = props.group?.id;
  if (!gid) {
    return;
  }
  try {
    localStorage.setItem(testResultsKey(gid), JSON.stringify(modelTestResults.value));
  } catch {
    /* quota / private mode — silent */
  }
}

function modelTestState(modelId: string): ModelTestState | undefined {
  return modelTestResults.value[modelId];
}

// hover tooltip 文本: 成功/失败 + 耗时 + 状态码 + error + 多久前
function modelTestTooltip(modelId: string): string {
  const s = modelTestResults.value[modelId];
  if (!s) {
    return t("v3.testModel");
  }
  const parts: string[] = [];
  parts.push(s.ok ? "✓ OK" : "✗ FAIL");
  if (s.statusCode) {
    parts.push(`HTTP ${s.statusCode}`);
  }
  parts.push(`${s.ms}ms`);
  if (s.error) {
    parts.push(s.error);
  }
  if (s.testedAt) {
    const ago = Date.now() - s.testedAt;
    const min = Math.round(ago / 60000);
    if (min < 1) {
      parts.push(t("v3.testJustNow"));
    } else if (min < 60) {
      parts.push(t("v3.testMinAgo", { n: min }));
    } else {
      parts.push(t("v3.testHourAgo", { n: Math.round(min / 60) }));
    }
  }
  return parts.join(" · ");
}

async function testModel(modelId: string) {
  if (!props.group?.id) {
    return;
  }
  if (testingModels.value.has(modelId)) {
    return;
  }
  testingModels.value.add(modelId);
  try {
    const res = await keysApi.testGroupModel(props.group.id, modelId);
    modelTestResults.value = {
      ...modelTestResults.value,
      [modelId]: {
        ok: res.is_valid,
        ms: res.duration_ms,
        statusCode: res.status_code,
        error: res.is_valid ? undefined : res.error || `HTTP ${res.status_code}`,
        resolved: res.resolved_group,
        viaAgg: res.is_via_aggregate,
        testedAt: Date.now(),
      },
    };
    persistTestResults();
    if (res.is_valid) {
      message.success(t("v3.modelTestOk", { model: modelId, ms: res.duration_ms }));
    } else {
      message.error(t("v3.modelTestFail", { model: modelId, err: res.error || `HTTP ${res.status_code}` }));
    }
  } catch (e) {
    const err = (e as Error).message || String(e);
    modelTestResults.value = {
      ...modelTestResults.value,
      [modelId]: { ok: false, ms: 0, statusCode: 0, error: err, testedAt: Date.now() },
    };
    persistTestResults();
    message.error(err);
  } finally {
    testingModels.value.delete(modelId);
  }
}

// P11.38: 批量/一键测试 — 控制并发度防止打爆上游 + 后端
const bulkTesting = ref(false);
const BULK_TEST_CONCURRENCY = 3;

async function bulkTestModels(modelIds: string[]) {
  if (bulkTesting.value || modelIds.length === 0) return;
  // 跳过 in-flight, 其它的都重测一次 (用户 click 这个按钮就是想刷新所有状态)
  const queue = modelIds.filter(m => !testingModels.value.has(m));
  if (queue.length === 0) return;
  bulkTesting.value = true;
  message.info(t("v3.bulkTestStart", { n: queue.length }));
  try {
    let idx = 0;
    const workers = Array.from({ length: Math.min(BULK_TEST_CONCURRENCY, queue.length) }, async () => {
      // 简单 worker pool: 每个 worker 不断从队头取
      while (idx < queue.length) {
        const myIdx = idx++;
        const modelId = queue[myIdx];
        // testModel 自带防重复; 我们这里只调它即可, 失败也不阻塞下一项
        try {
          await testModel(modelId);
        } catch {
          /* 单项失败 testModel 内部已经写了 result, 这里继续下一个 */
        }
      }
    });
    await Promise.all(workers);
    // 统计成功/失败 — 给用户一个完成 toast
    const ok = queue.filter(m => modelTestResults.value[m]?.ok === true).length;
    const fail = queue.length - ok;
    if (fail === 0) {
      message.success(t("v3.bulkTestDoneOk", { n: ok }));
    } else {
      message.warning(t("v3.bulkTestDoneMixed", { ok, fail }));
    }
  } finally {
    bulkTesting.value = false;
  }
}

// tab 角标:specified 模式展示白名单数量(强调已暴露这件事);
// passthrough / 聚合分组展示上游全量数。
const modelCount = computed(() => {
  if (!isAggregate.value && routingMode.value === "specified") {
    return exposedModels.value.length;
  }
  return groupModels.value.length;
});

// === Filters & Persistence ===
const modelSearch = ref("");
const modelFilterFree = ref(false);

// Load persisted filters when group changes
watch(
  () => props.group?.id,
  (newId) => {
    if (!newId) return;
    const saved = localStorage.getItem(`group_models_filter_${newId}`);
    if (saved) {
      try {
        const parsed = JSON.parse(saved);
        modelSearch.value = parsed.search || "";
        modelFilterFree.value = !!parsed.freeOnly;
      } catch { /* ignore */ }
    } else {
      modelSearch.value = "";
      modelFilterFree.value = false;
    }
  },
  { immediate: true }
);

// Save filters on change
watch([modelSearch, modelFilterFree], () => {
  if (!props.group?.id) return;
  localStorage.setItem(`group_models_filter_${props.group.id}`, JSON.stringify({
    search: modelSearch.value,
    freeOnly: modelFilterFree.value
  }));
});


// === Routing mode + exposed_models (specified 模式白名单) ===

type RoutingMode = "passthrough" | "specified";

const routingMode = ref<RoutingMode>("passthrough");
const exposedModels = ref<string[]>([]);
const blockedModels = ref<string[]>([]);
const availFilter = ref<"all" | "free" | "trial" | "paid" | "recommended">("all");

// "推荐" 过滤的可见性 — 单分组看 matchedProvider, aggregate 看是否任一 sub-group
// 能匹配到 free provider. 否则按钮过滤永远空, 没用.
const showRecommendedFilter = computed(() => {
  if (isAggregate.value) {
    return subGroups.value.some(
      sg => !!findProviderByUpstreams(sg.group?.upstreams || [])?.id,
    );
  }
  return !!matchedProvider.value?.id;
});

// 仅 Gitee 显示 "体验" 过滤 (trial-quota 模式只 gitee/longcat/nvidia 有,
// 但只有 gitee 把它作为常态产品定位 — 其它两家也按业务展示)
const showTrialFilter = computed(() => {
  const id = (matchedProvider.value?.id || "").toLowerCase();
  return id === "gitee-ai" || id === "longcat" || id === "nvidia-nim";
});

function modelFreeKind(modelId: string): "full" | "trial" | "paid" | null {
  return getFreeStatus(matchedProvider.value?.id, modelId);
}

// 取 registry 里此模型的 tags (能力分类), 找不到返回空数组
function tagsForModel(modelId: string): string[] {
  const reg = lookupRegistry(matchedProvider.value?.id, modelId);
  return reg?.tags || [];
}
const savingMode = ref(false);

function syncFromGroup() {
  routingMode.value = (props.group?.model_routing_mode as RoutingMode) || "passthrough";
  exposedModels.value = Array.isArray(props.group?.exposed_models)
    ? [...(props.group!.exposed_models as string[])]
    : [];
  blockedModels.value = Array.isArray(props.group?.blocked_models)
    ? [...(props.group!.blocked_models as string[])]
    : [];
}
syncFromGroup();
// 只在切换分组时重置本地状态;同一分组下的服务端更新交给 updateGroup 的
// 响应自行同步 — 否则父组件 refresh 一次会把乐观更新打回旧值。
watch(() => props.group?.id, syncFromGroup);

const exposedSet = computed(() => new Set(exposedModels.value));
const blockedSet = computed(() => new Set(blockedModels.value));

// 已暴露列表(顶部区,specified 模式才显示) — 应用 search + free/paid/recommended 过滤
const filteredExposed = computed(() => {
  let list = exposedModels.value;
  const q = modelSearch.value.toLowerCase().trim();
  if (q) {
    list = list.filter(m => m.toLowerCase().includes(q));
  }
  if (availFilter.value === "free") {
    // "免费" = 凡 isFree=true 都算 (含 NVIDIA trial-credits / LongCat daily-tokens
    // 等配额限免, 用户视角"能免费用的"无需细分). full/trial 区分通过 badge 显示.
    list = list.filter(m => {
      const k = modelFreeKind(m);
      if (k === "full" || k === "trial") return true;
      if (k !== null) return false;
      return isFreeModel(m); // registry miss fallback
    });
  } else if (availFilter.value === "trial") {
    list = list.filter(m => modelFreeKind(m) === "trial");
  } else if (availFilter.value === "paid") {
    list = list.filter(m => {
      const k = modelFreeKind(m);
      if (k === "paid") return true;
      if (k !== null) return false;
      return !isFreeModel(m);
    });
  } else if (availFilter.value === "recommended") {
    list = list.filter(m => isRecommendedModel(m));
  }
  return list;
});

// 上游"未暴露"候选列表(底部区) — 先排除顶部已暴露的(不重复展示),再应用 search + free/paid/recommended 过滤
const filteredAvailable = computed(() => {
  let list = groupModels.value;
  // 仅 specified 分离视图(顶部「已暴露」+ 底部「上游全部」)才把已暴露的从候选池排除:
  // 它们已展示在上方,底部不重复。passthrough / aggregate 是单一「全部模型」列表,
  // 没有顶部区兜底 — 若在此排除, 已暴露模型会凭空消失。与模板分支
  // v-if="!isAggregate && routingMode === 'specified'" 保持一致。
  if (!isAggregate.value && routingMode.value === "specified") {
    const exposed = exposedSet.value;
    list = list.filter(m => !exposed.has(m));
  }
  const q = modelSearch.value.toLowerCase().trim();
  if (q) {
    list = list.filter(m => m.toLowerCase().includes(q));
  }
  if (availFilter.value === "free") {
    // "免费" = 凡 isFree=true 都算 (含 NVIDIA trial-credits / LongCat daily-tokens
    // 等配额限免, 用户视角"能免费用的"无需细分). full/trial 区分通过 badge 显示.
    list = list.filter(m => {
      const k = modelFreeKind(m);
      if (k === "full" || k === "trial") return true;
      if (k !== null) return false;
      return isFreeModel(m); // registry miss fallback
    });
  } else if (availFilter.value === "trial") {
    list = list.filter(m => modelFreeKind(m) === "trial");
  } else if (availFilter.value === "paid") {
    list = list.filter(m => {
      const k = modelFreeKind(m);
      if (k === "paid") return true;
      if (k !== null) return false;
      return !isFreeModel(m);
    });
  } else if (availFilter.value === "recommended") {
    list = list.filter(m => isRecommendedModel(m));
  }
  return list;
});

// specified 模式:上游模型是否已全部进白名单 — 底部候选池空的判定,区别于"搜索无匹配"
const allUpstreamExposed = computed(
  () => groupModels.value.length > 0 && groupModels.value.every(m => exposedSet.value.has(m)),
);

async function persistGroupPatch(patch: {
  model_routing_mode?: RoutingMode;
  exposed_models?: string[];
  blocked_models?: string[];
}) {
  if (!props.group?.id) {
    return;
  }
  try {
    const updated = (await keysApi.updateGroup(
      props.group.id,
      patch
    )) as unknown as {
      model_routing_mode?: string;
      exposed_models?: string[] | null;
    };
    // 检测 schema 漂移:后端没识别新字段时返回的 group 保留旧值,
    // 此时不要让父级 refresh 把乐观状态打回去。
    const modeOk =
      patch.model_routing_mode === undefined ||
      updated.model_routing_mode === patch.model_routing_mode;
    const exposedOk =
      patch.exposed_models === undefined ||
      JSON.stringify(updated.exposed_models || []) ===
        JSON.stringify(patch.exposed_models);
    const blockedOk =
      patch.blocked_models === undefined ||
      JSON.stringify((updated as { blocked_models?: string[] }).blocked_models || []) ===
        JSON.stringify(patch.blocked_models);
    if (!modeOk || !exposedOk || !blockedOk) {
      message.warning(
        t("v3.routingPersistDrift") ||
          "后端未识别新字段,请重启服务并刷新 — 改动暂存于本地"
      );
      return; // 保留乐观值, 不发 refresh
    }
    emit("refresh");
  } catch (e) {
    message.error((e as Error).message || (t("common.requestFailed") as string));
    syncFromGroup(); // 真错才回滚
  }
}

async function setRoutingMode(mode: RoutingMode) {
  if (savingMode.value || routingMode.value === mode) {
    return;
  }
  savingMode.value = true;
  routingMode.value = mode; // optimistic
  try {
    await persistGroupPatch({ model_routing_mode: mode });
  } finally {
    savingMode.value = false;
  }
}

async function addToExposed(modelId: string) {
  if (exposedSet.value.has(modelId)) {
    return;
  }
  const next = [...exposedModels.value, modelId];
  exposedModels.value = next; // optimistic
  await persistGroupPatch({ exposed_models: next });
}

async function removeFromExposed(modelId: string) {
  const next = exposedModels.value.filter(m => m !== modelId);
  exposedModels.value = next; // optimistic
  await persistGroupPatch({ exposed_models: next });
}

// 黑名单 toggle:加入 blocked_models 后,即使在 exposed 或 alias 里也不会被路由
// === 多选 + 批量操作 ===========================================================
// selectedModels 跟踪用户在 specified 已暴露 / 上游全部 / passthrough 全部 区
// 勾选的 modelId. 整张卡片 click toggle.
const selectedModels = ref<Set<string>>(new Set());
function toggleSelected(modelId: string) {
  const s = new Set(selectedModels.value);
  if (s.has(modelId)) {
    s.delete(modelId);
  } else {
    s.add(modelId);
  }
  selectedModels.value = s;
}
function isSelected(modelId: string): boolean {
  return selectedModels.value.has(modelId);
}
function clearSelection() {
  selectedModels.value = new Set();
}

async function bulkAddToExposed() {
  const toAdd = Array.from(selectedModels.value).filter(m => !exposedSet.value.has(m));
  if (!toAdd.length) {
    return;
  }
  const next = [...exposedModels.value, ...toAdd];
  exposedModels.value = next;
  await persistGroupPatch({ exposed_models: next });
  message.success(t("v5.bulkAddedExposed", { n: toAdd.length }) || `已加入 ${toAdd.length} 个`);
  clearSelection();
}

async function bulkRemoveFromExposed() {
  const toRemove = selectedModels.value;
  if (!toRemove.size) {
    return;
  }
  const next = exposedModels.value.filter(m => !toRemove.has(m));
  const removed = exposedModels.value.length - next.length;
  if (!removed) {
    return;
  }
  exposedModels.value = next;
  await persistGroupPatch({ exposed_models: next });
  message.success(t("v5.bulkRemovedExposed", { n: removed }) || `已移除 ${removed} 个`);
  clearSelection();
}

async function bulkToggleBlock(addNotRemove: boolean) {
  const targets = selectedModels.value;
  if (!targets.size) {
    return;
  }
  const cur = new Set(blockedModels.value);
  let changed = 0;
  for (const m of targets) {
    if (addNotRemove && !cur.has(m)) {
      cur.add(m);
      changed += 1;
    } else if (!addNotRemove && cur.has(m)) {
      cur.delete(m);
      changed += 1;
    }
  }
  if (!changed) {
    return;
  }
  const next = Array.from(cur);
  blockedModels.value = next;
  await persistGroupPatch({ blocked_models: next });
  const key = addNotRemove ? "v5.bulkBlocked" : "v5.bulkUnblocked";
  const fb = addNotRemove ? `已加入黑名单 ${changed} 个` : `已解除拉黑 ${changed} 个`;
  message.success(t(key, { n: changed }) || fb);
  clearSelection();
}

// 选中至少一个 还未暴露 → 显示"加入暴露"
const selectedHasUnexposed = computed(() =>
  Array.from(selectedModels.value).some(m => !exposedSet.value.has(m))
);
// 选中至少一个 已暴露 → 显示"从暴露移除"
const selectedHasExposed = computed(() =>
  Array.from(selectedModels.value).some(m => exposedSet.value.has(m))
);
// 选中至少一个 已拉黑 → 显示"解除拉黑";否则只显示"加入黑名单"
const selectedHasBlocked = computed(() =>
  Array.from(selectedModels.value).some(m => blockedSet.value.has(m))
);
const selectedHasUnblocked = computed(() =>
  Array.from(selectedModels.value).some(m => !blockedSet.value.has(m))
);

async function toggleBlock(modelId: string) {
  const isBlocked = blockedSet.value.has(modelId);
  const next = isBlocked
    ? blockedModels.value.filter(m => m !== modelId)
    : [...blockedModels.value, modelId];
  blockedModels.value = next;
  await persistGroupPatch({ blocked_models: next });
}

// 拖拽排序(specified 模式上方区)
const dragIdx = ref<number | null>(null);

function onExposedDragStart(idx: number) {
  dragIdx.value = idx;
}

async function onExposedDrop(targetIdx: number) {
  const src = dragIdx.value;
  dragIdx.value = null;
  if (src == null || src === targetIdx) {
    return;
  }
  const next = [...exposedModels.value];
  const [moved] = next.splice(src, 1);
  next.splice(targetIdx, 0, moved);
  exposedModels.value = next;
  await persistGroupPatch({ exposed_models: next });
}

// === Key 编辑弹窗 (key value + notes 同时改, 走 P11.32 后端 PUT /keys/:id) ===
// 旧的 inline notes-only 编辑已替换 — 笔图标改为打开此弹窗.
const keyEditDialogShow = ref(false);
const editingKey = ref<KeyRow | null>(null);
const editKeyValueInput = ref(""); // 空 = 不改 key value
const editNotesInput = ref("");
const editKeySaving = ref(false);

function openEditKeyDialog(k: KeyRow) {
  editingKey.value = k;
  // 预填原密钥明文 (后端 ListKeysInGroup 已解密返回 key_value), 编辑时能看到原密钥
  // — 尤其同步产生多个同名/同值 key 时便于辨别。没改就只更新 notes。
  editKeyValueInput.value = k.key_value || "";
  editNotesInput.value = k.notes || "";
  keyEditDialogShow.value = true;
}

async function saveKeyEdit() {
  if (!editingKey.value || editKeySaving.value) {
    return;
  }
  const k = editingKey.value;
  const trimmedKey = editKeyValueInput.value.trim();
  const original = (k.key_value || "").trim();
  // 预填了原密钥, 只有真改动才提交新 key (避免无谓重新加密 + status 重置)
  const keyChanged = trimmedKey !== "" && trimmedKey !== original;
  const newNotes = editNotesInput.value.trim();
  editKeySaving.value = true;
  const previousNotes = k.notes;
  k.notes = newNotes;
  try {
    await keysApi.updateKey(k.id, keyChanged ? trimmedKey : "", newNotes);
    message.success(
      keyChanged
        ? t("keys.keyUpdated") || "Key updated"
        : t("keys.notesUpdated") || "Notes updated",
    );
    keyEditDialogShow.value = false;
    if (keyChanged) {
      // status 被后端重置 active, key_value 也变了 — 重新拉列表
      loadKeys();
    }
  } catch (e) {
    k.notes = previousNotes;
    console.error("update key failed", e);
    message.error(t("common.requestFailed"));
  } finally {
    editKeySaving.value = false;
  }
}

// === Label(notes) 卡片内 inline 编辑 ===
// 点击 Label 变输入框, 失焦/回车保存, Esc 取消。只改 notes 走轻量
// updateKeyNotes 接口, 保存后本地更新 k.notes 不整列表 reload — 保持
// 稳定排序下的卡片位置不跳。改 key value 仍走笔图标弹窗。
const vFocus = {
  mounted: (el: HTMLInputElement) => {
    el.focus();
    el.select();
  },
};
const inlineNotesEditId = ref<number | null>(null);
const inlineNotesDraft = ref("");

function startInlineNotes(k: KeyRow) {
  inlineNotesEditId.value = k.id;
  inlineNotesDraft.value = k.notes || "";
}

function cancelInlineNotes() {
  // 仅关闭编辑态, 丢弃草稿; 输入框卸载触发的 blur 会因 editId 已清空而空转
  inlineNotesEditId.value = null;
}

async function commitInlineNotes(k: KeyRow) {
  if (inlineNotesEditId.value !== k.id) {
    return; // 已被 Esc / 二次事件关闭, 避免重复保存
  }
  const next = inlineNotesDraft.value.trim();
  inlineNotesEditId.value = null; // 先关闭编辑态, 阻止 blur 二次进入
  if (next === (k.notes || "")) {
    return; // 无改动, 不打接口
  }
  const previousNotes = k.notes;
  k.notes = next;
  try {
    await keysApi.updateKeyNotes(k.id, next);
  } catch (e) {
    k.notes = previousNotes;
    console.error("update key notes failed", e);
    message.error(t("common.requestFailed"));
  }
}

async function copyText(value: string, msg = "Copied") {
  const ok = await copyToClipboard(value);
  if (ok) {
    message.success(msg);
  } else {
    message.error("Copy failed");
  }
}

async function copyKey(k: KeyRow) {
  const ok = await copyToClipboard(k.key_value);
  if (!ok) {
    message.error("Copy failed");
    return;
  }
  message.success(t("keys.keyCopied") || "Key copied");
  copiedKeyId.value = k.id;
  if (copiedTimer) {
    clearTimeout(copiedTimer);
  }
  copiedTimer = window.setTimeout(() => {
    if (copiedKeyId.value === k.id) {
      copiedKeyId.value = null;
    }
  }, 1400);
}

async function testKey(k: KeyRow) {
  if (!props.group?.id || !k.key_value || testingKeyIds.value.has(k.id)) {
    return;
  }
  testingKeyIds.value.add(k.id);
  try {
    const r = await keysApi.testKeys(props.group.id, k.key_value);
    const cur = r.results?.[0];
    if (cur?.is_valid) {
      message.success(t("keys.testFinished") || "Key OK");
    } else {
      message.error(cur?.error || t("keys.testFailed") || "Key failed");
    }
    refreshAll();
    triggerSyncOperationRefresh(props.group.name, "TEST_SINGLE");
    flashKey(k.id);
  } finally {
    testingKeyIds.value.delete(k.id);
  }
}

// Inline 2-step delete: first click arms; second click within 3s deletes.
function deleteKey(k: KeyRow) {
  if (!props.group?.id) {
    return;
  }
  if (confirmingDeleteId.value !== k.id) {
    confirmingDeleteId.value = k.id;
    if (confirmDeleteTimer) {
      clearTimeout(confirmDeleteTimer);
    }
    confirmDeleteTimer = window.setTimeout(() => {
      if (confirmingDeleteId.value === k.id) {
        confirmingDeleteId.value = null;
      }
    }, 3000);
    return;
  }
  if (confirmDeleteTimer) {
    clearTimeout(confirmDeleteTimer);
  }
  confirmingDeleteId.value = null;
  doDeleteKey(k);
}

async function doDeleteKey(k: KeyRow) {
  if (!props.group?.id) {
    return;
  }
  const groupId = props.group.id;
  const groupName = props.group.name;
  try {
    await keysApi.deleteKeys(groupId, k.key_value);
    refreshAll();
    triggerSyncOperationRefresh(groupName, "DELETE_SINGLE");
  } catch (e) {
    console.error("delete key failed", e);
  }
}

function restoreKey(k: KeyRow) {
  if (!props.group?.id) {
    return;
  }
  const groupId = props.group.id;
  const groupName = props.group.name;
  const d = dialog.warning({
    title: t("keys.restoreKey") || "Restore key",
    content: t("keys.confirmRestoreKey", { key: maskKey(k.key_value) }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      d.loading = true;
      try {
        await keysApi.restoreKeys(groupId, k.key_value);
        refreshAll();
        triggerSyncOperationRefresh(groupName, "RESTORE_SINGLE");
      } finally {
        d.loading = false;
      }
    },
  });
}

function validateAll(scope: "all" | "active" | "invalid" = "all") {
  if (!props.group?.id) {
    return;
  }
  const groupId = props.group.id;
  // 批量验证是后端异步任务; 前端把 scope 内每一把 key 都标记为验证中(转圈),
  // 任务完成后由下方 groupDataRefreshTrigger 的 watch 统一清除。
  keys.value.forEach(k => {
    const inScope =
      scope === "all" ||
      (scope === "active" && k.status === "active") ||
      (scope === "invalid" && k.status === "invalid");
    if (inScope && k.id) testingKeyIds.value.add(k.id);
  });
  keysApi.validateGroupKeys(groupId, scope === "all" ? undefined : scope).then(() => {
    localStorage.removeItem("last_closed_task");
    appState.taskPollingTrigger++;
  });
}

function clearInvalid() {
  if (!props.group?.id) {
    return;
  }
  const groupId = props.group.id;
  const groupName = props.group.name;
  const d = dialog.warning({
    title: t("keys.clearKeys") || "Clear invalid keys",
    content: t("keys.confirmClearInvalidKeys") || "Clear all invalid keys in this group?",
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      d.loading = true;
      try {
        const { data } = await keysApi.clearAllInvalidKeys(groupId);
        message.success(data?.message || t("keys.clearSuccess") || "Cleared");
        refreshAll();
        triggerSyncOperationRefresh(groupName, "CLEAR_ALL_INVALID");
      } finally {
        d.loading = false;
      }
    },
  });
}

function deleteGroup() {
  if (!props.group?.id) {
    return;
  }
  const id = props.group.id;
  const name = props.group.name;
  const d = dialog.warning({
    title: t("keys.deleteGroup") || "Delete group",
    content: t("keys.confirmDeleteGroup", { name }) || `Delete group "${name}"?`,
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      d.loading = true;
      try {
        await keysApi.deleteGroup(id);
        emit("refresh");
      } finally {
        d.loading = false;
      }
    },
  });
}

function exportKeys(scope: "all" | "active" | "invalid") {
  if (!props.group?.id) {
    return;
  }
  keysApi.exportKeys(props.group.id, scope);
}

function fmtFailRate(failed?: number, total?: number): string {
  if (!total) {
    return "0%";
  }
  return `${(((failed || 0) / total) * 100).toFixed(1)}%`;
}

function formatRelative(date?: string): string {
  if (!date) {
    return t("keys.never") || "—";
  }
  const diffSec = Math.floor((Date.now() - new Date(date).getTime()) / 1000);
  if (diffSec < 60) {
    return `${diffSec}s ago`;
  }
  if (diffSec < 3600) {
    return `${Math.floor(diffSec / 60)}m ago`;
  }
  if (diffSec < 86400) {
    return `${Math.floor(diffSec / 3600)}h ago`;
  }
  return `${Math.floor(diffSec / 86400)}d ago`;
}

function refreshFromCreate() {
  showAddKey.value = false;
  refreshAll();
  if (props.group?.name) {
    triggerSyncOperationRefresh(props.group.name, "BATCH_ADD");
  }
}

function refreshFromDelete() {
  showDeleteKey.value = false;
  refreshAll();
  if (props.group?.name) {
    triggerSyncOperationRefresh(props.group.name, "BATCH_DELETE");
  }
}

function onGroupUpdated() {
  showEditGroup.value = false;
  emit("refresh");
}

function onGroupCopied() {
  showCopyGroup.value = false;
  emit("refresh");
}

const filterCounts = computed(() => ({
  all: stats.value?.key_stats.total_keys ?? 0,
  valid: stats.value?.key_stats.active_keys ?? 0,
  invalid: stats.value?.key_stats.invalid_keys ?? 0,
}));

</script>

<template>
  <section class="v5-detail">
    <!-- ===== HERO ===== -->
    <div class="v5-hero">
      <div class="v5-hero__top">
        <!-- 统一走 ProviderLogo 三层 fallback (lobehub 品牌 SVG → host apex favicon
             → 首字母), 与 V3GroupSidebar 一致; 旧的 FAVICON_DOMAIN_MAP 硬编码表覆盖不到
             agnes 等 → 这些 provider 详情头部之前没 icon. 传 host 让 favicon 生效. -->
        <div class="v5-hero__avatar v5-picon" style="width: 52px; height: 52px">
          <ProviderLogo
            :hint="providerHint"
            :host="group.upstreams?.[0]?.url"
            :fallback-initial="getGroupDisplayName(group)"
            :size="44"
            style="border-radius: 8px"
          />
        </div>

        <div class="v5-hero__main">
          <div class="v5-hero__title">
            {{ getGroupDisplayName(group) }}
            <n-icon v-if="group.is_system" :component="LockClosedOutline" :size="13" />
            <span v-if="isAggregate" class="v3-chip v3-chip--info" style="font-size: 11px">
              {{ t("v5.aggregateChip") }}
            </span>
            <n-tooltip v-if="friendlyHint" :style="{ maxWidth: '280px' }">
              <template #trigger>
                <span class="v5-helpicon" tabindex="0">
                  <n-icon :component="HelpCircleOutline" :size="12" />
                </span>
              </template>
              {{ friendlyHint }}
            </n-tooltip>
          </div>
          <div class="v5-hero__path">
            <code>{{ sdkBaseUrl }}</code>
            <button
              class="v5-hero__copy"
              @click="copyText(sdkBaseUrl, t('keys.endpointCopied') || 'Endpoint copied')"
            >
              <n-icon :component="CopyOutline" :size="11" />
              {{ t("v5.copyUrl") }}
            </button>
          </div>
        </div>

        <!-- Actions -->
        <div class="v5-hero__actions">
          <a
            v-if="!isAggregate && matchedProvider?.docsUrl"
            :href="matchedProvider.docsUrl"
            target="_blank"
            rel="noopener"
            class="v5-hero__btn"
          >
            <n-icon :component="OpenOutline" :size="12" />
            {{ t("v3.docs") || "Docs" }}
          </a>
          <n-tooltip v-if="!group.is_system">
            <template #trigger>
              <button class="v5-hero__btn v5-hero__btn--icon" @click="showEditGroup = true">
                <n-icon :component="PencilOutline" :size="13" />
              </button>
            </template>
            {{ t("common.edit") }}
          </n-tooltip>
          <n-tooltip v-if="!isAggregate">
            <template #trigger>
              <button class="v5-hero__btn v5-hero__btn--icon" @click="showCopyGroup = true">
                <n-icon :component="CopyOutline" :size="13" />
              </button>
            </template>
            {{ t("keys.copyGroup") || "Copy group" }}
          </n-tooltip>
          <n-tooltip v-if="!group.is_system">
            <template #trigger>
              <button
                class="v5-hero__btn v5-hero__btn--icon v5-hero__btn--danger"
                @click="deleteGroup"
              >
                <n-icon :component="Trash" :size="13" />
              </button>
            </template>
            {{ t("keys.deleteGroup") || "Delete group" }}
          </n-tooltip>
        </div>
      </div>

      <!-- Big stats row -->
      <div class="v5-hero__stats">
        <!-- Aggregate: Sub-groups / Active keys (sum) / 24h / 7d -->
        <template v-if="isAggregate">
          <div>
            <div class="v5-hero__stat-l">{{ t("v5.aggSubGroups") }}</div>
            <div class="v5-hero__stat-v">{{ subGroups.length }}</div>
            <div class="v5-hero__stat-s">{{ t("v5.aggSubGroupsSub") }}</div>
          </div>
          <div>
            <div class="v5-hero__stat-l">{{ t("v5.aggActiveKeys") }}</div>
            <div class="v5-hero__stat-v">{{ aggKeyTotal.toLocaleString() }}</div>
            <div class="v5-hero__stat-s">
              <template v-if="aggKeyInvalid">
                {{ aggKeyInvalid }} {{ t("keys.invalid") || "invalid" }}
              </template>
              <template v-else>
                {{ t("v5.aggActiveKeysSub") }}
              </template>
            </div>
          </div>
        </template>
        <!-- Standard: Keys / Models -->
        <template v-else>
          <div>
            <div class="v5-hero__stat-l">{{ t("v3.keys") || "Keys" }}</div>
            <div class="v5-hero__stat-v">
              {{ (stats?.key_stats.total_keys ?? 0).toLocaleString() }}
            </div>
            <div class="v5-hero__stat-s">
              {{ stats?.key_stats.active_keys ?? 0 }} {{ t("keys.valid") || "valid" }} ·
              {{ stats?.key_stats.invalid_keys ?? 0 }} {{ t("keys.invalid") || "invalid" }}
            </div>
          </div>
          <div>
            <div class="v5-hero__stat-l">{{ t("v5.models") }}</div>
            <div class="v5-hero__stat-v">{{ modelCount }}</div>
            <div class="v5-hero__stat-s">{{ t("v5.modelsAvailable") }}</div>
          </div>
        </template>

        <!-- Common: 24h + 7d -->
        <div>
          <div class="v5-hero__stat-l">{{ t("v3.req24h") }}</div>
          <div class="v5-hero__stat-v">
            {{ (stats?.stats_24_hour.total_requests ?? 0).toLocaleString() }}
          </div>
          <div
            class="v5-hero__stat-s"
            :style="{
              color: stats?.stats_24_hour.failed_requests ? 'var(--v3-danger)' : undefined,
            }"
          >
            {{
              fmtFailRate(stats?.stats_24_hour.failed_requests, stats?.stats_24_hour.total_requests)
            }}
            {{ t("v5.errorRate") }}
          </div>
        </div>
        <div>
          <div class="v5-hero__stat-l">{{ t("v5.requests7d") }}</div>
          <div class="v5-hero__stat-v">
            {{ (stats?.stats_7_day.total_requests ?? 0).toLocaleString() }}
          </div>
          <div class="v5-hero__stat-s">
            {{ stats?.stats_7_day.failed_requests ?? 0 }} {{ t("v3.failures") || "failures" }}
          </div>
        </div>
      </div>
    </div>

    <!-- ===== TABS ===== -->
    <div class="v5-tabs">
      <button :class="['v5-tab', tab === 'keys' ? 'v5-tab--active' : '']" @click="tab = 'keys'">
        <n-icon :component="isAggregate ? LinkOutline : KeyOutline" :size="13" />
        {{ isAggregate ? t("v5.tabSubGroups") : t("v5.tabKeys") }}
        <span class="v5-tab__badge">
          {{ isAggregate ? subGroups.length : (stats?.key_stats.total_keys ?? 0) }}
        </span>
      </button>
      <button :class="['v5-tab', tab === 'models' ? 'v5-tab--active' : '']" @click="tab = 'models'">
        <n-icon :component="CubeOutline" :size="13" />
        {{ t("v5.tabModels") }}
        <span class="v5-tab__badge">{{ modelCount }}</span>
      </button>
      <button
        :class="['v5-tab', tab === 'settings' ? 'v5-tab--active' : '']"
        @click="tab = 'settings'"
      >
        <n-icon :component="SettingsOutline" :size="13" />
        {{ t("v5.tabSettings") }}
      </button>
      <div class="v5-tabs__spacer" />
      <div class="v5-tabs__actions">
        <button class="v3-btn v3-btn--sm" @click="refreshAll">
          <n-icon :component="RefreshOutline" :size="11" />
          {{ t("common.refresh") || "Refresh" }}
        </button>
        <button v-if="!isAggregate" class="v3-btn v3-btn--sm" @click="exportKeys('all')">
          <n-icon :component="DownloadOutline" :size="11" />
          {{ t("v3.exportAll") || "Export" }}
        </button>
      </div>
    </div>

    <!-- ===== CONTENT ===== -->
    <div class="v5-content scroll">
      <!-- ===== AGGREGATE: SUB-GROUPS TAB ===== -->
      <div v-if="tab === 'keys' && isAggregate">
        <!-- P6: 健康度 dashboard (10s 自动刷新) -->
        <details v-if="healthRows.length" open style="margin: 0 0 16px; padding: 12px 16px; border: 1px solid var(--v3-line); border-radius: 8px; background: var(--v3-bg-soft, transparent)">
          <summary style="cursor: pointer; font: 600 13px var(--v3-sans); display: flex; justify-content: space-between; align-items: center">
            <span>{{ t("health.title") }}</span>
            <span style="font: 400 11px var(--v3-mono); color: var(--v3-ink-4)">{{ t("health.meta", { n: healthRows.length }) }}</span>
          </summary>
          <table style="width: 100%; margin-top: 10px; border-collapse: collapse; font: 12px var(--v3-sans)">
            <thead>
              <tr style="text-align: left; color: var(--v3-ink-4); border-bottom: 1px solid var(--v3-line)">
                <th style="padding: 6px 8px; font-weight: 500">{{ t("health.colSubGroup") }}</th>
                <th style="padding: 6px 8px; font-weight: 500">{{ t("health.colLatency") }}</th>
                <th style="padding: 6px 8px; font-weight: 500">{{ t("health.colWeight") }}</th>
                <th style="padding: 6px 8px; font-weight: 500">{{ t("health.colModels") }}</th>
                <th style="padding: 6px 8px; font-weight: 500">{{ t("health.colStatus") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="h in healthRows" :key="h.sub_group_id" style="border-bottom: 1px solid var(--v3-line-soft, var(--v3-line))">
                <td style="padding: 6px 8px"><code style="font: 12px var(--v3-mono)">{{ h.name }}</code></td>
                <td style="padding: 6px 8px; font: 12px var(--v3-mono)">{{ formatLatency(h.latency_ewma_ms) }}</td>
                <td style="padding: 6px 8px; font: 12px var(--v3-mono)">
                  {{ h.effective_weight }}<span v-if="h.effective_weight !== h.weight" style="color: var(--v3-ink-4)"> / {{ h.weight }}</span>
                </td>
                <td style="padding: 6px 8px; font: 12px var(--v3-mono)">{{ h.has_models_cache ? h.models_count : "—" }}</td>
                <td style="padding: 6px 8px">
                  <span v-if="h.in_cooldown" style="padding: 2px 6px; border-radius: 4px; font: 500 11px var(--v3-mono); color: var(--v3-warn); background: oklch(from var(--v3-warn) l c h / 0.15)">
                    {{ t("health.statusCooldown", { t: cooldownRemain(h.cooldown_until) }) }}
                  </span>
                  <span v-else-if="h.consecutive_failures > 0" style="padding: 2px 6px; border-radius: 4px; font: 500 11px var(--v3-mono); color: var(--v3-warn); background: oklch(from var(--v3-warn) l c h / 0.1)">
                    {{ t("health.statusFails", { n: h.consecutive_failures }) }}
                  </span>
                  <span v-else style="padding: 2px 6px; border-radius: 4px; font: 500 11px var(--v3-mono); color: var(--v3-ok); background: oklch(from var(--v3-ok) l c h / 0.12)">{{ t("health.statusOk") }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </details>
        <v3-sub-group-table
          :selected-group="group"
          :sub-groups="subGroups"
          :groups="allGroups"
          :loading="subGroupsLoading"
          @refresh="onSubGroupRefresh"
          @group-select="onSubGroupSelect"
        />
      </div>

      <!-- ===== STANDARD: KEYS TAB ===== -->
      <div v-else-if="tab === 'keys'">
        <div class="v5-toolbar">
          <div class="v5-segctrl">
            <button
              :class="statusFilter === 'all' ? 'is-active' : ''"
              @click="statusFilter = 'all'"
            >
              {{ t("common.all") || "All" }} ({{ filterCounts.all }})
            </button>
            <button
              :class="statusFilter === 'active' ? 'is-active' : ''"
              @click="statusFilter = 'active'"
            >
              {{ t("keys.valid") || "Valid" }} ({{ filterCounts.valid }})
            </button>
            <button
              :class="statusFilter === 'invalid' ? 'is-active' : ''"
              @click="statusFilter = 'invalid'"
            >
              {{ t("keys.invalid") || "Invalid" }} ({{ filterCounts.invalid }})
            </button>
          </div>
          <div class="v5-search">
            <n-icon :component="SearchOutline" :size="12" />
            <input
              v-model="search"
              :placeholder="t('keys.searchKeyPlaceholder') || 'Filter keys…'"
              @input="onSearchInput"
            />
          </div>
          <div class="v5-toolbar__spacer">
            <button class="v3-btn v3-btn--sm" @click="validateAll('all')">
              <n-icon :component="CheckmarkCircle" :size="11" />
              {{ t("v3.testAll") || "Test all" }}
            </button>
            <button
              v-if="filterCounts.invalid > 0"
              class="v3-btn v3-btn--sm v3-btn--danger"
              @click="clearInvalid"
            >
              <n-icon :component="Trash" :size="11" />
              {{ t("v3.clearInvalid") || "Clear invalid" }}
            </button>
            <button class="v3-btn v3-btn--sm" @click="showDeleteKey = true">
              <n-icon :component="RemoveCircleOutline" :size="11" />
              {{ t("v3.batchDelete") || "Batch delete" }}
            </button>
            <a
              v-if="!isAggregate && matchedProvider?.signupUrl"
              :href="matchedProvider.signupUrl"
              target="_blank"
              rel="noopener"
              class="v3-btn v3-btn--sm"
            >
              <n-icon :component="KeyOutline" :size="11" />
              {{ t("v3.getMoreKeys") || "Get API key" }} →
            </a>
            <button class="v3-btn v3-btn--accent v3-btn--sm" @click="showAddKey = true">
              <n-icon :component="AddOutline" :size="11" />
              {{ t("v3.addKey") || "Add key" }}
            </button>
          </div>
        </div>

        <n-spin :show="keysLoading">
          <div v-if="keys.length" class="v5-cardgrid">
            <div
              v-for="k in keys"
              :key="k.id"
              :class="[
                'v5-keycard',
                k.status === 'active'
                  ? 'v5-keycard--ok'
                  : k.status === 'invalid'
                    ? 'v5-keycard--invalid'
                    : 'v5-keycard--unverified',
                flashKeyId === k.id ? 'v5-keycard--flash' : '',
                testingKeyIds.has(k.id) ? 'v5-keycard--testing' : '',
              ]"
            >
              <!-- Zone 1 · Header: Label 主标题(inline 编辑) + 状态信号灯 -->
              <div class="v5-keycard__head">
                <div class="v5-keycard__titlewrap">
                  <input
                    v-if="inlineNotesEditId === k.id"
                    v-focus
                    v-model="inlineNotesDraft"
                    class="v5-keycard__title-input"
                    maxlength="255"
                    spellcheck="false"
                    :placeholder="t('keys.addLabelPlaceholder')"
                    @blur="commitInlineNotes(k)"
                    @keydown.enter.prevent="commitInlineNotes(k)"
                    @keydown.esc.prevent="cancelInlineNotes()"
                  />
                  <n-tooltip v-else-if="k.notes" placement="top">
                    <template #trigger>
                      <button
                        class="v5-keycard__title"
                        :title="k.notes"
                        @click.stop="startInlineNotes(k)"
                      >{{ k.notes }}</button>
                    </template>
                    {{ t("keys.editLabelTip") || "Click to edit label" }}
                  </n-tooltip>
                  <button
                    v-else
                    class="v5-keycard__title v5-keycard__title--empty"
                    :title="t('keys.editLabelTip') || 'Click to add label'"
                    @click.stop="startInlineNotes(k)"
                  >
                    <n-icon :component="PencilOutline" :size="11" />
                    {{ t("keys.addLabel") }}
                  </button>
                </div>
                <span
                  :class="[
                    'v5-keycard__signal',
                    k.status === 'active'
                      ? 'v5-keycard__signal--ok'
                      : k.status === 'invalid'
                        ? 'v5-keycard__signal--invalid'
                        : 'v5-keycard__signal--unverified',
                  ]"
                >
                  <span class="v5-keycard__led"></span>
                  {{
                    k.status === "active"
                      ? t("keys.valid") || "Valid"
                      : k.status === "invalid"
                        ? t("keys.invalid") || "Invalid"
                        : t("v5.kcUnverified")
                  }}
                </span>
              </div>

              <!-- Zone 2 · 凭证框: key 首尾指纹 + 内嵌复制 -->
              <div class="v5-keycard__cred">
                <code class="v5-keycard__cred-val" :title="maskKey(k.key_value)">{{ maskKey(k.key_value) }}</code>
                <n-tooltip>
                  <template #trigger>
                    <button
                      :class="[
                        'v5-keycard__cred-copy',
                        copiedKeyId === k.id ? 'is-copied' : '',
                      ]"
                      @click.stop="copyKey(k)"
                    >
                      <n-icon :component="copiedKeyId === k.id ? Checkmark : CopyOutline" :size="14" />
                    </button>
                  </template>
                  {{ copiedKeyId === k.id ? t("v5.copied") : t("v5.copy") }}
                </n-tooltip>
              </div>

              <!-- Zone 3 · 指标: 请求 / 失败 / 活跃时间 -->
              <div class="v5-keycard__metrics">
                <div class="v5-keycard__metric">
                  <span class="v5-keycard__metric-l">{{ t("v5.kcReqLabel") }}</span>
                  <span class="v5-keycard__metric-v">{{ (k.request_count ?? 0).toLocaleString() }}</span>
                </div>
                <div
                  :class="[
                    'v5-keycard__metric',
                    (k.failure_count || 0) > 0 ? 'v5-keycard__metric--alert' : '',
                  ]"
                >
                  <span class="v5-keycard__metric-l">{{ t("v5.kcFailLabel") }}</span>
                  <span class="v5-keycard__metric-v">{{ k.failure_count ?? 0 }}</span>
                </div>
                <div class="v5-keycard__metric">
                  <span class="v5-keycard__metric-l">{{ t("v5.kcActiveLabel") }}</span>
                  <span class="v5-keycard__metric-v v5-keycard__metric-v--time">{{ formatRelative(k.last_used_at) }}</span>
                </div>
              </div>

              <!-- Zone 4 · 操作: 测试(主) · 编辑 · 恢复(invalid) | 删除(隔离) -->
              <div class="v5-keycard__bar">
                <button
                  class="v5-keycard__test"
                  :disabled="testingKeyIds.has(k.id)"
                  @click.stop="testKey(k)"
                >
                  <n-icon
                    :component="testingKeyIds.has(k.id) ? RefreshOutline : FlashOutline"
                    :size="14"
                    :class="testingKeyIds.has(k.id) ? 'v5-keycard__pill-spin' : ''"
                  />
                  {{ testingKeyIds.has(k.id) ? t("v5.kcTesting") : t("v5.kcTest") || "Test" }}
                </button>
                <n-tooltip>
                  <template #trigger>
                    <button class="v5-keycard__act" @click.stop="openEditKeyDialog(k)">
                      <n-icon :component="PencilOutline" :size="15" />
                    </button>
                  </template>
                  {{ t("keys.editKeyTitle") }}
                </n-tooltip>
                <n-tooltip v-if="k.status === 'invalid'">
                  <template #trigger>
                    <button class="v5-keycard__act v5-keycard__act--restore" @click="restoreKey(k)">
                      <n-icon :component="RefreshOutline" :size="15" />
                    </button>
                  </template>
                  {{ t("keys.restoreKey") || "Restore" }}
                </n-tooltip>
                <span class="v5-keycard__bar-gap"></span>
                <i class="v5-keycard__bar-div"></i>
                <n-tooltip>
                  <template #trigger>
                    <button
                      :class="[
                        'v5-keycard__act',
                        'v5-keycard__act--danger',
                        confirmingDeleteId === k.id ? 'is-armed' : '',
                      ]"
                      @click="deleteKey(k)"
                    >
                      <n-icon :component="Trash" :size="15" />
                    </button>
                  </template>
                  {{
                    confirmingDeleteId === k.id
                      ? t("v5.kcConfirmDelete")
                      : t("common.delete") || "Delete"
                  }}
                </n-tooltip>
              </div>
            </div>
          </div>

          <div v-else class="v5-empty">
            <div class="v5-empty__icon">
              <n-icon :component="KeyOutline" :size="22" />
            </div>
            <div class="v5-empty__title">
              {{ t("v5.noKeysTitle") }}
            </div>
            <div class="v5-empty__sub">
              {{ matchedProvider?.signupUrl ? t("v5.noKeysSubWithProvider") : t("v5.noKeysSub") }}
            </div>
            <button
              class="v3-btn v3-btn--accent"
              style="margin-top: 8px"
              @click="showAddKey = true"
            >
              <n-icon :component="AddOutline" :size="12" />
              {{ t("v3.addKey") }}
            </button>
          </div>
        </n-spin>

        <div v-if="totalPages > 1" class="v5-pagebar">
          <n-pagination
            v-model:page="page"
            :page-count="totalPages"
            :page-size="pageSize"
            :item-count="total"
          />
        </div>
      </div>

      <!-- ===== MODELS TAB ===== -->
      <div v-else-if="tab === 'models'">
        <!-- Toolbar — two layers so users read config first, then browse -->
        <!-- Config layer: routing mode toggle + refresh models (what this group exposes) -->
        <div class="v5-toolbar v5-toolbar--config">
          <div class="v5-toolbar__hint">
            {{ t("v5.modelsHint") }}
            <span v-if="modelsRefreshedAtDisplay" style="margin-left: 8px; color: var(--v3-ink-4)">
              · {{ t("v5.refreshedAt") }} {{ modelsRefreshedAtDisplay }}
            </span>
          </div>
          <div class="v5-toolbar__spacer" style="gap: 10px">
            <div v-if="!isAggregate" class="v5-modemode">
              <button
                class="v3-btn v3-btn--sm"
                :class="{ 'v3-btn--accent': routingMode === 'passthrough' }"
                :disabled="savingMode"
                @click="setRoutingMode('passthrough')"
              >{{ t("v3.modePassthrough") || "透传" }}</button>
              <button
                class="v3-btn v3-btn--sm"
                :class="{ 'v3-btn--accent': routingMode === 'specified' }"
                :disabled="savingMode"
                @click="setRoutingMode('specified')"
              >{{ t("v3.modeSpecified") || "指定" }}</button>
            </div>
            <button
              v-if="!isAggregate"
              class="v3-btn v3-btn--sm"
              :disabled="modelsRefreshing"
              @click="refreshGroupModels"
            >
              <n-icon :component="RefreshOutline" :size="11" :class="modelsRefreshing ? 'v5-keycard__pill-spin' : ''" />
              {{ modelsRefreshing ? t("common.loading") : t("keys.refreshModelsBtn") }}
            </button>
          </div>
        </div>
        <!-- Mode consequence hint: spells out what the toggle above decides -->
        <div v-if="!isAggregate" class="v5-modehint">
          <n-icon :component="HelpCircleOutline" :size="12" />
          <span>{{ routingMode === "specified" ? (t("v3.modeSpecifiedHint") || "指定 = 白名单:仅「已暴露」段的模型对外可调,下方「上游全部」是候选池") : (t("v3.modePassthroughHint") || "透传 = 放行:上游声明的模型全部对外可调,不做白名单筛选") }}</span>
        </div>

        <!-- Browse layer: search + filters (narrow the list you see) -->
        <div class="v5-toolbar v5-toolbar--browse">
          <div class="v5-search" style="width: 220px">
            <n-icon :component="SearchOutline" :size="12" />
            <input v-model="modelSearch" :placeholder="t('v3.filterModels') || 'Filter models…'" />
          </div>
          <div class="v5-modemode v5-modemode--filters">
            <button class="v3-btn v3-btn--sm" :class="{ 'v3-btn--accent': availFilter === 'all' }" @click="availFilter = 'all'">
              {{ t("common.all") || "All" }}
            </button>
            <button class="v3-btn v3-btn--sm" :class="{ 'v3-btn--accent': availFilter === 'free' }" @click="availFilter = 'free'">
              🆓 {{ t("v3.freeText") }}
            </button>
            <button
              v-if="showTrialFilter"
              class="v3-btn v3-btn--sm"
              :class="{ 'v3-btn--accent': availFilter === 'trial' }"
              @click="availFilter = 'trial'"
            >
              {{ t("v3.trial") || "体验" }}
            </button>
            <button class="v3-btn v3-btn--sm" :class="{ 'v3-btn--accent': availFilter === 'paid' }" @click="availFilter = 'paid'">
              {{ t("v3.paid") || "收费" }}
            </button>
            <button
              v-if="showRecommendedFilter"
              class="v3-btn v3-btn--sm"
              :class="{ 'v3-btn--accent': availFilter === 'recommended' }"
              @click="availFilter = 'recommended'"
            >
              ⭐ {{ t("v3.recommended") || "推荐" }}
            </button>
          </div>
          <div v-if="selectedModels.size > 0" class="v5-bulk-bar v5-bulk-bar--inline">
          <span class="v5-bulk-bar__count">
            {{ t("v5.bulkSelected", { n: selectedModels.size }) || `已选 ${selectedModels.size} 个` }}
          </span>
          <button
            v-if="!isAggregate && routingMode === 'specified' && selectedHasUnexposed"
            class="v3-btn v3-btn--sm v3-btn--accent"
            @click="bulkAddToExposed"
          >
            + {{ t("v5.bulkAddExposed") || "批量加入暴露" }}
          </button>
          <button
            v-if="!isAggregate && routingMode === 'specified' && selectedHasExposed"
            class="v3-btn v3-btn--sm"
            @click="bulkRemoveFromExposed"
          >
            <n-icon :component="CloseOutline" :size="11" />
            {{ t("v5.bulkRemoveExposed") || "批量移除暴露" }}
          </button>
          <button
            v-if="selectedHasUnblocked"
            class="v3-btn v3-btn--sm v3-btn--danger"
            @click="bulkToggleBlock(true)"
          >
            {{ t("v5.bulkBlock") || "批量加入黑名单" }}
          </button>
          <button
            v-if="selectedHasBlocked"
            class="v3-btn v3-btn--sm"
            @click="bulkToggleBlock(false)"
          >
            {{ t("v5.bulkUnblock") || "批量解除拉黑" }}
          </button>
          <button class="v3-btn v3-btn--sm v3-btn--ghost" @click="clearSelection">
            {{ t("v5.bulkClear") || "取消选择" }}
          </button>
          </div>
        </div>

        <!-- =============================================== -->
        <!-- SPECIFIED MODE: top "已暴露" + bottom "上游全部" -->
        <!-- =============================================== -->
        <template v-if="!isAggregate && routingMode === 'specified'">
          <div class="v5-section-head">
            <span class="v5-section-head__title">{{ t("v3.exposedModels") || "已暴露" }}</span>
            <span class="v3-chip" style="font-size: 10px">{{ exposedModels.length }}</span>
            <span class="v5-section-head__hint">{{ t("v3.exposedHint") || "白名单 — 仅这些模型可调用,可加别名,支持拖拽排序" }}</span>
            <div class="v5-section-head__spacer" style="flex: 1"></div>
            <!-- P11.38: 一键测试 - 测当前 filteredExposed 列表里的所有 model. < 30 显示 -->
            <button
              v-if="filteredExposed.length > 0 && filteredExposed.length < 30"
              class="v3-btn v3-btn--sm"
              :disabled="bulkTesting"
              :title="t('v3.bulkTestTip')"
              @click="bulkTestModels(filteredExposed)"
            >
              <n-icon :component="PulseOutline" :size="11" />
              {{ bulkTesting ? t("v3.bulkTestRunning") : t("v3.bulkTestBtn", { n: filteredExposed.length }) }}
            </button>
          </div>

          <div v-if="filteredExposed.length" class="v5-cardgrid">
            <div
              v-for="modelId in filteredExposed"
              :key="`exposed-${modelId}`"
              :data-model-id="modelId"
              class="v5-modelcard v5-modelcard--exposed"
              :class="{
                'v5-modelcard--dragging': dragIdx === exposedModels.indexOf(modelId),
                'v5-modelcard--blocked': blockedSet.has(modelId),
                'v5-modelcard--selected': isSelected(modelId),
                'v5-modelcard--testfail': modelTestState(modelId)?.ok === false,
                'v5-modelcard--testing': testingModels.has(modelId),
              }"
              draggable="true"
              @click="toggleSelected(modelId)"
              @dragstart="onExposedDragStart(exposedModels.indexOf(modelId))"
              @dragover.prevent
              @drop="onExposedDrop(exposedModels.indexOf(modelId))"
            >
              <div class="v5-modelcard__row" style="align-items: flex-start; gap: 6px">
                <code
                  class="v5-modelcard__name"
                  style="flex: 1; min-width: 0"
                  :title="t('v3.modelClickToCopy') || '点击复制 model ID'"
                  @click.stop="copyModelId(modelId)"
                >{{ modelId }} <FreeBadge v-if="isFreeModel(modelId)" /><span v-if="isRecommendedModel(modelId)" style="font-size: 10px; margin-left: 2px" title="推荐">⭐</span></code>
                <span
                  v-if="testingModels.has(modelId)"
                  class="v5-modelcard__dot v5-modelcard__dot--testing"
                  :title="t('v3.testModelTesting')"
                />
                <span
                  v-else-if="modelTestState(modelId)"
                  class="v5-modelcard__dot"
                  :class="modelTestState(modelId)?.ok ? 'v5-modelcard__dot--ok' : 'v5-modelcard__dot--bad'"
                  :title="modelTestTooltip(modelId)"
                />
              </div>
              <div class="v5-modelcard__row" style="margin-top: 6px; align-items: center; gap: 6px">
                <!-- P8.3: 测试按钮挪到 row 2 第一位, 默认位置可见可点 -->
                <n-tooltip trigger="hover" placement="top">
                  <template #trigger>
                    <button
                      class="v5-modelcard__test"
                      :class="{
                        'v5-modelcard__test--testing': testingModels.has(modelId),
                        'v5-modelcard__test--ok': modelTestState(modelId)?.ok === true,
                        'v5-modelcard__test--bad': modelTestState(modelId)?.ok === false,
                      }"
                      :title="modelTestTooltip(modelId)"
                      :disabled="testingModels.has(modelId)"
                      @click.stop="testModel(modelId)"
                    >
                      <span v-if="testingModels.has(modelId)" class="v5-dots" aria-label="testing">
                        <span></span><span></span><span></span>
                      </span>
                      <n-icon v-else :component="PulseOutline" :size="12" />
                    </button>
                  </template>
                  <template v-if="testingModels.has(modelId)">{{ t("v3.testModelTesting") }}</template>
                  <template v-else-if="modelTestState(modelId)?.ok === true">
                    {{ t("v3.modelTestOk", { model: modelId, ms: modelTestState(modelId)?.ms }) }}
                  </template>
                  <template v-else-if="modelTestState(modelId)?.ok === false">
                    {{ t("v3.modelTestFail", { model: modelId, err: modelTestState(modelId)?.error }) }}
                  </template>
                  <template v-else>{{ t("v3.testModel") }}</template>
                </n-tooltip>
                <n-tooltip trigger="hover" placement="top">
                  <template #trigger>
                    <SpeedBadge :speed="speedForModel(modelId)" :size="11" />
                  </template>
                  {{ speedReasonFor(modelId) }}
                </n-tooltip>
                <CapabilityIcons :tags="tagsForModel(modelId)" :size="11" />

                <span v-if="blockedSet.has(modelId)" class="v3-chip v3-chip--danger" style="flex-shrink: 0; font-size: 10px">
                  🚫 {{ t("v3.blocked") || "blocked" }}
                </span>
                <div class="v5-modelcard__spacer" style="flex: 1"></div>
                <div class="v5-modelcard__aliases">
                  <n-tooltip v-for="a in aliasesFor(modelId)" :key="a.id" placement="top">
                    <template #trigger>
                      <button class="v5-modelcard__alias" style="font-size: 10.5px; padding: 2px 6px" @click.stop="goToAliases(a.alias)">
                        <span>{{ a.alias }}</span>
                      </button>
                    </template>
                    {{ t("v5.mcAliasTip", { weight: a.weight, priority: a.priority }) }}
                  </n-tooltip>
                  <button class="v5-modelcard__alias-add" style="font-size: 10.5px; padding: 2px 8px; min-height: 22px" @click.stop="addAliasFor(modelId)">
                    @{{ t("v3.alias") || "别名" }}
                  </button>
                </div>
              </div>
            </div>
          </div>
          <div v-else class="v5-empty-mini">
            <span v-if="exposedModels.length === 0">{{ t("v3.noExposedModels") || "暂无已暴露模型,从下方加入" }}</span>
            <span v-else>{{ t("v3.noModelsMatching") }}</span>
          </div>

          <div class="v5-section-head" style="margin-top: 16px">
            <span class="v5-section-head__title">{{ t("v3.upstreamAll") || "上游全部" }}</span>
            <span class="v3-chip" style="font-size: 10px">{{ filteredAvailable.length }} / {{ groupModels.length }}</span>
            <span class="v5-section-head__hint">{{ t("v3.upstreamHint") || "上游声明的全部模型 — 从这里把模型加入上方「已暴露」白名单" }}</span>
            <div class="v5-section-head__spacer" style="flex: 1"></div>
            <!-- P11.38: 当前 filtered < 30 时提供一键测试 (上限保护 provider) -->
            <button
              v-if="filteredAvailable.length > 0 && filteredAvailable.length < 30"
              class="v3-btn v3-btn--sm"
              :disabled="bulkTesting"
              :title="t('v3.bulkTestTip')"
              @click="bulkTestModels(filteredAvailable)"
            >
              <n-icon :component="PulseOutline" :size="11" />
              {{ bulkTesting ? t("v3.bulkTestRunning") : t("v3.bulkTestBtn", { n: filteredAvailable.length }) }}
            </button>
          </div>

          <div v-if="filteredAvailable.length" class="v5-cardgrid">
            <div
              v-for="modelId in filteredAvailable"
              :key="`avail-${modelId}`"
              :data-model-id="modelId"
              class="v5-modelcard"
              :class="{
                'v5-modelcard--inexposed': exposedSet.has(modelId),
                'v5-modelcard--blocked': blockedSet.has(modelId),
                'v5-modelcard--selected': isSelected(modelId),
                'v5-modelcard--testfail': modelTestState(modelId)?.ok === false,
                'v5-modelcard--testing': testingModels.has(modelId),
              }"
              @click="toggleSelected(modelId)"
            >
              <div class="v5-modelcard__row" style="align-items: flex-start; gap: 6px">
                <code
                  class="v5-modelcard__name"
                  style="flex: 1; min-width: 0"
                  :title="t('v3.modelClickToCopy') || '点击复制 model ID'"
                  @click.stop="copyModelId(modelId)"
                >{{ modelId }} <FreeBadge v-if="isFreeModel(modelId)" /><span v-if="isRecommendedModel(modelId)" style="font-size: 10px; margin-left: 2px" title="推荐">⭐</span></code>
                <span
                  v-if="testingModels.has(modelId)"
                  class="v5-modelcard__dot v5-modelcard__dot--testing"
                  :title="t('v3.testModelTesting')"
                />
                <span
                  v-else-if="modelTestState(modelId)"
                  class="v5-modelcard__dot"
                  :class="modelTestState(modelId)?.ok ? 'v5-modelcard__dot--ok' : 'v5-modelcard__dot--bad'"
                  :title="modelTestTooltip(modelId)"
                />
              </div>
              <div class="v5-modelcard__row" style="margin-top: 6px; align-items: center; gap: 6px">
                <!-- P8.3: 测试按钮挪到 row 2 第一位, 默认位置可见可点 -->
                <n-tooltip trigger="hover" placement="top">
                  <template #trigger>
                    <button
                      class="v5-modelcard__test"
                      :class="{
                        'v5-modelcard__test--testing': testingModels.has(modelId),
                        'v5-modelcard__test--ok': modelTestState(modelId)?.ok === true,
                        'v5-modelcard__test--bad': modelTestState(modelId)?.ok === false,
                      }"
                      :title="modelTestTooltip(modelId)"
                      :disabled="testingModels.has(modelId)"
                      @click.stop="testModel(modelId)"
                    >
                      <span v-if="testingModels.has(modelId)" class="v5-dots" aria-label="testing">
                        <span></span><span></span><span></span>
                      </span>
                      <n-icon v-else :component="PulseOutline" :size="12" />
                    </button>
                  </template>
                  <template v-if="testingModels.has(modelId)">{{ t("v3.testModelTesting") }}</template>
                  <template v-else-if="modelTestState(modelId)?.ok === true">
                    {{ t("v3.modelTestOk", { model: modelId, ms: modelTestState(modelId)?.ms }) }}
                  </template>
                  <template v-else-if="modelTestState(modelId)?.ok === false">
                    {{ t("v3.modelTestFail", { model: modelId, err: modelTestState(modelId)?.error }) }}
                  </template>
                  <template v-else>{{ t("v3.testModel") }}</template>
                </n-tooltip>
                <n-tooltip trigger="hover" placement="top">
                  <template #trigger>
                    <SpeedBadge :speed="speedForModel(modelId)" :size="11" />
                  </template>
                  {{ speedReasonFor(modelId) }}
                </n-tooltip>
                <CapabilityIcons :tags="tagsForModel(modelId)" :size="11" />

                <span v-if="blockedSet.has(modelId)" class="v3-chip v3-chip--danger" style="flex-shrink: 0; font-size: 10px">
                  🚫 {{ t("v3.blocked") || "blocked" }}
                </span>
                <div class="v5-modelcard__spacer" style="flex: 1"></div>
                <button v-if="exposedSet.has(modelId)" class="v3-btn v3-btn--sm" disabled style="font-size: 10.5px; padding: 2px 8px; min-height: 22px">
                  ✓ {{ t("v3.alreadyExposed") || "已加入" }}
                </button>
                <button v-else class="v3-btn v3-btn--sm v3-btn--accent" style="font-size: 10.5px; padding: 2px 8px; min-height: 22px" @click.stop="addToExposed(modelId)">
                  + {{ t("v3.addToExposed") || "加入" }}
                </button>
              </div>
            </div>
          </div>
          <div v-else class="v5-empty-mini">
            <span v-if="groupModels.length === 0">{{ t("v3.upstreamEmpty") || "上游模型列表为空,请先刷新" }}</span>
            <span v-else-if="allUpstreamExposed">{{ t("v3.allExposed") || "上游模型已全部暴露,无可添加项" }}</span>
            <span v-else>{{ t("v3.noModelsMatching") }}</span>
          </div>
        </template>

        <!-- =================================== -->
        <!-- PASSTHROUGH MODE / AGGREGATE: 单列表 -->
        <!-- =================================== -->
        <template v-else>
          <!-- P11.38: filtered < 30 时加一键测试 head; 否则保持原来无 head 极简布局 -->
          <div
            v-if="filteredAvailable.length > 0 && filteredAvailable.length < 30"
            class="v5-section-head"
          >
            <span class="v5-section-head__title">{{ t("v3.allModelsTitle") || "全部模型" }}</span>
            <span class="v3-chip" style="font-size: 10px">{{ filteredAvailable.length }} / {{ groupModels.length }}</span>
            <div class="v5-section-head__spacer" style="flex: 1"></div>
            <button
              class="v3-btn v3-btn--sm"
              :disabled="bulkTesting"
              :title="t('v3.bulkTestTip')"
              @click="bulkTestModels(filteredAvailable)"
            >
              <n-icon :component="PulseOutline" :size="11" />
              {{ bulkTesting ? t("v3.bulkTestRunning") : t("v3.bulkTestBtn", { n: filteredAvailable.length }) }}
            </button>
          </div>
          <div v-if="filteredAvailable.length" class="v5-cardgrid">
            <div
              v-for="modelId in filteredAvailable"
              :key="modelId"
              class="v5-modelcard"
              :class="{
                'v5-modelcard--blocked': blockedSet.has(modelId),
                'v5-modelcard--selected': isSelected(modelId),
                'v5-modelcard--testfail': modelTestState(modelId)?.ok === false,
                'v5-modelcard--testing': testingModels.has(modelId),
              }"
              @click="toggleSelected(modelId)"
            >
              <div class="v5-modelcard__row" style="align-items: flex-start; gap: 6px">
                <code
                  class="v5-modelcard__name"
                  style="flex: 1; min-width: 0"
                  :title="t('v3.modelClickToCopy') || '点击复制 model ID'"
                  @click.stop="copyModelId(modelId)"
                >{{ modelId }} <FreeBadge v-if="isFreeModel(modelId)" /><span v-if="isRecommendedModel(modelId)" style="font-size: 10px; margin-left: 2px" title="推荐">⭐</span></code>
                <span
                  v-if="testingModels.has(modelId)"
                  class="v5-modelcard__dot v5-modelcard__dot--testing"
                  :title="t('v3.testModelTesting')"
                />
                <span
                  v-else-if="modelTestState(modelId)"
                  class="v5-modelcard__dot"
                  :class="modelTestState(modelId)?.ok ? 'v5-modelcard__dot--ok' : 'v5-modelcard__dot--bad'"
                  :title="modelTestTooltip(modelId)"
                />
              </div>
              <div class="v5-modelcard__row" style="margin-top: 6px; align-items: center; gap: 6px">
                <!-- P8.3: 测试按钮挪到 row 2 第一位, 默认位置可见可点 -->
                <n-tooltip trigger="hover" placement="top">
                  <template #trigger>
                    <button
                      class="v5-modelcard__test"
                      :class="{
                        'v5-modelcard__test--testing': testingModels.has(modelId),
                        'v5-modelcard__test--ok': modelTestState(modelId)?.ok === true,
                        'v5-modelcard__test--bad': modelTestState(modelId)?.ok === false,
                      }"
                      :title="modelTestTooltip(modelId)"
                      :disabled="testingModels.has(modelId)"
                      @click.stop="testModel(modelId)"
                    >
                      <span v-if="testingModels.has(modelId)" class="v5-dots" aria-label="testing">
                        <span></span><span></span><span></span>
                      </span>
                      <n-icon v-else :component="PulseOutline" :size="12" />
                    </button>
                  </template>
                  <template v-if="testingModels.has(modelId)">{{ t("v3.testModelTesting") }}</template>
                  <template v-else-if="modelTestState(modelId)?.ok === true">
                    {{ t("v3.modelTestOk", { model: modelId, ms: modelTestState(modelId)?.ms }) }}
                  </template>
                  <template v-else-if="modelTestState(modelId)?.ok === false">
                    {{ t("v3.modelTestFail", { model: modelId, err: modelTestState(modelId)?.error }) }}
                  </template>
                  <template v-else>{{ t("v3.testModel") }}</template>
                </n-tooltip>
                <n-tooltip trigger="hover" placement="top">
                  <template #trigger>
                    <SpeedBadge :speed="speedForModel(modelId)" :size="11" />
                  </template>
                  {{ speedReasonFor(modelId) }}
                </n-tooltip>
                <CapabilityIcons :tags="tagsForModel(modelId)" :size="11" />

                <span v-if="blockedSet.has(modelId)" class="v3-chip v3-chip--danger" style="flex-shrink: 0; font-size: 10px">
                  🚫 {{ t("v3.blocked") || "blocked" }}
                </span>
                <div class="v5-modelcard__spacer" style="flex: 1"></div>
                <div class="v5-modelcard__aliases">
                  <n-tooltip v-for="a in aliasesFor(modelId)" :key="a.id" placement="top">
                    <template #trigger>
                      <button class="v5-modelcard__alias" style="font-size: 10.5px; padding: 2px 6px" @click.stop="goToAliases(a.alias)">
                        <span>{{ a.alias }}</span>
                      </button>
                    </template>
                    {{ t("v5.mcAliasTip", { weight: a.weight, priority: a.priority }) }}
                  </n-tooltip>
                  <button class="v5-modelcard__alias-add" style="font-size: 10.5px; padding: 2px 8px; min-height: 22px" @click.stop="addAliasFor(modelId)">
                    @{{ t("v3.alias") || "别名" }}
                  </button>
                </div>
              </div>
            </div>
          </div>
          <div v-else class="v5-empty">
            <div class="v5-empty__icon"><n-icon :component="CubeOutline" :size="22" /></div>
            <div class="v5-empty__title">{{ t("v5.noModelsTitle") }}</div>
            <div class="v5-empty__sub">
              <template v-if="modelSearch || availFilter !== 'all'">
                {{ t("v3.noModelsMatching") }}
              </template>
              <template v-else>{{ t("v5.noModelsSub") }}</template>
            </div>
            <button v-if="!group.is_system && !modelSearch && availFilter === 'all'" class="v3-btn" style="margin-top: 8px" @click="showEditGroup = true">
              <n-icon :component="PencilOutline" :size="12" />
              {{ t("v5.openGroupSettings") }}
            </button>
            <button v-if="modelSearch || availFilter !== 'all'" class="v3-btn" style="margin-top: 8px" @click="() => { modelSearch = ''; availFilter = 'all'; }">
              {{ t("v3.clearFilters") }}
            </button>
          </div>
        </template>
      </div>

      <!-- ===== SETTINGS TAB ===== -->
      <div v-else>
        <div class="v5-toolbar">
          <div class="v5-toolbar__hint">{{ t("v5.settingsHint") }}</div>
          <div class="v5-toolbar__spacer">
            <button v-if="!group.is_system" class="v3-btn v3-btn--sm" @click="showEditGroup = true">
              <n-icon :component="PencilOutline" :size="11" />
              {{ t("common.edit") }}
            </button>
          </div>
        </div>

        <div class="v5-settings">
          <div class="v5-settings__row">
            <div class="v5-settings__lbl">
              {{ t("v5.setChannel") }}
              <n-tooltip>
                <template #trigger>
                  <span class="v5-helpicon">
                    <n-icon :component="HelpCircleOutline" :size="11" />
                  </span>
                </template>
                {{ t("v5.setChannelSub") }}
              </n-tooltip>
            </div>
            <div class="v5-settings__val">{{ group.channel_type }}</div>
          </div>
          <div v-if="!isAggregate" class="v5-settings__row">
            <div class="v5-settings__lbl">
              {{ t("v5.setUpstream") }}
              <n-tooltip>
                <template #trigger>
                  <span class="v5-helpicon">
                    <n-icon :component="HelpCircleOutline" :size="11" />
                  </span>
                </template>
                {{ t("v5.setUpstreamSub") }}
              </n-tooltip>
            </div>
            <div class="v5-settings__val">
              <template v-if="group.upstreams?.length">
                {{ group.upstreams[0].url }}
                <span v-if="group.upstreams.length > 1" style="color: var(--v3-ink-4)">
                  + {{ group.upstreams.length - 1 }}
                </span>
              </template>
              <template v-else>—</template>
            </div>
          </div>
          <div class="v5-settings__row">
            <div class="v5-settings__lbl">
              {{ t("v5.setProxyPath") }}
              <n-tooltip>
                <template #trigger>
                  <span class="v5-helpicon">
                    <n-icon :component="HelpCircleOutline" :size="11" />
                  </span>
                </template>
                {{ t("v5.setProxyPathSub") }}
              </n-tooltip>
            </div>
            <div class="v5-settings__val">{{ proxyUrl }}</div>
          </div>
          <div v-if="isAggregate" class="v5-settings__row">
            <div class="v5-settings__lbl">
              {{ t("v5.setMembers") }}
              <n-tooltip>
                <template #trigger>
                  <span class="v5-helpicon">
                    <n-icon :component="HelpCircleOutline" :size="11" />
                  </span>
                </template>
                {{ t("v5.setMembersSub") }}
              </n-tooltip>
            </div>
            <div class="v5-settings__val">{{ subGroups.length }} {{ t("v5.setMembersUnit") }}</div>
          </div>
          <div v-if="!isAggregate" class="v5-settings__row">
            <div class="v5-settings__lbl">
              {{ t("v5.setTestModel") }}
              <n-tooltip>
                <template #trigger>
                  <span class="v5-helpicon">
                    <n-icon :component="HelpCircleOutline" :size="11" />
                  </span>
                </template>
                {{ t("v5.setTestModelSub") }}
              </n-tooltip>
            </div>
            <div class="v5-settings__val">{{ group.test_model || "—" }}</div>
          </div>
          <div v-if="!isAggregate" class="v5-settings__row">
            <div class="v5-settings__lbl">
              {{ t("v5.setValidation") }}
              <n-tooltip>
                <template #trigger>
                  <span class="v5-helpicon">
                    <n-icon :component="HelpCircleOutline" :size="11" />
                  </span>
                </template>
                {{ t("v5.setValidationSub") }}
              </n-tooltip>
            </div>
            <div class="v5-settings__val">
              {{ group.validation_endpoint || t("v5.setValidationDefault") }}
            </div>
          </div>
          <div class="v5-settings__row">
            <div>
              <div class="v5-settings__lbl">{{ t("v5.setProxyPath") }}</div>
              <div class="v5-settings__sub">{{ t("v5.setProxyPathSub") }}</div>
            </div>
            <div class="v5-settings__val">{{ proxyUrl }}</div>
          </div>
          <div class="v5-settings__row">
            <div>
              <div class="v5-settings__lbl">{{ t("v5.setTestModel") }}</div>
              <div class="v5-settings__sub">{{ t("v5.setTestModelSub") }}</div>
            </div>
            <div class="v5-settings__val">{{ group.test_model || "—" }}</div>
          </div>
          <div class="v5-settings__row">
            <div>
              <div class="v5-settings__lbl">{{ t("v5.setValidation") }}</div>
              <div class="v5-settings__sub">{{ t("v5.setValidationSub") }}</div>
            </div>
            <div class="v5-settings__val">
              {{ group.validation_endpoint || t("v5.setValidationDefault") }}
            </div>
          </div>
          <div class="v5-settings__row">
            <div class="v5-settings__lbl">
              {{ t("v5.setSystem") }}
              <n-tooltip>
                <template #trigger>
                  <span class="v5-helpicon">
                    <n-icon :component="HelpCircleOutline" :size="11" />
                  </span>
                </template>
                {{ t("v5.setSystemSub") }}
              </n-tooltip>
            </div>
            <div class="v5-settings__val">
              {{ group.is_system ? t("v5.setSystemYes") : t("v5.setSystemNo") }}
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Modals -->
    <key-create-dialog
      v-model:show="showAddKey"
      :group-id="group.id ?? 0"
      :group-name="group.name"
      @success="refreshFromCreate"
    />
    <key-delete-dialog
      v-model:show="showDeleteKey"
      :group-id="group.id ?? 0"
      :group-name="group.name"
      @success="refreshFromDelete"
    />
    <group-form-modal v-model:show="showEditGroup" :group="group" @success="onGroupUpdated" />
    <group-copy-modal v-model:show="showCopyGroup" :source-group="group" @success="onGroupCopied" />
    <model-alias-modal
      v-if="aliasModalModel"
      v-model:show="showAliasModal"
      :group="group"
      :model-id="aliasModalModel"
      :all-groups="allGroups"
      @success="onAliasCreated"
      @toggle-block="toggleBlock"
      @remove-exposed="removeFromExposed"
    />

    <!-- Key 编辑弹窗: 预填原密钥明文, 未改动只更新 notes; 改了走 in-place rotate -->
    <n-modal
      v-model:show="keyEditDialogShow"
      preset="dialog"
      :title="t('keys.editKeyTitle')"
    >
      <div style="display: flex; flex-direction: column; gap: 12px; margin-top: 8px">
        <div>
          <label style="font: 500 12px var(--v3-sans); color: var(--v3-ink-3); display: block; margin-bottom: 4px">
            {{ t("keys.editKeyValueLabel") }}
            <span style="font: 400 11px var(--v3-mono); color: var(--v3-ink-4); margin-left: 4px">
              {{ t("keys.editKeyValueHint") }}
            </span>
          </label>
          <n-input
            v-model:value="editKeyValueInput"
            type="textarea"
            :placeholder="t('keys.editKeyValuePlaceholder')"
            :rows="3"
            spellcheck="false"
          />
        </div>
        <div>
          <label style="font: 500 12px var(--v3-sans); color: var(--v3-ink-3); display: block; margin-bottom: 4px">
            {{ t("v3.editNotes") || "Notes" }}
          </label>
          <n-input
            v-model:value="editNotesInput"
            type="textarea"
            :placeholder="t('v3.notesPlaceholder')"
            :rows="2"
            maxlength="255"
            show-count
          />
        </div>
      </div>
      <template #action>
        <n-button @click="keyEditDialogShow = false">{{ t("common.cancel") }}</n-button>
        <n-button
          type="primary"
          :disabled="editKeySaving"
          @click="saveKeyEdit"
        >
          {{ editKeySaving ? t("v3.saving") : t("common.save") }}
        </n-button>
      </template>
    </n-modal>
  </section>
</template>
