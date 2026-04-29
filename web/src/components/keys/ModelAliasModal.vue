<script setup lang="ts">
import { aliasesApi, type ModelAliasRow } from "@/api/aliases";
import type { Group } from "@/types/models";
import { BanOutline, CloseOutline, HelpCircleOutline } from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NIcon,
  NInput,
  NModal,
  NPopconfirm,
  NTag,
  NTooltip,
  useMessage,
} from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  group: Group;
  modelId: string;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
  (e: "toggle-block", modelId: string): void;
  (e: "remove-exposed", modelId: string): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();

const loading = ref(false);
const allAliases = ref<ModelAliasRow[]>([]);
const allAliasesLoading = ref(false);
const selectedAliases = ref<string[]>([]);

// Defaults applied to every newly created mapping — not user-editable here.
const DEFAULT_WEIGHT = 100;
const DEFAULT_PRIORITY = 0;
const DEFAULT_ENABLED = true;

watch(
  () => props.show,
  show => {
    if (show) {
      reset();
      loadAliases();
    }
  }
);

function reset() {
  selectedAliases.value = [];
}

async function loadAliases() {
  allAliasesLoading.value = true;
  try {
    const res = await aliasesApi.list();
    allAliases.value = (res.data as unknown as ModelAliasRow[]) || [];
  } catch (e) {
    console.error("load aliases failed", e);
    allAliases.value = [];
  } finally {
    allAliasesLoading.value = false;
  }
}

const existingMappedNames = computed(() => {
  return allAliases.value
    .filter(a => a.group_id === props.group?.id && a.real_model === props.modelId)
    .sort((a, b) => a.alias.localeCompare(b.alias));
});

async function handleDeleteExisting(row: ModelAliasRow) {
  try {
    await aliasesApi.remove(row.id);
    await loadAliases();
    emit("success");
    message.success(t("common.operationSuccess"));
  } catch {
    message.error(t("common.requestFailed"));
  }
}

const newAliasInput = ref("");

// 已存在但还没绑定到当前模型的别名 — 用于"快速复用"chip 列表
const quickPickOptions = computed<string[]>(() => {
  const seen = new Set<string>();
  const existingSet = new Set(existingMappedNames.value.map(a => a.alias));
  const selectedSet = new Set(selectedAliases.value);
  const out: string[] = [];
  for (const a of allAliases.value) {
    if (seen.has(a.alias)) {
      continue;
    }
    seen.add(a.alias);
    if (existingSet.has(a.alias) || selectedSet.has(a.alias)) {
      continue;
    }
    out.push(a.alias);
  }
  return out.sort((x, y) => x.localeCompare(y));
});

function addAliasChip(name: string) {
  const v = name.trim();
  if (!v) {
    return;
  }
  if (selectedAliases.value.includes(v)) {
    return;
  }
  if (existingMappedNames.value.some(a => a.alias === v)) {
    message.warning(t("v5.maAlreadyBound", { alias: v }) || `已绑定:${v}`);
    return;
  }
  selectedAliases.value = [...selectedAliases.value, v];
  newAliasInput.value = "";
}

function removeAliasChip(name: string) {
  selectedAliases.value = selectedAliases.value.filter(a => a !== name);
}

function onInputEnter() {
  addAliasChip(newAliasInput.value);
}

function handleClose() {
  if (loading.value) {
    return;
  }
  emit("update:show", false);
}

// 模型相对当前 group 的状态:已暴露(specified mode) / 已拉黑(black list)
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
const isSpecifiedMode = computed(
  () => (props.group as unknown as { model_routing_mode?: string }).model_routing_mode === "specified"
);
const exposedList = computed(() =>
  parseModelArray((props.group as unknown as { exposed_models?: unknown }).exposed_models)
);
const blockedList = computed(() =>
  parseModelArray((props.group as unknown as { blocked_models?: unknown }).blocked_models)
);
const isExposed = computed(() => exposedList.value.includes(props.modelId));
const isBlocked = computed(() => blockedList.value.includes(props.modelId));

function onToggleBlock() {
  emit("toggle-block", props.modelId);
}
function onRemoveExposed() {
  emit("remove-exposed", props.modelId);
}

