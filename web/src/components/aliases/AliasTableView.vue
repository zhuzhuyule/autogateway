<script setup lang="ts">
// 表格视图 —— 密集审计用。
//
// 与卡片视图的分工: 卡片是"看一个别名里有什么", 表格是"看所有候选里哪些有问题"。
// 所以这里刻意**打平**成一行一个候选, 支持按任意列排序 —— 比如按 24h 调用倒序,
// 一眼看出流量到底落在谁身上; 或按状态排序, 把所有失效候选集中到顶部。
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { NIcon, NInput, NSelect, NSwitch } from "naive-ui";
import { SearchOutline } from "@vicons/ionicons5";
import type { AliasView, CandidateRowView, CandidateState } from "@/components/aliases/types";
import type { ModelAliasRow } from "@/api/aliases";
import StatePill from "@/components/aliases/StatePill.vue";

const props = defineProps<{ aliases: AliasView[]; loading: boolean }>();
const emit = defineEmits<{
  (e: "edit", alias: string): void;
  (e: "fix", row: ModelAliasRow): void;
  (e: "toggle", row: ModelAliasRow): void;
  (e: "remove", row: ModelAliasRow): void;
}>();

const { t } = useI18n();

interface FlatRow extends CandidateRowView {
  alias: string;
  isReserved: boolean;
  /** 该候选在所属别名里的实际占比(24h), 无流量时为 0。 */
  actualPct: number;
}

const query = ref("");
const stateFilter = ref<CandidateState | "all" | "problem">("all");
type SortKey = "alias" | "group" | "model" | "state" | "share" | "calls";
const sortKey = ref<SortKey>("alias");
const sortDesc = ref(false);

const stateOptions = computed(() => [
  { label: t("aliases.table.filterAll"), value: "all" },
  { label: t("aliases.table.filterProblem"), value: "problem" },
  { label: t("v3.aliasStateUsable"), value: "usable" },
  { label: t("v3.aliasStateUnexposed"), value: "unexposed" },
  { label: t("v3.aliasStateBlocked"), value: "blocked" },
  { label: t("v3.aliasStateDisabled"), value: "disabled" },
]);

const flat = computed<FlatRow[]>(() => {
  const out: FlatRow[] = [];
  for (const a of props.aliases) {
    for (const r of a.rows) {
      out.push({
        ...r,
        alias: a.alias,
        isReserved: a.isReserved,
        actualPct: a.calls > 0 ? Math.round((r.calls / a.calls) * 100) : 0,
      });
    }
  }
  return out;
});

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase();
  return flat.value.filter(r => {
    if (stateFilter.value === "problem" && r.state === "usable") {
      return false;
    }
    if (
      stateFilter.value !== "all" &&
      stateFilter.value !== "problem" &&
      r.state !== stateFilter.value
    ) {
      return false;
    }
    if (!q) {
      return true;
    }
    return (
      r.alias.toLowerCase().includes(q) ||
      r.groupName.toLowerCase().includes(q) ||
      r.row.real_model.toLowerCase().includes(q)
    );
  });
});

// 状态的排序要按"问题严重度", 不能按字典序 —— 否则 unexposed/usabled 之类
// 的字母序毫无意义。
const STATE_RANK: Record<CandidateState, number> = {
  unexposed: 0,
  blocked: 1,
  disabled: 2,
  usable: 3,
};

const sorted = computed(() => {
  const arr = filtered.value.slice();
  const dir = sortDesc.value ? -1 : 1;
  const key = sortKey.value;
  arr.sort((a, b) => {
    let d = 0;
    switch (key) {
      case "alias":
        d = a.alias.localeCompare(b.alias);
        break;
      case "group":
        d = a.groupName.localeCompare(b.groupName);
        break;
      case "model":
        d = a.row.real_model.localeCompare(b.row.real_model);
        break;
      case "state":
        d = STATE_RANK[a.state] - STATE_RANK[b.state];
        break;
      case "share":
        d = a.share - b.share;
        break;
      case "calls":
        d = a.calls - b.calls;
        break;
    }
    if (d === 0) {
      // 同键时保持稳定: 用 (别名, 模型) 兜底, 避免每次重排顺序抖动。
      d = a.alias.localeCompare(b.alias) || a.row.real_model.localeCompare(b.row.real_model);
    }
    return d * dir;
  });
  return arr;
});

