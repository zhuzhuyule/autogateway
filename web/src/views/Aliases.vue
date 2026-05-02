<script setup lang="ts">
import { computed } from "vue";
import { NTabs, NTabPane } from "naive-ui";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import AliasManageTab from "@/components/aliases/AliasManageTab.vue";
import AliasQuickSetupTab from "@/components/aliases/AliasQuickSetupTab.vue";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();

const VALID_TABS = ["manage", "quick"] as const;
type TabKey = (typeof VALID_TABS)[number];

const activeTab = computed<TabKey>({
  get() {
    const raw = (route.query.tab as string) || "manage";
    return (VALID_TABS as readonly string[]).includes(raw) ? (raw as TabKey) : "manage";
  },
  set(val) {
    router.replace({ query: { ...route.query, tab: val } });
  },
});
</script>

<template>
  <NTabs v-model:value="activeTab" type="line" animated>
    <NTabPane name="manage" :tab="t('aliases.tabManage')">
      <AliasManageTab />
    </NTabPane>
    <NTabPane name="quick" :tab="t('aliases.tabQuick')">
      <AliasQuickSetupTab />
    </NTabPane>
  </NTabs>
</template>
