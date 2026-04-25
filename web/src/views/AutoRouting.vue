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
  useMessage,
} from "naive-ui";
import { Save } from "@vicons/ionicons5";

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
    message.error("Failed to load auto route configuration");
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
      message.success("Configuration saved successfully");
    } else {
      message.error("Failed to save configuration");
    }
  } catch (_error) {
    message.error("Failed to save configuration");
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
    <n-card title="Auto Complexity Routing" hoverable bordered>
      <n-form label-placement="left" label-width="200">
        <n-form-item label="Enable Auto Routing">
          <n-switch v-model:value="config.enabled" />
        </n-form-item>

        <n-form-item label="Simple Threshold (tokens)">
          <n-input-number
            v-model:value="config.simple_threshold"
            :min="0"
            style="width: 100%"
          />
        </n-form-item>

        <n-form-item label="Complex Threshold (tokens)">
          <n-input-number
            v-model:value="config.complex_threshold"
            :min="0"
            style="width: 100%"
          />
        </n-form-item>
      </n-form>
    </n-card>

    <n-card title="Group Mappings" hoverable bordered>
      <n-form label-placement="left" label-width="150">
        <div v-for="(mapping, key) in config.group_mapping" :key="key" style="margin-bottom: 12px;">
          <n-space>
            <n-input :value="String(key)" disabled style="width: 150px;" />
            <n-input v-model:value="mapping.simple_group" placeholder="Simple group" style="width: 150px;" />
            <n-input v-model:value="mapping.medium_group" placeholder="Medium group" style="width: 150px;" />
            <n-input v-model:value="mapping.complex_group" placeholder="Complex group" style="width: 150px;" />
            <n-button type="error" size="small" @click="removeMapping(String(key))">Remove</n-button>
          </n-space>
        </div>

        <n-divider>Add New Mapping</n-divider>

        <n-space>
          <n-input v-model:value="newMappingGroup" placeholder="Model name (e.g., gpt-4o)" style="width: 150px;" />
          <n-input v-model:value="newMappingSimple" placeholder="Simple group" style="width: 150px;" />
          <n-input v-model:value="newMappingMedium" placeholder="Medium group" style="width: 150px;" />
          <n-input v-model:value="newMappingComplex" placeholder="Complex group" style="width: 150px;" />
          <n-button type="primary" size="small" @click="addMapping">Add</n-button>
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
        Save Configuration
      </n-button>
    </div>
  </n-space>
</template>
