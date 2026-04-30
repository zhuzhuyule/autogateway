<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  /** 仅标识,不显示文字 (对于 🆓 来说,compact 模式可能不再需要) */
  compact?: boolean;
  /** 自定义文字,缺省时取 i18n v3.free */
  label?: string;
}

const props = withDefaults(defineProps<Props>(), {
  compact: false,
  label: "",
});

const { t } = useI18n();
const text = computed(() => props.label || t("v3.free") || "🆓");
</script>

<template>
  <span class="free-tag-simple" :title="text">
    {{ text }}
  </span>
</template>

<style scoped>
.free-tag-simple {
  display: inline-block;
  margin-left: 4px;
  font-size: 12px;
  vertical-align: middle;
  cursor: default;
  filter: grayscale(0.2);
  opacity: 0.9;
}
</style>
