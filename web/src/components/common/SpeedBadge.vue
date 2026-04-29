<script setup lang="ts">
// 模型速率档位 — 直接消费 FreeModels Registry 的 speed 字段, 无客户端推断。
// fast / balanced / slow 各一种颜色 + 图标。
import { FlashOutline, SpeedometerOutline, HourglassOutline } from "@vicons/ionicons5";
import { NIcon } from "naive-ui";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  speed: "fast" | "balanced" | "slow";
  compact?: boolean;
  size?: number;
}
const props = withDefaults(defineProps<Props>(), {
  compact: false,
  size: 11,
});

const { t } = useI18n();
const meta = computed(() => {
  if (props.speed === "fast") {
    return { icon: FlashOutline, label: t("v3.fast") || "fast" };
  }
  if (props.speed === "slow") {
    return { icon: HourglassOutline, label: t("v3.slow") || "slow" };
  }
  return { icon: SpeedometerOutline, label: t("v3.balanced") || "balanced" };
});
</script>

<template>
  <span
    class="speed-badge"
    :class="[`speed-badge--${speed}`, { 'speed-badge--compact': compact }]"
    :title="meta.label"
  >
    <n-icon :component="meta.icon" :size="size" class="speed-badge__icon" />
    <span v-if="!compact" class="speed-badge__text">{{ meta.label }}</span>
  </span>
</template>

<style scoped>
.speed-badge {
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
.speed-badge--fast {
  background: var(--v3-ok-soft, oklch(0.96 0.05 145));
  color: var(--v3-ok, oklch(0.55 0.16 145));
}
.speed-badge--balanced {
  background: var(--v3-info-soft, oklch(0.96 0.05 240));
  color: var(--v3-info, oklch(0.55 0.16 240));
}
.speed-badge--slow {
  background: var(--v3-surface-2, oklch(0.96 0 0));
  color: var(--v3-ink-3, oklch(0.55 0 0));
}
.speed-badge--compact {
  padding: 1px 3px;
  gap: 0;
}
.speed-badge__icon {
  display: inline-flex;
}
.speed-badge__text {
  text-transform: lowercase;
}
</style>
