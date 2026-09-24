<script setup lang="ts">
// 批量选模型加入某个别名。整块从 AliasManageTab 原样搬出, 职责不变:
// 选 (分组, 模型) 进暂存区, 确认时一次性建候选 + 补 exposed。
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { NButton, NIcon, NInput, NModal } from "naive-ui";
import {
  AddOutline,
  BanOutline,
  CheckmarkCircle,
  CloseOutline,
  LockClosedOutline,
  PulseOutline,
} from "@vicons/ionicons5";
import { findProviderByUpstreams, isFree } from "@/data/freeProviders";
import { getGroupDisplayName } from "@/utils/display";
import { createAliasCandidates, useAliasData } from "@/services/aliases";
import type { PendingPick } from "@/components/aliases/types";

const props = defineProps<{
  show: boolean;
  /** 目标别名 —— 候选建到这个名字下。 */
  alias: string;
  /** 打开时预填的暂存区(家族建议一键采纳走这条路)。 */
  seed?: PendingPick[] | null;
}>();

const emit = defineEmits<{
  (e: "update:show", v: boolean): void;
  (e: "created", ok: number, fail: number): void;
}>();

const { t } = useI18n();
const { groups, rows, groupNameById, modelsByGroup, groupInfoById } = useAliasData();

const search = ref("");
const activeGroupId = ref<number | null>(null);
const pending = ref<PendingPick[]>([]);
const submitting = ref(false);

watch(
  () => props.show,
  open => {
    if (!open) {
      return;
    }
    // Seed 必须在翻转 show 之前进入暂存区: 每次打开都会重置 pending,
    // 打开之后再 seed 会被清掉。
    pending.value = props.seed ? [...props.seed] : [];
    search.value = "";
    if (!activeGroupId.value && groups.value.length) {
      activeGroupId.value = groups.value.find(g => g.group_type !== "aggregate")?.id || null;
    }
  },
  { immediate: true }
);

const keySet = computed(() => new Set(pending.value.map(p => `${p.groupId}:${p.modelId}`)));

function isPending(modelId: string): boolean {
  if (!activeGroupId.value) {
    return false;
  }
  return keySet.value.has(`${activeGroupId.value}:${modelId}`);
}

interface PickerModelView {
  id: string;
  isFree: boolean;
  alreadyBound: boolean;
  blocked: boolean;
}

const filteredModels = computed<PickerModelView[]>(() => {
  if (!activeGroupId.value) {
    return [];
  }
  const g = groups.value.find(gr => gr.id === activeGroupId.value);
  if (!g) {
    return [];
  }

  // 1. 可选模型列表统一走 modelsByGroup —— 口径与"模型"标签页 / 详情抽屉一致
  const source: string[] = modelsByGroup.value[g.id as number] || [];

  // 2. provider 上下文 — 用于免费检测
  const providerId = findProviderByUpstreams(g.upstreams || [])?.id;

  // 3. 已为当前 alias 在该 group 绑定的 real_model 集合
  const boundSet = new Set(
    rows.value.filter(r => r.alias === props.alias && r.group_id === g.id).map(r => r.real_model)
  );
  // 黑名单 - 这些选了也不会被路由
  const blockedSet = groupInfoById.value[g.id as number]?.blocked || new Set<string>();

  // 4. 搜索过滤
  const q = search.value.toLowerCase().trim();
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

function togglePendingPick(modelId: string) {
  if (!activeGroupId.value) {
    return;
  }
  const groupId = activeGroupId.value;
  const groupName =
    groupNameById.value[groupId] || groups.value.find(g => g.id === groupId)?.name || "";
  const key = `${groupId}:${modelId}`;
  const idx = pending.value.findIndex(p => `${p.groupId}:${p.modelId}` === key);
  if (idx >= 0) {
    pending.value.splice(idx, 1);
  } else {
    pending.value.push({ groupId, groupName, modelId });
  }
}

function removePending(p: PendingPick) {
  const key = `${p.groupId}:${p.modelId}`;
  pending.value = pending.value.filter(x => `${x.groupId}:${x.modelId}` !== key);
}

async function commit(): Promise<void> {
  if (!props.alias || !pending.value.length) {
    return;
  }
  submitting.value = true;
  const { ok, fail } = await createAliasCandidates(props.alias, pending.value);
  submitting.value = false;
  emit("created", ok, fail);
  if (ok > 0 || (!ok && !fail)) {
    // ok=0 且 fail=0: 选中的候选全都已存在 —— 无事可做, 静默关闭即可,
    // 报"全部失败"是错的。
    emit("update:show", false);
  }
}
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    style="width: 840px"
    :title="t('v3.aliasAddMember')"
    @update:show="v => emit('update:show', v)"
  >
    <!-- 已选暂存区 -->
    <div class="v3-picker-pending">
      <div class="v3-picker-pending__head">
        <span class="v3-picker-pending__lbl">
          {{ t("v3.aliasPendingTitle") }} ({{ pending.length }})
        </span>
        <span v-if="!pending.length" class="v3-picker-pending__hint">
          {{ t("v3.aliasPendingHint") }}
        </span>
      </div>
      <div v-if="pending.length" class="v3-picker-pending__list">
        <div
          v-for="p in pending"
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
            :class="{ 'v3-picker-group-row--active': activeGroupId === g.id }"
            @click="activeGroupId = g.id as number"
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
            v-model:value="search"
            :placeholder="t('v3.filterModels')"
            clearable
            size="small"
          >
            <template #prefix><n-icon :component="PulseOutline" /></template>
          </n-input>
        </div>
        <div class="scroll" style="flex: 1; overflow-y: auto; padding: 12px; min-height: 0">
          <div
            v-if="!filteredModels.length"
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
              v-for="m in filteredModels"
              :key="m.id"
              class="v3-picker-model-btn"
              :class="{
                'v3-picker-model-btn--picked': isPending(m.id),
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
                v-else-if="isPending(m.id)"
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
        <n-button @click="emit('update:show', false)">{{ t("common.cancel") }}</n-button>
        <n-button type="primary" :loading="submitting" :disabled="!pending.length" @click="commit">
          {{ t("v3.aliasPendingConfirm", { n: pending.length }) }}
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
/* Custom Picker Styles —— 从 AliasManageTab 原样搬过来 */
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
.v3-picker-model-btn:hover .v3-picker-model-btn__add {
  color: var(--v3-info);
  opacity: 1;
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
</style>
