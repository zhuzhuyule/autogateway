<script setup lang="ts">
import V3LogTable from "@/components/v3/V3LogTable.vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
</script>

<template>
  <div>
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.logs") }}</div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("nav.logs") }}
      <span class="v3-viewtitle__meta">live tail</span>
    </h1>
    <v3-log-table />
  </div>
</template>
