<script setup lang="ts">
// 状态标签 —— 四个视图共用。
//
// 之前每个视图各写一份: `.v3-arow__state`(卡片)、`.atv__state`(表格)、
// `.asv__state` + `.asv__dot`(分栏)、`.ahv__dot`(健康)。四套几乎一样的圆角、
// 颜色、字号, 而且各自调 `t(STATE_LABEL_KEY[...])` —— 一旦某个视图漏改,
// 同一状态在两个视图里就会显示不同的词。
//
// 抽出来之后: 加一个状态只改这一处, 四个视图自动一致。
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { STATE_LABEL_KEY, type CandidateState } from "@/components/aliases/types";

const props = withDefaults(defineProps<{ state: CandidateState; variant?: "pill" | "dot" }>(), {
  variant: "pill",
});

const { t } = useI18n();
const label = computed(() => t(STATE_LABEL_KEY[props.state]));
</script>

<template>
  <span
    v-if="variant === 'dot'"
    class="sp__dot"
    :class="`sp__dot--${state}`"
    :title="label"
    role="img"
    :aria-label="label"
  />
  <span v-else class="sp" :class="`sp--${state}`">
    <span class="sp__dot" :class="`sp__dot--${state}`" />
    {{ label }}
  </span>
</template>

<style scoped>
.sp {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font: 600 9px/1 var(--v3-mono);
  letter-spacing: 0.04em;
  padding: 3px 6px;
  border-radius: 3px;
  white-space: nowrap;
}
.sp__dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--v3-ink-4);
}
.sp__dot--usable {
  background: var(--v3-ok);
}
.sp__dot--unexposed {
  background: var(--v3-warn);
}
.sp__dot--blocked {
  background: var(--v3-danger);
}
.sp__dot--disabled {
  background: var(--v3-ink-4);
}

.sp--usable {
  background: var(--v3-ok-soft);
  color: oklch(0.42 0.13 145);
}
.sp--unexposed {
  background: var(--v3-warn-soft);
  color: oklch(0.45 0.13 65);
}
.sp--blocked {
  background: var(--v3-danger-soft);
  color: oklch(0.45 0.16 25);
}
.sp--disabled {
  background: var(--v3-surface-3);
  color: var(--v3-ink-3);
}
</style>
