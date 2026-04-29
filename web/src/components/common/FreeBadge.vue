<script setup lang="ts">
import { RibbonOutline } from "@vicons/ionicons5";
import { NIcon } from "naive-ui";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  /** 仅图标,不显示文字 */
  compact?: boolean;
  /** 自定义文字,缺省时取 i18n v3.free */
  label?: string;
  /** 图标尺寸 */
  size?: number;
}

const props = withDefaults(defineProps<Props>(), {
  compact: false,
  label: "",
  size: 11,
});

const { t } = useI18n();
const text = computed(() => props.label || t("v3.free") || "free");
</script>

<template>
  <span class="free-badge" :class="{ 'free-badge--compact': compact }" :title="text">
    <n-icon :component="RibbonOutline" :size="size" class="free-badge__icon" />
    <span v-if="!compact" class="free-badge__text">{{ text }}</span>
  </span>
</template>

<style scoped>
.free-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--v3-ok-soft, oklch(0.96 0.05 145));
  color: var(--v3-ok, oklch(0.55 0.16 145));
  font: 600 10.5px/1.4 var(--v3-mono, ui-monospace, monospace);
  letter-spacing: 0.02em;
  flex-shrink: 0;
  border: 1px solid transparent;
  transition: border-color 120ms;
}
.free-badge:hover {
  border-color: var(--v3-ok, oklch(0.55 0.16 145));
}
.free-badge--compact {
  padding: 1px 3px;
  gap: 0;
}
.free-badge__icon {
  display: inline-flex;
}
.free-badge__text {
  text-transform: lowercase;
}
</style>
