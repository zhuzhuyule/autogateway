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
  NIcon,
  NInput,
  NSelect,
  NSwitch,
  useMessage,
  type DataTableColumns,
  type SelectOption,
} from "naive-ui";
import { Refresh } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";
import { findFreeModel, FREE_PROVIDERS, type ModelTier } from "@/data/freeProviders";

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

// 筛选条件
const searchText = ref("");
const tierFilter = ref<ModelTier | "all">("all");
const providerFilter = ref<string | "all">("all");
const freeOnly = ref(false);

const tierOptions: SelectOption[] = [
  { label: t("modelcatalog.allTiers"), value: "all" },
  { label: t("modelcatalog.tierFast"), value: "fast" },
  { label: t("modelcatalog.tierBalanced"), value: "balanced" },
  { label: t("modelcatalog.tierMax"), value: "max" },
];

const providerOptions = computed<SelectOption[]>(() => [
  { label: t("modelcatalog.allProviders"), value: "all" },
  ...FREE_PROVIDERS.map((p) => ({ label: p.name, value: p.id })),
]);

const authHeader = computed(() => {
  const authKey = localStorage.getItem("authKey");
  return authKey ? `Bearer ${authKey}` : "";
});

// 给每条 catalog 行附加 free/tier/providerId 元数据
interface AugmentedItem extends ModelItem {
  isFree: boolean;
  tier?: ModelTier;
  freeProviderId?: string;
}

const augmented = computed<AugmentedItem[]>(() =>
  catalogData.value.map((row) => {
    const free = findFreeModel(row.id);
    return {
      ...row,
      isFree: !!free,
      tier: free?.tier,
      freeProviderId: free?.providerId,
    };
  }),
);

const filtered = computed<AugmentedItem[]>(() => {
  const q = searchText.value.trim().toLowerCase();
  return augmented.value
    .filter((row) => {
      if (q && !row.id.toLowerCase().includes(q) && !row.display_name.toLowerCase().includes(q)) {
        return false;
      }
      if (freeOnly.value && !row.isFree) return false;
      if (tierFilter.value !== "all" && row.tier !== tierFilter.value) return false;
      if (providerFilter.value !== "all" && row.freeProviderId !== providerFilter.value) return false;
      return true;
    })
    // 免费模型排在前
    .sort((a, b) => {
      if (a.isFree !== b.isFree) return a.isFree ? -1 : 1;
      return a.id.localeCompare(b.id);
    });
});

function tierTagType(tier?: ModelTier): "success" | "warning" | "error" | "default" {
  switch (tier) {
    case "fast": return "success";
    case "balanced": return "warning";
    case "max": return "error";
    default: return "default";
  }
}

const columns: DataTableColumns<AugmentedItem> = [
  {
    title: t("modelcatalog.modelId"),
    key: "id",
    ellipsis: { tooltip: true },
    render: (row) => {
      return h("div", { style: "display:flex;align-items:center;gap:6px;" }, [
        h("span", { style: "font-family:monospace;" }, row.id),
        row.isFree
          ? h(NTag, { size: "tiny", type: "success", round: true, bordered: false }, () => "🆓 " + t("modelcatalog.freeTag"))
          : null,
      ]);
    },
  },
  {
    title: t("modelcatalog.tier"),
    key: "tier",
    width: 110,
    render: (row) => {
      if (!row.tier) return h("span", { style: "color:#999;" }, "—");
      return h(
        NTag,
        { size: "small", type: tierTagType(row.tier), bordered: false },
        () => t(`modelcatalog.tier${row.tier!.charAt(0).toUpperCase() + row.tier!.slice(1)}`),
      );
    },
  },
  {
    title: t("modelcatalog.ownedBy"),
    key: "owned_by",
    width: 120,
  },
  {
    title: t("modelcatalog.groups"),
    key: "groups",
    width: 220,
    render: (row) => {
      if (!row.groups || row.groups.length === 0) {
        return h(NTag, { size: "small", type: "warning" }, () => t("modelcatalog.noGroups"));
      }
      return row.groups.slice(0, 4).map((g) =>
        h(NTag, { size: "small", type: "info", style: "margin-right: 4px; margin-bottom: 2px;" }, () => g)
      );
    },
  },
];

const hasData = computed(() => catalogData.value.length > 0);
const filteredCount = computed(() => filtered.value.length);
const freeCount = computed(() => augmented.value.filter((r) => r.isFree).length);

onMounted(async () => {
  await fetchCatalog();
});

async function fetchCatalog() {
  loading.value = true;
  fetchError.value = false;
  try {
    const response = await fetch("/api/models", {
      headers: {
        "Authorization": authHeader.value,
      },
    });
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
        <n-space :size="8" align="center">
          <span class="catalog-stats">
            {{ t("modelcatalog.statsLine", { total: catalogData.length, filtered: filteredCount, free: freeCount }) }}
          </span>
          <n-button size="small" @click="fetchCatalog" :loading="loading">
            <template #icon>
              <n-icon :component="Refresh" />
            </template>
            {{ t("common.refresh") }}
          </n-button>
        </n-space>
      </template>

      <!-- 筛选栏 -->
      <div class="filter-bar">
        <n-input
          v-model:value="searchText"
          :placeholder="t('modelcatalog.searchPlaceholder')"
          clearable
          style="width: 240px"
        />
        <n-select
          v-model:value="tierFilter"
          :options="tierOptions"
          style="width: 140px"
        />
        <n-select
          v-model:value="providerFilter"
          :options="providerOptions"
          filterable
          style="width: 200px"
        />
        <span class="filter-switch">
          <n-switch v-model:value="freeOnly" size="small" />
          <span style="margin-left: 6px">🆓 {{ t("modelcatalog.freeOnly") }}</span>
        </span>
      </div>

      <n-spin :show="loading">
        <n-data-table
          v-if="hasData"
          :columns="columns"
          :data="filtered"
          :bordered="false"
          striped
          :pagination="{ pageSize: 20 }"
          :row-key="(row: AugmentedItem) => row.id"
        />
        <n-empty v-else-if="!fetchError" :description="t('modelcatalog.noData')" />
      </n-spin>
    </n-card>
  </n-space>
</template>

<style scoped>
.filter-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.filter-switch {
  display: inline-flex;
  align-items: center;
  font-size: 13px;
  color: var(--text-color-2, #666);
}

.catalog-stats {
  font-size: 12px;
  color: var(--text-color-3, #999);
}
</style>
