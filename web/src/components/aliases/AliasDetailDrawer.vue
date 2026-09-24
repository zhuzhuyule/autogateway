<script setup lang="ts">
// 别名详情抽屉 —— 一个别名的全貌 + 编辑入口。
//
// 三个设计决定:
//   1. **编辑单位是整个别名, 不是一行。** 候选池的构成、顺序、占比都是相互关联的,
//      一次只改一行看不出全貌。抽屉里一次改完, 点保存走一个整体替换接口
//      (PUT /api/aliases/candidates), 事务内完成, 不会留下半改状态。
//   2. **权重按百分比, 不再让用户看裸数字。** 打开时把库里的整数 weight 归一化成
//      百分比; 保存时直接把百分比当 weight 下发 —— SWRR 只关心比例, 60/40 和
//      6/4 等价, 所以这么换算不丢语义, 但用户能直接读懂。
//   3. **priority 从界面消失, 改为拖拽顺序。** priority 在后端只是 SWRR 累加值
//      打平时的 tie-break(见 router_engine.swrr), 单独摆一个输入框会让人以为它
//      能排序 —— 它不能。现在拖拽顺序落库为 priority(从 1 开始), 语义单一。
//
// 另外两件事在这里就地做, 不再把用户踢到别的页面:
//   - 候选因分组未公开而失效 → 「公开」按钮直接补 exposed_models。
//   - 页脚给出该别名窗口内的实测(请求数 / 错误率 / 平均耗时), 和配置占比并排,
//     并注明两者为什么会偏离。
import { computed, h, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  NButton,
  NDrawer,
  NDrawerContent,
  NInput,
  NInputNumber,
  NSelect,
  NSwitch,
  NTooltip,
  useDialog,
  useMessage,
  useNotification,
} from "naive-ui";
import { AddOutline, TrashOutline } from "@vicons/ionicons5";
import { aliasesApi, type ModelAliasRow } from "@/api/aliases";
import { copy } from "@/utils/clipboard";
import { getGroupDisplayName } from "@/utils/display";
import { parseStringList } from "@/services/aliases";
import StatePill from "@/components/aliases/StatePill.vue";
import type { AliasView, CandidateState } from "@/components/aliases/types";
import type { Group } from "@/types/models";

const props = defineProps<{
  show: boolean;
  alias: string;
  /** 该别名当前的候选行(不含 group_id=0 的占位行)。 */
  rows: ModelAliasRow[];
  /** 可选目标分组。 */
  groups: Group[];
  /** 分组 id -> 展示名。 */
  groupNameById: Record<number, string>;
  /** 分组 id -> 该分组下可选的真实模型(与"模型"页口径一致)。 */
  modelsByGroup: Record<number, string[]>;
  /** `${group_id}::${请求名}` -> 24h 实际调用数。别名路由下请求名 = 别名本身。 */
  traffic: Record<string, number>;
  /** traffic 是否可用; false 时隐藏实测相关列 —— 一排 0 会被读成"没人用"。 */
  showMeasured: boolean;
  /** 本别名在窗口内的整体指标, 用于页脚摘要。 */
  summary?: AliasView | null;
  /** auto 的三个档位按名字寻址, 不允许改名。 */
  isReserved?: boolean;
}>();

const emit = defineEmits<{
  (e: "update:show", v: boolean): void;
  (e: "saved"): void;
  (e: "exposed"): void;
}>();

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();
const notification = useNotification();

interface DraftCandidate {
  key: string;
  groupId: number;
  realModel: string;
  /** 用户视角的百分比(整数)。保存时直接当 weight 下发。 */
  weight: number;
  enabled: boolean;
  /** 打开抽屉时不存在于库里的新候选 —— 只有它们能在保存前直接移除。 */
  isNew: boolean;
}

const draft = ref<DraftCandidate[]>([]);
const saving = ref(false);
/** 改名用的草稿。保留别名不允许改 —— 它是 auto 智能路由的三档池, 按名字寻址。 */
const nameDraft = ref("");
/** 打开时快照一份, 用于判断"有没有改动"以及取消时还原。 */
const baseline = ref("");

function snapshot(list: DraftCandidate[]): string {
  return JSON.stringify(list.map(c => [c.groupId, c.realModel, c.weight, c.enabled]));
}

