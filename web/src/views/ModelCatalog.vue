<script setup lang="ts">
import { ref, onMounted } from "vue";
import {
  NCard,
  NDataTable,
  NButton,
  NSpace,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";

interface ModelCatalogItem {
  model_name: string;
  display_name: string;
  group_name: string;
  is_aggregate: boolean;
}

const message = useMessage();
const loading = ref(false);
const catalogData = ref<ModelCatalogItem[]>([]);

const columns: DataTableColumns<ModelCatalogItem> = [
  {
    title: "Model Name",
    key: "model_name",
  },
  {
    title: "Display Name",
    key: "display_name",
  },
  {
    title: "Group",
    key: "group_name",
  },
  {
    title: "Type",
    key: "is_aggregate",
    render: (row) => {
      return row.is_aggregate
        ? h(NTag, { type: "success", size: "small" }, () => "Aggregate")
        : h(NTag, { type: "info", size: "small" }, () => "Single");
    },
  },
];

onMounted(async () => {
  await fetchCatalog();
});

async function fetchCatalog() {
  loading.value = true;
  try {
    const response = await fetch("/api/model-catalog/list");
    if (response.ok) {
      const data = await response.json();
      catalogData.value = data;
    } else {
      message.error("Failed to load model catalog");
    }
  } catch (_error) {
    message.error("Failed to load model catalog");
  } finally {
    loading.value = false;
  }
}

import { h } from "vue";
</script>

<template>
  <n-space vertical>
    <n-card title="Model Catalog" hoverable bordered>
      <template #header-extra>
        <n-button size="small" @click="fetchCatalog">Refresh</n-button>
      </template>

      <n-data-table
        :columns="columns"
        :data="catalogData"
        :loading="loading"
        :bordered="false"
        striped
      />
    </n-card>
  </n-space>
</template>
