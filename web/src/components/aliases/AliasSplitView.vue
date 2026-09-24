<script setup lang="ts">
// 分栏视图 —— 连续调优用。
//
// 左列表 + 右详情, 选中即看, 不用为每个别名开一次抽屉。行内直接能做的
// (启停 / 移除 / 修失效) 就地做掉, 只有"改结构"(增删候选、调权重、排序)
// 才需要打开完整编辑抽屉 —— 因为那类改动要一次保存, 不适合逐行即时生效。
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { NIcon, NInput, NSwitch } from "naive-ui";
import { SearchOutline } from "@vicons/ionicons5";
import type { AliasView, CandidateRowView } from "@/components/aliases/types";
import type { ModelAliasRow } from "@/api/aliases";
import StatePill from "@/components/aliases/StatePill.vue";
import MetricCell from "@/components/aliases/MetricCell.vue";

const props = defineProps<{ aliases: AliasView[]; loading: boolean }>();
const emit = defineEmits<{
  (e: "edit", alias: string): void;
  (e: "fix", row: ModelAliasRow): void;
  (e: "toggle", row: ModelAliasRow): void;
  (e: "remove", row: ModelAliasRow): void;
  (e: "copy", alias: string): void;
  (e: "add", alias: string): void;
}>();

const { t } = useI18n();

const query = ref("");
const selected = ref<string | null>(null);

const list = computed(() => {
  const q = query.value.trim().toLowerCase();
  if (!q) {
    return props.aliases;
  }
  return props.aliases.filter(
    a => a.alias.toLowerCase().includes(q) || a.rows.some(r => r.row.real_model.toLowerCase().includes(q))
  );
});

// 选中项跟随数据变化: 默认选第一个; 选中的别名被删了就回落到第一个。
// (放在 watch 里而不是 onMounted —— 别名列表是异步来的, 挂载时可能还是空的。)
watch(
  () => props.aliases,
  listNow => {
    if (!listNow.length) {
      selected.value = null;
      return;
    }
    if (!selected.value || !listNow.some(a => a.alias === selected.value)) {
      selected.value = listNow[0].alias;
    }
  },
  { immediate: true }
);

const current = computed<AliasView | null>(
  () => props.aliases.find(a => a.alias === selected.value) || null
);

