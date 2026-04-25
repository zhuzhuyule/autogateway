<script setup lang="ts">
import { ref, onMounted } from "vue";
import {
  NCard,
  NDataTable,
  NButton,
  NSpace,
  NTag,
  NModal,
  NInput,
  useMessage,
  type DataTableColumns,
} from "naive-ui";

interface DedupSuggestion {
  model_name: string;
  source_groups: string[];
  suggested_aggregate_name: string;
}

const message = useMessage();
const loading = ref(false);
const suggestions = ref<DedupSuggestion[]>([]);
const showConfirmModal = ref(false);
const selectedSuggestion = ref<DedupSuggestion | null>(null);
const aggregateName = ref("");

const columns: DataTableColumns<DedupSuggestion> = [
  {
    title: "Model Name",
    key: "model_name",
  },
  {
    title: "Source Groups",
    key: "source_groups",
    render: (row) => {
      return row.source_groups.map((g) => h(NTag, { size: "small", type: "info" }, () => g));
    },
  },
  {
    title: "Suggested Aggregate Name",
    key: "suggested_aggregate_name",
  },
  {
    title: "Action",
    key: "action",
    render: (row) => {
      return h(
        NButton,
        {
          size: "small",
          type: "primary",
          onClick: () => openConfirmModal(row),
        },
        () => "Create Aggregate"
      );
    },
  },
];

onMounted(async () => {
  await fetchSuggestions();
});

async function fetchSuggestions() {
  loading.value = true;
  try {
    const response = await fetch("/api/model-dedup/suggestions");
    if (response.ok) {
      const data = await response.json();
      suggestions.value = data;
    } else {
      message.error("Failed to load dedup suggestions");
    }
  } catch (_error) {
    message.error("Failed to load dedup suggestions");
  } finally {
    loading.value = false;
  }
}

function openConfirmModal(suggestion: DedupSuggestion) {
  selectedSuggestion.value = suggestion;
  aggregateName.value = suggestion.suggested_aggregate_name;
  showConfirmModal.value = true;
}

async function handleCreateAggregate() {
  if (!selectedSuggestion.value) return;

  try {
    const response = await fetch("/api/model-dedup/create-aggregate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        model_name: selectedSuggestion.value.model_name,
        aggregate_name: aggregateName.value,
        source_groups: selectedSuggestion.value.source_groups,
      }),
    });

    if (response.ok) {
      message.success("Aggregate created successfully");
      showConfirmModal.value = false;
      await fetchSuggestions();
    } else {
      message.error("Failed to create aggregate");
    }
  } catch (_error) {
    message.error("Failed to create aggregate");
  }
}

import { h } from "vue";
</script>

<template>
  <n-space vertical>
    <n-card title="Model Deduplication" hoverable bordered>
      <template #header-extra>
        <n-button size="small" @click="fetchSuggestions">Refresh</n-button>
      </template>

      <n-data-table
        :columns="columns"
        :data="suggestions"
        :loading="loading"
        :bordered="false"
        striped
      />
    </n-card>

    <n-modal
      v-model:show="showConfirmModal"
      preset="dialog"
      title="Create Aggregate Model"
      positive-text="Create"
      negative-text="Cancel"
      @positive-click="handleCreateAggregate"
    >
      <n-space vertical>
        <p>Create aggregate model for "{{ selectedSuggestion?.model_name }}"?</p>
        <n-input v-model:value="aggregateName" placeholder="Aggregate name" />
      </n-space>
    </n-modal>
  </n-space>
</template>