async function handleSave() {
  if (loading.value) {
    return;
  }
  if (!selectedAliases.value.length) {
    message.warning(t("v5.maPickFirst"));
    return;
  }
  if (!props.group?.id) {
    message.error("Group missing");
    return;
  }
  loading.value = true;
  let ok = 0;
  let fail = 0;
  for (const alias of selectedAliases.value) {
    try {
      await aliasesApi.create({
        alias,
        group_id: props.group.id,
        real_model: props.modelId,
        weight: DEFAULT_WEIGHT,
        priority: DEFAULT_PRIORITY,
        enabled: DEFAULT_ENABLED,
      });
      ok += 1;
    } catch (e) {
      console.error(`create alias ${alias} failed`, e);
      fail += 1;
    }
  }
  loading.value = false;
  if (ok > 0) {
    message.success(t("v5.maCreated", { ok, fail }));
    emit("success");
    emit("update:show", false);
  } else if (fail > 0) {
    message.error(t("v5.maAllFailed"));
  }
}
</script>

<template>
  <n-modal :show="show" class="v3-modal" @update:show="handleClose">
    <n-card
      class="v3-modal-card v5-ma-card"
      :title="t('v5.maTitle')"
      :bordered="false"
      size="huge"
      role="dialog"
      aria-modal="true"
      style="max-width: 480px; max-height: 80vh"
    >
      <template #header-extra>
        <n-button quaternary circle size="small" @click="handleClose">
          <template #icon><n-icon :component="CloseOutline" /></template>
        </n-button>
      </template>

      <div class="v5-ma-target">
        <div class="v5-ma-target__l">{{ t("v5.maTarget") }}</div>
        <div class="v5-ma-target__v">
          <code>{{ modelId }}</code>
          <span class="v5-ma-target__sep">·</span>
          <span class="v5-ma-target__group">{{ group?.display_name || group?.name }}</span>
        </div>
      </div>

      <!-- 模型行为:加入黑名单 / 从暴露移除 -->
      <div class="v5-ma-actions">
        <button
          class="v5-ma-action"
          :class="{ 'v5-ma-action--active': isBlocked }"
          @click="onToggleBlock"
        >
          <n-icon :component="BanOutline" :size="13" />
          <span>{{
            isBlocked
              ? (t("v3.unblock") || "解除拉黑")
              : (t("v3.block") || "加入黑名单")
          }}</span>
        </button>
        <n-popconfirm
          v-if="isSpecifiedMode && isExposed"
          :positive-text="t('common.confirm') || 'OK'"
          :negative-text="t('common.cancel') || 'Cancel'"
          @positive-click="onRemoveExposed"
        >
          <template #trigger>
            <button class="v5-ma-action v5-ma-action--danger">
              <n-icon :component="CloseOutline" :size="13" />
              <span>{{ t("v3.removeFromExposed") || "从暴露移除" }}</span>
            </button>
          </template>
          {{ t("v5.maRemoveExposedConfirm", { model: modelId }) || `从已暴露列表中移除 ${modelId}?` }}
        </n-popconfirm>
      </div>

      <!-- Existing Aliases Section -->
      <div v-if="existingMappedNames.length" class="v5-ma-field">
        <div class="v5-ma-field__lbl">
          {{ t("v5.maExistingTitle") || "已绑定别名" }} ({{ existingMappedNames.length }})
        </div>
        <div class="v5-ma-chips">
          <div v-for="a in existingMappedNames" :key="a.id" class="v5-ma-chip">
            <span class="v5-ma-chip__label">{{ a.alias }}</span>
            <n-tag v-if="a.weight !== 100" size="tiny" :bordered="false" type="info">
              w {{ a.weight }}
            </n-tag>
            <n-tag
              v-if="!a.enabled"
              size="tiny"
              :bordered="false"
              type="warning"
            >
              {{ t("common.disable") || "disabled" }}
            </n-tag>
            <n-popconfirm
              :positive-text="t('common.confirm') || 'OK'"
              :negative-text="t('common.cancel') || 'Cancel'"
              @positive-click="handleDeleteExisting(a)"
            >
              <template #trigger>
                <button class="v5-ma-chip__delete" :title="t('common.delete')">
                  <n-icon :component="CloseOutline" :size="12" />
                </button>
              </template>
              {{ t("v5.maDeleteConfirm", { alias: a.alias }) || `确认删除别名 ${a.alias}?` }}
            </n-popconfirm>
          </div>
        </div>
      </div>

      <div v-if="existingMappedNames.length" class="v5-ma-divider"></div>

      <div class="v5-ma-field">
        <div class="v5-ma-field__lbl">
          {{ t("v5.maAddTitle") || "添加别名" }}
          <n-tooltip>
            <template #trigger>
              <span class="v5-helpicon"><n-icon :component="HelpCircleOutline" :size="11" /></span>
            </template>
            {{ t("v5.maAliasesTip") }}
          </n-tooltip>
        </div>

        <!-- 输入新别名 -->
        <div class="v5-ma-inputrow">
          <n-input
            v-model:value="newAliasInput"
            :placeholder="t('v5.maInputPlaceholder') || '输入别名,回车或点 + 添加'"
            @keydown.enter.prevent="onInputEnter"
          />
          <n-button
            type="primary"
            ghost
            :disabled="!newAliasInput.trim()"
            @click="onInputEnter"
          >
            +
          </n-button>
        </div>

        <!-- 即将创建的 (selectedAliases) chip 行 -->
        <div v-if="selectedAliases.length" class="v5-ma-pending">
          <span class="v5-ma-pending__lbl">
            {{ t("v5.maPendingLbl") || "即将创建" }} ({{ selectedAliases.length }})
          </span>
          <div class="v5-ma-chips">
            <div
              v-for="a in selectedAliases"
              :key="a"
              class="v5-ma-chip v5-ma-chip--pending"
            >
              <span class="v5-ma-chip__label">{{ a }}</span>
              <button
                class="v5-ma-chip__delete"
                :title="t('common.delete')"
                @click="removeAliasChip(a)"
              >
                <n-icon :component="CloseOutline" :size="12" />
              </button>
            </div>
          </div>
        </div>

        <!-- 快速复用已有别名 -->
        <div v-if="quickPickOptions.length" class="v5-ma-quickpick">
          <span class="v5-ma-quickpick__lbl">
            {{ t("v5.maQuickPickLbl") || "复用已有" }}
          </span>
          <div class="v5-ma-chips">
            <button
              v-for="opt in quickPickOptions"
              :key="opt"
              class="v5-ma-chip v5-ma-chip--quickpick"
              @click="addAliasChip(opt)"
            >
              <span class="v5-ma-chip__plus">+</span>
              <span class="v5-ma-chip__label">{{ opt }}</span>
            </button>
          </div>
        </div>

        <div class="v5-ma-field__hint">
          {{ t("v5.maAddHint") || "选已有别名 = 把当前模型加入候选池;输入新名回车 = 创建新别名" }}
        </div>
      </div>

      <template #footer>
        <div style="display: flex; justify-content: flex-end; gap: 8px">
          <n-button @click="handleClose">{{ t("common.cancel") }}</n-button>
          <n-button
            type="primary"
            :loading="loading"
            :disabled="!selectedAliases.length"
            @click="handleSave"
          >
            {{ t("v5.maCreate", { n: selectedAliases.length }) }}
          </n-button>
        </div>
      </template>
    </n-card>
  </n-modal>
