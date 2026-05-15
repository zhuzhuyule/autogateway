<script setup lang="ts">
import { computed } from "vue";
import { NCheckbox } from "naive-ui";
import { useI18n } from "vue-i18n";
import type { DedupModelEntry } from "@/api/dedup";

const props = defineProps<{
  entry: DedupModelEntry;
  selected: boolean;
  highlightAlias: string | null;
  searchQuery: string;
}>();

const emit = defineEmits<{
  (e: "toggle", entry: DedupModelEntry): void;
  (e: "click-alias", alias: string): void;
}>();

const { t } = useI18n();

const isHighlighted = computed(
  () => !!props.highlightAlias && (props.entry.aliases ?? []).includes(props.highlightAlias),
);

const nameSegments = computed(() => {
  const q = props.searchQuery.trim().toLowerCase();
  const name = props.entry.real_model;
  if (!q) return [{ text: name, hit: false }];
  const lower = name.toLowerCase();
  const segs: { text: string; hit: boolean }[] = [];
  let i = 0;
  while (i < name.length) {
    const idx = lower.indexOf(q, i);
    if (idx === -1) {
      segs.push({ text: name.slice(i), hit: false });
      break;
    }
    if (idx > i) segs.push({ text: name.slice(i, idx), hit: false });
    segs.push({ text: name.slice(idx, idx + q.length), hit: true });
    i = idx + q.length;
  }
  return segs;
});
</script>

<template>
  <label class="quick-row" :class="{ 'quick-row--hl': isHighlighted, 'quick-row--sel': selected }">
    <NCheckbox
      :checked="selected"
      @update:checked="emit('toggle', entry)"
    />
    <span class="quick-row__group">{{ entry.group_name }}</span>
    <span class="quick-row__sep">→</span>
    <code class="quick-row__model">
      <template v-for="(seg, i) in nameSegments" :key="i">
        <mark v-if="seg.hit" class="quick-row__hit">{{ seg.text }}</mark>
        <template v-else>{{ seg.text }}</template>
      </template>
    </code>
    <span class="quick-row__chips">
      <button
        v-for="a in entry.aliases ?? []"
        :key="a"
        type="button"
        class="quick-row__alias-chip"
        :title="t('aliases.quick.aliasChipPrefix') + ' ' + a"
        @click.prevent="emit('click-alias', a)"
      >
        {{ a }}
      </button>
    </span>
  </label>
</template>

<style scoped>
.quick-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  border: 1px solid var(--v3-line);
  border-radius: 4px;
  background: var(--v3-surface-2);
  cursor: pointer;
  transition: border-color 120ms, background 120ms;
}
.quick-row:hover {
  border-color: var(--v3-accent);
}
.quick-row--sel {
  border-color: var(--v3-accent);
  background: var(--v3-accent-soft);
}
.quick-row--hl {
  border-color: var(--v3-accent);
  box-shadow: 0 0 0 1px var(--v3-accent-soft);
}
.quick-row__group {
  font: 600 12px var(--v3-sans);
  color: var(--v3-ink);
  min-width: 120px;
}
.quick-row__sep {
  color: var(--v3-ink-4);
  font-size: 11px;
}
.quick-row__model {
  font: 500 12px var(--v3-mono);
  color: var(--v3-ink-2);
  background: var(--v3-surface);
  padding: 2px 6px;
  border-radius: 3px;
  flex: 1;
  word-break: break-all;
}
.quick-row__hit {
  background: var(--v3-warn-soft, oklch(0.95 0.05 80));
  color: var(--v3-warn);
  padding: 0 1px;
  border-radius: 2px;
}
.quick-row__chips {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.quick-row__alias-chip {
  font: 600 10px var(--v3-mono);
  padding: 2px 6px;
  border-radius: 999px;
  border: 1px solid var(--v3-accent);
  color: var(--v3-accent);
  background: transparent;
  cursor: pointer;
}
.quick-row__alias-chip:hover {
  background: var(--v3-accent-soft);
}
</style>
