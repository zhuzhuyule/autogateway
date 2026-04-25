<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import {
  NCard,
  NForm,
  NFormItem,
  NSwitch,
  NButton,
  NSpace,
  NInput,
  NInputNumber,
  NDivider,
  NAlert,
  NEmpty,
  NSpin,
  NSelect,
  NTag,
  NDescriptions,
  NDescriptionsItem,
  useMessage,
} from "naive-ui";
import type { SelectOption } from "naive-ui";
import { Save, Refresh } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";
import { getGroupList } from "@/api/dashboard";

const { t } = useI18n();

interface RouteConfig {
  enabled: boolean;
  simple_threshold: number;
  complex_threshold: number;
  group_mapping: Record<string, {
    simple_group: string;
    medium_group: string;
    complex_group: string;
  }>;
}

interface RouteAnalysis {
  level: string;
  estimated_tokens: number;
  has_vision: boolean;
  tool_count: number;
  message_count: number;
  has_tools: boolean;
  max_msg_length: number;
}

interface TestResult {
  target_group: string;
  analysis: RouteAnalysis | null;
  message?: string;
}

const PRESETS: Record<string, { simple: number; complex: number }> = {
  economy: { simple: 500, complex: 2000 },
  balanced: { simple: 2000, complex: 8000 },
  performance: { simple: 4000, complex: 16000 },
};

const DEFAULT_TEST_BODY = JSON.stringify({
  model: "gpt-4o",
  messages: [
    { role: "user", content: "Hello, how are you?" },
  ],
}, null, 2);

const message = useMessage();
const loading = ref(false);
const fetchLoading = ref(false);
const config = ref<RouteConfig>({
  enabled: false,
  simple_threshold: 2000,
  complex_threshold: 8000,
  group_mapping: {},
});
const fetchError = ref(false);

const groupOptions = ref<SelectOption[]>([]);

interface CatalogEntry {
  id: string;
  display_name: string;
  groups: string[];
}
const catalogModels = ref<CatalogEntry[]>([]);
const modelGroupsMap = computed<Record<string, string[]>>(() => {
  const map: Record<string, string[]> = {};
  for (const m of catalogModels.value) {
    map[m.id] = m.groups || [];
  }
  return map;
});

const hasMappings = computed(() => Object.keys(config.value.group_mapping).length > 0);

const newMappingGroup = ref<string | null>(null);
const newMappingSimple = ref<string | null>(null);
const newMappingMedium = ref<string | null>(null);
const newMappingComplex = ref<string | null>(null);

const modelOptions = computed<SelectOption[]>(() =>
  catalogModels.value.map((m) => {
    const used = !!config.value.group_mapping[m.id];
    const suffix = m.groups.length ? ` · ${m.groups.length} 组` : "";
    return {
      label: `${m.id}${suffix}${used ? "  (已映射)" : ""}`,
      value: m.id,
      disabled: used,
    };
  }),
);

const newMappingHintGroups = computed<string[]>(() =>
  newMappingGroup.value ? modelGroupsMap.value[newMappingGroup.value] || [] : [],
);

const testModelKey = ref<string | null>(null);
const testBody = ref(DEFAULT_TEST_BODY);
const testLoading = ref(false);
const testResult = ref<TestResult | null>(null);
const testError = ref<string>("");

const mappingKeyOptions = computed<SelectOption[]>(() =>
  Object.keys(config.value.group_mapping).map((k) => ({ label: k, value: k })),
);

const authHeader = computed(() => {
  const authKey = localStorage.getItem("authKey");
  return authKey ? `Bearer ${authKey}` : "";
});

onMounted(async () => {
  await Promise.all([fetchConfig(), fetchGroups(), fetchCatalog()]);
});

async function fetchCatalog() {
  try {
    const response = await fetch("/api/models", {
      headers: {
        "Authorization": authHeader.value,
      },
    });
    if (!response.ok) return;
    const result = await response.json();
    catalogModels.value = (result.data || []) as CatalogEntry[];
  } catch {
    // 拉取失败不影响其他功能
  }
}

function onPickNewMappingModel(modelId: string | null) {
  if (!modelId) return;
  const groups = modelGroupsMap.value[modelId];
  if (!groups || groups.length === 0) return;
  // 仅当对应槽位为空时填充,避免覆盖用户已选
  const pick = (idx: number) => groups[Math.min(idx, groups.length - 1)];
  if (!newMappingSimple.value) newMappingSimple.value = pick(0);
  if (!newMappingMedium.value) newMappingMedium.value = pick(Math.floor(groups.length / 2));
  if (!newMappingComplex.value) newMappingComplex.value = pick(groups.length - 1);
  if (groups.length > 0) {
    message.info(t("autoroute.autoFilledFromCatalog"));
  }
}

