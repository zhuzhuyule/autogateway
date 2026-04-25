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
  useMessage,
} from "naive-ui";
import { Save, Refresh } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";

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

const hasMappings = computed(() => Object.keys(config.value.group_mapping).length > 0);

const newMappingGroup = ref("");
const newMappingSimple = ref("");
const newMappingMedium = ref("");
const newMappingComplex = ref("");

onMounted(async () => {
  await fetchConfig();
});

async function fetchConfig() {
  fetchLoading.value = true;
  fetchError.value = false;
  try {
    const response = await fetch("/api/auto-routing/config");
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const result = await response.json();
    if (result.success && result.config) {
      config.value = result.config;
    }
  } catch (error) {
    fetchError.value = true;
    message.error(t("common.requestFailed"));
  } finally {
    fetchLoading.value = false;
  }
}

async function handleSubmit() {
  loading.value = true;
  try {
    const response = await fetch("/api/auto-routing/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(config.value),
    });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    message.success(t("common.operationSuccess"));
  } catch (error) {
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
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
    simple_group: newMappingSimple.value,
    medium_group: newMappingMedium.value,
    complex_group: newMappingComplex.value,
  };
  newMappingGroup.value = "";
  newMappingSimple.value = "";
  newMappingMedium.value = "";
  newMappingComplex.value = "";
}

function removeMapping(key: string) {
  delete config.value.group_mapping[key];
}

function clearNewMappingInputs() {
  newMappingGroup.value = "";
  newMappingSimple.value = "";
  newMappingMedium.value = "";
  newMappingComplex.value = "";
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
              <n-input :value="String(key)" disabled style="width: 140px;" />
              <n-input v-model:value="mapping.simple_group" :placeholder="t('autoroute.simpleGroup')" style="width: 130px;" />
              <n-input v-model:value="mapping.medium_group" :placeholder="t('autoroute.mediumGroup')" style="width: 130px;" />
              <n-input v-model:value="mapping.complex_group" :placeholder="t('autoroute.complexGroup')" style="width: 130px;" />
              <n-button type="error" size="small" @click="removeMapping(String(key))">
                {{ t("common.delete") }}
              </n-button>
            </n-space>
          </div>
        </div>
        <n-empty v-else :description="t('autoroute.noMappings')" />

        <n-divider>{{ t('autoroute.addNewMapping') }}</n-divider>

        <n-space align="center">
          <n-input v-model:value="newMappingGroup" :placeholder="t('autoroute.modelNamePlaceholder')" style="width: 140px;" />
          <n-input v-model:value="newMappingSimple" :placeholder="t('autoroute.simpleGroup')" style="width: 130px;" />
          <n-input v-model:value="newMappingMedium" :placeholder="t('autoroute.mediumGroup')" style="width: 130px;" />
          <n-input v-model:value="newMappingComplex" :placeholder="t('autoroute.complexGroup')" style="width: 130px;" />
          <n-button type="primary" size="small" @click="addMapping">
            {{ t("common.add") }}
          </n-button>
          <n-button size="small" @click="clearNewMappingInputs">
            {{ t("common.reset") }}
          </n-button>
        </n-space>
      </n-form>
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
</style>
