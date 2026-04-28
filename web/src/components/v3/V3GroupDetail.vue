<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { aliasesApi, type ModelAliasRow } from "@/api/aliases";
import KeyCreateDialog from "@/components/keys/KeyCreateDialog.vue";
import KeyDeleteDialog from "@/components/keys/KeyDeleteDialog.vue";
import GroupCopyModal from "@/components/keys/GroupCopyModal.vue";
import GroupFormModal from "@/components/keys/GroupFormModal.vue";
import ModelAliasModal from "@/components/keys/ModelAliasModal.vue";
import V3SubGroupTable from "@/components/v3/V3SubGroupTable.vue";
import { findProviderByUpstreams, isFree } from "@/data/freeProviders";
import type { APIKey, Group, GroupStatsResponse, KeyStatus, SubGroupInfo } from "@/types/models";
import { appState, triggerSyncOperationRefresh } from "@/utils/app-state";
import { copy as copyToClipboard } from "@/utils/clipboard";
import { getGroupDisplayName, maskKey } from "@/utils/display";
import {
  AddOutline,
  CheckmarkCircle,
  CloseOutline,
  CopyOutline,
  CubeOutline,
  DownloadOutline,
  HelpCircleOutline,
  KeyOutline,
  LinkOutline,
  LockClosedOutline,
  OpenOutline,
  PencilOutline,
  RefreshOutline,
  RemoveCircleOutline,
  SearchOutline,
  SettingsOutline,
  Trash,
} from "@vicons/ionicons5";
import { NIcon, NPagination, NSpin, NTooltip, useDialog, useMessage } from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";

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
const tab = ref<"keys" | "models" | "settings">("keys");
const faviconFailed = ref(false);

// Aggregate-specific
const subGroups = ref<SubGroupInfo[]>([]);
const subGroupsLoading = ref(false);

// Per-key UI state
const testingKeyId = ref<number | null>(null);
const confirmingDeleteId = ref<number | null>(null);
let confirmDeleteTimer: number | undefined;

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

const groupAvatarShort = computed(() => {
  if (!props.group) {
    return "??";
  }
  const src = props.group.display_name || props.group.name;
  return (
    src
      .replace(/[^A-Za-z0-9]/g, "")
      .slice(0, 2)
      .toUpperCase() || "??"
  );
});

const groupAvatarClass = computed(() => {
  const g = props.group;
  if (!g) {
    return "v3-pav-default";
  }
  if (g.channel_type === "anthropic") {
    return "v3-pav-anthropic";
  }
  if (g.channel_type === "gemini") {
    return "v3-pav-google";
  }
  if (g.is_system) {
    return "v3-pav-default";
  }
  const lower = g.name.toLowerCase();
  for (const key of [
    "groq",
    "cerebras",
    "openrouter",
    "together",
    "cloudflare",
    "mistral",
    "google",
    "cohere",
    "github",
    "anthropic",
  ]) {
    if (lower.includes(key)) {
      return `v3-pav-${key}`;
    }
  }
  return "v3-pav-default";
});

const FAVICON_DOMAIN_MAP: Record<string, string> = {
  groq: "groq.com",
  cerebras: "cerebras.ai",
  openrouter: "openrouter.ai",
  together: "together.ai",
  cloudflare: "cloudflare.com",
  mistral: "mistral.ai",
  google: "ai.google.dev",
  cohere: "cohere.com",
  github: "github.com",
  anthropic: "anthropic.com",
  "default-openai": "openai.com",
  "default-anthropic": "anthropic.com",
  "default-gemini": "gemini.google.com",
};

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

