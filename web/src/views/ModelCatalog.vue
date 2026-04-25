<script setup lang="ts">
import { ref, onMounted, h } from "vue";
import {
  NCard,
  NDataTable,
  NButton,
  NSpace,
  NTag,
  useMessage,
  type DataTableColumns,
} from "naive-ui";

interface ModelItem {
  id: string;
  display_name: string;
  owned_by: string;
  groups: string[];
}

const message = useMessage();
const loading = ref(false);
const catalogData = ref<ModelItem[]>([]);

const columns: DataTableColumns<ModelItem> = [
  {
    title: "Model ID",
    key: "id",
  },
  {
    title: "Display Name",
    key: "display_name",
  },
  {
    title: "Owned By",
    key: "owned_by",
  },
  {
    title: "Groups",
    key: "groups",
    render: (row) => {
      if (!row.groups || row.groups.length === 0) {
        return h(NTag, { size: "small", type: "warning" }, () => "No groups");
      }
      return row.groups.map((g) => h(NTag, { size: "small", type: "info", style: "margin-right: 4px;" }, () => g));
    },
  },
];

onMounted(async () => {
  await fetchCatalog();
});

async function fetchCatalog() {
  loading.value = true;
  try {
    const response = await fetch("/api/models");
    if (response.ok) {
      const result = await response.json();
      if (result.data) {
        catalogData.value = result.data;
      }
    } else {
      message.error("Failed to load model catalog");
    }
  } catch (_error) {
    message.error("Failed to load model catalog");
  } finally {
    loading.value = false;
  }
}
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