// 归一化成百分比: SWRR 只看比例, 所以把 100/1/1 这种原始值换算成 98/1/1 是等价的,
// 但用户一眼能读懂。全为 0 或只有一条时退化为均分。
function normalizeWeights(rows: ModelAliasRow[]): number[] {
  const sum = rows.reduce((s, r) => s + Math.max(r.weight, 0), 0);
  if (rows.length === 0) {
    return [];
  }
  if (sum <= 0) {
    return rows.map(() => Math.max(1, Math.round(100 / rows.length)));
  }
  return rows.map(r => Math.max(1, Math.round((Math.max(r.weight, 0) / sum) * 100)));
}

function buildDraft(rows: ModelAliasRow[]): DraftCandidate[] {
  // 顺序 = 后端的顺序(priority asc, 再按 id), 拖拽即改 priority。
  const sorted = rows.slice().sort((a, b) => a.priority - b.priority || a.id - b.id);
  const pcts = normalizeWeights(sorted);
  return sorted.map((r, i) => ({
    key: `${r.group_id}:${r.real_model}`,
    groupId: r.group_id,
    realModel: r.real_model,
    weight: pcts[i],
    enabled: r.enabled,
    isNew: false,
  }));
}

watch(
  () => props.show,
  open => {
    if (!open) {
      return;
    }
    draft.value = buildDraft(props.rows);
    baseline.value = snapshot(draft.value);
    nameDraft.value = props.alias;
  },
  { immediate: true }
);

const dirty = computed(
  () => snapshot(draft.value) !== baseline.value || nameDraft.value.trim() !== props.alias
);
const configuredTotal = computed(() => draft.value.reduce((s, c) => s + Math.max(c.weight, 0), 0));

// === 实际分流(24h) ===
function callsOf(c: DraftCandidate): number {
  // 按 (分组, 别名) 归因: 日志的 model 列存的是**请求名**, 别名请求下就是别名本身,
  // 不是 real_model —— 用 real_model 查永远命中不了(这正是之前"实测占比恒为 0"的根因)。
  // 同一分组在本别名下有多条候选时, 日志分不开, 只能给分组级合计, 用
  // sharedGroupNote 如实标注, 不假装是模型级数字。
  return props.traffic[`${c.groupId}::${props.alias}`] || 0;
}
const sharedGroupNote = computed(() => {
  const hits: Record<number, number> = {};
  for (const c of draft.value) {
    hits[c.groupId] = (hits[c.groupId] || 0) + 1;
  }
  return Object.values(hits).some(n => n > 1);
});
const actualTotal = computed(() => {
  // 按分组去重求和: callsOf 是分组级合计, 同组两条候选各加一次会翻倍。
  const perGroup = new Map<number, number>();
  for (const c of draft.value) {
    perGroup.set(c.groupId, callsOf(c));
  }
  return Array.from(perGroup.values()).reduce((s, n) => s + n, 0);
});
function actualPct(c: DraftCandidate): number {
  return actualTotal.value > 0 ? Math.round((callsOf(c) / actualTotal.value) * 100) : 0;
}
function configuredPct(c: DraftCandidate): number {
  return configuredTotal.value > 0
    ? Math.round((Math.max(c.weight, 0) / configuredTotal.value) * 100)
    : 0;
}

// === 候选状态 + 就地修复 ===
function stateOf(c: DraftCandidate): CandidateState {
  // 与 services/aliases.ts 的 candidateState 同一套判定, 但作用在草稿行上
  // (草稿可能还没入库, 没有 ModelAliasRow 可用)。
  if (!c.enabled) {
    return "disabled";
  }
  const g = props.groups.find(x => x.id === c.groupId);
  if (!g) {
    return "usable";
  }
  if (parseStringList(g.blocked_models).includes(c.realModel)) {
    return "blocked";
  }
  if (
    g.model_routing_mode === "specified" &&
    !parseStringList(g.exposed_models).includes(c.realModel)
  ) {
    return "unexposed";
  }
  return "usable";
}

const exposing = ref<Record<string, boolean>>({});

async function exposeCandidate(c: DraftCandidate): Promise<void> {
  const key = `${c.groupId}:${c.realModel}`;
  exposing.value = { ...exposing.value, [key]: true };
  try {
    const res = await aliasesApi.exposeModel(props.alias, c.groupId, c.realModel);
    const status = (res as unknown as { data: { status: string } }).data.status;
    message.success(
      status === "not_needed"
        ? t("aliases.drawer.exposeNotNeeded", { model: c.realModel })
        : status === "already_ok"
          ? t("aliases.drawer.exposeAlreadyOk", { model: c.realModel })
          : t("aliases.drawer.exposeDone", { model: c.realModel })
    );
    // 暴露状态来自 group.exposed_models, 只有重拉分组才能刷新徽标。
    emit("exposed");
  } catch (e) {
    message.error(
      e instanceof Error && e.message.includes("blocked")
        ? t("aliases.drawer.exposeBlocked")
        : t("common.requestFailed")
    );
  } finally {
    const next = { ...exposing.value };
    delete next[key];
    exposing.value = next;
  }
}

