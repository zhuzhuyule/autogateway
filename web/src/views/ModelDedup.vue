<script setup lang="ts">
import { ref, onMounted, h, computed } from "vue";
import {
  NCard,
  NDataTable,
  NButton,
  NSpace,
  NTag,
  NModal,
  NInput,
  NEmpty,
  NSpin,
  NAlert,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { Refresh, CheckmarkCircle } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

interface DedupSuggestion {
  model_name: string;
  source_groups: string[];
  suggested_aggregate_name: string;
}

const message = useMessage();
const loading = ref(false);
const fetchError = ref(false);
const suggestions = ref<DedupSuggestion[]>([]);
const showConfirmModal = ref(false);
const selectedSuggestion = ref<DedupSuggestion | null>(null);
const aggregateName = ref("");
const submitting = ref(false);

const columns: DataTableColumns<DedupSuggestion> = [
  {
    title: t("dedup.modelName"),
    key: "model_name",
    ellipsis: { tooltip: true },
  },
  {
    title: t("dedup.sourceGroups"),
    key: "source_groups",
    width: 250,
    render: (row) => {
      return row.source_groups.slice(0, 3).map((g) =>
        h(NTag, { size: "small", type: "info", style: "margin-right: 4px; margin-bottom: 2px;" }, () => g)
      );
    },
  },
  {
    title: t("dedup.suggestedName"),
    key: "suggested_aggregate_name",
    ellipsis: { tooltip: true },
  },
  {
    title: t("common.actions"),
    key: "action",
    width: 150,
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

const hasData = computed(() => suggestions.value.length > 0);

onMounted(async () => {
  await fetchSuggestions();
});

async function fetchSuggestions() {
  loading.value = true;
  fetchError.value = false;
  try {
    const response = await fetch("/api/dedup/suggestions");
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const data = await response.json();
    suggestions.value = data;
  } catch (error) {
    fetchError.value = true;
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

function closeModal() {
  showConfirmModal.value = false;
  selectedSuggestion.value = null;
  aggregateName.value = "";
}

async function handleCreateAggregate() {
  if (!selectedSuggestion.value || !aggregateName.value) {
    message.warning(t("dedup.pleaseEnterAggregateName"));
    return;
  }

  submitting.value = true;
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

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }

    message.success(t("common.operationSuccess"));
    closeModal();
    await fetchSuggestions();
  } catch (error) {
    message.error(t("common.requestFailed"));
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <n-space vertical>
    <n-alert v-if="fetchError" type="error" :title="t('common.error')" closable @close="fetchError = false">
      {{ t("dedup.loadFailed") }}
    </n-alert>

    <n-card :title="t('dedup.title')" hoverable>
      <template #header-extra>
        <n-button size="small" @click="fetchSuggestions" :loading="loading">
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
          :data="suggestions"
          :bordered="false"
          striped
          :pagination="{ pageSize: 10 }"
        />
        <n-empty v-else-if="!fetchError" :description="t('dedup.noSuggestions')" />
      </n-spin>
    </n-card>

    <n-modal
      v-model:show="showConfirmModal"
      preset="dialog"
      :title="t('dedup.createAggregateTitle')"
      positive-text=""
      negative-text=""
      @positive-click="handleCreateAggregate"
      @negative-click="closeModal"
      @close="closeModal"
    >
      <n-space vertical size="large">
        <n-alert type="info" :title="selectedSuggestion?.model_name">
          {{ t("dedup.createAggregateConfirm", { model: selectedSuggestion?.model_name }) }}
        </n-alert>

        <n-input
          v-model:value="aggregateName"
          :placeholder="t('dedup.aggregateNamePlaceholder')"
          size="large"
        />

        <n-space justify="end">
          <n-button @click="closeModal">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" :loading="submitting" @click="() => handleCreateAggregate()">
            <template #icon>
              <n-icon :component="CheckmarkCircle" />
            </template>
            {{ t("common.confirm") }}
          </n-button>
        </n-space>
      </n-space>
    </n-modal>
  </n-space>
</template>
