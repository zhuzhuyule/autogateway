<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(
  defineProps<{
    data: number[];
    color?: string;
    height?: number;
  }>(),
  {
    color: "currentColor",
    height: 22,
  }
);

const points = computed(() => {
  const data = props.data;
  if (!data || data.length < 2) {
    return "";
  }
  const w = 80;
  const h = props.height;
  const max = Math.max(...data);
  const min = Math.min(...data);
  const range = max - min || 1;
  return data
    .map((v, i) => {
      const x = (i / (data.length - 1)) * w;
      const y = h - ((v - min) / range) * h;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
});
</script>

<template>
  <svg
    width="100%"
    :height="height"
    :viewBox="`0 0 80 ${height}`"
    preserveAspectRatio="none"
    style="overflow: visible"
  >
    <polyline fill="none" :stroke="color" stroke-width="1.5" :points="points" />
  </svg>
</template>