async function fetchConfig() {
  fetchLoading.value = true;
  fetchError.value = false;
  try {
    const response = await fetch("/api/auto-routing/config", {
      headers: {
        "Authorization": authHeader.value,
        "Content-Type": "application/json",
      },
    });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const result = await response.json();
    if (result.success && result.config) {
      config.value = {
        enabled: result.config.enabled ?? false,
        simple_threshold: result.config.simple_threshold ?? 2000,
        complex_threshold: result.config.complex_threshold ?? 8000,
        group_mapping: result.config.group_mapping ?? {},
      };
    }
  } catch {
    fetchError.value = true;
    message.error(t("common.requestFailed"));
  } finally {
    fetchLoading.value = false;
  }
}

interface GroupRow {
  name: string;
  display_name?: string;
  available_models?: unknown;
}
const groupModelMap = ref<Record<string, string[]>>({});

function parseModels(raw: unknown): string[] {
  if (Array.isArray(raw)) return raw.filter((m): m is string => typeof m === "string");
  if (typeof raw === "string" && raw.trim().length > 0) {
    try {
      const arr = JSON.parse(raw);
      return Array.isArray(arr) ? arr.filter((m): m is string => typeof m === "string") : [];
    } catch {
      return [];
    }
  }
  return [];
}

async function fetchGroups() {
  try {
    const response = await getGroupList();
    const list = (response as unknown as { data: GroupRow[] }).data || [];
    const map: Record<string, string[]> = {};
    groupOptions.value = list.map((g) => {
      const models = parseModels(g.available_models);
      map[g.name] = models;
      const display = g.display_name ? `${g.display_name} (${g.name})` : g.name;
      const suffix = models.length > 0
        ? ` · ${models.length} ${t("autoroute.modelsSuffix")}`
        : "";
      return {
        label: display + suffix,
        value: g.name,
      };
    });
    groupModelMap.value = map;
  } catch {
    // 拉取失败时退化为可手填(tag mode)
  }
}

function groupModelHint(groupName: string | null | undefined, max = 3): string {
  if (!groupName) return "";
  const models = groupModelMap.value[groupName] || [];
  if (models.length === 0) return "";
  const sample = models.slice(0, max).join(", ");
  const more = models.length > max ? ` +${models.length - max}` : "";
  return `${sample}${more}`;
}

