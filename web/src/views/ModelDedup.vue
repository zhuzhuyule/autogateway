<script setup lang="ts">
import { ref, onMounted, h } from "vue";
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
import { useI18n } from "vue-i18n";

const { t } = useI18n();

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
    title: t("dedup.modelName"),
    key: "model_name",
  },
  {
    title: t("dedup.sourceGroups"),
    key: "source_groups",
    render: (row) => {
      return row.source_groups.map((g) => h(NTag, { size: "small", type: "info", style: "margin-right: 4px;" }, () => g));
    },
  },
  {
    title: t("dedup.suggestedName"),
    key: "suggested_aggregate_name",
  },
  {
    title: t("common.actions"),
    key: "action",
    render: (row) => {
      return h(
        NButton,
        {
          size: "small",
          type: "primary",
          onClick: () => openConfirmModal(row),
        },
        () => t("dedup.createAggregate")
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
    const response = await fetch("/api/dedup/suggestions");
    if (response.ok) {
      const data = await response.json();
      suggestions.value = data;
    } else {
      message.error(t("common.requestFailed"));
    }
  } catch (_error) {
    message.error(t("common.requestFailed"));
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
    const response = await fetch("/api/dedup/create", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        model_name: selectedSuggestion.value.model_name,
        aggregate_name: aggregateName.value,
        source_groups: selectedSuggestion.value.source_groups,
      }),
    });

    if (response.ok) {
      message.success(t("common.operationSuccess"));
      showConfirmModal.value = false;
      await fetchSuggestions();
    } else {
      message.error(t("common.requestFailed"));
    }
  } catch (_error) {
    message.error(t("common.requestFailed"));
  }
}
</script>

<template>
  <n-space vertical>
    <n-card :title="t('dedup.title')" hoverable bordered>
      <template #header-extra>
        <n-button size="small" @click="fetchSuggestions">{{ t("common.refresh") }}</n-button>
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
      :title="t('dedup.createAggregateTitle')"
      positive-text="Create"
      negative-text="Cancel"
      @positive-click="handleCreateAggregate"
    >
      <n-space vertical>
        <p>{{ t('dedup.createAggregateConfirm', { model: selectedSuggestion?.model_name }) }}</p>
        <n-input v-model:value="aggregateName" :placeholder="t('dedup.aggregateNamePlaceholder')" />
      </n-space>
    </n-modal>
  </n-space>
</template>
