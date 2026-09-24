<script setup lang="ts">
// 别名页只有一个视图。
//
// 之前是 manage / quick 两个并列 tab —— 但它们是**同一个对象的两套心智模型**:
// 都建别名, 却走两套不同的 API, 用户得先判断"我该去哪个 tab"。现在 quick 的
// 家族整理收进主视图的"按家族整理"弹窗, 页面只剩一条路径。
//
// 兼容老链接: ?tab=quick 仍然有效 —— 落到主视图并直接打开家族弹窗。
import { computed } from "vue";
import { useRoute } from "vue-router";
import AliasManageTab from "@/components/aliases/AliasManageTab.vue";

const route = useRoute();
const openFamily = computed(() => route.query.tab === "quick");
</script>

<template>
  <AliasManageTab :initial-family-open="openFamily" />
</template>