// === 拖拽排序 ===
const dragIndex = ref<number | null>(null);
function onDragStart(i: number): void {
  dragIndex.value = i;
}
function onDragOver(i: number): void {
  const from = dragIndex.value;
  if (from === null || from === i) {
    return;
  }
  const arr = draft.value;
  const [moved] = arr.splice(from, 1);
  arr.splice(i, 0, moved);
  dragIndex.value = i;
}
function onDragEnd(): void {
  dragIndex.value = null;
}

// 拖拽对键盘/读屏用户不可用 —— 给一组上下移按钮, 与拖拽等价(都只改顺序)。
function move(i: number, delta: number): void {
  const j = i + delta;
  if (j < 0 || j >= draft.value.length) {
    return;
  }
  const arr = draft.value;
  const tmp = arr[i];
  arr[i] = arr[j];
  arr[j] = tmp;
}

function removeCandidate(i: number): void {
  draft.value.splice(i, 1);
}

// === 添加候选 ===
const addGroupId = ref<number | null>(null);
const addModel = ref<string | null>(null);
const addGroupOptions = computed(() =>
  props.groups
    .filter(g => g.id && g.group_type !== "aggregate")
    .map(g => ({ label: getGroupDisplayName(g), value: g.id as number }))
);
const addModelOptions = computed(() =>
  (addGroupId.value ? props.modelsByGroup[addGroupId.value] || [] : []).map(m => ({
    label: m,
    value: m,
  }))
);
function addCandidate(): void {
  const gid = addGroupId.value;
  const model = (addModel.value || "").trim();
  if (!gid || !model) {
    message.warning(t("aliases.edit.needGroupAndModel"));
    return;
  }
  const key = `${gid}:${model}`;
  if (draft.value.some(c => c.key === key)) {
    message.warning(t("aliases.edit.duplicateCandidate"));
    return;
  }
  draft.value.push({ key, groupId: gid, realModel: model, weight: 10, enabled: true, isNew: true });
  addModel.value = null;
}

// 草稿 -> 接口载荷。保存和"撤销删除"共用, 保证两条路径写出去的东西一致。
function toPayload(list: DraftCandidate[]) {
  return list.map((c, i) => ({
    group_id: c.groupId,
    real_model: c.realModel,
    // 百分比直接当 weight 下发 —— SWRR 只看比例, 60/40 与 6/4 等价。
    weight: Math.max(1, c.weight),
    // 拖拽顺序 -> priority。从 1 开始: 后端把 0 当"未设置"替换成 100。
    priority: i + 1,
    enabled: c.enabled,
  }));
}

async function save(): Promise<void> {
  if (!draft.value.length) {
    message.warning(t("aliases.edit.needAtLeastOne"));
    return;
  }
  const newName = nameDraft.value.trim();
  const renamed = newName !== props.alias;
  saving.value = true;
  try {
    // 先改名再改候选: 候选行是挂在名字下的, 顺序反了会先写到旧名字上。
    // 改名是独立的一步, 失败就直接中止 —— 不会动候选。
    if (renamed) {
      await aliasesApi.rename(props.alias, newName);
    }
    await aliasesApi.replaceCandidates(renamed ? newName : props.alias, toPayload(draft.value));
    message.success(t("common.operationSuccess"));
    baseline.value = snapshot(draft.value);
    emit("saved");
    emit("update:show", false);
  } catch {
    message.error(t("common.requestFailed"));
  } finally {
    saving.value = false;
  }
}

