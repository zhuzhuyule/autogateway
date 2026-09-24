<script setup lang="ts">
// 指标格 —— 数值在上、小标签在下。分栏视图和编辑抽屉共用。
//
// 与 StatePill 同理: 之前 `.asv__metric` 在分栏视图里写了一份, 编辑抽屉里的
// 占比/实际占比又是另一套行内写法。抽出来保证"配置占比"这类指标在所有地方
// 长得一样。
defineProps<{ value: string; label: string }>();
</script>

<template>
  <span class="mc">
    <span class="mc__v">{{ value }}</span>
    <span class="mc__k">{{ label }}</span>
  </span>
</template>

<style scoped>
.mc {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 1px;
  min-width: 44px;
}
.mc__v {
  font: 600 11px var(--v3-mono);
  color: var(--v3-ink-2);
  white-space: nowrap;
}
.mc__k {
  font: 400 8.5px var(--v3-mono);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--v3-ink-4);
  white-space: nowrap;
}
</style>
