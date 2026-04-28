<script setup lang="ts">
import {
  aliasesApi,
  RESERVED_ALIASES,
  routingSettingsApi,
  type ModelAliasRow,
  type RoutingSettings,
} from "@/api/aliases";
import { keysApi } from "@/api/keys";
import type { Group } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import { V3_PROVIDER_DIR, pavClass } from "@/data/v3Catalog";
import { findProviderByUpstreams, isFree } from "@/data/freeProviders";
import {
  AddOutline,
  BanOutline,
  CheckmarkCircle,
  CloseOutline,
  CreateOutline,
  CubeOutline,
  FlashOutline,
  HelpCircleOutline,
  LockClosedOutline,
  PulseOutline,
  RefreshOutline,
  SettingsOutline,
  Trash,
} from "@vicons/ionicons5";
import {
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSlider,
  NSpin,
  NSwitch,
  NTooltip,
  useDialog,
  useMessage,
} from "naive-ui";
import { computed, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();

const loading = ref(false);
const rows = ref<ModelAliasRow[]>([]);
const groups = ref<Group[]>([]);
const settings = ref<RoutingSettings>({
  Enabled: true,
  SimpleThreshold: 2000,
  ComplexThreshold: 8000,
});

const DEFAULT_WEIGHT = 100;
const DEFAULT_PRIORITY = 0;

const groupNameById = computed<Record<number, string>>(() => {
  const m: Record<number, string> = {};
  for (const g of groups.value) {
    if (g.id) {m[g.id] = getGroupDisplayName(g);}
  }
  return m;
});

// === Custom Picker Logic ===
const pickerOpen = ref(false);
const pickerSearch = ref("");
const pickerActiveGroupId = ref<number | null>(null);
const pickerTargetAlias = ref<string>("");

function openPicker(alias: string) {
  pickerTargetAlias.value = alias;
  pickerSearch.value = "";
  pickerOpen.value = true;
  if (!pickerActiveGroupId.value && groups.value.length) {
    pickerActiveGroupId.value = groups.value.find(g => g.group_type !== "aggregate")?.id || null;
  }
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

  // 1. 与 group 的 routing mode 同步:specified 用 exposed_models,
  //    passthrough 用 available_models — 与"模型"标签页里看到的一致
  const mode =
    ((g as unknown as { model_routing_mode?: string }).model_routing_mode || "passthrough");
  let source: string[] =
    mode === "specified"
      ? parseModelArray((g as unknown as { exposed_models?: unknown }).exposed_models)
      : parseModelArray((g as unknown as { available_models?: unknown }).available_models);
  // specified 模式下若 exposed 为空,降级用 available 兜底,避免空列表
  if (mode === "specified" && source.length === 0) {
    source = parseModelArray((g as unknown as { available_models?: unknown }).available_models);
  }

  // 2. provider 上下文 — 用于免费检测
  const providerId = findProviderByUpstreams(g.upstreams || [])?.id;

  // 3. 已为当前 alias 在该 group 绑定的 real_model 集合
  const aliasName = pickerTargetAlias.value;
  const boundSet = new Set(
    rows.value
      .filter(r => r.alias === aliasName && r.group_id === g.id)
      .map(r => r.real_model)
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
    groupNameById.value[groupId] ||
    groups.value.find(g => g.id === groupId)?.name ||
    "";
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
    pendingPicks.value = [];
  }
});