const faviconUrl = computed(() => {
  const g = props.group;
  if (!g) {
    return "";
  }
  const role = (g.system_role || "").trim();
  if (role && FAVICON_DOMAIN_MAP[role]) {
    return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(FAVICON_DOMAIN_MAP[role])}&sz=64`;
  }
  const lower = g.name.toLowerCase();
  for (const k of Object.keys(FAVICON_DOMAIN_MAP)) {
    if (lower.includes(k)) {
      return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(FAVICON_DOMAIN_MAP[k])}&sz=64`;
    }
  }
  const host = extractHost(g.upstreams?.[0]?.url);
  if (host) {
    return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(host)}&sz=64`;
  }
  return "";
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
    tab.value = "keys";
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
  message.success(t("common.refresh") || "Refreshed");
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

const groupModels = computed<string[]>(() => {
  if (isAggregate.value) {
    // Union of sub-groups' available_models
    const set = new Set<string>();
    for (const sg of subGroups.value) {
      const raw = (sg.group as unknown as { available_models?: unknown })?.available_models;
      for (const m of parseAvailableModels(raw)) {
        set.add(m);
      }
    }
    return Array.from(set).sort();
  }
  const raw = (props.group as unknown as { available_models?: unknown })?.available_models;
  return parseAvailableModels(raw);
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

function tierForModel(modelId: string): "fast" | "balanced" | "max" {
  const id = modelId.toLowerCase();
  if (
    id.includes("flash") ||
    id.includes("haiku") ||
    id.includes("8b") ||
    id.includes("instant") ||
    id.includes("mini") ||
    id.includes("small")
  ) {
    return "fast";
  }
  if (
    id.includes("pro") ||
    id.includes("opus") ||
    id.includes("sonnet") ||
    id.includes("70b") ||
    id.includes("405b") ||
    id.includes("4o") ||
    id.includes("o1") ||
    id.includes("r1")
  ) {
    return "max";
  }
  return "balanced";
}

function tierChipClass(tier: "fast" | "balanced" | "max"): string {
  if (tier === "fast") {
    return "v3-chip v3-chip--ok";
  }
  if (tier === "max") {
    return "v3-chip v3-chip--info";
  }
  return "v3-chip v3-chip--warn";
}

function tierLabel(tier: "fast" | "balanced" | "max"): string {
  if (tier === "fast") {
    return t("v3.fast") || "fast";
  }
  if (tier === "max") {
    return t("v3.max") || "max";
  }
  return t("v3.balanced") || "balanced";
}

function isFreeModel(modelId: string): boolean {
  return isFree(matchedProvider.value?.id, modelId) === true;
}

const modelCount = computed(() => groupModels.value.length);

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
const availFilter = ref<"all" | "free" | "paid">("all");
const savingMode = ref(false);

function syncFromGroup() {
  routingMode.value = (props.group?.model_routing_mode as RoutingMode) || "passthrough";
  exposedModels.value = Array.isArray(props.group?.exposed_models)
    ? [...(props.group!.exposed_models as string[])]
    : [];
}
syncFromGroup();
// 只在切换分组时重置本地状态;同一分组下的服务端更新交给 updateGroup 的
// 响应自行同步 — 否则父组件 refresh 一次会把乐观更新打回旧值。
watch(() => props.group?.id, syncFromGroup);

const exposedSet = computed(() => new Set(exposedModels.value));

// 已暴露列表(顶部区,specified 模式才显示) — 应用 search + free/paid 过滤
const filteredExposed = computed(() => {
  let list = exposedModels.value;
  const q = modelSearch.value.toLowerCase().trim();
  if (q) {
    list = list.filter(m => m.toLowerCase().includes(q));
  }
  if (availFilter.value === "free") {
    list = list.filter(m => isFreeModel(m));
  } else if (availFilter.value === "paid") {
    list = list.filter(m => !isFreeModel(m));
  }
  return list;
});

// 上游全部列表(底部区) — 应用 search + free/paid 过滤
const filteredAvailable = computed(() => {
  let list = groupModels.value;
  const q = modelSearch.value.toLowerCase().trim();
  if (q) {
    list = list.filter(m => m.toLowerCase().includes(q));
  }
  if (availFilter.value === "free") {
    list = list.filter(m => isFreeModel(m));
  } else if (availFilter.value === "paid") {
    list = list.filter(m => !isFreeModel(m));
  }
  return list;
});

async function persistGroupPatch(patch: {
  model_routing_mode?: RoutingMode;
  exposed_models?: string[];
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
    if (!modeOk || !exposedOk) {
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

// inline notes editing
const editingNoteId = ref<number | null>(null);
const editingNoteText = ref("");
const savingNotesId = ref<number | null>(null);

function startEditNotes(k: KeyRow) {
  editingNoteId.value = k.id;
  editingNoteText.value = k.notes || "";
}

function cancelNotes() {
  editingNoteId.value = null;
  editingNoteText.value = "";
}

async function saveNotes(k: KeyRow) {
  const trimmed = editingNoteText.value.trim();
  if (trimmed === (k.notes || "")) {
    cancelNotes();
    return;
  }
  savingNotesId.value = k.id;
  const previous = k.notes;
  k.notes = trimmed;
  try {
    await keysApi.updateKeyNotes(k.id, trimmed);
    message.success(t("keys.notesUpdated") || "Notes updated");
    cancelNotes();
  } catch (e) {
    k.notes = previous;
    console.error("update notes failed", e);
    message.error(t("common.requestFailed"));
  } finally {
    savingNotesId.value = null;
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
  await copyText(k.key_value, t("keys.keyCopied") || "Key copied");
}

async function testKey(k: KeyRow) {
  if (!props.group?.id || !k.key_value || testingKeyId.value === k.id) {
    return;
  }
  testingKeyId.value = k.id;
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
  } finally {
    testingKeyId.value = null;
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
        <!-- Avatar (favicon w/ fallback letter avatar) -->
        <div class="v5-hero__avatar v5-picon" style="width: 52px; height: 52px">
          <img
            v-if="faviconUrl && !faviconFailed"
            :src="faviconUrl"
            alt=""
            draggable="false"
            @error="faviconFailed = true"
          />
          <span
            v-else
            :class="['v3-pav', groupAvatarClass]"
            style="width: 100%; height: 100%; border-radius: 0; font-size: 14px"
          >
            {{ groupAvatarShort }}
          </span>
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
            v-if="!isAggregate && matchedProvider?.signupUrl"
            :href="matchedProvider.signupUrl"
            target="_blank"
            rel="noopener"
            class="v5-hero__btn v5-hero__btn--accent"
          >
            <n-icon :component="KeyOutline" :size="13" />
            {{ t("v3.getMoreKeys") || "Get API key" }} →
          </a>
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
            {{ t("common.edit") || "Edit" }}
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
              ]"
            >
              <!-- Row 1: pill (click → test) + (notes OR mask) + edit + copy -->
              <div class="v5-keycard__row">
                <n-tooltip>
                  <template #trigger>
                    <button
                      :class="[
                        'v5-keycard__pill',
                        'v5-keycard__pill--clickable',
                        k.status === 'active'
                          ? 'v5-keycard__pill--ok'
                          : k.status === 'invalid'
                            ? 'v5-keycard__pill--invalid'
                            : 'v5-keycard__pill--unverified',
                        testingKeyId === k.id ? 'v5-keycard__pill--testing' : '',
                      ]"
                      :disabled="testingKeyId === k.id"
                      @click.stop="testKey(k)"
                    >
                      <n-icon
                        v-if="testingKeyId === k.id"
                        :component="RefreshOutline"
                        :size="13"
                        class="v5-keycard__pill-spin"
                      />
                      <n-icon
                        v-else
                        :component="
                          k.status === 'active'
                            ? CheckmarkCircle
                            : k.status === 'invalid'
                              ? CloseOutline
                              : HelpCircleOutline
                        "
                        :size="13"
                      />
                      {{
                        testingKeyId === k.id
                          ? t("v5.kcTesting")
                          : k.status === "active"
                            ? t("keys.valid") || "Valid"
                            : k.status === "invalid"
                              ? t("keys.invalid") || "Invalid"
                              : t("v5.kcUnverified")
                      }}
                    </button>
                  </template>
                  {{ t("v5.kcPillTip") }}
                </n-tooltip>

                <template v-if="editingNoteId === k.id">
                  <input
                    v-model="editingNoteText"
                    class="v5-keycard__notes-input"
                    :placeholder="t('v3.notesPlaceholder')"
                    @keyup.enter="saveNotes(k)"
                    @keyup.esc="cancelNotes"
                    @click.stop
                  />
                  <n-tooltip>
                    <template #trigger>
                      <button
                        class="v5-keycard__iconbtn v5-keycard__iconbtn--ok"
                        :disabled="savingNotesId === k.id"
                        @click.stop="saveNotes(k)"
                      >
                        <n-icon :component="CheckmarkCircle" :size="14" />
                      </button>
                    </template>
                    {{ t("common.save") || "Save" }}
                  </n-tooltip>
                  <n-tooltip>
                    <template #trigger>
                      <button class="v5-keycard__iconbtn" @click.stop="cancelNotes">
                        <n-icon :component="CloseOutline" :size="14" />
                      </button>
                    </template>
                    {{ t("common.cancel") || "Cancel" }}
                  </n-tooltip>
                </template>

                <template v-else>
                  <n-tooltip v-if="k.notes" placement="top">
                    <template #trigger>
                      <code class="v5-keycard__mask v5-keycard__mask--notes">{{ k.notes }}</code>
                    </template>
                    <span style="font-family: var(--v3-mono); font-size: 11.5px">
                      {{ maskKey(k.key_value) }}
                    </span>
                  </n-tooltip>
                  <code v-else class="v5-keycard__mask">{{ maskKey(k.key_value) }}</code>
                  <n-tooltip>
                    <template #trigger>
                      <button class="v5-keycard__iconbtn" @click.stop="startEditNotes(k)">
                        <n-icon :component="PencilOutline" :size="14" />
                      </button>
                    </template>
                    {{ t("v3.editNotes") || "Notes" }}
                  </n-tooltip>
                  <n-tooltip>
                    <template #trigger>
                      <button class="v5-keycard__iconbtn" @click.stop="copyKey(k)">
                        <n-icon :component="CopyOutline" :size="14" />
                      </button>
                    </template>
                    {{ t("v5.copy") }}
                  </n-tooltip>
                </template>
              </div>

              <!-- Row 2: stats + icon actions -->
              <div class="v5-keycard__row">
                <div class="v5-keycard__inline">
                  <span>
                    {{ t("v5.kcReq") }}
                    <b>{{ (k.request_count ?? 0).toLocaleString() }}</b>
                  </span>
                  <span :class="(k.failure_count || 0) > 0 ? 'v5-keycard__inline-fail--err' : ''">
                    {{ t("v5.kcFail") }}
                    <b>{{ k.failure_count ?? 0 }}</b>
                  </span>
                  <span>{{ formatRelative(k.last_used_at) }}</span>
                </div>
                <div class="v5-keycard__actions">
                  <n-tooltip v-if="k.status === 'invalid'">
                    <template #trigger>
                      <button
                        class="v5-keycard__iconbtn v5-keycard__iconbtn--lg"
                        @click="restoreKey(k)"
                      >
                        <n-icon :component="RefreshOutline" :size="16" />
                      </button>
                    </template>
                    {{ t("keys.restoreKey") || "Restore" }}
                  </n-tooltip>
                  <n-tooltip>
                    <template #trigger>
                      <button
                        :class="[
                          'v5-keycard__iconbtn',
                          'v5-keycard__iconbtn--lg',
                          'v5-keycard__iconbtn--danger',
                          confirmingDeleteId === k.id ? 'v5-keycard__iconbtn--armed' : '',
                        ]"
                        @click="deleteKey(k)"
                      >
                        <n-icon :component="Trash" :size="16" />
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
        <!-- Toolbar: mode toggle + search + free/paid filter -->
        <div class="v5-toolbar">
          <div class="v5-toolbar__hint">
            {{ t("v5.modelsHint") }}
            <span v-if="modelsRefreshedAtDisplay" style="margin-left: 8px; color: var(--v3-ink-4)">
              · {{ t("v5.refreshedAt") }} {{ modelsRefreshedAtDisplay }}
            </span>
          </div>
          <div class="v5-toolbar__spacer" style="gap: 16px; flex-wrap: wrap">
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
            <div class="v5-search" style="width: 220px">
              <n-icon :component="SearchOutline" :size="12" />
              <input v-model="modelSearch" :placeholder="t('v3.filterModels') || 'Filter models…'" />
            </div>
            <div class="v5-modemode">
              <button class="v3-btn v3-btn--sm" :class="{ 'v3-btn--accent': availFilter === 'all' }" @click="availFilter = 'all'">
                {{ t("common.all") || "All" }}
              </button>
              <button class="v3-btn v3-btn--sm" :class="{ 'v3-btn--accent': availFilter === 'free' }" @click="availFilter = 'free'">
                🆓 {{ t("modelcatalog.freeTag") || "Free" }}
              </button>
              <button class="v3-btn v3-btn--sm" :class="{ 'v3-btn--accent': availFilter === 'paid' }" @click="availFilter = 'paid'">
                {{ t("v3.paid") || "Paid" }}
              </button>
            </div>
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
          </div>

          <div v-if="filteredExposed.length" class="v5-cardgrid">
            <div
              v-for="modelId in filteredExposed"
              :key="`exposed-${modelId}`"
              class="v5-modelcard v5-modelcard--exposed"
              :class="{ 'v5-modelcard--dragging': dragIdx === exposedModels.indexOf(modelId) }"
              draggable="true"
              @dragstart="onExposedDragStart(exposedModels.indexOf(modelId))"
              @dragover.prevent
              @drop="onExposedDrop(exposedModels.indexOf(modelId))"
            >
              <div class="v5-modelcard__row">
                <code class="v5-modelcard__name">{{ modelId }}</code>
              </div>
              <div class="v5-modelcard__row" style="margin-top: 6px; align-items: center; gap: 6px">
                <span :class="tierChipClass(tierForModel(modelId))" style="flex-shrink: 0; font-size: 10px">
                  {{ tierLabel(tierForModel(modelId)) }}
                </span>
                <span v-if="isFreeModel(modelId)" class="v3-chip v3-chip--info" style="flex-shrink: 0; font-size: 10px">
                  {{ t("v3.free") || "free" }}
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
                  <button class="v3-btn v3-btn--sm v3-btn--icon" style="min-height: 22px" :title="t('v3.removeFromExposed') || 'Remove from exposed'" @click.stop="removeFromExposed(modelId)">
                    <n-icon :component="CloseOutline" :size="12" />
                  </button>
                </div>
              </div>
            </div>
          </div>
          <div v-else class="v5-empty-mini">
            <span v-if="exposedModels.length === 0">{{ t("v3.noExposedModels") || "暂无已暴露模型,从下方加入" }}</span>
            <span v-else>{{ t("v3.noModelsMatching") || "No models match your current filters." }}</span>
          </div>

          <div class="v5-section-head" style="margin-top: 16px">
            <span class="v5-section-head__title">{{ t("v3.upstreamAll") || "上游全部" }}</span>
            <span class="v3-chip" style="font-size: 10px">{{ filteredAvailable.length }} / {{ groupModels.length }}</span>
          </div>

          <div v-if="filteredAvailable.length" class="v5-cardgrid">
            <div
              v-for="modelId in filteredAvailable"
              :key="`avail-${modelId}`"
              class="v5-modelcard"
              :class="{ 'v5-modelcard--inexposed': exposedSet.has(modelId) }"
            >
              <div class="v5-modelcard__row">
                <code class="v5-modelcard__name">{{ modelId }}</code>
              </div>
              <div class="v5-modelcard__row" style="margin-top: 6px; align-items: center; gap: 6px">
                <span :class="tierChipClass(tierForModel(modelId))" style="flex-shrink: 0; font-size: 10px">
                  {{ tierLabel(tierForModel(modelId)) }}
                </span>
                <span v-if="isFreeModel(modelId)" class="v3-chip v3-chip--info" style="flex-shrink: 0; font-size: 10px">
                  {{ t("v3.free") || "free" }}
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
            <span>{{ groupModels.length === 0 ? (t("v3.upstreamEmpty") || "上游模型列表为空,请先刷新") : (t("v3.noModelsMatching") || "No models match.") }}</span>
          </div>
        </template>

        <!-- =================================== -->
        <!-- PASSTHROUGH MODE / AGGREGATE: 单列表 -->
        <!-- =================================== -->
        <template v-else>
          <div v-if="filteredAvailable.length" class="v5-cardgrid">
            <div v-for="modelId in filteredAvailable" :key="modelId" class="v5-modelcard">
              <div class="v5-modelcard__row">
                <code class="v5-modelcard__name">{{ modelId }}</code>
              </div>
              <div class="v5-modelcard__row" style="margin-top: 6px; align-items: center; gap: 6px">
                <span :class="tierChipClass(tierForModel(modelId))" style="flex-shrink: 0; font-size: 10px">
                  {{ tierLabel(tierForModel(modelId)) }}
                </span>
                <span v-if="isFreeModel(modelId)" class="v3-chip v3-chip--info" style="flex-shrink: 0; font-size: 10px">
                  {{ t("v3.free") || "free" }}
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
                {{ t("v3.noModelsMatching") || "No models match your current filters." }}
              </template>
              <template v-else>{{ t("v5.noModelsSub") }}</template>
            </div>
            <button v-if="!group.is_system && !modelSearch && availFilter === 'all'" class="v3-btn" style="margin-top: 8px" @click="showEditGroup = true">
              <n-icon :component="PencilOutline" :size="12" />
              {{ t("v5.openGroupSettings") }}
            </button>
            <button v-if="modelSearch || availFilter !== 'all'" class="v3-btn" style="margin-top: 8px" @click="() => { modelSearch = ''; availFilter = 'all'; }">
              {{ t("v3.clearFilters") || "Clear filters" }}
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
              {{ t("common.edit") || "Edit" }}
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
      @success="onAliasCreated"
    />
  </section>
</template>
