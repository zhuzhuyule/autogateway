<script setup lang="ts">
import { ref, onMounted, h, computed } from "vue";
import {
  NCard,
  NDataTable,
  NButton,
  NSpace,
  NTag,
  NEmpty,
  NSpin,
  NAlert,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { Refresh } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

interface ModelItem {
  id: string;
  display_name: string;
  owned_by: string;
  groups: string[];
}

const message = useMessage();
const loading = ref(false);
const fetchError = ref(false);
const catalogData = ref<ModelItem[]>([]);

const columns: DataTableColumns<ModelItem> = [
  {
    title: t("modelcatalog.modelId"),
    key: "id",
    ellipsis: { tooltip: true },
  },
  {
    title: t("modelcatalog.displayName"),
    key: "display_name",
    ellipsis: { tooltip: true },
  },
  {
    title: t("modelcatalog.ownedBy"),
    key: "owned_by",
    width: 120,
  },
  {
    title: t("modelcatalog.groups"),
    key: "groups",
    width: 200,
    render: (row) => {
      if (!row.groups || row.groups.length === 0) {
        return h(NTag, { size: "small", type: "warning" }, () => t("modelcatalog.noGroups"));
      }
      return row.groups.slice(0, 3).map((g) =>
        h(NTag, { size: "small", type: "info", style: "margin-right: 4px; margin-bottom: 2px;" }, () => g)
      );
    },
  },
];

const hasData = computed(() => catalogData.value.length > 0);

onMounted(async () => {
  await fetchCatalog();
});

async function fetchCatalog() {
  loading.value = true;
  fetchError.value = false;
  try {
    const response = await fetch("/api/models");
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const result = await response.json();
    if (result.data) {
      catalogData.value = result.data;
    }
  } catch (error) {
    fetchError.value = true;
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <n-space vertical>
    <n-alert v-if="fetchError" type="error" :title="t('common.error')" closable @close="fetchError = false">
      {{ t("modelcatalog.loadFailed") }}
    </n-alert>

    <n-card :title="t('modelcatalog.title')" hoverable>
      <template #header-extra>
        <n-button size="small" @click="fetchCatalog" :loading="loading">
          <template #icon>
            <n-icon :component="Refresh" />
          </template>
          {{ t("common.refresh") }}
        </n-button>
      </template>

      <n-spin :show="loading">
        <n-data-table
          v-if="hasData"
          :columns="columns"
          :data="catalogData"
          :bordered="false"
          striped
          :pagination="{ pageSize: 15 }"
        />
        <n-empty v-else-if="!fetchError" :description="t('modelcatalog.noData')" />
      </n-spin>
    </n-card>
  </n-space>
</template>