/** 供表头 aria-sort 用 —— 屏幕阅读器要知道当前排序列和方向。 */
function ariaSort(k: SortKey): "ascending" | "descending" | "none" {
  if (sortKey.value !== k) {
    return "none";
  }
  return sortDesc.value ? "descending" : "ascending";
}

function toggleSort(k: SortKey): void {
  if (sortKey.value === k) {
    sortDesc.value = !sortDesc.value;
  } else {
    sortKey.value = k;
    // 数值列默认倒序(先看大的), 文本列默认正序。
    sortDesc.value = k === "share" || k === "calls";
  }
}

const summary = computed(() => ({
  shown: sorted.value.length,
  total: flat.value.length,
}));
</script>

<template>
  <div class="atv">
    <div class="atv__bar">
      <NInput
        v-model:value="query"
        size="small"
        clearable
        :placeholder="t('aliases.table.search')"
        class="atv__search"
      >
        <template #prefix><NIcon :component="SearchOutline" /></template>
      </NInput>
      <NSelect
        v-model:value="stateFilter"
        size="small"
        :options="stateOptions"
        class="atv__filter"
      />
      <span class="atv__count">
        {{ t("aliases.table.showing", { shown: summary.shown, total: summary.total }) }}
      </span>
    </div>

    <div class="atv__wrap">
      <table class="atv__table">
        <thead>
          <tr>
            <th scope="col" class="atv__th--sort" :aria-sort="ariaSort('alias')" @click="toggleSort('alias')">
              {{ t("aliases.table.colAlias") }}
              <span v-if="sortKey === 'alias'" class="atv__caret">{{ sortDesc ? "▾" : "▴" }}</span>
            </th>
            <th scope="col" class="atv__th--sort" :aria-sort="ariaSort('group')" @click="toggleSort('group')">
              {{ t("aliases.table.colGroup") }}
              <span v-if="sortKey === 'group'" class="atv__caret">{{ sortDesc ? "▾" : "▴" }}</span>
            </th>
            <th scope="col" class="atv__th--sort" :aria-sort="ariaSort('model')" @click="toggleSort('model')">
              {{ t("aliases.table.colModel") }}
              <span v-if="sortKey === 'model'" class="atv__caret">{{ sortDesc ? "▾" : "▴" }}</span>
            </th>
            <th scope="col" class="atv__th--sort atv__num" :aria-sort="ariaSort('state')" @click="toggleSort('state')">
              {{ t("aliases.table.colState") }}
              <span v-if="sortKey === 'state'" class="atv__caret">{{ sortDesc ? "▾" : "▴" }}</span>
            </th>
            <th scope="col" class="atv__th--sort atv__num" :aria-sort="ariaSort('share')" @click="toggleSort('share')">
              {{ t("aliases.table.colShare") }}
              <span v-if="sortKey === 'share'" class="atv__caret">{{ sortDesc ? "▾" : "▴" }}</span>
            </th>
            <th scope="col" class="atv__th--sort atv__num" :aria-sort="ariaSort('calls')" @click="toggleSort('calls')">
              {{ t("aliases.table.colCalls") }}
              <span v-if="sortKey === 'calls'" class="atv__caret">{{ sortDesc ? "▾" : "▴" }}</span>
            </th>
            <th scope="col" class="atv__num">{{ t("aliases.table.colActions") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="r in sorted"
            :key="r.row.id"
            class="atv__row"
            :class="{ 'atv__row--dead': r.state !== 'usable' }"
          >
            <td>
              <button class="atv__alias" :title="t('v3.aliasClickToCopy')" @click="emit('edit', r.alias)">
                {{ r.alias }}
              </button>
            </td>
            <td class="atv__muted">{{ r.groupName }}</td>
            <td class="atv__model" :title="r.row.real_model">{{ r.row.real_model }}</td>
            <td class="atv__num">
              <StatePill :state="r.state" />
            </td>
            <td class="atv__num atv__mono">{{ r.share }}%</td>
            <td class="atv__num atv__mono">
              <template v-if="r.calls > 0">
                {{ r.calls }}
                <span class="atv__pct">({{ r.actualPct }}%)</span>
              </template>
              <span v-else class="atv__muted">—</span>
            </td>
            <td class="atv__num">
              <div class="atv__actions">
                <button
                  v-if="r.state === 'unexposed'"
                  class="atv__act atv__act--fix"
                  :title="t('v3.aliasUnexposedFixTip')"
                  @click="emit('fix', r.row)"
                >
                  {{ t("v3.aliasFix") }}
                </button>
                <NSwitch
                  :value="r.row.enabled"
                  size="small"
                  @update:value="emit('toggle', r.row)"
                />
                <button class="atv__act" :title="t('common.delete')" @click="emit('remove', r.row)">
                  ✕
                </button>
              </div>
            </td>
          </tr>
          <tr v-if="!sorted.length">
            <td colspan="7" class="atv__empty">{{ t("aliases.table.empty") }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.atv {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.atv__bar {
  display: flex;
  align-items: center;
  gap: 10px;
}
.atv__search {
  max-width: 280px;
}
.atv__filter {
  width: 140px;
}
.atv__count {
  margin-left: auto;
  font: 500 10.5px var(--v3-mono);
  color: var(--v3-ink-4);
}
.atv__wrap {
  border: 1px solid var(--v3-line);
  border-radius: 8px;
  overflow: hidden;
  background: var(--v3-surface);
}
.atv__table {
  width: 100%;
  border-collapse: collapse;
  font: 400 12px var(--v3-sans);
}
.atv__table thead th {
  position: sticky;
  top: 0;
  z-index: 1;
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
.atv__th--sort {
  cursor: pointer;
  user-select: none;
}
.atv__th--sort:hover {
  color: var(--v3-ink);
}
.atv__caret {
  margin-left: 3px;
  color: var(--v3-accent);
}
.atv__table td {
  padding: 6px 10px;
  border-bottom: 1px solid var(--v3-line);
  vertical-align: middle;
}
.atv__row:hover {
  background: var(--v3-surface-2);
}
.atv__row--dead {
  background: oklch(from var(--v3-warn) l c h / 0.04);
}
.atv__num {
  text-align: right;
}
.atv__mono {
  font-family: var(--v3-mono);
  white-space: nowrap;
}
.atv__pct {
  color: var(--v3-ink-4);
  font-size: 10px;
}
.atv__muted {
  color: var(--v3-ink-4);
}
.atv__alias {
  font: 600 11.5px var(--v3-mono);
  color: var(--v3-accent);
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
}
.atv__alias:hover {
  text-decoration: underline;
}
.atv__model {
  font: 500 11.5px var(--v3-mono);
  color: var(--v3-ink);
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.atv__actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 6px;
}
.atv__act {
  font: 600 10px var(--v3-mono);
  border: 1px solid var(--v3-line);
  background: transparent;
  color: var(--v3-ink-3);
  border-radius: 3px;
  padding: 2px 6px;
  cursor: pointer;
}
.atv__act:hover {
  border-color: var(--v3-danger);
  color: var(--v3-danger);
}
.atv__act--fix {
  border-color: var(--v3-warn);
  color: oklch(0.45 0.13 65);
}
.atv__act--fix:hover {
  background: var(--v3-warn);
  color: white;
  border-color: var(--v3-warn);
}
.atv__empty {
  text-align: center;
  color: var(--v3-ink-4);
  font-style: italic;
  padding: 40px 10px !important;
}
</style>
