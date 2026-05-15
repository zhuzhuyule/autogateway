<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NIcon } from "naive-ui";
import { ChevronDownOutline, ChevronForwardOutline } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";
import type { DedupFamily, DedupModelEntry } from "@/api/dedup";
import ModelEntryRow from "./ModelEntryRow.vue";

const props = defineProps<{
  families: DedupFamily[];
  searchQuery: string;
  selected: Map<string, DedupModelEntry>;
  highlightAlias: string | null;
}>();

const emit = defineEmits<{
  (e: "toggle", entry: DedupModelEntry): void;
  (e: "click-alias", alias: string): void;
  (e: "bulk-toggle", payload: { entries: DedupModelEntry[]; mode: "add" | "remove" }): void;
}>();

const { t } = useI18n();

function entryKey(e: DedupModelEntry): string {
  return `${e.group_id}::${e.real_model}`;
}

// Persist fold state across sessions — family list is stable day-to-day,
// re-collapsing every navigation is more friction than memory cost.
const FOLD_STORAGE_KEY = "aliases.quick.foldState.v1";
function loadStoredFold(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(FOLD_STORAGE_KEY);
    if (raw) return JSON.parse(raw) as Record<string, boolean>;
  } catch {
    /* ignore — fall back to defaults */
  }
  return {};
}

const expanded = ref<Record<string, boolean>>(loadStoredFold());
watch(
  expanded,
  v => {
    try {
      localStorage.setItem(FOLD_STORAGE_KEY, JSON.stringify(v));
    } catch {
      /* quota exceeded / private mode — non-fatal */
    }
  },
  { deep: true },
);

function toggleFamily(fam: string) {
  expanded.value[fam] = !expanded.value[fam];
}

// Initial / reload defaults: only fill in unknown families. Preserve any
// state the user has already toggled (or restored from localStorage), so
// submit→reload doesn't reset folds. Default-expand any family with multiple
// candidate models — single-model families are dedup no-ops, keep them folded.
watch(
  () => props.families,
  fams => {
    const next: Record<string, boolean> = { ...expanded.value };
    for (const f of fams) {
      if (next[f.family] === undefined) {
        next[f.family] = f.models.length > 1;
      }
    }
    expanded.value = next;
  },
  { immediate: true },
);

// Bulk select: "全选" adds every model in the family to the selection;
// "清空" removes every model in the family. Does NOT skip already-selected
// rows in add mode (idempotent at the parent) — keeps semantics obvious.
function bulkAdd(f: DedupFamily, e: Event) {
  e.stopPropagation();
  emit("bulk-toggle", { entries: f.models, mode: "add" });
}
function bulkClear(f: DedupFamily, e: Event) {
  e.stopPropagation();
  emit("bulk-toggle", { entries: f.models, mode: "remove" });
}
function familyHasAnySelected(f: DedupFamily): boolean {
  for (const m of f.models) {
    if (props.selected.has(entryKey(m))) return true;
  }
  return false;
}
function familyFullySelected(f: DedupFamily): boolean {
  for (const m of f.models) {
    if (!props.selected.has(entryKey(m))) return false;
  }
  return f.models.length > 0;
}

// Search-hit auto-expand lives in its own watcher so visibleFamilies stays pure.
watch(
  () => props.searchQuery,
  q => {
    const query = q.trim().toLowerCase();
    if (!query) return;
    for (const f of props.families) {
      const famHit = f.family.toLowerCase().includes(query);
      const modelHit = f.models.some(m => m.real_model.toLowerCase().includes(query));
      if (famHit || modelHit) {
        expanded.value[f.family] = true;
      }
    }
  },
);

