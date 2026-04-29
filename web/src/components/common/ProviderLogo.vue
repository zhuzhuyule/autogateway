<script setup lang="ts">
import { computed } from "vue";
import { resolveProviderLogo } from "@/data/providerLogos";

interface Props {
  /** 解析参考字符串,通常是 system_role / group_name / upstream host */
  hint: string;
  /** 渲染尺寸,默认继承父容器 */
  size?: number | string;
}

const props = withDefaults(defineProps<Props>(), {
  size: "1em",
});

const sizeStyle = computed(() => {
  const v = typeof props.size === "number" ? `${props.size}px` : props.size;
  return { width: v, height: v };
});

const svgHtml = computed(() => resolveProviderLogo(props.hint) || "");
</script>

<template>
  <span v-if="svgHtml" class="provider-logo" :style="sizeStyle" v-html="svgHtml" />
</template>

<style scoped>
.provider-logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  line-height: 0;
}
.provider-logo :deep(svg) {
  width: 100%;
  height: 100%;
}
</style>
