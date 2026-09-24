<script setup lang="ts">
// 别名列表 —— 页面主轴。一行 = 一个别名(不是一条候选), 这是这一版重构的出发点:
// 之前页面按 auto 的三个档位分栏, 自定义别名被当成候选塞进档位里, 于是
// "61 条映射" 之类的标题按数据库行说话, 而用户心里想的是"我有几个别名"。
//
// 三个筛选器取代了原来四个视图模式(卡片/表格/分栏/健康):
//   搜索   — 别名 / 候选模型 / 分组名
//   状态   — 全部 | 有问题 | auto 档位
//   分组   — 只看用到某个分组的别名(回答"删了 g1 会波及谁")
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { NIcon, NInput, NSelect, NSpin } from "naive-ui";
import { LockClosedOutline, SearchOutline } from "@vicons/ionicons5";
import StatePill from "@/components/aliases/StatePill.vue";
import { STATE_PILL_FOR, type AliasView } from "@/components/aliases/types";
import { getGroupDisplayName } from "@/utils/display";
import type { Group } from "@/types/models";

const props = defineProps<{
  aliases: AliasView[];
  groups: Group[];
  loading: boolean;
  /** traffic 接口是否可用; false 时整组实测列不渲染 —— 全列「—」会被读成"没人用"。 */
  showMeasured: boolean;
  /** 刚创建/外部跳转来的别名, 闪一下让人找得到。 */
  highlight?: string | null;
}>();

const emit = defineEmits<{
  (e: "open", alias: string): void;
  (e: "add", alias: string): void;
  (e: "copy", alias: string): void;
}>();

const { t } = useI18n();
const query = ref("");
const status = ref<"all" | "problem" | "auto">("all");
const groupId = ref<number | "all">("all");
type SortKey = "calls" | "errorRate" | "avgMs" | "alias";
const sortKey = ref<SortKey>("calls");
const sortDesc = ref(true);

const problemTotal = computed(() => props.aliases.filter(a => a.problem).length);

const statusOptions = computed(() => [
  { value: "all" as const, label: t("aliases.list.filterAll") },
  { value: "problem" as const, label: t("aliases.list.filterProblem", { n: problemTotal.value }) },
  { value: "auto" as const, label: t("aliases.list.filterAuto") },
]);

const groupOptions = computed(() => [
  { value: "all" as const, label: t("aliases.list.filterAnyGroup") },
  ...props.groups
    .filter(g => g.id && g.group_type !== "aggregate")
    .map(g => ({ value: g.id as number, label: getGroupDisplayName(g) })),
]);

const visible = computed(() => {
  const q = query.value.trim().toLowerCase();
  const list = props.aliases.filter(a => {
    if (status.value === "problem" && !a.problem) {
      return false;
    }
    if (status.value === "auto" && !a.tier) {
      return false;
    }
    if (groupId.value !== "all" && !a.rows.some(r => r.row.group_id === groupId.value)) {
      return false;
    }
    if (!q) {
      return true;
    }
    return (
      a.alias.toLowerCase().includes(q) ||
      a.rows.some(
        r => r.row.real_model.toLowerCase().includes(q) || r.groupName.toLowerCase().includes(q)
      )
    );
  });
  const dir = sortDesc.value ? -1 : 1;
  return list.slice().sort((x, y) => {
    // 档位永远排在最前 —— 它们是 auto 的实现细节, 不是和用户一样的普通别名。
    const tx = (x.tier ? 0 : 1) - (y.tier ? 0 : 1);
    if (tx !== 0) {
      return tx;
    }
    switch (sortKey.value) {
      case "calls":
        return (x.calls - y.calls) * dir;
      case "errorRate":
        return (x.errorRate - y.errorRate) * dir;
      case "avgMs":
        return (x.avgMs - y.avgMs) * dir;
      default:
        return x.alias.localeCompare(y.alias) * dir;
    }
  });
});

function ariaSort(k: SortKey): "ascending" | "descending" | "none" {
  return sortKey.value === k ? (sortDesc.value ? "descending" : "ascending") : "none";
}
function toggleSort(k: SortKey): void {
  if (sortKey.value === k) {
    sortDesc.value = !sortDesc.value;
  } else {
    sortKey.value = k;
    sortDesc.value = true;
  }
}

function problemCountOf(a: AliasView, state: string): number {
  return a.rows.filter(r => r.state === state).length;
}

function problemLabel(a: AliasView): string {
  switch (a.problem) {
    case "no-candidates":
      return t("aliases.list.problemNoCandidates");
    case "unexposed":
      return t("aliases.list.problemUnexposed", { n: problemCountOf(a, "unexposed") });
    case "blocked":
      return t("aliases.list.problemBlocked", { n: problemCountOf(a, "blocked") });
    case "disabled":
      return t("aliases.list.problemDisabled", { n: problemCountOf(a, "disabled") });
    default:
      return "";
  }
}

