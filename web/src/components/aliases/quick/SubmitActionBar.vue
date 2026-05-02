<script setup lang="ts">
import { computed, ref } from "vue";
import { NInput, NButton, NIcon } from "naive-ui";
import { CheckmarkCircle } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";
import type { DedupModelEntry } from "@/api/dedup";
import { deriveFamilyClient } from "@/api/dedup";

const props = defineProps<{
  selected: Map<string, DedupModelEntry>;
  targetAlias: string | null;
  submitting: boolean;
}>();

const emit = defineEmits<{
  (e: "submit", payload: { alias: string; entries: DedupModelEntry[] }): void;
}>();

const { t } = useI18n();

// Cached user-typed name; preserved across target-alias toggles per spec §6.2.
const typedName = ref("");

const familyHint = computed(() => {
  if (!props.selected.size) return "";
  const counts = new Map<string, number>();
  let firstModel = "";
  for (const e of props.selected.values()) {
    if (!firstModel) firstModel = e.real_model;
    const fam = deriveFamilyClient(e.real_model);
    if (fam) counts.set(fam, (counts.get(fam) ?? 0) + 1);
  }
  if (!counts.size) return firstModel;
  const sorted = Array.from(counts.entries()).sort((a, b) => {
    if (b[1] !== a[1]) return b[1] - a[1];
    return a[0].localeCompare(b[0]);
  });
  return sorted[0][0];
});

const effectiveName = computed(() => {
  if (props.targetAlias) return props.targetAlias;
  return typedName.value.trim() || familyHint.value;
});

const buttonLabel = computed(() => {
  if (props.targetAlias) {
    return t("aliases.quick.appendButton", { alias: props.targetAlias });
  }
  return t("aliases.quick.createButton", { family: familyHint.value || "—" });
});

const canSubmit = computed(
  () => props.selected.size > 0 && effectiveName.value.length > 0 && !props.submitting,
);

function onSubmit() {
  if (!canSubmit.value) return;
  emit("submit", {
    alias: effectiveName.value,
    entries: Array.from(props.selected.values()),
  });
}
</script>

<template>
  <div class="qbar">
    <div class="qbar__count">
      {{ selected.size === 0
        ? t("aliases.quick.selectionEmpty")
        : t("aliases.quick.selectionCount", { n: selected.size }) }}
    </div>
    <div class="qbar__input">
      <NInput
        v-if="!targetAlias"
        v-model:value="typedName"
        :placeholder="familyHint || t('aliases.quick.nameRequired')"
        size="medium"
      />
      <code v-else class="qbar__locked">{{ targetAlias }}</code>
    </div>
    <NButton
      type="primary"
      :disabled="!canSubmit"
      :loading="submitting"
      @click="onSubmit"
    >
      <template #icon><NIcon :component="CheckmarkCircle" /></template>
      {{ buttonLabel }}
    </NButton>
  </div>
</template>

<style scoped>
.qbar {
  position: sticky;
  bottom: 0;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  background: var(--v3-surface-2);
  box-shadow: var(--v3-shadow-md);
}
.qbar__count {
  font: 500 11.5px var(--v3-mono);
  color: var(--v3-ink-2);
  white-space: nowrap;
}
.qbar__input {
  flex: 1;
}
.qbar__locked {
  font: 600 13px var(--v3-mono);
  padding: 6px 10px;
  background: var(--v3-accent-soft);
  color: var(--v3-accent);
  border-radius: 4px;
  display: inline-block;
}
</style>