</template>

<style scoped>
.v5-ma-card :deep(.n-card__content) {
  display: flex;
  flex-direction: column;
  gap: 14px;
  max-height: calc(80vh - 140px);
  overflow-y: auto;
}
.v5-ma-field__hint {
  font: 400 11px/1.5 var(--v3-sans);
  color: var(--v3-ink-3);
  margin-top: 6px;
}

.v5-ma-target {
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius);
  padding: 10px 12px;
}

.v5-ma-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.v5-ma-action {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  background: transparent;
  border: 1px solid var(--v3-line);
  border-radius: 4px;
  font: 500 11px var(--v3-sans);
  color: var(--v3-ink-2);
  cursor: pointer;
  transition: all 120ms;
}
.v5-ma-action:hover {
  border-color: var(--v3-warn, oklch(0.7 0.16 80));
  color: var(--v3-warn, oklch(0.55 0.18 70));
  background: var(--v3-warn-soft, oklch(0.95 0.05 80));
}
.v5-ma-action--active {
  border-color: var(--v3-warn, oklch(0.7 0.16 80));
  color: var(--v3-warn, oklch(0.55 0.18 70));
  background: var(--v3-warn-soft, oklch(0.95 0.05 80));
}
.v5-ma-action--danger:hover {
  border-color: var(--v3-danger);
  color: var(--v3-danger);
  background: var(--v3-danger-soft);
}
.v5-ma-target__l {
  font: 500 11px/1 var(--v3-sans);
  color: var(--v3-ink-3);
  letter-spacing: 0.04em;
  text-transform: uppercase;
  flex-shrink: 0;
}
.v5-ma-target__v {
  display: flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
}
.v5-ma-target__v code {
  font: 500 12.5px var(--v3-mono);
  color: var(--v3-ink);
}
.v5-ma-target__sep {
  color: var(--v3-ink-4);
}
.v5-ma-target__group {
  font: 400 12px var(--v3-sans);
  color: var(--v3-ink-3);
}
.v5-ma-field__lbl {
  font: 500 12px/1.3 var(--v3-sans);
  color: var(--v3-ink-3);
  margin-bottom: 8px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  text-transform: uppercase;
  letter-spacing: 0.02em;
}

