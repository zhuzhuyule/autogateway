<script setup lang="ts">
import { aliasesApi, type ModelAliasRow } from "@/api/aliases";
import { expandProviderAliases, lookupRegistry } from "@/api/freemodels";
import type { Group } from "@/types/models";
import { BanOutline, CloseOutline, HelpCircleOutline, LinkOutline } from "@vicons/ionicons5";
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
  /** 用户的全部 groups, 用于把 registry 的跨 provider aliases 反查到实际的 groupId. */
  allGroups?: Group[];
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
  (e: "toggle-block", modelId: string): void;
  (e: "remove-exposed", modelId: string): void;
}

const props = withDefaults(defineProps<Props>(), {
  allGroups: () => [],
});
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
  selectedCrossProvider.value = [];
}

// === FreeModels Registry 跨 provider 同名候选 ===========================
// registry 的每条 model meta 都带 aliases: ["groq/llama-3.3-70b-versatile", ...]
// 我们解析这些 string, 找到用户实际拥有的 group, 让用户一键把它们加入同一别名
// 候选池, 实现"一个别名跨 provider 路由"。
interface CrossProviderCandidate {
  raw: string; // 原始 alias 串, e.g. "groq/llama-3.3-70b-versatile"
  providerId: string; // "groq"
  realModel: string; // "llama-3.3-70b-versatile"
  groupId: number | null; // 用户实际的 group, null = 未配置该 provider
  groupName: string;
}

function parseAliasRef(raw: string): { providerId: string; realModel: string } | null {
  const idx = raw.indexOf("/");
  if (idx <= 0 || idx === raw.length - 1) {
    return null;
  }
  return { providerId: raw.slice(0, idx), realModel: raw.slice(idx + 1) };
}

function findGroupForProvider(providerId: string): Group | null {
  if (!providerId || !props.allGroups.length) {
    return null;
  }
  // 用 provider 别名映射展开 (e.g. "bigmodel" → ["zhipu", "bigmodel"]),
  // 因为 FreeModels CDN 用 9 家精简命名, 我们 freeProviders 用 30+ 家细分命名.
  const candidates = expandProviderAliases(providerId);
  for (const c of candidates) {
    // 1. group.name 子串命中
    const byName = props.allGroups.find(
      g => g.group_type !== "aggregate" && g.name.toLowerCase().includes(c)
    );
    if (byName) {
      return byName;
    }
    // 2. upstream host 命中
    const byHost = props.allGroups.find(g => {
      if (g.group_type === "aggregate") {
        return false;
      }
      const upstreams = (g as unknown as { upstreams?: Array<{ url?: string }> }).upstreams || [];
      return upstreams.some(u => (u.url || "").toLowerCase().includes(c));
    });
    if (byHost) {
      return byHost;
    }
  }
  return null;
}

const crossProviderCandidates = computed<CrossProviderCandidate[]>(() => {
  const meta = lookupRegistry(undefined, props.modelId);
  if (!meta?.aliases?.length) {
    return [];
  }
  const out: CrossProviderCandidate[] = [];
  const seen = new Set<string>();
  for (const raw of meta.aliases) {
    const parsed = parseAliasRef(raw);
    if (!parsed) {
      continue;
    }
    // 排除当前 (group, model) 自身
    const matchedGroup = findGroupForProvider(parsed.providerId);
    if (matchedGroup?.id === props.group?.id && parsed.realModel === props.modelId) {
      continue;
    }
    const dedupeKey = `${parsed.providerId}/${parsed.realModel}`;
    if (seen.has(dedupeKey)) {
      continue;
    }
    seen.add(dedupeKey);
    out.push({
      raw,
      providerId: parsed.providerId,
      realModel: parsed.realModel,
      groupId: matchedGroup?.id ?? null,
      groupName: matchedGroup?.display_name || matchedGroup?.name || parsed.providerId,
    });
  }
  return out;
});

// 用户已勾选的跨 provider 候选 (raw 字符串作为 key)
const selectedCrossProvider = ref<string[]>([]);