// 删除整个别名 = 把候选集合替换成空。复用 replaceCandidates 而不是逐行 DELETE,
// 这样和其它保存走同一条事务路径, 不需要额外的后端接口。
function deleteAlias(): void {
  const restore = toPayload(draft.value);
  dialog.warning({
    title: t("aliases.edit.deleteTitle"),
    content: t("aliases.edit.deleteConfirm", { alias: props.alias, n: restore.length }),
    positiveText: t("common.delete"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      saving.value = true;
      try {
        await aliasesApi.replaceCandidates(props.alias, []);
        emit("saved");
        emit("update:show", false);
        // 删除是高风险且不可逆的动作 —— 给一个真的能撤销的窗口, 而不是只弹
        // 一句"操作成功"。撤销就是把刚才那份候选原样写回去。
        const n = notification.warning({
          title: t("aliases.edit.deletedTitle", { alias: props.alias }),
          content: t("aliases.edit.deletedUndo", { n: restore.length }),
          duration: 8000,
          // notification 的 action 是 render function(返回 VNode), 不是 dialog
          // 那种 { label, onClick } 对象。
          action: () =>
            h(
              NButton,
              {
                size: "small",
                onClick: async () => {
                  try {
                    await aliasesApi.replaceCandidates(props.alias, restore);
                    message.success(t("aliases.edit.undone"));
                    emit("saved");
                  } catch {
                    message.error(t("common.requestFailed"));
                  } finally {
                    n.destroy();
                  }
                },
              },
              { default: () => t("aliases.edit.undo") }
            ),
        });
      } catch {
        message.error(t("common.requestFailed"));
      } finally {
        saving.value = false;
      }
    },
  });
}

function confirmDiscard(close: () => void): void {
  if (!dirty.value) {
    close();
    return;
  }
  dialog.warning({
    title: t("aliases.edit.discardTitle"),
    content: t("aliases.edit.discardConfirm"),
    positiveText: t("aliases.edit.discard"),
    negativeText: t("common.cancel"),
    onPositiveClick: close,
  });
}

function requestClose(): void {
  confirmDiscard(() => emit("update:show", false));
}

async function copyAliasName(): Promise<void> {
  await copy(props.alias);
  message.success(t("v3.aliasCopied", { alias: props.alias }));
}
</script>

