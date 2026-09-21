<script setup lang="ts">
// Probe-shape chip: shows which request body a per-model connectivity test
// will send, and lets the operator pin one shape for this model. Auto mode
// resolves from registry capabilities (see probeModalityOf).
import type { ProbeModality } from "@/data/freeProviders";
import { ChevronDownOutline } from "@vicons/ionicons5";
import { NDropdown, NIcon, NTooltip } from "naive-ui";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  probe: { modality: ProbeModality; manual: boolean };
}
const props = defineProps<Props>();

const emit = defineEmits<{ (e: "select", modality: ProbeModality | ""): void }>();

const { t } = useI18n();

const SHORT_KEYS: Record<ProbeModality, string> = {
  chat: "v3.probeShortChat",
  image: "v3.probeShortImage",
  vision: "v3.probeShortVision",
  tts: "v3.probeShortTts",
  asr: "v3.probeShortAsr",
};

const options = computed(() => [
  { label: t("v3.testModalityAuto"), key: "" },
  { label: t("v3.testModalityChat"), key: "chat" },
  { label: t("v3.testModalityImage"), key: "image" },
  { label: t("v3.testModalityVision"), key: "vision" },
  { label: t("v3.testModalityTts"), key: "tts" },
  { label: t("v3.testModalityAsr"), key: "asr" },
]);

function shortFor(modality: ProbeModality): string {
  return t(SHORT_KEYS[modality]);
}
</script>

<template>
  <n-tooltip trigger="hover" placement="top">
    <template #trigger>
      <n-dropdown
        trigger="click"
        size="small"
        :options="options"
        @select="key => emit('select', key as ProbeModality | '')"
      >
        <button
          type="button"
          class="v5-probechip"
          :class="{ 'v5-probechip--manual': props.probe.manual }"
          @click.stop
        >
          <span>{{ shortFor(props.probe.modality) }}</span>
          <n-icon :component="ChevronDownOutline" :size="9" />
        </button>
      </n-dropdown>
    </template>
    {{
      props.probe.manual
        ? t("v3.probeChipManual", { mode: shortFor(props.probe.modality) })
        : t("v3.probeChipAuto", { mode: shortFor(props.probe.modality) })
    }}
  </n-tooltip>
</template>

<style scoped>
.v5-probechip {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  height: 22px;
  padding: 0 5px;
  border-radius: 6px;
  border: 1px dashed var(--v3-line);
  background: transparent;
  color: var(--v3-ink-3);
  font: 600 10px/1 var(--v3-mono);
  cursor: pointer;
  transition: all 120ms;
}
.v5-probechip:hover {
  border-color: var(--v3-accent);
  border-style: solid;
  color: var(--v3-accent);
}
.v5-probechip--manual {
  border-style: solid;
  border-color: oklch(from var(--v3-accent) l c h / 0.5);
  color: var(--v3-accent);
  background: var(--v3-accent-soft, oklch(from var(--v3-accent) 96% 0.05 h));
}
</style>