// Search-driven filter: NO side effects.
const visibleFamilies = computed(() => {
  const q = props.searchQuery.trim().toLowerCase();
  if (!q) return props.families;
  const out: DedupFamily[] = [];
  for (const f of props.families) {
    const famHit = f.family.toLowerCase().includes(q);
    const modelHits = f.models.filter(m => m.real_model.toLowerCase().includes(q));
    if (famHit) {
      out.push(f);
    } else if (modelHits.length > 0) {
      out.push({ ...f, models: modelHits });
    }
  }
  return out;
});

function familyLabel(fam: string): string {
  return fam || t("aliases.quick.otherFamily");
}
</script>

<template>
  <div class="qfam">
    <div v-for="f in visibleFamilies" :key="f.family || '__empty__'" class="qfam__group">
      <button
        type="button"
        class="qfam__head"
        :aria-expanded="!!expanded[f.family]"
        @click="toggleFamily(f.family)"
      >
        <NIcon
          :component="expanded[f.family] ? ChevronDownOutline : ChevronForwardOutline"
          :size="13"
        />
        <span class="qfam__name">{{ familyLabel(f.family) }}</span>
        <span class="qfam__meta">
          {{ t("aliases.quick.familyMeta", { n: f.models.length, g: f.group_count }) }}
        </span>
        <span class="qfam__bulk">
          <button
            type="button"
            class="qfam__bulk-btn"
            :disabled="familyFullySelected(f)"
            :title="t('aliases.quick.bulkSelectAll')"
            @click="bulkAdd(f, $event)"
          >
            {{ t("aliases.quick.bulkSelectAll") }}
          </button>
          <button
            type="button"
            class="qfam__bulk-btn qfam__bulk-btn--ghost"
            :disabled="!familyHasAnySelected(f)"
            :title="t('aliases.quick.bulkClear')"
            @click="bulkClear(f, $event)"
          >
            {{ t("aliases.quick.bulkClear") }}
          </button>
        </span>
      </button>
      <div v-if="expanded[f.family]" class="qfam__rows">
        <ModelEntryRow
          v-for="m in f.models"
          :key="entryKey(m)"
          :entry="m"
          :selected="selected.has(entryKey(m))"
          :highlight-alias="highlightAlias"
          :search-query="searchQuery"
          @toggle="(e) => emit('toggle', e)"
          @click-alias="(a) => emit('click-alias', a)"
        />
      </div>
    </div>
    <div v-if="!visibleFamilies.length" class="qfam__empty">
      {{ t("modelcatalog.noData") }}
    </div>
  </div>
</template>

<style scoped>
.qfam {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.qfam__group {
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  overflow: hidden;
  background: var(--v3-surface);
}
.qfam__head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 10px 14px;
  background: var(--v3-surface-2);
  border: 0;
  cursor: pointer;
  font: 600 12px var(--v3-sans);
  color: var(--v3-ink);
}
.qfam__head:hover {
  background: var(--v3-surface-3, var(--v3-surface-2));
}
.qfam__name {
  font: 700 13px var(--v3-mono);
  color: var(--v3-ink);
}
.qfam__meta {
  margin-left: auto;
  font: 500 11px var(--v3-mono);
  color: var(--v3-ink-3);
}
.qfam__bulk {
  display: inline-flex;
  gap: 4px;
  margin-left: 8px;
}
.qfam__bulk-btn {
  font: 600 10.5px var(--v3-sans);
  padding: 2px 8px;
  border-radius: 3px;
  border: 1px solid var(--v3-line);
  background: var(--v3-surface);
  color: var(--v3-ink-2);
  cursor: pointer;
  transition: background 120ms, border-color 120ms, color 120ms;
}
.qfam__bulk-btn:hover:not(:disabled) {
  border-color: var(--v3-accent);
  color: var(--v3-accent);
  background: var(--v3-accent-soft);
}
.qfam__bulk-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.qfam__bulk-btn--ghost {
  color: var(--v3-ink-3);
}
.qfam__rows {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
}
.qfam__empty {
  padding: 40px;
  text-align: center;
  color: var(--v3-ink-4);
  font-size: 12px;
}
</style>