<template>
  <NDrawer
    :show="show"
    :width="600"
    placement="right"
    @update:show="v => (v ? emit('update:show', true) : requestClose())"
  >
    <NDrawerContent :native-scrollbar="false" closable @close="requestClose">
      <template #header>
        <span class="aed__title">
          <span class="aed__title-label">{{ t("aliases.edit.title") }}</span>
          <code
            v-if="isReserved"
            class="aed__alias"
            :title="t('v3.aliasClickToCopy')"
            @click="copyAliasName"
          >
            {{ alias }}
          </code>
          <template v-else>
            <NInput
              v-model:value="nameDraft"
              size="tiny"
              class="aed__name"
              :placeholder="t('aliases.edit.namePlaceholder')"
            />
            <span class="aed__copy" :title="t('v3.aliasClickToCopy')" @click="copyAliasName">
              ⧉
            </span>
          </template>
        </span>
      </template>

      <div class="aed">
        <div class="aed__meta">
          <span>{{ t("aliases.edit.candidateCount", { n: draft.length }) }}</span>
          <span v-if="showMeasured && actualTotal > 0" class="aed__meta-sep">
            · {{ t("aliases.edit.actual24h", { n: actualTotal }) }}
            <template v-if="sharedGroupNote">· {{ t("aliases.drawer.actualGroupLevel") }}</template>
          </span>
        </div>

        <div class="aed__list">
          <div
            v-for="(c, i) in draft"
            :key="c.key"
            class="aed__row"
            :class="{ 'aed__row--dragging': dragIndex === i, 'aed__row--off': !c.enabled }"
            draggable="true"
            @dragstart="onDragStart(i)"
            @dragover.prevent="onDragOver(i)"
            @drop.prevent="onDragEnd"
            @dragend="onDragEnd"
          >
            <span class="aed__handle" :title="t('aliases.edit.dragHint')">⠿</span>
            <span class="aed__moves">
              <button
                class="aed__move"
                :disabled="i === 0"
                :title="t('aliases.edit.moveUp')"
                @click.stop="move(i, -1)"
              >
                ↑
              </button>
              <button
                class="aed__move"
                :disabled="i === draft.length - 1"
                :title="t('aliases.edit.moveDown')"
                @click.stop="move(i, 1)"
              >
                ↓
              </button>
            </span>
            <span class="aed__text">
              <span class="aed__model">{{ c.realModel }}</span>
              <span class="aed__group">
                {{ groupNameById[c.groupId] || c.groupId }}
                <template v-if="showMeasured && callsOf(c) > 0">
                  · {{ t("aliases.edit.actualCalls", { n: callsOf(c) }) }}
                </template>
              </span>
              <span v-if="stateOf(c) !== 'usable'" class="aed__flags">
                <StatePill :state="stateOf(c)" />
                <button
                  v-if="stateOf(c) === 'unexposed'"
                  class="aed__expose"
                  :disabled="exposing[`${c.groupId}:${c.realModel}`]"
                  :title="t('aliases.drawer.exposeTip')"
                  @click.stop="exposeCandidate(c)"
                >
                  {{ t("aliases.drawer.expose") }}
                </button>
              </span>
            </span>

            <span class="aed__share">
              <span class="aed__share-num">{{ configuredPct(c) }}%</span>
              <span
                v-if="showMeasured && actualTotal > 0"
                class="aed__share-actual"
                :title="t('aliases.edit.actualShareTip')"
              >
                {{ t("aliases.edit.actualShort", { n: actualPct(c) }) }}
              </span>
            </span>

            <NInputNumber
              v-model:value="c.weight"
              size="tiny"
              :min="1"
              :max="100"
              :step="5"
              class="aed__weight"
            />

            <NTooltip trigger="hover">
              <template #trigger>
                <NSwitch v-model:value="c.enabled" size="small" />
              </template>
              {{ c.enabled ? t("aliases.edit.enabledOn") : t("aliases.edit.enabledOff") }}
            </NTooltip>

            <NButton quaternary size="tiny" @click="removeCandidate(i)">
              <template #icon><TrashOutline /></template>
            </NButton>
          </div>

          <div v-if="!draft.length" class="aed__empty">
            {{ t("aliases.edit.empty") }}
          </div>
        </div>

        <div class="aed__hint">{{ t("aliases.edit.orderHint") }}</div>

        <div class="aed__add">
          <NSelect
            v-model:value="addGroupId"
            :options="addGroupOptions"
            :placeholder="t('aliases.edit.pickGroup')"
            size="small"
            class="aed__add-group"
          />
          <NSelect
            v-model:value="addModel"
            :options="addModelOptions"
            :placeholder="t('aliases.edit.pickModel')"
            size="small"
            filterable
            tag
            :disabled="!addGroupId"
            class="aed__add-model"
          />
          <NButton size="small" type="primary" @click="addCandidate">
            <template #icon><AddOutline /></template>
            {{ t("aliases.edit.add") }}
          </NButton>
        </div>

        <!-- 窗口实测摘要: 与上面的"配置占比"并排, 是为了让偏离可见 ——
             不解释的话, 用户会以为配置的 70% 就该拿到 70% 流量。 -->
        <div v-if="summary && showMeasured && summary.calls" class="aed__summary">
          <span class="aed__summary-k">{{ t("aliases.drawer.window") }}</span>
          <span class="aed__mono">{{ summary.calls.toLocaleString() }}</span>
          <span class="aed__dim">·</span>
          <span class="aed__summary-k">{{ t("aliases.drawer.errRate") }}</span>
          <span class="aed__mono">{{ summary.errorRate.toFixed(1) }}%</span>
          <span class="aed__dim">·</span>
          <span class="aed__summary-k">{{ t("aliases.drawer.avgMs") }}</span>
          <span class="aed__mono">{{ summary.avgMs }}ms</span>
          <template v-if="summary.costUsd > 0">
            <span class="aed__dim">·</span>
            <span class="aed__summary-k">{{ t("aliases.drawer.cost") }}</span>
            <span class="aed__mono">${{ summary.costUsd.toFixed(4) }}</span>
          </template>
          <NTooltip trigger="hover">
            <template #trigger>
              <span class="aed__why">ⓘ</span>
            </template>
            {{ t("aliases.drawer.shareDivergedTip") }}
          </NTooltip>
        </div>
      </div>

      <template #footer>
        <div class="aed__footer">
          <NButton size="small" quaternary type="error" @click="deleteAlias">
            <template #icon><TrashOutline /></template>
            {{ t("aliases.edit.deleteAlias") }}
          </NButton>
          <div class="aed__footer-actions">
            <NButton size="small" @click="requestClose">{{ t("common.cancel") }}</NButton>
            <NButton size="small" type="primary" :loading="saving" @click="save">
              {{ t("common.save") }}
            </NButton>
          </div>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.aed__title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font: 600 13px var(--v3-sans);
}
.aed__title-label {
  font: 600 12px var(--v3-mono);
  color: var(--v3-ink-3);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.aed__name {
  width: 220px;
}
.aed__name :deep(input) {
  font-family: var(--v3-mono);
  font-weight: 600;
}
.aed__copy {
  font-size: 12px;
  color: var(--v3-ink-4);
  cursor: pointer;
}
.aed__copy:hover {
  color: var(--v3-accent);
}
.aed__alias {
  font: 600 12px var(--v3-mono);
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--v3-surface-2);
  color: var(--v3-ink);
  cursor: pointer;
}
.aed {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.aed__meta {
  font: 500 11px var(--v3-mono);
  color: var(--v3-ink-3);
}
.aed__meta-sep {
  margin-left: 4px;
}
.aed__list {
  display: flex;
  flex-direction: column;
  gap: 3px;
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  padding: 5px;
  min-height: 60px;
}
.aed__row {
  display: grid;
  grid-template-columns: 14px 30px minmax(0, 1fr) auto 72px 40px 26px;
  align-items: center;
  gap: 7px;
  padding: 5px 6px;
  border-radius: 5px;
  background: var(--v3-surface-2);
  cursor: grab;
}
.aed__row--dragging {
  opacity: 0.5;
}
.aed__row--off {
  background: var(--v3-surface-3);
}
.aed__handle {
  color: var(--v3-ink-4);
  font-size: 11px;
  text-align: center;
  user-select: none;
}
.aed__moves {
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.aed__move {
  font: 600 9px var(--v3-mono);
  line-height: 11px;
  width: 16px;
  padding: 0;
  border: 1px solid var(--v3-line);
  border-radius: 3px;
  background: var(--v3-surface);
  color: var(--v3-ink-3);
  cursor: pointer;
}
.aed__move:hover:not(:disabled) {
  border-color: var(--v3-accent);
  color: var(--v3-accent);
}
.aed__move:disabled {
  opacity: 0.35;
  cursor: default;
}
.aed__text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.aed__model {
  font: 500 11.5px/1.3 var(--v3-mono);
  color: var(--v3-ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.aed__group {
  font: 400 10px/1.2 var(--v3-mono);
  color: var(--v3-ink-4);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 失效原因徽标 + 就地修复按钮 —— 放在模型/分组下面, 不再靠压暗整行表达。 */
.aed__flags {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-top: 2px;
}
.aed__expose {
  font: 600 9.5px var(--v3-mono);
  border: 1px solid var(--v3-accent);
  border-radius: 3px;
  background: transparent;
  color: var(--v3-accent);
  padding: 1px 5px;
  cursor: pointer;
  white-space: nowrap;
}
.aed__expose:hover:not(:disabled) {
  background: oklch(from var(--v3-accent) l c h / 0.12);
}
.aed__expose:disabled {
  opacity: 0.5;
  cursor: default;
}
.aed__share {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 1px;
}
.aed__share-num {
  font: 600 11px var(--v3-mono);
  color: var(--v3-ink-2);
}
.aed__share-actual {
  font: 400 9.5px var(--v3-mono);
  color: var(--v3-ink-4);
  white-space: nowrap;
}
.aed__weight {
  width: 72px;
}
.aed__empty {
  font: 400 11px var(--v3-sans);
  color: var(--v3-ink-4);
  font-style: italic;
  padding: 10px 6px;
}
.aed__hint {
  font: 400 10.5px var(--v3-sans);
  color: var(--v3-ink-4);
}
.aed__add {
  display: grid;
  grid-template-columns: 160px minmax(0, 1fr) auto;
  gap: 6px;
  align-items: center;
  padding-top: 4px;
  border-top: 1px solid var(--v3-line);
}
.aed__footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
/* 窗口实测摘要 —— 配置占比 vs 实测占比为什么会不一样, 一句话讲清楚。 */
.aed__summary {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 5px;
  padding: 6px 8px;
  border: 1px solid var(--v3-line);
  border-radius: 5px;
  background: var(--v3-surface-2);
  font: 500 10.5px var(--v3-sans);
  color: var(--v3-ink-2);
}
.aed__summary-k {
  color: var(--v3-ink-4);
  font: 500 9.5px var(--v3-mono);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.aed__mono {
  font: 600 10.5px var(--v3-mono);
}
.aed__dim {
  color: var(--v3-ink-4);
}
.aed__why {
  cursor: help;
  color: var(--v3-ink-4);
  font-size: 11px;
}
.aed__why:hover {
  color: var(--v3-accent);
}
.aed__footer-actions {
  display: flex;
  gap: 8px;
}
</style>