function fmtCalls(n: number): string {
  return n.toLocaleString();
}
/** errorRate 后端就是百分数(0..100), 与 top-models 同口径, 这里只加个 % 和一位小数。 */
function fmtRate(r: number): string {
  return `${r.toFixed(1)}%`;
}
function fmtMs(ms: number): string {
  if (!ms) {
    return "—";
  }
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms`;
}
</script>

<template>
  <div class="alz">
    <div class="alz__bar">
      <NInput
        v-model:value="query"
        size="small"
        clearable
        :placeholder="t('aliases.list.search')"
        class="alz__q"
      >
        <template #prefix><NIcon :component="SearchOutline" /></template>
      </NInput>
      <NSelect v-model:value="status" size="small" :options="statusOptions" class="alz__f" />
      <NSelect
        v-model:value="groupId"
        size="small"
        filterable
        :options="groupOptions"
        class="alz__f alz__f--grp"
      />
      <span class="alz__count">
        {{ t("aliases.list.showing", { shown: visible.length, total: aliases.length }) }}
      </span>
    </div>

    <NSpin :show="loading">
      <table class="alz__table">
        <thead>
          <tr>
            <th
              scope="col"
              class="alz__th--sort"
              :aria-sort="ariaSort('alias')"
              @click="toggleSort('alias')"
            >
              {{ t("aliases.list.colAlias") }}
            </th>
            <th scope="col">{{ t("aliases.list.colRole") }}</th>
            <th scope="col" class="alz__num">{{ t("aliases.list.colCandidates") }}</th>
            <th
              v-if="showMeasured"
              scope="col"
              class="alz__th--sort alz__num"
              :aria-sort="ariaSort('calls')"
              @click="toggleSort('calls')"
            >
              {{ t("aliases.list.colCalls") }}
            </th>
            <th
              v-if="showMeasured"
              scope="col"
              class="alz__th--sort alz__num"
              :aria-sort="ariaSort('errorRate')"
              @click="toggleSort('errorRate')"
            >
              {{ t("aliases.list.colErrorRate") }}
            </th>
            <th
              v-if="showMeasured"
              scope="col"
              class="alz__th--sort alz__num"
              :aria-sort="ariaSort('avgMs')"
              @click="toggleSort('avgMs')"
            >
              {{ t("aliases.list.colAvg") }}
            </th>
            <th scope="col">{{ t("aliases.list.colState") }}</th>
            <th scope="col" class="alz__num">{{ t("aliases.list.colActions") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="a in visible"
            :key="a.alias"
            class="alz__row"
            :class="{ 'alz__row--problem': !!a.problem, 'alz__row--flash': a.alias === highlight }"
            tabindex="0"
            @click="emit('open', a.alias)"
            @keydown.enter.prevent="emit('open', a.alias)"
          >
            <td>
              <code class="alz__name">{{ a.alias }}</code>
              <span v-if="a.isReserved && !a.tier" class="alz__lock">
                <NIcon :component="LockClosedOutline" :size="10" />
              </span>
            </td>
            <td>
              <span v-if="a.tier" class="alz__tier" :class="`alz__tier--${a.tier}`">
                {{ t("aliases.list.roleTier", { tier: t(`v3.${a.tier}`) }) }}
              </span>
              <span v-else class="alz__dim">—</span>
            </td>
            <td class="alz__num alz__mono">
              {{ a.usable }}
              <span v-if="a.unusable" class="alz__bad">/{{ a.total }}</span>
              <span v-else class="alz__dim">/{{ a.total }}</span>
            </td>
            <td v-if="showMeasured" class="alz__num alz__mono">
              {{ a.calls ? fmtCalls(a.calls) : "—" }}
            </td>
            <td
              v-if="showMeasured"
              class="alz__num alz__mono"
              :class="{ alz__bad: a.errorRate >= 10 }"
            >
              {{ a.calls ? fmtRate(a.errorRate) : "—" }}
            </td>
            <td v-if="showMeasured" class="alz__num alz__mono">{{ fmtMs(a.avgMs) }}</td>
            <td>
              <StatePill v-if="a.problem" :state="STATE_PILL_FOR[a.problem]" />
              <span v-else class="alz__ok">●</span>
              <div v-if="problemLabel(a)" class="alz__problem">{{ problemLabel(a) }}</div>
              <div v-if="a.crossTier" class="alz__dim alz__tiny">
                {{ t("aliases.list.crossTier") }}
              </div>
            </td>
            <td class="alz__num" @click.stop>
              <div class="alz__acts">
                <button class="alz__act" @click="emit('copy', a.alias)">
                  {{ t("aliases.list.copyName") }}
                </button>
                <button class="alz__act alz__act--primary" @click="emit('add', a.alias)">
                  {{ t("aliases.list.addCandidate") }}
                </button>
                <button class="alz__act" @click="emit('open', a.alias)">
                  {{ t("aliases.list.edit") }}
                </button>
              </div>
            </td>
          </tr>
          <tr v-if="!visible.length">
            <td :colspan="showMeasured ? 8 : 5" class="alz__empty">
              {{ t("aliases.list.empty") }}
            </td>
          </tr>
        </tbody>
      </table>
    </NSpin>
  </div>
</template>

<style scoped>
.alz {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.alz__bar {
  display: flex;
  align-items: center;
  gap: 10px;
}
.alz__q {
  max-width: 280px;
}
.alz__f {
  width: 150px;
}
.alz__f--grp {
  width: 190px;
}
.alz__count {
  margin-left: auto;
  font: 500 10.5px var(--v3-mono);
  color: var(--v3-ink-4);
}
.alz__table {
  width: 100%;
  border-collapse: collapse;
  font: 400 12px var(--v3-sans);
  background: var(--v3-surface);
  border: 1px solid var(--v3-line);
  border-radius: 8px;
  overflow: hidden;
}
.alz__table thead th {
  position: sticky;
  top: 0;
  background: var(--v3-surface-2);
  border-bottom: 1px solid var(--v3-line);
  padding: 7px 10px;
  text-align: left;
  font: 600 10px var(--v3-mono);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--v3-ink-3);
  white-space: nowrap;
}
.alz__th--sort {
  cursor: pointer;
  user-select: none;
}
.alz__th--sort:hover {
  color: var(--v3-ink);
}
.alz__table td {
  padding: 7px 10px;
  border-bottom: 1px solid var(--v3-line);
  vertical-align: top;
}
.alz__row {
  cursor: pointer;
}
.alz__row:hover {
  background: var(--v3-surface-2);
}
.alz__row:focus-visible {
  outline: 2px solid var(--v3-accent);
  outline-offset: -2px;
}
.alz__row--problem {
  background: oklch(from var(--v3-warn) l c h / 0.04);
}
@keyframes alz-flash {
  0% {
    box-shadow: inset 0 0 0 2px var(--v3-accent);
  }
  100% {
    box-shadow: inset 0 0 0 2px transparent;
  }
}
.alz__row--flash {
  animation: alz-flash 1.5s ease-out 1;
}
.alz__num {
  text-align: right;
}
.alz__mono {
  font-family: var(--v3-mono);
  white-space: nowrap;
}
.alz__name {
  font: 600 11.5px var(--v3-mono);
  color: var(--v3-ink);
}
.alz__dim {
  color: var(--v3-ink-4);
}
.alz__tiny {
  font-size: 9.5px;
  margin-top: 2px;
  white-space: nowrap;
}
.alz__bad {
  color: var(--v3-danger);
}
.alz__ok {
  color: var(--v3-ok);
  font-size: 9px;
}
.alz__tier {
  font: 600 9.5px var(--v3-mono);
  padding: 1px 6px;
  border-radius: 999px;
  white-space: nowrap;
}
.alz__tier--simple {
  background: oklch(from var(--v3-ok) l c h / 0.12);
  color: oklch(0.45 0.1 150);
}
.alz__tier--medium {
  background: oklch(from var(--v3-warn) l c h / 0.12);
  color: oklch(0.5 0.12 65);
}
.alz__tier--complex {
  background: oklch(from var(--v3-danger) l c h / 0.12);
  color: oklch(0.48 0.16 25);
}
.alz__lock {
  color: var(--v3-warn);
  margin-left: 3px;
}
.alz__problem {
  font: 400 9.5px var(--v3-sans);
  color: oklch(0.5 0.12 65);
  margin-top: 2px;
  white-space: nowrap;
}
.alz__acts {
  display: flex;
  justify-content: flex-end;
  gap: 5px;
}
.alz__act {
  font: 600 10px var(--v3-mono);
  border: 1px solid var(--v3-line);
  background: transparent;
  color: var(--v3-ink-3);
  border-radius: 3px;
  padding: 2px 6px;
  cursor: pointer;
}
.alz__act:hover {
  border-color: var(--v3-accent);
  color: var(--v3-accent);
}
.alz__act--primary {
  border-color: var(--v3-accent);
  color: var(--v3-accent);
}
.alz__empty {
  text-align: center;
  color: var(--v3-ink-4);
  font-style: italic;
  padding: 40px 10px !important;
}
</style>
