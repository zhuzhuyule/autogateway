<script setup lang="ts">
import { RibbonOutline, GiftOutline } from "@vicons/ionicons5";
import { NIcon } from "naive-ui";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  /** 仅图标,不显示文字 */
  compact?: boolean;
  /** 自定义文字,缺省时按 variant 取 i18n */
  label?: string;
  /** 图标尺寸 */
  size?: number;
  /**
   * 免费档位:
   *   full  — 完全免费 (默认)
   *   trial — 体验/试用 (Gitee 等 provider 有此模式)
   */
  variant?: "full" | "trial";
}

const props = withDefaults(defineProps<Props>(), {
  compact: false,
  label: "",
  size: 11,
  variant: "full",
});

const { t } = useI18n();
const text = computed(() => {
  if (props.label) {
    return props.label;
  }
  if (props.variant === "trial") {
    return t("v3.trial") || "trial";
  }
  return t("v3.free") || "free";
});
const iconComp = computed(() => (props.variant === "trial" ? GiftOutline : RibbonOutline));
</script>

<template>
  <span
    class="free-badge"
    :class="[`free-badge--${variant}`, { 'free-badge--compact': compact }]"
    :title="text"
  >
    <n-icon :component="iconComp" :size="size" class="free-badge__icon" />
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
  font: 600 10.5px/1.4 var(--v3-mono, ui-monospace, monospace);
  letter-spacing: 0.02em;
  flex-shrink: 0;
  border: 1px solid transparent;
  transition: border-color 120ms;
}
.free-badge--full {
  background: var(--v3-ok-soft, oklch(0.96 0.05 145));
  color: var(--v3-ok, oklch(0.55 0.16 145));
}
.free-badge--full:hover {
  border-color: var(--v3-ok, oklch(0.55 0.16 145));
}
.free-badge--trial {
  background: var(--v3-warn-soft, oklch(0.95 0.05 80));
  color: var(--v3-warn, oklch(0.55 0.18 70));
}
.free-badge--trial:hover {
  border-color: var(--v3-warn, oklch(0.55 0.18 70));
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
