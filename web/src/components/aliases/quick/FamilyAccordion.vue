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
}>();

const { t } = useI18n();

function entryKey(e: DedupModelEntry): string {
  return `${e.group_id}::${e.real_model}`;
}

const expanded = ref<Record<string, boolean>>({});
function toggleFamily(fam: string) {
  expanded.value[fam] = !expanded.value[fam];
}

// Initial / reload defaults: only fill in unknown families. Preserve any
// state the user has already toggled, so submit→reload doesn't reset folds.
watch(
  () => props.families,
  fams => {
    const next: Record<string, boolean> = { ...expanded.value };
    for (const f of fams) {
      if (next[f.family] === undefined) {
        next[f.family] = f.group_count > 1;
      }
    }
    expanded.value = next;
  },
  { immediate: true },
);

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
