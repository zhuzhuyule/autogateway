<script setup lang="ts">
import { ref, onMounted } from "vue";
import { NCard, NForm, NFormItem, NSwitch, NButton, NSpace, useMessage } from "naive-ui";
import { Save } from "@vicons/ionicons5";

interface RouteConfig {
  enabled: boolean;
  simple_prompt_threshold: number;
  medium_prompt_threshold: number;
  simple_model: string;
  medium_model: string;
  complex_model: string;
}

const message = useMessage();
const loading = ref(false);
const config = ref<RouteConfig>({
  enabled: false,
  simple_prompt_threshold: 500,
  medium_prompt_threshold: 2000,
  simple_model: "",
  medium_model: "",
  complex_model: "",
});

onMounted(async () => {
  await fetchConfig();
});

async function fetchConfig() {
  try {
    const response = await fetch("/api/autoroute/config");
    if (response.ok) {
      const data = await response.json();
      config.value = data;
    }
  } catch (_error) {
    message.error("Failed to load auto route configuration");
  }
}

async function handleSubmit() {
  loading.value = true;
  try {
    const response = await fetch("/api/autoroute/config", {
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
</script>

<template>
  <n-space vertical>
    <n-card title="Auto Complexity Routing" hoverable bordered>
      <n-form label-placement="left" label-width="200">
        <n-form-item label="Enable Auto Routing">
          <n-switch v-model:value="config.enabled" />
        </n-form-item>

        <n-form-item label="Simple Prompt Threshold">
          <n-input-number
            v-model:value="config.simple_prompt_threshold"
            :min="0"
            style="width: 100%"
          />
        </n-form-item>

        <n-form-item label="Medium Prompt Threshold">
          <n-input-number
            v-model:value="config.medium_prompt_threshold"
            :min="0"
            style="width: 100%"
          />
        </n-form-item>

        <n-form-item label="Simple Model">
          <n-input v-model:value="config.simple_model" placeholder="e.g., gpt-3.5-turbo" />
        </n-form-item>

        <n-form-item label="Medium Model">
          <n-input v-model:value="config.medium_model" placeholder="e.g., gpt-4" />
        </n-form-item>

        <n-form-item label="Complex Model">
          <n-input v-model:value="config.complex_model" placeholder="e.g., gpt-4-turbo" />
        </n-form-item>
      </n-form>

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
    </n-card>
  </n-space>
</template>
