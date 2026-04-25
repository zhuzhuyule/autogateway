<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { findFreeModel, type ModelTier } from "@/data/freeProviders";
import type {
  Group,
  GroupConfigOption,
  GroupStatsResponse,
  ParentAggregateGroup,
  SubGroupInfo,
} from "@/types/models";
import { appState } from "@/utils/app-state";
import { copy } from "@/utils/clipboard";
import { getGroupDisplayName, maskProxyKeys } from "@/utils/display";
import { CopyOutline, EyeOffOutline, EyeOutline, Pencil, RefreshOutline, Trash } from "@vicons/ionicons5";
import {
  NButton,
  NButtonGroup,
  NCard,
  NCollapse,
  NCollapseItem,
  NEmpty,
  NForm,
  NFormItem,
  NGrid,
  NGridItem,
  NIcon,
  NInput,
  NSpin,
  NTag,
  NTooltip,
  useDialog,
  useMessage,
} from "naive-ui";
import { computed, h, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import AggregateGroupModal from "./AggregateGroupModal.vue";
import GroupCopyModal from "./GroupCopyModal.vue";
import GroupFormModal from "./GroupFormModal.vue";

const { t } = useI18n();

interface Props {
  group: Group | null;
  groups?: Group[];
  subGroups?: SubGroupInfo[];
}

interface Emits {
  (e: "refresh", value: Group): void;
  (e: "delete", value: Group): void;
  (e: "copy-success", group: Group): void;
  (e: "navigate-to-group", groupId: number): void;
}

const props = defineProps<Props>();

const emit = defineEmits<Emits>();

const stats = ref<GroupStatsResponse | null>(null);
const loading = ref(false);
const dialog = useDialog();
const message = useMessage();

// 上游模型(只读浏览)
const modelsExpanded = ref<string[]>([]);
const modelsSearch = ref("");
const modelsRefreshLoading = ref(false);

// 解析 group.available_models (后端返回 datatypes.JSON,前端可能是 array 或 string)
function parseAvailable(raw: unknown): string[] {
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

const groupAvailableModels = computed<string[]>(() => {
  if (!props.group) return [];
  return parseAvailable((props.group as unknown as { available_models?: unknown }).available_models);
});

const filteredGroupModels = computed<string[]>(() => {
  const q = modelsSearch.value.trim().toLowerCase();
  return groupAvailableModels.value
    .filter((m) => !q || m.toLowerCase().includes(q))
    .slice()
    .sort((a, b) => {
      const fa = !!findFreeModel(a);
      const fb = !!findFreeModel(b);
      if (fa !== fb) return fa ? -1 : 1;
      return a.localeCompare(b);
    });
});

// 聚合分组: 各子分组贡献的模型并集 + 来源
interface AggregatedModel {
  modelId: string;
  providers: string[]; // 子分组 displayName 或 name
}
const aggregateModels = computed<AggregatedModel[]>(() => {
  if (!props.subGroups || props.subGroups.length === 0) return [];
  const map = new Map<string, Set<string>>();
  for (const sg of props.subGroups) {
    const list = parseAvailable((sg.group as unknown as { available_models?: unknown }).available_models);
    const label = sg.group.display_name || sg.group.name;
    for (const m of list) {
      if (!map.has(m)) map.set(m, new Set());
      map.get(m)!.add(label);
    }
  }
  return Array.from(map.entries()).map(([modelId, set]) => ({
    modelId,
    providers: Array.from(set),
  }));
});

const filteredAggregateModels = computed<AggregatedModel[]>(() => {
  const q = modelsSearch.value.trim().toLowerCase();
  return aggregateModels.value
    .filter((r) => !q || r.modelId.toLowerCase().includes(q))
    .slice()
    .sort((a, b) => {
      const fa = !!findFreeModel(a.modelId);
      const fb = !!findFreeModel(b.modelId);
      if (fa !== fb) return fa ? -1 : 1;
      return a.modelId.localeCompare(b.modelId);
    });
});

const modelsRefreshedAtDisplay = computed(() => {
  const at = (props.group as unknown as { models_refreshed_at?: string | null })?.models_refreshed_at;
  if (!at) return "";
  try {
    return new Date(at).toLocaleString();
  } catch {
    return at;
  }
});

function tierTagType(tier?: ModelTier): "success" | "warning" | "error" | "default" {
  switch (tier) {
    case "fast": return "success";
    case "balanced": return "warning";
    case "max": return "error";
    default: return "default";
  }
}
function tierLabel(tier?: ModelTier): string {
  if (!tier) return "";
  return t(`modelcatalog.tier${tier.charAt(0).toUpperCase() + tier.slice(1)}`);
}

async function refreshGroupModels() {
  if (!props.group?.id) return;
  modelsRefreshLoading.value = true;
  try {
    const response = await fetch(`/api/groups/${props.group.id}/refresh-models`, {
      method: "POST",
      headers: {
        "Authorization": `Bearer ${localStorage.getItem("authKey") || ""}`,
        "Content-Type": "application/json",
      },
    });
    const result = await response.json();
    if (!response.ok || result.code !== 0) {
      throw new Error(result.message || `HTTP ${response.status}`);
    }
    const list = (result.data?.models || []) as string[];
    message.success(t("keys.refreshModelsSuccess", { n: list.length }));
    emit("refresh", props.group);
  } catch (e) {
    message.error((e as Error).message);
  } finally {
    modelsRefreshLoading.value = false;
  }
}

async function copyModelId(id: string) {
  await copy(id);
  message.success(t("keys.modelIdCopied"));
}
const showEditModal = ref(false);
const showCopyModal = ref(false);
const showAggregateEditModal = ref(false);
const delLoading = ref(false);
const confirmInput = ref("");
const expandedName = ref<string[]>([]);
const configOptions = ref<GroupConfigOption[]>([]);
const showProxyKeys = ref(false);
const parentAggregateGroups = ref<ParentAggregateGroup[]>([]);

const proxyKeysDisplay = computed(() => {
  if (!props.group?.proxy_keys) {
    return "-";
  }
  if (showProxyKeys.value) {
    return props.group.proxy_keys.replace(/,/g, "\n");
  }
  return maskProxyKeys(props.group.proxy_keys);
});

const hasAdvancedConfig = computed(() => {
  return (
    (props.group?.config && Object.keys(props.group.config).length > 0) ||
    props.group?.param_overrides ||
    (props.group?.header_rules && props.group.header_rules.length > 0)
  );
});

// 判断是否为聚合分组
const isAggregateGroup = computed(() => {
  return props.group?.group_type === "aggregate";
});

// 计算有效子分组数（weight > 0 且有可用密钥）
const activeSubGroupsCount = computed(() => {
  return props.subGroups?.filter(sg => sg.weight > 0 && sg.active_keys > 0).length || 0;
});

// 计算禁用子分组数（weight = 0）
const disabledSubGroupsCount = computed(() => {
  return props.subGroups?.filter(sg => sg.weight === 0).length || 0;
});

// 计算无效子分组数（weight > 0 但无可用密钥）
const unavailableSubGroupsCount = computed(() => {
  return props.subGroups?.filter(sg => sg.weight > 0 && sg.active_keys === 0).length || 0;
});

async function copyProxyKeys() {
  if (!props.group?.proxy_keys) {
    return;
  }
  const keysToCopy = props.group.proxy_keys.replace(/,/g, "\n");
  const success = await copy(keysToCopy);
  if (success) {
    window.$message.success(t("keys.proxyKeysCopied"));
  } else {
    window.$message.error(t("keys.copyFailed"));
  }
}

onMounted(() => {
  loadStats();
  loadConfigOptions();
  loadParentAggregateGroups();
});

watch(
  () => props.group,
  () => {
    resetPage();
    loadStats();
    loadParentAggregateGroups();
  }
);

// 监听任务完成事件，自动刷新当前分组数据
watch(
  () => [appState.groupDataRefreshTrigger, appState.syncOperationTrigger],
  () => {
    if (!props.group) {
      return;
    }

    // 检查是否需要刷新当前分组的数据
    const isCurrentGroupTask =
      appState.lastCompletedTask && appState.lastCompletedTask.groupName === props.group.name;
    const isCurrentGroupSync =
      appState.lastSyncOperation && appState.lastSyncOperation.groupName === props.group.name;

    const shouldRefresh =
      (isCurrentGroupTask &&
        ["KEY_VALIDATION", "KEY_IMPORT", "KEY_DELETE"].includes(
          appState.lastCompletedTask?.taskType || ""
        )) ||
      isCurrentGroupSync;

    if (shouldRefresh) {
      loadStats();
    }
  }
);

async function loadStats() {
  if (!props.group?.id) {
    stats.value = null;
    return;
  }

  try {
    loading.value = true;
    if (props.group?.id) {
      stats.value = await keysApi.getGroupStats(props.group.id);
    }
  } finally {
    loading.value = false;
  }
}

async function loadConfigOptions() {
  try {
    const options = await keysApi.getGroupConfigOptions();
    configOptions.value = options || [];
  } catch (error) {
    console.error("Failed to load config options:", error);
  }
}

async function loadParentAggregateGroups() {
  if (!props.group?.id || props.group.group_type === "aggregate") {
    parentAggregateGroups.value = [];
    return;
  }

  try {
    const parentGroups = await keysApi.getParentAggregateGroups(props.group.id);
    parentAggregateGroups.value = parentGroups || [];
  } catch (error) {
    console.error("Failed to load parent aggregate groups:", error);
    parentAggregateGroups.value = [];
  }
}

function getConfigDisplayName(key: string): string {
  const option = configOptions.value.find(opt => opt.key === key);
  return option?.name || key;
}

function getConfigDescription(key: string): string {
  const option = configOptions.value.find(opt => opt.key === key);
  return option?.description || t("keys.noDescription");
}

function handleEdit() {
  if (!props.group) {
    return;
  }
  if (props.group.group_type === "aggregate") {
    showAggregateEditModal.value = true;
    return;
  }
  showEditModal.value = true;
}

function handleCopy() {
  showCopyModal.value = true;
}

function handleNavigateToGroup(groupId: number) {
  emit("navigate-to-group", groupId);
}

function handleGroupEdited(newGroup: Group) {
  showEditModal.value = false;
  if (newGroup) {
    emit("refresh", newGroup);
  }
}

function handleAggregateGroupEdited(newGroup: Group) {
  showAggregateEditModal.value = false;
  if (newGroup) {
    emit("refresh", newGroup);
  }
}

function handleGroupCopied(newGroup: Group) {
  showCopyModal.value = false;
  if (newGroup) {
    emit("copy-success", newGroup);
  }
}

async function handleDelete() {
  if (!props.group || delLoading.value) {
    return;
  }

  dialog.warning({
    title: t("keys.deleteGroup"),
    content: t("keys.confirmDeleteGroup", { name: getGroupDisplayName(props.group) }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: () => {
      confirmInput.value = ""; // Reset before opening second dialog
      dialog.create({
        title: t("keys.enterGroupNameToConfirm"),
        content: () =>
          h("div", null, [
            h("p", null, [
              t("keys.dangerousOperation"),
              h("strong", { style: { color: "#d03050" } }, props.group?.name),
              t("keys.toConfirmDeletion"),
            ]),
            h(NInput, {
              value: confirmInput.value,
              "onUpdate:value": v => {
                confirmInput.value = v;
              },
              placeholder: t("keys.enterGroupName"),
            }),
          ]),
        positiveText: t("keys.confirmDelete"),
        negativeText: t("common.cancel"),
        onPositiveClick: async () => {
          if (confirmInput.value !== props.group?.name) {
            window.$message.error(t("keys.incorrectGroupName"));
            return false; // Prevent dialog from closing
          }

          delLoading.value = true;
          try {
            if (props.group?.id) {
              await keysApi.deleteGroup(props.group.id);
              emit("delete", props.group);
            }
          } finally {
            delLoading.value = false;
          }
        },
      });
    },
  });
}

function formatNumber(num: number): string {
  if (num >= 1000) {
    return `${(num / 1000).toFixed(1)}K`;
  }
  return num.toString();
}

function formatPercentage(num: number): string {
  if (num <= 0) {
    return "0";
  }
  return `${(num * 100).toFixed(1)}%`;
}

async function copyUrl(url: string) {
  if (!url) {
    return;
  }
  const success = await copy(url);
  if (success) {
    window.$message.success(t("keys.urlCopied"));
  } else {
    window.$message.error(t("keys.copyFailed"));
  }
}

function resetPage() {
  showEditModal.value = false;
  showCopyModal.value = false;
  expandedName.value = [];
}
</script>

<template>
  <div class="group-info-container">
    <n-card :bordered="false" class="group-info-card">
      <template #header>
        <div class="card-header">
          <div class="header-left">
            <h3 class="group-title">
              {{ group ? getGroupDisplayName(group) : t("keys.selectGroup") }}
              <n-tooltip trigger="hover" v-if="group && group.endpoint">
                <template #trigger>
                  <code class="group-url" @click="copyUrl(group.endpoint)">
                    {{ group.endpoint }}
                  </code>
                </template>
                {{ t("keys.clickToCopy") }}
              </n-tooltip>
            </h3>
          </div>
          <div class="header-actions">
            <n-button
              v-if="group?.group_type !== 'aggregate'"
              quaternary
              circle
              size="small"
              @click="handleCopy"
              :title="t('keys.copyGroup')"
              :disabled="!group"
            >
              <template #icon>
                <n-icon :component="CopyOutline" />
              </template>
            </n-button>
            <n-button
              quaternary
              circle
              size="small"
              @click="handleEdit"
              :title="t('keys.editGroup')"
            >
              <template #icon>
                <n-icon :component="Pencil" />
              </template>
            </n-button>
            <n-button
              v-if="!group?.is_system"
              quaternary
              circle
              size="small"
              @click="handleDelete"
              :title="t('keys.deleteGroup')"
              type="error"
              :disabled="!group"
            >
              <template #icon>
                <n-icon :component="Trash" />
              </template>
            </n-button>
          </div>
        </div>
      </template>

      <n-divider style="margin: 0; margin-bottom: 12px" />
      <!-- 统计摘要区 -->
      <div class="stats-summary">
        <n-spin :show="loading" size="small">
          <n-grid cols="2 s:4" :x-gap="12" :y-gap="12" responsive="screen">
            <n-grid-item span="1">
              <!-- 聚合分组：子分组统计 -->
              <n-statistic
                v-if="isAggregateGroup"
                :label="`${t('keys.subGroups')}：${props.subGroups?.length || 0}`"
              >
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="success" size="20">
                      {{ activeSubGroupsCount }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.activeSubGroups") }}
                </n-tooltip>
                <n-divider vertical />
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="warning" size="20">
                      {{ disabledSubGroupsCount }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.disabledSubGroups") }}
                </n-tooltip>
                <n-divider vertical />
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="error" size="20">
                      {{ unavailableSubGroupsCount }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.unavailableSubGroups") }}
                </n-tooltip>
              </n-statistic>

              <!-- 标准分组：密钥统计 -->
              <n-statistic
                v-else
                :label="`${t('keys.keyCount')}：${stats?.key_stats?.total_keys ?? 0}`"
              >
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="success" size="20">
                      {{ stats?.key_stats?.active_keys ?? 0 }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.activeKeyCount") }}
                </n-tooltip>
                <n-divider vertical />
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="error" size="20">
                      {{ stats?.key_stats?.invalid_keys ?? 0 }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.invalidKeyCount") }}
                </n-tooltip>
              </n-statistic>
            </n-grid-item>
            <n-grid-item span="1">
              <n-statistic
                :label="`${t('keys.stats24Hour')}：${formatNumber(stats?.stats_24_hour?.total_requests ?? 0)}`"
              >
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="error" size="20">
                      {{ formatNumber(stats?.stats_24_hour?.failed_requests ?? 0) }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.stats24HourFailed") }}
                </n-tooltip>
                <n-divider vertical />
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="error" size="20">
                      {{ formatPercentage(stats?.stats_24_hour?.failure_rate ?? 0) }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.stats24HourFailureRate") }}
                </n-tooltip>
              </n-statistic>
            </n-grid-item>
            <n-grid-item span="1">
              <n-statistic
                :label="`${t('keys.stats7Day')}：${formatNumber(stats?.stats_7_day?.total_requests ?? 0)}`"
              >
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="error" size="20">
                      {{ formatNumber(stats?.stats_7_day?.failed_requests ?? 0) }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.stats7DayFailed") }}
                </n-tooltip>
                <n-divider vertical />
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="error" size="20">
                      {{ formatPercentage(stats?.stats_7_day?.failure_rate ?? 0) }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.stats7DayFailureRate") }}
                </n-tooltip>
              </n-statistic>
            </n-grid-item>
            <n-grid-item span="1">
              <n-statistic
                :label="`${t('keys.stats30Day')}：${formatNumber(stats?.stats_30_day?.total_requests ?? 0)}`"
              >
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="error" size="20">
                      {{ formatNumber(stats?.stats_30_day?.failed_requests ?? 0) }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.stats30DayFailed") }}
                </n-tooltip>
                <n-divider vertical />
                <n-tooltip trigger="hover">
                  <template #trigger>
                    <n-gradient-text type="error" size="20">
                      {{ formatPercentage(stats?.stats_30_day?.failure_rate ?? 0) }}
                    </n-gradient-text>
                  </template>
                  {{ t("keys.stats30DayFailureRate") }}
                </n-tooltip>
              </n-statistic>
            </n-grid-item>
          </n-grid>
        </n-spin>
      </div>
      <n-divider style="margin: 0" />

      <!-- 详细信息区（可折叠） -->
      <div class="details-section">
        <n-collapse accordion v-model:expanded-names="expandedName">
          <n-collapse-item :title="t('keys.detailInfo')" name="details">
            <div class="details-content">
              <div class="detail-section">
                <h4 class="section-title">{{ t("keys.basicInfo") }}</h4>
                <n-form label-placement="left" label-width="140px" label-align="right">
                  <n-grid cols="1 m:2">
                    <n-grid-item>
                      <n-form-item :label="`${t('keys.groupName')}：`">
                        {{ group?.name }}
                      </n-form-item>
                    </n-grid-item>
                    <n-grid-item>
                      <n-form-item :label="`${t('keys.displayName')}：`">
                        {{ group?.display_name }}
                      </n-form-item>
                    </n-grid-item>
                    <n-grid-item>
                      <n-form-item :label="`${t('keys.channelType')}：`">
                        {{ group?.channel_type }}
                      </n-form-item>
                    </n-grid-item>
                    <n-grid-item>
                      <n-form-item :label="`${t('keys.sortOrder')}：`">
                        {{ group?.sort }}
                      </n-form-item>
                    </n-grid-item>
                    <!-- 标准分组才显示测试模型和测试路径 -->
                    <n-grid-item v-if="!isAggregateGroup">
                      <n-form-item :label="`${t('keys.testModel')}：`">
                        {{ group?.test_model }}
                      </n-form-item>
                    </n-grid-item>
                    <n-grid-item v-if="!isAggregateGroup && group?.channel_type !== 'gemini'">
                      <n-form-item :label="`${t('keys.testPath')}：`">
                        {{ group?.validation_endpoint }}
                      </n-form-item>
                    </n-grid-item>
                    <n-grid-item :span="2">
                      <n-form-item :label="`${t('keys.proxyKeys')}：`">
                        <div class="proxy-keys-content">
                          <span class="key-text">{{ proxyKeysDisplay }}</span>
                          <n-button-group size="small" class="key-actions" v-if="group?.proxy_keys">
                            <n-tooltip trigger="hover">
                              <template #trigger>
                                <n-button quaternary circle @click="showProxyKeys = !showProxyKeys">
                                  <template #icon>
                                    <n-icon
                                      :component="showProxyKeys ? EyeOffOutline : EyeOutline"
                                    />
                                  </template>
                                </n-button>
                              </template>
                              {{ showProxyKeys ? t("keys.hideKeys") : t("keys.showKeys") }}
                            </n-tooltip>
                            <n-tooltip trigger="hover">
                              <template #trigger>
                                <n-button quaternary circle @click="copyProxyKeys">
                                  <template #icon>
                                    <n-icon :component="CopyOutline" />
                                  </template>
                                </n-button>
                              </template>
                              {{ t("keys.copyKeys") }}
                            </n-tooltip>
                          </n-button-group>
                        </div>
                      </n-form-item>
                    </n-grid-item>
                    <n-grid-item :span="2">
                      <n-form-item :label="`${t('common.description')}：`">
                        <div class="description-content">
                          {{ group?.description || "-" }}
                        </div>
                      </n-form-item>
                    </n-grid-item>
                  </n-grid>
                </n-form>
              </div>

              <!-- 聚合引用区（仅普通分组且存在被引用关系时显示） -->
              <div
                class="detail-section"
                v-if="!isAggregateGroup && parentAggregateGroups.length > 0"
              >
                <h4 class="section-title">{{ t("keys.aggregateReferences") }}</h4>
                <n-form label-placement="left" label-width="140px">
                  <n-form-item
                    v-for="(parent, index) in parentAggregateGroups"
                    :key="parent.group_id"
                    class="aggregate-ref-item"
                    :label="`${t('keys.aggregateGroup')} ${index + 1}:`"
                  >
                    <span class="aggregate-weight">
                      <n-tag size="small" type="info">
                        {{ t("keys.weight") }}: {{ parent.weight }}
                      </n-tag>
                    </span>
                    <n-input
                      class="aggregate-name"
                      :value="parent.display_name || parent.name"
                      readonly
                      size="small"
                      style="margin-left: 5px; margin-right: 8px"
                    />
                    <n-button
                      round
                      tertiary
                      type="default"
                      size="tiny"
                      @click="handleNavigateToGroup(parent.group_id)"
                      :title="t('keys.viewGroupInfo')"
                    >
                      <template #icon>
                        <n-icon :component="EyeOutline" />
                      </template>
                      {{ t("common.view") }}
                    </n-button>
                  </n-form-item>
                </n-form>
              </div>

              <!-- 标准分组才显示上游地址 -->
              <div class="detail-section" v-if="!isAggregateGroup">
                <h4 class="section-title">{{ t("keys.upstreamAddresses") }}</h4>
                <n-form label-placement="left" label-width="140px">
                  <n-form-item
                    v-for="(upstream, index) in group?.upstreams ?? []"
                    :key="index"
                    class="upstream-item"
                    :label="`${t('keys.upstream')} ${index + 1}:`"
                  >
                    <span class="upstream-weight">
                      <n-tag size="small" type="info">
                        {{ t("keys.weight") }}: {{ upstream.weight }}
                      </n-tag>
                    </span>
                    <n-input class="upstream-url" :value="upstream.url" readonly size="small" />
                  </n-form-item>
                </n-form>
              </div>

              <!-- 上游模型列表(标准分组) / 聚合后模型(聚合分组) -->
              <div class="detail-section">
                <n-collapse v-model:expanded-names="modelsExpanded">
                  <n-collapse-item name="models">
                    <template #header>
                      <div class="upstream-models-header">
                        <span class="section-title" style="margin: 0; padding: 0; border: none">
                          {{ isAggregateGroup ? t("keys.aggregatedModelsList") : t("keys.upstreamModelsList") }}
                        </span>
                        <n-tag size="tiny" type="info" :bordered="false">
                          {{ isAggregateGroup ? aggregateModels.length : groupAvailableModels.length }}
                        </n-tag>
                        <span v-if="!isAggregateGroup && modelsRefreshedAtDisplay" class="hint">
                          {{ t("keys.lastRefreshed", { at: modelsRefreshedAtDisplay }) }}
                        </span>
                      </div>
                    </template>

                    <!-- 标准分组: 列出本分组上游模型 -->
                    <template v-if="!isAggregateGroup">
                      <div class="models-toolbar">
                        <n-input
                          v-model:value="modelsSearch"
                          :placeholder="t('keys.searchModelPlaceholder')"
                          clearable
                          style="width: 240px"
                        />
                        <n-button size="small" :loading="modelsRefreshLoading" @click="refreshGroupModels">
                          <template #icon>
                            <n-icon :component="RefreshOutline" />
                          </template>
                          {{ t("common.refresh") }}
                        </n-button>
                      </div>
                      <div v-if="filteredGroupModels.length === 0">
                        <n-empty
                          size="small"
                          :description="groupAvailableModels.length === 0 ? t('keys.upstreamModelsEmpty') : t('common.noData')"
                        />
                      </div>
                      <div v-else class="models-grid">
                        <div v-for="m in filteredGroupModels" :key="m" class="model-item">
                          <div class="model-item-main">
                            <span class="model-item-id">{{ m }}</span>
                            <n-tag v-if="findFreeModel(m)" size="tiny" type="success" :bordered="false">
                              🆓 {{ t("modelcatalog.freeTag") }}
                            </n-tag>
                            <n-tag
                              v-if="findFreeModel(m)?.tier"
                              size="tiny"
                              :type="tierTagType(findFreeModel(m)?.tier)"
                              :bordered="false"
                            >
                              {{ tierLabel(findFreeModel(m)?.tier) }}
                            </n-tag>
                          </div>
                          <n-button size="tiny" text @click="copyModelId(m)">
                            {{ t("common.copy") }}
                          </n-button>
                        </div>
                      </div>
                    </template>

                    <!-- 聚合分组: 列出合并去重后的模型 + 各子分组来源 -->
                    <template v-else>
                      <div class="models-toolbar">
                        <n-input
                          v-model:value="modelsSearch"
                          :placeholder="t('keys.searchModelPlaceholder')"
                          clearable
                          style="width: 240px"
                        />
                        <span class="hint">
                          {{ t("keys.aggregatedModelsHint", { sub: subGroups?.length || 0, total: aggregateModels.length }) }}
                        </span>
                      </div>
                      <div v-if="filteredAggregateModels.length === 0">
                        <n-empty size="small" :description="t('keys.aggregatedModelsEmpty')" />
                      </div>
                      <div v-else class="models-grid">
                        <div v-for="r in filteredAggregateModels" :key="r.modelId" class="model-item model-item-tall">
                          <div class="model-item-main">
                            <span class="model-item-id">{{ r.modelId }}</span>
                            <n-tag v-if="findFreeModel(r.modelId)" size="tiny" type="success" :bordered="false">
                              🆓 {{ t("modelcatalog.freeTag") }}
                            </n-tag>
                            <n-tag
                              v-if="findFreeModel(r.modelId)?.tier"
                              size="tiny"
                              :type="tierTagType(findFreeModel(r.modelId)?.tier)"
                              :bordered="false"
                            >
                              {{ tierLabel(findFreeModel(r.modelId)?.tier) }}
                            </n-tag>
                            <span class="model-providers">
                              {{ r.providers.join(", ") }}
                            </span>
                          </div>
                          <n-button size="tiny" text @click="copyModelId(r.modelId)">
                            {{ t("common.copy") }}
                          </n-button>
                        </div>
                      </div>
                    </template>
                  </n-collapse-item>
                </n-collapse>
              </div>

              <!-- 标准分组才显示高级配置 -->
              <div class="detail-section" v-if="!isAggregateGroup && hasAdvancedConfig">
                <h4 class="section-title">{{ t("keys.advancedConfig") }}</h4>
                <n-form label-placement="left">
                  <n-form-item v-for="(value, key) in group?.config || {}" :key="key">
                    <template #label>
                      <n-tooltip trigger="hover" :delay="300" placement="top">
                        <template #trigger>
                          <span class="config-label">
                            {{ getConfigDisplayName(key) }}:
                            <n-icon size="14" class="config-help-icon">
                              <svg viewBox="0 0 24 24">
                                <path
                                  fill="currentColor"
                                  d="M12,2A10,10 0 0,0 2,12A10,10 0 0,0 12,22A10,10 0 0,0 22,12A10,10 0 0,0 12,2M12,17A1.5,1.5 0 0,1 10.5,15.5A1.5,1.5 0 0,1 12,14A1.5,1.5 0 0,1 13.5,15.5A1.5,1.5 0 0,1 12,17M12,10.5C10.07,10.5 8.5,8.93 8.5,7A3.5,3.5 0 0,1 12,3.5A3.5,3.5 0 0,1 15.5,7C15.5,8.93 13.93,10.5 12,10.5Z"
                                />
                              </svg>
                            </n-icon>
                          </span>
                        </template>
                        <div class="config-tooltip">
                          <div class="tooltip-title">{{ getConfigDisplayName(key) }}</div>
                          <div class="tooltip-description">{{ getConfigDescription(key) }}</div>
                          <div class="tooltip-key">{{ t("keys.configKey") }}: {{ key }}</div>
                        </div>
                      </n-tooltip>
                    </template>
                    {{ value || "-" }}
                  </n-form-item>
                  <n-form-item
                    v-if="group?.header_rules && group.header_rules.length > 0"
                    :label="`${t('keys.customHeaders')}：`"
                    :span="2"
                  >
                    <div class="header-rules-display">
                      <div
                        v-for="(rule, index) in group.header_rules"
                        :key="index"
                        class="header-rule-item"
                      >
                        <n-tag :type="rule.action === 'remove' ? 'error' : 'default'" size="small">
                          {{ rule.key }}
                        </n-tag>
                        <span class="header-separator">:</span>
                        <span class="header-value" v-if="rule.action === 'set'">
                          {{ rule.value || t("keys.emptyValue") }}
                        </span>
                        <span class="header-removed" v-else>{{ t("common.delete") }}</span>
                      </div>
                    </div>
                  </n-form-item>
                  <n-form-item
                    v-if="group?.model_redirect_rules"
                    :label="`${t('keys.modelRedirectPolicy')}：`"
                    :span="2"
                  >
                    <n-tag
                      :type="group?.model_redirect_strict ? 'warning' : 'success'"
                      size="small"
                    >
                      {{
                        group?.model_redirect_strict
                          ? t("keys.modelRedirectStrictMode")
                          : t("keys.modelRedirectLooseMode")
                      }}
                    </n-tag>
                  </n-form-item>
                  <n-form-item
                    v-if="group?.model_redirect_rules"
                    :label="`${t('keys.modelRedirectRules')}：`"
                    :span="2"
                  >
                    <pre class="config-json">{{
                      JSON.stringify(group?.model_redirect_rules || {}, null, 2)
                    }}</pre>
                  </n-form-item>
                  <n-form-item
                    v-if="group?.param_overrides"
                    :label="`${t('keys.paramOverrides')}：`"
                    :span="2"
                  >
                    <pre class="config-json">{{
                      JSON.stringify(group?.param_overrides || "", null, 2)
                    }}</pre>
                  </n-form-item>
                </n-form>
              </div>
            </div>
          </n-collapse-item>
        </n-collapse>
      </div>
    </n-card>

    <group-form-modal v-model:show="showEditModal" :group="group" @success="handleGroupEdited" />
    <aggregate-group-modal
      v-model:show="showAggregateEditModal"
      :group="group"
      :groups="props.groups"
      @success="handleAggregateGroupEdited"
    />
    <group-copy-modal
      v-model:show="showCopyModal"
      :source-group="group"
      @success="handleGroupCopied"
    />
  </div>
</template>

<style scoped>
.group-info-container {
  width: 100%;
}

:deep(.n-card-header) {
  padding: 12px 24px;
}

.group-info-card {
  background: var(--card-bg-solid);
  border-radius: var(--border-radius-md);
  border: 1px solid var(--border-color);
  animation: fadeInUp 0.2s ease-out;
  box-shadow: var(--shadow-sm);
}

.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
}

.header-left {
  flex: 1;
}

.group-title {
  font-size: 1.2rem;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0 0 8px 0;
  display: flex;
  align-items: center;
  gap: 8px;
}

.group-url {
  font-size: 0.8rem;
  color: var(--primary-color);
  margin-left: 8px;
  font-family: monospace;
  background: var(--bg-secondary);
  border-radius: 4px;
  padding: 2px 6px;
  margin-right: 4px;
  border: 1px solid var(--border-color);
  cursor: pointer;
  transition: all 0.2s ease;
}

.group-id {
  font-size: 0.75rem;
  color: var(--text-secondary);
  opacity: 0.7;
}

.header-actions {
  display: flex;
  gap: 8px;
}

.stats-summary {
  margin-bottom: 12px;
  text-align: center;
}

.status-cards-container:deep(.n-card) {
  max-width: 160px;
}

:deep(.status-card-failure .n-card-header__main) {
  color: var(--error-color, #d03050);
}

.status-title {
  color: var(--text-secondary);
  font-size: 12px;
}

.details-section {
  margin-top: 12px;
}

.details-content {
  margin-top: 12px;
}

.detail-section {
  margin-bottom: 24px;
}

.detail-section:last-child {
  margin-bottom: 0;
}

.section-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0 0 12px 0;
  padding-bottom: 8px;
  border-bottom: 2px solid var(--border-color);
}

.upstream-models-header {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.models-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 8px 0 12px;
  flex-wrap: wrap;
}

.models-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 6px;
  max-height: 360px;
  overflow-y: auto;
  padding: 4px 2px;
}

.model-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  padding: 6px 10px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  font-size: 12px;
}

.model-item-tall {
  align-items: flex-start;
}

.model-item-main {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
  min-width: 0;
  flex: 1;
}

.model-item-id {
  font-family: monospace;
  font-size: 12px;
}

.model-providers {
  flex-basis: 100%;
  color: var(--text-color-3, #999);
  font-size: 11px;
  margin-top: 2px;
}

.hint {
  color: var(--text-color-3, #999);
  font-size: 12px;
}


.upstream-url {
  font-family: monospace;
  font-size: 0.9rem;
  color: var(--text-primary);
  margin-left: 5px;
}

.upstream-weight {
  min-width: 70px;
}

.config-json {
  background: var(--bg-secondary);
  border-radius: var(--border-radius-sm);
  padding: 12px;
  font-size: 0.8rem;
  color: var(--text-primary);
  margin: 8px 0;
  overflow-x: auto;
}

@keyframes fadeInUp {
  from {
    opacity: 0;
    transform: translateY(20px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

:deep(.n-form-item-feedback-wrapper) {
  min-height: 0;
}

/* 描述内容样式 */
.description-content {
  white-space: pre-wrap;
  word-wrap: break-word;
  line-height: 1.5;
  min-height: 20px;
  color: var(--text-primary);
}

.aggregate-weight {
  min-width: 70px;
}

.aggregate-name {
  font-family: monospace;
  font-size: 0.9rem;
  color: var(--text-primary);
  width: 200px;
}

.proxy-keys-content {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  width: 100%;
  gap: 8px;
}

.key-text {
  flex-grow: 1;
  font-family: monospace;
  white-space: pre-wrap;
  word-break: break-all;
  line-height: 1.5;
  padding-top: 4px; /* Align with buttons */
  color: var(--text-primary);
}

.key-actions {
  flex-shrink: 0;
}

/* 配置项tooltip样式 */
.config-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  cursor: help;
}

.config-help-icon {
  color: var(--text-tertiary);
  transition: color 0.2s ease;
}

.config-label:hover .config-help-icon {
  color: var(--primary-color);
}

.config-tooltip {
  max-width: 300px;
  padding: 8px 0;
}

.tooltip-title {
  font-weight: 600;
  color: white;
  margin-bottom: 4px;
  font-size: 0.9rem;
}

.tooltip-description {
  color: rgba(255, 255, 255, 0.9);
  margin-bottom: 6px;
  line-height: 1.4;
  font-size: 0.85rem;
}

.tooltip-key {
  color: rgba(255, 255, 255, 0.8);
  font-size: 0.75rem;
  font-family: monospace;
  background: rgba(255, 255, 255, 0.15);
  padding: 2px 6px;
  border-radius: 4px;
  display: inline-block;
}

/* Header rules display styles */
.header-rules-display {
  display: flex;
  flex-direction: column;
  gap: 6px;
  background: var(--bg-secondary);
  border-radius: var(--border-radius-sm);
  padding: 8px;
}

.header-rule-item {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.875rem;
}

.header-separator {
  color: var(--text-secondary);
  font-weight: 500;
}

.header-value {
  color: var(--text-primary);
  font-family: monospace;
  background: var(--bg-secondary);
  padding: 2px 6px;
  border-radius: 3px;
  font-size: 0.8rem;
}

.header-removed {
  color: var(--error-color, #dc2626);
  font-style: italic;
  font-size: 0.8rem;
}
</style>
