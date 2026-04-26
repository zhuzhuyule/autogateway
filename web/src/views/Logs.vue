<script setup lang="ts">
import LogTable from "@/components/logs/LogTable.vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
</script>

<template>
  <div>
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">CONSOLE / LOGS</div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("nav.logs") }}
      <span class="v3-viewtitle__meta">live tail</span>
    </h1>
    <div class="v3-card">
      <div class="v3-card__body">
        <log-table />
      </div>
    </div>
  </div>
</template>
