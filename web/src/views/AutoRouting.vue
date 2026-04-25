<script setup lang="ts">
import { ref, onMounted } from "vue";
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
  useMessage,
} from "naive-ui";
import { Save } from "@vicons/ionicons5";
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
const config = ref<RouteConfig>({
  enabled: false,
  simple_threshold: 2000,
  complex_threshold: 8000,
  group_mapping: {},
});

const newMappingGroup = ref("");
const newMappingSimple = ref("");
const newMappingMedium = ref("");
const newMappingComplex = ref("");

onMounted(async () => {
  await fetchConfig();
});

async function fetchConfig() {
  try {
    const response = await fetch("/api/auto-routing/config");
    if (response.ok) {
      const result = await response.json();
      if (result.success && result.config) {
        config.value = result.config;
      }
    }
  } catch (_error) {
    message.error(t("common.requestFailed"));
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
    if (response.ok) {
      message.success(t("common.operationSuccess"));
    } else {
      message.error(t("common.requestFailed"));
    }
  } catch (_error) {
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
}

function addMapping() {
  if (!newMappingGroup.value) return;
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
</script>

<template>
  <n-space vertical>
    <n-card :title="t('autoroute.title')" hoverable bordered>
      <n-form label-placement="left" label-width="200">
        <n-form-item :label="t('autoroute.enableAutoRouting')">
          <n-switch v-model:value="config.enabled" />
        </n-form-item>

        <n-form-item :label="t('autoroute.simpleThreshold')">
          <n-input-number
            v-model:value="config.simple_threshold"
            :min="0"
            style="width: 100%"
          />
        </n-form-item>

        <n-form-item :label="t('autoroute.complexThreshold')">
          <n-input-number
            v-model:value="config.complex_threshold"
            :min="0"
            style="width: 100%"
          />
        </n-form-item>
      </n-form>
    </n-card>

    <n-card :title="t('autoroute.groupMappings')" hoverable bordered>
      <n-form label-placement="left" label-width="150">
        <div v-for="(mapping, key) in config.group_mapping" :key="key" style="margin-bottom: 12px;">
          <n-space>
            <n-input :value="String(key)" disabled style="width: 150px;" />
            <n-input v-model:value="mapping.simple_group" :placeholder="t('autoroute.simpleGroup')" style="width: 150px;" />
            <n-input v-model:value="mapping.medium_group" :placeholder="t('autoroute.mediumGroup')" style="width: 150px;" />
            <n-input v-model:value="mapping.complex_group" :placeholder="t('autoroute.complexGroup')" style="width: 150px;" />
            <n-button type="error" size="small" @click="removeMapping(String(key))">{{ t('common.delete') }}</n-button>
          </n-space>
        </div>

        <n-divider>{{ t('autoroute.addNewMapping') }}</n-divider>

        <n-space>
          <n-input v-model:value="newMappingGroup" :placeholder="t('autoroute.modelNamePlaceholder')" style="width: 150px;" />
          <n-input v-model:value="newMappingSimple" :placeholder="t('autoroute.simpleGroup')" style="width: 150px;" />
          <n-input v-model:value="newMappingMedium" :placeholder="t('autoroute.mediumGroup')" style="width: 150px;" />
          <n-input v-model:value="newMappingComplex" :placeholder="t('autoroute.complexGroup')" style="width: 150px;" />
          <n-button type="primary" size="small" @click="addMapping">{{ t('common.add') }}</n-button>
        </n-space>
      </n-form>
    </n-card>

    <div style="display: flex; justify-content: center; padding-top: 12px">
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
        {{ t('common.save') }}
      </n-button>
    </div>
  </n-space>
</template>