function toggleCrossProvider(raw: string) {
  const idx = selectedCrossProvider.value.indexOf(raw);
  if (idx >= 0) {
    selectedCrossProvider.value.splice(idx, 1);
  } else {
    selectedCrossProvider.value.push(raw);
  }
}

function isCrossProviderSelected(raw: string): boolean {
  return selectedCrossProvider.value.includes(raw);
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
  // 主候选 + 跨 provider 候选 — 都作为同一组 alias 的候选池
  const targets: Array<{ groupId: number; realModel: string }> = [
    { groupId: props.group.id, realModel: props.modelId },
  ];
  for (const raw of selectedCrossProvider.value) {
    const c = crossProviderCandidates.value.find(x => x.raw === raw);
    if (c?.groupId) {
      targets.push({ groupId: c.groupId, realModel: c.realModel });
    }
  }
  for (const alias of selectedAliases.value) {
    for (const tgt of targets) {
      try {
        await aliasesApi.create({
          alias,
          group_id: tgt.groupId,
          real_model: tgt.realModel,
          weight: DEFAULT_WEIGHT,
          priority: DEFAULT_PRIORITY,
          enabled: DEFAULT_ENABLED,
        });
        ok += 1;
      } catch (e) {
        console.error(`create alias ${alias} for ${tgt.groupId}/${tgt.realModel} failed`, e);
        fail += 1;
      }
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

      <!-- 跨 provider 同名候选 (FreeModels Registry 的 aliases 字段) -->
      <div v-if="crossProviderCandidates.length" class="v5-ma-divider" />
      <div v-if="crossProviderCandidates.length" class="v5-ma-field">
        <div class="v5-ma-field__lbl">
          <n-icon :component="LinkOutline" :size="11" />
          {{ t("v5.maCrossProviderTitle") || "跨 provider 同名候选" }}
          <n-tooltip>
            <template #trigger>
              <span class="v5-helpicon"><n-icon :component="HelpCircleOutline" :size="11" /></span>
            </template>
            {{
              t("v5.maCrossProviderTip") ||
                "FreeModels 注册表显示这些 provider 也提供同名模型。勾选后,本次创建的别名会同时把它们加入候选池,实现跨 provider failover。"
            }}
          </n-tooltip>
        </div>
        <div class="v5-ma-chips">
          <button
            v-for="c in crossProviderCandidates"
            :key="c.raw"
            class="v5-ma-chip v5-ma-chip--cross"
            :class="{
              'v5-ma-chip--cross-selected': isCrossProviderSelected(c.raw),
              'v5-ma-chip--cross-disabled': !c.groupId,
            }"
            :disabled="!c.groupId"
            :title="
              c.groupId
                ? `${c.groupName} · ${c.realModel}`
                : t('v5.maCrossProviderUnconfigured', { provider: c.providerId }) ||
                  `用户未配置 ${c.providerId} group`
            "
            @click="c.groupId && toggleCrossProvider(c.raw)"
          >
            <span v-if="c.groupId" class="v5-ma-chip__plus">
              {{ isCrossProviderSelected(c.raw) ? "✓" : "+" }}
            </span>
            <span class="v5-ma-chip__cross-prov">{{ c.providerId }}</span>
            <span class="v5-ma-chip__cross-sep">·</span>
            <span class="v5-ma-chip__label">{{ c.realModel }}</span>
          </button>
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

.v5-ma-chip--cross {
  cursor: pointer;
  background: transparent;
  border-style: dashed;
  color: var(--v3-ink-2);
}
.v5-ma-chip--cross:hover:not(.v5-ma-chip--cross-disabled) {
  border-color: var(--v3-info);
  background: var(--v3-info-soft);
}
.v5-ma-chip--cross-selected {
  border-color: var(--v3-info);
  background: var(--v3-info-soft);
  color: var(--v3-info);
}
.v5-ma-chip--cross-disabled {
  cursor: not-allowed;
  opacity: 0.45;
}
.v5-ma-chip__cross-prov {
  font: 700 10px var(--v3-mono);
  color: var(--v3-ink-3);
  text-transform: uppercase;
}
.v5-ma-chip__cross-sep {
  color: var(--v3-ink-4);
}
</style>
