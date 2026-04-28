<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { appState } from "@/utils/app-state";
import { AlertCircleOutline, DocumentTextOutline } from "@vicons/ionicons5";
import { NButton, NCard, NIcon, NModal } from "naive-ui";
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  groupId: number;
  groupName?: string;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
}

const props = defineProps<Props>();

const emit = defineEmits<Emits>();

const { t } = useI18n();

const loading = ref(false);
const keysText = ref("");

watch(
  () => props.show,
  show => {
    if (show) {
      resetForm();
    }
  }
);

function resetForm() {
  keysText.value = "";
}

function handleClose() {
  emit("update:show", false);
}

async function handleSubmit() {
  if (loading.value || !keysText.value.trim()) {
    return;
  }

  try {
    loading.value = true;

    await keysApi.deleteKeysAsync(props.groupId, keysText.value);
    resetForm();

    handleClose();
    window.$message.success(t("keys.deleteTaskStarted"));
    appState.taskPollingTrigger++;
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="v3-modal">
    <n-card
      class="v3-modal-card"
      :title="t('keys.deleteKeysFromGroup', { group: groupName || t('keys.currentGroup') })"
      :bordered="false"
      size="huge"
      role="dialog"
      aria-modal="true"
    >
      <template #header-extra>
        <n-button quaternary circle size="small" @click="handleClose" class="v3-modal-close">
          <template #icon>
            <n-icon :component="DocumentTextOutline" />
          </template>
        </n-button>
      </template>

      <div class="v3-modal-body">
        <div class="v3-danger-notice">
          <n-icon :component="AlertCircleOutline" class="v3-danger-icon" />
          <span>{{ t("keys.deleteKeysWarning") }}</span>
        </div>

        <div class="v3-textarea-wrapper">
          <textarea
            v-model="keysText"
            class="v3-textarea"
            :placeholder="t('keys.enterKeysToDeletePlaceholder')"
            rows="8"
          />
        </div>
      </div>

      <template #action>
        <div class="v3-modal-footer">
          <div />
          <div class="v3-modal-actions">
            <n-button size="small" @click="handleClose">{{ t("common.cancel") }}</n-button>
            <n-button
              type="error"
              size="small"
              @click="handleSubmit"
              :loading="loading"
              :disabled="!keysText"
            >
              {{ t("common.delete") }}
            </n-button>
          </div>
        </div>
      </template>
    </n-card>
  </n-modal>
</template>

<style scoped>
.v3-modal {
  width: 600px;
  max-width: 90vw;
}

.v3-modal-card {
  border-radius: var(--v3-radius-md);
  border: 1px solid var(--v3-line);
  box-shadow: var(--v3-shadow-md);
}

.v3-modal-close {
  opacity: 0.6;
}

.v3-modal-close:hover {
  opacity: 1;
}

.v3-modal-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.v3-danger-notice {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  background: var(--v3-danger-soft);
  border: 1px solid oklch(0.82 0.1 25);
  border-radius: var(--v3-radius);
  font: 500 12px/1.4 var(--v3-sans);
  color: oklch(0.42 0.16 25);
}

.v3-danger-icon {
  flex-shrink: 0;
  width: 18px;
  height: 18px;
}

.v3-textarea-wrapper {
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius);
  overflow: hidden;
}

.v3-textarea {
  width: 100%;
  min-height: 180px;
  padding: 12px;
  border: none;
  background: var(--v3-surface);
  font: 500 12px/1.5 var(--v3-mono);
  color: var(--v3-ink);
  resize: vertical;
  outline: none;
}

.v3-textarea::placeholder {
  color: var(--v3-ink-4);
}

.v3-textarea:focus {
  background: var(--v3-surface);
}

.v3-modal-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.v3-modal-actions {
  display: flex;
  gap: 8px;
}
</style>
