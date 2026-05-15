<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { DedupFamily } from "@/api/dedup";

const props = defineProps<{
  families: DedupFamily[];
  targetAlias: string | null;
}>();

const emit = defineEmits<{
  (e: "select", alias: string | null): void;
}>();

const { t } = useI18n();

const RESERVED = ["simple", "medium", "complex"];

const aliasList = computed(() => {
  const counts = new Map<string, number>();
  for (const f of props.families) {
    for (const m of f.models) {
      for (const a of m.aliases ?? []) {
        counts.set(a, (counts.get(a) ?? 0) + 1);
      }
    }
  }
  for (const r of RESERVED) {
    if (!counts.has(r)) counts.set(r, 0);
  }
  return Array.from(counts.entries())
    .map(([alias, count]) => ({ alias, count, reserved: RESERVED.includes(alias) }))
    .sort((a, b) => {
      const ai = RESERVED.indexOf(a.alias);
      const bi = RESERVED.indexOf(b.alias);
      if (ai !== -1 && bi !== -1) return ai - bi;
      if (ai !== -1) return -1;
      if (bi !== -1) return 1;
      return a.alias.localeCompare(b.alias);
    });
});

function select(alias: string) {
  emit("select", props.targetAlias === alias ? null : alias);
}
</script>

<template>
  <div class="qpanel">
    <div class="qpanel__head">{{ t("aliases.quick.panelHeader") }}</div>
    <div class="qpanel__list">
      <button
        v-for="row in aliasList"
        :key="row.alias"
        type="button"
        class="qpanel__card"
        :class="{
          'qpanel__card--active': row.alias === targetAlias,
          'qpanel__card--reserved': row.reserved,
        }"
        :title="t('aliases.quick.targetCardHint')"
        @click="select(row.alias)"
      >
        <span class="qpanel__alias">{{ row.alias }}</span>
        <span class="qpanel__meta">{{ row.count }}</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.qpanel {
  display: flex;
  flex-direction: column;
}
.qpanel__head {
  padding: 12px 14px;
  font: 700 11px var(--v3-mono);
  color: var(--v3-ink-3);
  text-transform: uppercase;
  border-bottom: 1px solid var(--v3-line);
  background: var(--v3-surface-2);
}
.qpanel__list {
  display: flex;
  flex-direction: column;
  padding: 8px;
  gap: 4px;
}
.qpanel__card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border: 1px solid var(--v3-line);
  border-radius: 4px;
  background: var(--v3-surface);
  cursor: pointer;
  transition: border-color 120ms, background 120ms;
  font: inherit;
  color: inherit;
  text-align: left;
}
.qpanel__card:hover {
  border-color: var(--v3-accent);
}
.qpanel__card--active {
  border-color: var(--v3-accent);
  background: var(--v3-accent-soft);
}
.qpanel__card--reserved {
  border-color: oklch(from var(--v3-warn) l c h / 0.3);
}
.qpanel__alias {
  font: 600 12.5px var(--v3-mono);
  color: var(--v3-ink);
}
.qpanel__meta {
  font: 500 11px var(--v3-mono);
  color: var(--v3-ink-3);
}
</style>