.v5-ma-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.v5-ma-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  padding: 4px 10px;
  border-radius: 6px;
  transition: all 120ms;
}
.v5-ma-chip:hover {
  border-color: var(--v3-danger-soft);
  background: var(--v3-danger-soft);
}
.v5-ma-chip__label {
  font: 600 12px var(--v3-mono);
  color: var(--v3-ink);
}
.v5-ma-chip__delete {
  background: transparent;
  border: 0;
  cursor: pointer;
  padding: 2px;
  border-radius: 4px;
  color: var(--v3-ink-4);
  display: flex;
  align-items: center;
  transition: color 120ms;
}
.v5-ma-chip__delete:hover {
  color: var(--v3-danger);
}

.v5-ma-divider {
  height: 1px;
  background: var(--v3-line);
  margin: 4px 0;
  opacity: 0.5;
}

/* 添加别名 — chip 风格 */
.v5-ma-inputrow {
  display: flex;
  gap: 6px;
  align-items: stretch;
}
.v5-ma-inputrow :deep(.n-input) {
  flex: 1;
}
.v5-ma-pending {
  margin-top: 10px;
  padding: 8px 10px;
  background: var(--v3-accent-soft);
  border: 1px dashed var(--v3-accent);
  border-radius: var(--v3-radius);
}
.v5-ma-pending__lbl {
  display: block;
  font: 600 10.5px/1 var(--v3-mono);
  color: var(--v3-accent);
  letter-spacing: 0.04em;
  text-transform: uppercase;
  margin-bottom: 6px;
}
.v5-ma-chip--pending {
  background: var(--v3-bg);
  border-color: var(--v3-accent);
}
.v5-ma-quickpick {
  margin-top: 10px;
}
.v5-ma-quickpick__lbl {
  display: block;
  font: 500 10.5px/1 var(--v3-mono);
  color: var(--v3-ink-3);
  letter-spacing: 0.04em;
  text-transform: uppercase;
  margin-bottom: 6px;
}
.v5-ma-chip--quickpick {
  cursor: pointer;
  background: transparent;
  border-style: dashed;
  color: var(--v3-ink-2);
}
.v5-ma-chip--quickpick:hover {
  border-color: var(--v3-accent);
  background: var(--v3-accent-soft);
  color: var(--v3-accent);
}
.v5-ma-chip__plus {
  font: 700 12px var(--v3-mono);
  color: var(--v3-ink-4);
  margin-right: 2px;
}
.v5-ma-chip--quickpick:hover .v5-ma-chip__plus {
  color: var(--v3-accent);
}
</style>