async function commitPendingPicks() {
  if (!pickerTargetAlias.value || !pendingPicks.value.length) {
    return;
  }
  pickerSubmitting.value = true;
  let ok = 0;
  let fail = 0;
  for (const p of pendingPicks.value) {
    try {
      await aliasesApi.create({
        alias: pickerTargetAlias.value,
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
  pickerSubmitting.value = false;
  if (ok > 0) {
    message.success(t("v5.maCreated", { ok, fail }));
    pickerOpen.value = false;
    await loadAll();
  } else if (fail > 0) {
    message.error(t("v5.maAllFailed"));
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

// === Edit Mapping ===
const editModalOpen = ref(false);
const editDraft = ref<ModelAliasRow | null>(null);

function openEdit(row: ModelAliasRow) {
  editDraft.value = { ...row };
  editModalOpen.value = true;
}

async function commitEdit() {
  if (!editDraft.value || !editDraft.value.real_model.trim()) {
    return;
  }
  try {
    await aliasesApi.update(editDraft.value.id, {
      real_model: editDraft.value.real_model.trim(),
      weight: editDraft.value.weight,
      priority: editDraft.value.priority,
      enabled: editDraft.value.enabled,
    });
    editModalOpen.value = false;
    await loadAll();
    message.success(t("common.operationSuccess"));
  } catch {
    message.error(t("common.requestFailed"));
  }
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
const groupExposureById = computed<
  Record<number, { mode: string; exposed: Set<string> }>
>(() => {
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

function aliasIsDeadByExposure(row: ModelAliasRow): boolean {
  if (row.is_reserved && row.group_id === 0) {
    return false;
  }
  const info = groupExposureById.value[row.group_id];
  if (!info || info.mode !== "specified") {
    return false;
  }
  return !info.exposed.has(row.real_model);
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

onMounted(() => loadAll());

// === Tier board distribution ===
const tiers = computed(() => {
  return [
    {
      id: "auto-simple",
      title: t("v3.fast") || "Simple",
      icon: FlashOutline,
      class: "v3-tier--simple",
      models: grouped.value.find(g => g.alias === "auto-simple")?.members || [],
    },
    {
      id: "auto-medium",
      title: t("v3.balanced") || "Balanced",
      icon: PulseOutline,
      class: "v3-tier--medium",
      models: grouped.value.find(g => g.alias === "auto-medium")?.members || [],
    },
    {
      id: "auto-complex",
      title: t("v3.max") || "Complex",
      icon: CubeOutline,
      class: "v3-tier--complex",
      models: grouped.value.find(g => g.alias === "auto-complex")?.members || [],
    },
  ];
});

const customAliases = computed(() =>
  grouped.value.filter(g => !["auto-simple", "auto-medium", "auto-complex"].includes(g.alias))
);

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

function removeMapping(row: ModelAliasRow) {
  dialog.warning({
    title: t("v3.aliasDeleteTitle"),
    content: t("v3.aliasDeleteConfirm", { alias: row.alias, model: row.real_model }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      try {
        await aliasesApi.remove(row.id);
        await loadAll();
      } catch {
        message.error(t("common.requestFailed"));
      }
    },
  });
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
        <button class="v3-btn" @click="loadAll">
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

    <!-- Threshold settings with Custom Visual Slider -->
    <div class="v3-thresh-card" style="margin-bottom: 24px; padding: 20px 24px">
      <div style="display: flex; align-items: center; gap: 14px; margin-bottom: 20px">
        <div style="font: 600 13px var(--v3-sans); display: flex; align-items: center; gap: 8px">
          <n-icon :component="SettingsOutline" :size="14" />
          {{ t("v3.complexityThresholds") }}
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
        </div>
        <div class="v5-qs__quickbtns" style="margin-left: 12px">
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
              background: settings.SimpleThreshold === p.simple && settings.ComplexThreshold === p.complex ? p.color + '10' : undefined }"
            @click="applyPreset(p)"
          >
            {{ p.label }} · {{ p.simple }}/{{ p.complex }}
          </button>
        </div>
        <span style="margin-left: auto; display: flex; align-items: center; gap: 12px">
          <span style="font: 500 11px var(--v3-mono); color: var(--v3-ink-3)">
            {{ settings.Enabled ? t("v3.routingActive") : t("v3.routingPassthrough") }}
          </span>
          <n-switch v-model:value="settings.Enabled" @update:value="saveSettings" />
        </span>
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
                  ((settings.ComplexThreshold - settings.SimpleThreshold) / SLIDER_MAX) * 100 + '%',
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

    <!-- Tier Board (Grid Mode) -->
    <div class="v5-alias-grid" style="margin-bottom: 20px">
      <div v-for="tier in tiers" :key="tier.id" :class="['v3-tier', tier.class]">
        <div class="v3-tier__band" />
        <div class="v3-tier__head">
          <div class="v3-tier__head-main" style="align-items: center">
            <div class="v3-tier__title">{{ tier.title }}</div>
            <div class="v3-tier__rule" style="margin-top: 0">
              <span style="opacity: 0.7; font-weight: 500; margin-right: 4px">{{ tier.id }}</span>
              · {{ tier.models.length }}
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
          <div
            v-for="m in tier.models"
            :key="m.id"
            class="v3-alias-chip"
            :class="{
              'v3-alias-chip--disabled': !m.enabled,
              'v3-alias-chip--unexposed': aliasIsDeadByExposure(m),
            }"
            :title="
              aliasIsDeadByExposure(m)
                ? t('v3.aliasUnexposedTip', { model: m.real_model })
                : m.real_model
            "
          >
            <span
              :class="pavClass(inferProvider(m))"
              style="width: 16px; height: 16px; border-radius: 3px; font-size: 8px"
            >
              {{
                V3_PROVIDER_DIR[inferProvider(m)]?.short ||
                  inferProvider(m).slice(0, 2).toUpperCase()
              }}
            </span>
            <span class="v3-alias-chip__name">{{ m.real_model }}</span>
            <span v-if="aliasIsDeadByExposure(m)" class="v3-alias-chip__deadbadge">
              {{ t("v3.aliasUnexposed") || "失效" }}
            </span>
            <div class="v3-alias-chip__actions">
              <button class="v3-alias-chip__btn" @click="openEdit(m)">
                <n-icon :component="CreateOutline" :size="10" />
              </button>
            </div>
            <span
              class="v3-alias-chip__dot"
              :class="{ 'v3-alias-chip__dot--active': m.enabled }"
            />
          </div>
          <div v-if="!tier.models.length" class="v3-tier__empty-hint">
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
          v-for="grp in customAliases"
          :key="grp.alias"
          class="v3-tier"
          :class="{ 'v5-alias-card--reserved': grp.isReserved }"
        >
          <div class="v3-tier__band" />
          <div class="v3-tier__head">
            <div class="v3-tier__head-main" style="align-items: center">
              <div class="v3-tier__title">{{ grp.alias }}</div>
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
                {{ grp.members.length }}
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
            <div
              v-for="m in grp.members"
              :key="m.id"
              class="v3-alias-chip"
              :class="{
                'v3-alias-chip--disabled': !m.enabled,
                'v3-alias-chip--unexposed': aliasIsDeadByExposure(m),
              }"
              :title="
                aliasIsDeadByExposure(m)
                  ? t('v3.aliasUnexposedTip', { model: m.real_model })
                  : m.real_model
              "
            >
              <span
                :class="pavClass(inferProvider(m))"
                style="width: 16px; height: 16px; border-radius: 3px; font-size: 8px"
              >
                {{
                  V3_PROVIDER_DIR[inferProvider(m)]?.short ||
                    inferProvider(m).slice(0, 2).toUpperCase()
                }}
              </span>
              <span class="v3-alias-chip__name">{{ m.real_model }}</span>
              <span v-if="aliasIsDeadByExposure(m)" class="v3-alias-chip__deadbadge">
                {{ t("v3.aliasUnexposed") || "失效" }}
              </span>
              <div class="v3-alias-chip__actions">
                <button class="v3-alias-chip__btn" @click="openEdit(m)">
                  <n-icon :component="CreateOutline" :size="10" />
                </button>
              </div>
              <span
                class="v3-alias-chip__dot"
                :class="{ 'v3-alias-chip__dot--active': m.enabled }"
              />
            </div>
            <div v-if="!grp.members.length" class="v3-tier__empty-hint">
              {{ t("v3.noMappings") }}
            </div>
          </div>
        </div>

        <div
          v-if="!customAliases.length && !newCardOpen"
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
            {{ t("v3.aliasPendingTitle") || "已选" }} ({{ pendingPicks.length }})
          </span>
          <span v-if="!pendingPicks.length" class="v3-picker-pending__hint">
            {{ t("v3.aliasPendingHint") || "点击下方模型 + 加入,可跨分组多选,最后一并确认" }}
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
                    ? t('v3.aliasBlockedTip') || '此模型已被加入黑名单,不会被路由'
                    : m.alreadyBound
                      ? t('v3.aliasAlreadyBound') || '已绑定到此别名'
                      : m.id
                "
                @click="!m.alreadyBound && !m.blocked && togglePendingPick(m.id)"
              >
                <span
                  v-if="m.isFree"
                  class="v3-picker-model-btn__free"
                  :title="t('modelcatalog.freeTag')"
                >🆓</span>
                <span class="v3-picker-model-btn__id">{{ m.id }}</span>
                <n-icon
                  v-if="m.blocked"
                  :component="BanOutline"
                  class="v3-picker-model-btn__add"
                />
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
            {{ t("v3.aliasPendingConfirm", { n: pendingPicks.length }) || `确认添加 ${pendingPicks.length} 条` }}
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

    <!-- Edit Mapping Modal -->
    <n-modal
      v-model:show="editModalOpen"
      preset="dialog"
      :title="t('v3.editMappingTitle')"
      style="width: 440px"
    >
      <div v-if="editDraft" style="padding-top: 16px">
        <n-form label-placement="left" label-width="100" size="small">
          <n-form-item :label="t('v3.realModelLabel')">
            <n-input v-model:value="editDraft.real_model" readonly />
            <div style="font-size: 11px; color: var(--v3-ink-3); margin-top: 4px">
              {{ groupNameById[editDraft.group_id] }}
            </div>
          </n-form-item>
          <n-form-item :label="t('v3.weightLabel')">
            <n-input-number
              v-model:value="editDraft.weight"
              :min="1"
              :max="1000"
              style="width: 100%"
            />
          </n-form-item>
          <n-form-item :label="t('v3.priorityLabel')">
            <n-input-number v-model:value="editDraft.priority" style="width: 100%" />
          </n-form-item>
          <n-form-item :label="t('v3.enabledLabel')">
            <n-switch v-model:value="editDraft.enabled" />
          </n-form-item>
        </n-form>
        <div style="display: flex; justify-content: space-between; gap: 12px; margin-top: 20px">
          <button
            class="v3-btn v3-btn--danger v3-btn--ghost"
            @click="
              () => {
                if (editDraft) {
                  removeMapping(editDraft);
                  editModalOpen = false;
                }
              }
            "
          >
            <n-icon :component="Trash" :size="12" />
            {{ t("common.delete") }}
          </button>
          <div style="display: flex; gap: 12px">
            <button class="v3-btn" @click="editModalOpen = false">{{ t("common.cancel") }}</button>
            <button class="v3-btn v3-btn--accent" @click="commitEdit">
              {{ t("common.save") }}
            </button>
          </div>
        </div>
      </div>
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
.v3-tier__rule {
  font: 600 10px var(--v3-mono);
  color: var(--v3-ink-4);
}

.v3-tier__body {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-content: flex-start;
}
.v3-tier__empty-hint {
  font-size: 11px;
  color: var(--v3-ink-4);
  font-style: italic;
  padding: 2px 0;
}

.v3-alias-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  padding: 2px 6px;
  border-radius: 4px;
  height: 24px;
  transition: all 120ms;
  position: relative;
  flex-shrink: 0;
}
.v3-alias-chip--disabled {
  opacity: 0.5;
  filter: grayscale(1);
  border-style: dashed;
}
.v3-alias-chip--unexposed {
  opacity: 0.65;
  border-style: dashed;
  border-color: var(--v3-warn, oklch(0.7 0.16 80));
  background: oklch(from var(--v3-warn, oklch(0.7 0.16 80)) l c h / 0.06);
}
.v3-alias-chip__deadbadge {
  font: 700 9px var(--v3-mono);
  padding: 1px 4px;
  border-radius: 3px;
  background: var(--v3-warn-soft, oklch(0.95 0.05 80));
  color: var(--v3-warn, oklch(0.55 0.18 70));
  text-transform: uppercase;
  flex-shrink: 0;
}
.v3-alias-chip__name {
  font: 500 11px var(--v3-mono);
  color: var(--v3-ink-2);
  white-space: nowrap;
}
.v3-alias-chip__dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--v3-ink-4);
  flex-shrink: 0;
}
.v3-alias-chip__dot--active {
  background: var(--v3-ok);
}
.v3-alias-chip:hover .v3-alias-chip__dot {
  display: none;
}
.v3-alias-chip__actions {
  display: none;
  align-items: center;
}
.v3-alias-chip:hover .v3-alias-chip__actions {
  display: flex;
}
.v3-alias-chip__btn {
  background: transparent;
  border: 0;
  cursor: pointer;
  padding: 2px;
  border-radius: 4px;
  color: var(--v3-ink-3);
  display: flex;
  align-items: center;
}
.v3-alias-chip__btn:hover {
  background: var(--v3-surface-3);
  color: var(--v3-ink);
}

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
</style>
