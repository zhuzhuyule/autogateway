<script setup lang="ts">
import PeerSyncPanel from "@/components/v3/PeerSyncPanel.vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
</script>

<template>
  <div>
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("nav.sync") }}</div>
    </div>
    <h1 class="v3-viewtitle">{{ t("sync.peerSync") }}</h1>
    <PeerSyncPanel />
  </div>
</template>