function fmtMs(ms: number): string {
  if (ms <= 0) {
    return "—";
  }
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

function actualPct(r: CandidateRowView): number {
  const total = current.value?.calls || 0;
  return total > 0 ? Math.round((r.calls / total) * 100) : 0;
}

/** 左列表的健康条: 绿色占比 = 可用候选比例。 */
function healthPct(a: AliasView): number {
  return a.total > 0 ? Math.round((a.usable / a.total) * 100) : 0;
}
</script>

<template>
  <div class="asv">
    <aside class="asv__side">
      <div class="asv__side-head">
        <NInput
          v-model:value="query"
          size="tiny"
          clearable
          :placeholder="t('aliases.split.search')"
        >
          <template #prefix><NIcon :component="SearchOutline" /></template>
        </NInput>
      </div>
      <div class="asv__list">
        <button
          v-for="a in list"
          :key="a.alias"
          type="button"
          class="asv__item"
          :class="{ 'asv__item--active': a.alias === selected }"
          @click="selected = a.alias"
        >
          <span class="asv__item-top">
            <span class="asv__item-name">{{ a.alias }}</span>
            <span class="asv__item-num" :class="{ 'asv__item-num--bad': a.unusable > 0 }">
              {{ a.usable }}/{{ a.total }}
            </span>
          </span>
          <span class="asv__bar">
            <span class="asv__bar-fill" :style="{ width: healthPct(a) + '%' }" />
          </span>
        </button>
        <div v-if="!list.length" class="asv__empty">{{ t("aliases.split.empty") }}</div>
      </div>
    </aside>

    <section class="asv__detail">
      <template v-if="current">
        <div class="asv__head">
          <button class="asv__title" :title="t('v3.aliasClickToCopy')" @click="emit('copy', current.alias)">
            {{ current.alias }}
          </button>
          <span v-if="current.isReserved" class="asv__badge">{{ t("aliases.split.reserved") }}</span>
          <span class="asv__meta">
            {{ t("aliases.split.summary", { usable: current.usable, total: current.total }) }}
            <template v-if="current.calls > 0">
              · {{ t("aliases.edit.actual24h", { n: current.calls }) }}
            </template>
          </span>
          <div class="asv__head-actions">
            <button class="v3-btn v3-btn--sm" @click="emit('add', current.alias)">
              {{ t("aliases.split.addCandidate") }}
            </button>
            <button class="v3-btn v3-btn--sm v3-btn--accent" @click="emit('edit', current.alias)">
              {{ t("aliases.split.fullEdit") }}
            </button>
          </div>
        </div>

        <div class="asv__rows">
          <div
            v-for="r in current.rows"
            :key="r.row.id"
            class="asv__row"
            :class="{ 'asv__row--dead': r.state !== 'usable' }"
          >
            <StatePill :state="r.state" variant="dot" />

            <span class="asv__text">
              <span class="asv__model">{{ r.row.real_model }}</span>
              <span class="asv__sub">{{ r.groupName }}</span>
            </span>

            <StatePill :state="r.state" />

            <MetricCell :value="`${r.share}%`" :label="t('aliases.table.colShare')" />
            <MetricCell
              :value="r.calls > 0 ? `${actualPct(r)}%` : '—'"
              :label="t('aliases.split.actual')"
            />
            <MetricCell :value="fmtMs(r.avgMs)" :label="t('aliases.split.latency')" />
            <MetricCell :value="r.cost || '—'" :label="t('aliases.split.cost')" />

            <span class="asv__actions">
              <button
                v-if="r.state === 'unexposed'"
                class="asv__act asv__act--fix"
                :title="t('v3.aliasUnexposedFixTip')"
                @click="emit('fix', r.row)"
              >
                {{ t("v3.aliasFix") }}
              </button>
              <NSwitch :value="r.row.enabled" size="small" @update:value="emit('toggle', r.row)" />
              <button class="asv__act" :title="t('common.delete')" @click="emit('remove', r.row)">
                ✕
              </button>
            </span>
          </div>

          <div v-if="!current.rows.length" class="asv__empty asv__empty--detail">
            {{ t("aliases.edit.empty") }}
          </div>
        </div>
      </template>

      <div v-else class="asv__empty asv__empty--detail">
        {{ t("aliases.split.pick") }}
      </div>
    </section>
  </div>
</template>

<style scoped>
.asv {
  display: grid;
  grid-template-columns: 260px minmax(0, 1fr);
  gap: 14px;
  align-items: start;
}
.asv__side {
  border: 1px solid var(--v3-line);
  border-radius: 8px;
  background: var(--v3-surface);
  overflow: hidden;
}
.asv__side-head {
  padding: 8px;
  border-bottom: 1px solid var(--v3-line);
  background: var(--v3-surface-2);
}
.asv__list {
  max-height: 620px;
  overflow-y: auto;
  padding: 6px;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.asv__item {
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding: 7px 9px;
  border: 1px solid transparent;
  border-radius: 6px;
  background: transparent;
  cursor: pointer;
  text-align: left;
  font: inherit;
  color: inherit;
}
.asv__item:hover {
  background: var(--v3-surface-2);
}
.asv__item--active {
  background: var(--v3-accent-soft, oklch(0.96 0.04 240));
  border-color: var(--v3-accent);
}
.asv__item-top {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}
.asv__item-name {
  font: 600 11.5px var(--v3-mono);
  color: var(--v3-ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.asv__item-num {
  font: 600 10px var(--v3-mono);
  color: var(--v3-ink-4);
  flex-shrink: 0;
}
.asv__item-num--bad {
  color: var(--v3-warn);
}
.asv__bar {
  display: block;
  height: 3px;
  border-radius: 2px;
  background: var(--v3-surface-3);
  overflow: hidden;
}
.asv__bar-fill {
  display: block;
  height: 100%;
  background: var(--v3-ok);
  border-radius: 2px;
}

.asv__detail {
  border: 1px solid var(--v3-line);
  border-radius: 8px;
  background: var(--v3-surface);
  min-height: 200px;
}
.asv__head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--v3-line);
  background: var(--v3-surface-2);
  border-radius: 8px 8px 0 0;
}
.asv__title {
  font: 700 13px var(--v3-mono);
  color: var(--v3-ink);
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
}
.asv__title:hover {
  color: var(--v3-accent);
}
.asv__badge {
  font: 600 9px var(--v3-mono);
  padding: 1px 5px;
  border-radius: 3px;
  background: var(--v3-warn-soft);
  color: oklch(0.45 0.13 65);
}
.asv__meta {
  font: 500 10.5px var(--v3-mono);
  color: var(--v3-ink-4);
}
.asv__head-actions {
  margin-left: auto;
  display: flex;
  gap: 6px;
}

.asv__rows {
  display: flex;
  flex-direction: column;
  padding: 6px;
  gap: 2px;
}
.asv__row {
  display: grid;
  grid-template-columns: 7px minmax(0, 1fr) auto repeat(4, auto) auto;
  align-items: center;
  gap: 12px;
  padding: 7px 10px;
  border-radius: 6px;
}
.asv__row:hover {
  background: var(--v3-surface-2);
}
.asv__row--dead {
  background: oklch(from var(--v3-warn) l c h / 0.05);
}
.asv__text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.asv__model {
  font: 500 11.5px/1.3 var(--v3-mono);
  color: var(--v3-ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.asv__sub {
  font: 400 10px/1.2 var(--v3-mono);
  color: var(--v3-ink-4);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.asv__actions {
  display: flex;
  align-items: center;
  gap: 6px;
}
.asv__act {
  font: 600 10px var(--v3-mono);
  border: 1px solid var(--v3-line);
  background: transparent;
  color: var(--v3-ink-3);
  border-radius: 3px;
  padding: 2px 6px;
  cursor: pointer;
}
.asv__act:hover {
  border-color: var(--v3-danger);
  color: var(--v3-danger);
}
.asv__act--fix {
  border-color: var(--v3-warn);
  color: oklch(0.45 0.13 65);
}
.asv__act--fix:hover {
  background: var(--v3-warn);
  color: white;
}
.asv__empty {
  padding: 16px;
  text-align: center;
  font: 400 11px var(--v3-sans);
  color: var(--v3-ink-4);
  font-style: italic;
}
.asv__empty--detail {
  padding: 60px 16px;
}
</style>