async function handleSubmit() {
  loading.value = true;
  try {
    const response = await fetch("/api/auto-routing/config", {
      method: "POST",
      headers: {
        "Authorization": authHeader.value,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(config.value),
    });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    message.success(t("common.operationSuccess"));
  } catch {
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
}

function applyPreset(name: keyof typeof PRESETS) {
  const p = PRESETS[name];
  config.value.simple_threshold = p.simple;
  config.value.complex_threshold = p.complex;
  message.success(t("autoroute.presetApplied"));
}

function addMapping() {
  if (!newMappingGroup.value) {
    message.warning(t("autoroute.pleaseEnterModelName"));
    return;
  }
  if (config.value.group_mapping[newMappingGroup.value]) {
    message.warning(t("autoroute.modelAlreadyExists"));
    return;
  }
  config.value.group_mapping[newMappingGroup.value] = {
    simple_group: newMappingSimple.value ?? "",
    medium_group: newMappingMedium.value ?? "",
    complex_group: newMappingComplex.value ?? "",
  };
  clearNewMappingInputs();
}

function removeMapping(key: string) {
  delete config.value.group_mapping[key];
}

function clearNewMappingInputs() {
  newMappingGroup.value = null;
  newMappingSimple.value = null;
  newMappingMedium.value = null;
  newMappingComplex.value = null;
}

async function runTest() {
  testError.value = "";
  testResult.value = null;
  if (!testModelKey.value) {
    testError.value = t("autoroute.pleaseEnterModelName");
    return;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(testBody.value);
  } catch {
    testError.value = t("autoroute.testInvalidJSON");
    return;
  }
  testLoading.value = true;
  try {
    const response = await fetch("/api/auto-routing/test", {
      method: "POST",
      headers: {
        "Authorization": authHeader.value,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        group_name: testModelKey.value,
        request_body: parsed,
      }),
    });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const result = await response.json();
    if (!result.success) {
      throw new Error(result.error || "test failed");
    }
    testResult.value = {
      target_group: result.target_group,
      analysis: result.analysis,
      message: result.message,
    };
  } catch (e) {
    testError.value = (e as Error).message;
  } finally {
    testLoading.value = false;
  }
}

function levelTagType(level?: string) {
  switch (level) {
    case "simple":
      return "success";
    case "complex":
      return "error";
    case "medium":
      return "warning";
    default:
      return "default";
  }
}
</script>

<template>
  <n-space vertical>
    <n-alert v-if="fetchError" type="error" :title="t('common.error')" closable @close="fetchError = false">
      {{ t("autoroute.loadConfigFailed") }}
    </n-alert>

    <n-spin :show="fetchLoading">
      <n-card :title="t('autoroute.title')" hoverable>
        <n-form label-placement="left" label-width="200">
          <n-form-item :label="t('autoroute.enableAutoRouting')">
            <n-switch v-model:value="config.enabled" />
          </n-form-item>

          <n-form-item :label="t('autoroute.presets')">
            <n-space>
              <n-button size="small" @click="applyPreset('economy')">
                {{ t("autoroute.presetEconomy") }} ({{ PRESETS.economy.simple }} / {{ PRESETS.economy.complex }})
              </n-button>
              <n-button size="small" @click="applyPreset('balanced')">
                {{ t("autoroute.presetBalanced") }} ({{ PRESETS.balanced.simple }} / {{ PRESETS.balanced.complex }})
              </n-button>
              <n-button size="small" @click="applyPreset('performance')">
                {{ t("autoroute.presetPerformance") }} ({{ PRESETS.performance.simple }} / {{ PRESETS.performance.complex }})
              </n-button>
            </n-space>
          </n-form-item>

          <n-form-item :label="t('autoroute.simpleThreshold')">
            <n-input-number
              v-model:value="config.simple_threshold"
              :min="0"
              :step="100"
              style="width: 100%"
            />
          </n-form-item>

          <n-form-item :label="t('autoroute.complexThreshold')">
            <n-input-number
              v-model:value="config.complex_threshold"
              :min="0"
              :step="100"
              style="width: 100%"
            />
          </n-form-item>
        </n-form>
      </n-card>
    </n-spin>

    <n-card :title="t('autoroute.groupMappings')" hoverable>
      <template #header-extra>
        <n-button size="small" @click="fetchConfig" :loading="fetchLoading">
          <template #icon>
            <n-icon :component="Refresh" />
          </template>
          {{ t("common.refresh") }}
        </n-button>
      </template>

      <n-form label-placement="left" label-width="150">
        <div v-if="hasMappings">
          <div v-for="(mapping, key) in config.group_mapping" :key="key" class="mapping-item">
            <n-space align="center">
              <n-input :value="String(key)" disabled style="width: 160px;" />
              <n-select
                v-model:value="mapping.simple_group"
                :options="groupOptions"
                :placeholder="t('autoroute.simpleGroup')"
                filterable
                clearable
                tag
                style="width: 170px;"
              />
              <n-select
                v-model:value="mapping.medium_group"
                :options="groupOptions"
                :placeholder="t('autoroute.mediumGroup')"
                filterable
                clearable
                tag
                style="width: 170px;"
              />
              <n-select
                v-model:value="mapping.complex_group"
                :options="groupOptions"
                :placeholder="t('autoroute.complexGroup')"
                filterable
                clearable
                tag
                style="width: 170px;"
              />
              <n-button type="error" size="small" @click="removeMapping(String(key))">
                {{ t("common.delete") }}
              </n-button>
            </n-space>
            <div class="mapping-hints">
              <div v-if="groupModelHint(mapping.simple_group)" class="mapping-hint">
                <n-tag size="tiny" type="success" :bordered="false">simple</n-tag>
                <span class="mapping-hint-text">{{ groupModelHint(mapping.simple_group) }}</span>
              </div>
              <div v-if="groupModelHint(mapping.medium_group)" class="mapping-hint">
                <n-tag size="tiny" type="warning" :bordered="false">medium</n-tag>
                <span class="mapping-hint-text">{{ groupModelHint(mapping.medium_group) }}</span>
              </div>
              <div v-if="groupModelHint(mapping.complex_group)" class="mapping-hint">
                <n-tag size="tiny" type="error" :bordered="false">complex</n-tag>
                <span class="mapping-hint-text">{{ groupModelHint(mapping.complex_group) }}</span>
              </div>
            </div>
          </div>
        </div>
        <n-empty v-else :description="t('autoroute.noMappings')" />

        <n-divider>{{ t('autoroute.addNewMapping') }}</n-divider>

        <n-space vertical :size="8" style="width: 100%">
          <n-space align="center">
            <n-select
              v-model:value="newMappingGroup"
              :options="modelOptions"
              :placeholder="t('autoroute.selectModelPlaceholder')"
              filterable
              clearable
              tag
              style="width: 240px;"
              @update:value="onPickNewMappingModel"
            />
            <n-select
              v-model:value="newMappingSimple"
              :options="groupOptions"
              :placeholder="t('autoroute.simpleGroup')"
              filterable
              clearable
              tag
              style="width: 170px;"
            />
            <n-select
              v-model:value="newMappingMedium"
              :options="groupOptions"
              :placeholder="t('autoroute.mediumGroup')"
              filterable
              clearable
              tag
              style="width: 170px;"
            />
            <n-select
              v-model:value="newMappingComplex"
              :options="groupOptions"
              :placeholder="t('autoroute.complexGroup')"
              filterable
              clearable
              tag
              style="width: 170px;"
            />
            <n-button type="primary" size="small" @click="addMapping">
              {{ t("common.add") }}
            </n-button>
            <n-button size="small" @click="clearNewMappingInputs">
              {{ t("common.reset") }}
            </n-button>
          </n-space>
          <div v-if="newMappingHintGroups.length" class="hint">
            {{ t("autoroute.modelGroupsHint", { groups: newMappingHintGroups.join(", ") }) }}
          </div>
        </n-space>
      </n-form>
    </n-card>

    <n-card :title="t('autoroute.routeTester')" hoverable>
      <p class="hint">{{ t("autoroute.routeTesterTip") }}</p>
      <n-form label-placement="left" label-width="150">
        <n-form-item :label="t('autoroute.testModelLabel')">
          <n-select
            v-model:value="testModelKey"
            :options="mappingKeyOptions"
            :placeholder="t('autoroute.modelNamePlaceholder')"
            filterable
            style="width: 280px;"
          />
        </n-form-item>
        <n-form-item :label="t('autoroute.testRequestBody')">
          <n-input
            v-model:value="testBody"
            type="textarea"
            :autosize="{ minRows: 6, maxRows: 18 }"
            :placeholder="DEFAULT_TEST_BODY"
          />
        </n-form-item>
        <n-form-item label=" ">
          <n-button type="primary" :loading="testLoading" @click="runTest">
            {{ t("autoroute.runTest") }}
          </n-button>
        </n-form-item>
      </n-form>

      <n-alert v-if="testError" type="error" closable @close="testError = ''">
        {{ testError }}
      </n-alert>

      <div v-if="testResult" class="test-result">
        <n-divider>{{ t("autoroute.testResult") }}</n-divider>
        <n-alert v-if="testResult.message" type="info" :show-icon="false">
          {{ t("autoroute.testNoMapping") }}
        </n-alert>
        <n-descriptions
          v-if="testResult.analysis"
          bordered
          :column="2"
          label-placement="left"
        >
          <n-descriptions-item :label="t('autoroute.testTargetGroup')">
            <n-tag type="primary">{{ testResult.target_group }}</n-tag>
          </n-descriptions-item>
          <n-descriptions-item :label="t('autoroute.testLevel')">
            <n-tag :type="levelTagType(testResult.analysis.level)">
              {{ testResult.analysis.level }}
            </n-tag>
          </n-descriptions-item>
          <n-descriptions-item :label="t('autoroute.testTokens')">
            {{ testResult.analysis.estimated_tokens }}
          </n-descriptions-item>
          <n-descriptions-item :label="t('autoroute.testMessageCount')">
            {{ testResult.analysis.message_count }}
          </n-descriptions-item>
          <n-descriptions-item :label="t('autoroute.testHasVision')">
            {{ testResult.analysis.has_vision ? "✓" : "✗" }}
          </n-descriptions-item>
          <n-descriptions-item :label="t('autoroute.testToolCount')">
            {{ testResult.analysis.tool_count }}
          </n-descriptions-item>
        </n-descriptions>
      </div>
    </n-card>

    <div class="submit-area">
      <n-button
        type="primary"
        size="large"
        :loading="loading"
        :disabled="loading"
        @click="handleSubmit"
        style="min-width: 200px"
      >
        <template #icon>
          <n-icon :component="Save" />
        </template>
        {{ t("common.save") }}
      </n-button>
    </div>
  </n-space>
</template>

<style scoped>
.mapping-hints {
  margin: 4px 0 0 168px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 11px;
  color: var(--text-color-3, #999);
}

.mapping-hint {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.mapping-hint-text {
  font-family: monospace;
  font-size: 11px;
}

.mapping-item {
  padding: 8px 0;
  border-bottom: 1px solid var(--border-color);
}

.mapping-item:last-child {
  border-bottom: none;
}

.submit-area {
  display: flex;
  justify-content: center;
  padding-top: 16px;
}

.hint {
  color: var(--text-color-3, #999);
  font-size: 13px;
  margin-bottom: 12px;
}

.test-result {
  margin-top: 8px;
}
</style>
