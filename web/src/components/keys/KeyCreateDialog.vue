<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { detectFromText } from "@/data/keyDetector";
import { getProviderById } from "@/data/freeProviders";
import { appState } from "@/utils/app-state";
import { CloudUploadOutline, DocumentTextOutline } from "@vicons/ionicons5";
import { NButton, NCard, NIcon, NModal, NUpload, type UploadFileInfo } from "naive-ui";
import { computed, ref, watch } from "vue";
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
const inputMode = ref<"text" | "file">("text");
const fileList = ref<UploadFileInfo[]>([]);

const keyHint = computed(() => {
  if (inputMode.value !== "text" || !keysText.value.trim()) {
    return null;
  }
  const d = detectFromText(keysText.value);
  if (!d) {
    return null;
  }
  const p = getProviderById(d.providerId);
  return { ...d, providerDisplayName: p?.name ?? d.providerName };
});

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
  inputMode.value = "text";
  fileList.value = [];
}

function handleClose() {
  emit("update:show", false);
}

function toggleInputMode() {
  if (inputMode.value === "text") {
    inputMode.value = "file";
    keysText.value = "";
  } else {
    inputMode.value = "text";
    fileList.value = [];
  }
}

function beforeUpload(data: { file: UploadFileInfo; fileList: UploadFileInfo[] }) {
  if (!data.file.name?.endsWith(".txt")) {
    window.$message.error(t("keys.onlyTxtFileSupported"));
    return false;
  }
  return true;
}

function handleFileChange(options: { fileList: UploadFileInfo[] }) {
  fileList.value = options.fileList;
}

async function handleSubmit() {
  if (loading.value) {
    return;
  }

  if (inputMode.value === "text") {
    if (!keysText.value.trim()) {
      return;
    }
  } else {
    if (fileList.value.length === 0) {
      return;
    }
  }

  try {
    loading.value = true;

    if (inputMode.value === "text") {
      await keysApi.addKeysAsync(props.groupId, keysText.value);
    } else {
      const file = fileList.value[0].file as File;
      await keysApi.addKeysAsync(props.groupId, undefined, file);
    }

    resetForm();
    handleClose();
    window.$message.success(t("keys.importTaskStarted"));
    appState.taskPollingTrigger++;
  } finally {
    loading.value = false;
  }
}

function isSubmitDisabled() {
  if (inputMode.value === "text") {
    return !keysText.value.trim();
  } else {
    return fileList.value.length === 0;
  }
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="v3-modal">
    <n-card
      class="v3-modal-card"
      :title="t('keys.addKeysToGroup', { group: groupName || t('keys.currentGroup') })"
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
        <div class="v3-input-toggle">
          <button
            class="v3-toggle-btn"
            :class="{ active: inputMode === 'text' }"
            @click="inputMode = 'text'"
          >
            <n-icon :component="DocumentTextOutline" />
            {{ t("keys.manualInput") }}
          </button>
          <button
            class="v3-toggle-btn"
            :class="{ active: inputMode === 'file' }"
            @click="inputMode = 'file'"
          >
            <n-icon :component="CloudUploadOutline" />
            {{ t("keys.uploadFile") }}
          </button>
        </div>

        <div v-if="inputMode === 'text'" class="v3-textarea-wrapper">
          <textarea
            v-model="keysText"
            class="v3-textarea"
            :placeholder="t('keys.enterKeysPlaceholder')"
            rows="8"
          />
          <div v-if="keyHint" class="v3-key-hint">
            <span class="v3-key-hint__badge" :class="keyHint.confidence === 'high' ? 'ok' : 'warn'">
              {{ keyHint.confidence === "high" ? "✓" : "?" }}
            </span>
            <span>
              {{
                t("v3.smartDetectMatch", {
                  provider: keyHint.providerDisplayName,
                  hint: keyHint.hint,
                })
              }}
            </span>
          </div>
        </div>

        <div v-if="inputMode === 'text'" class="v3-format-hint">
          {{ t("keys.labelFormatHint") }}
        </div>

        <div v-else class="v3-upload-wrapper">
          <n-upload
            v-model:file-list="fileList"
            :max="1"
            accept=".txt"
            :before-upload="beforeUpload"
            @change="handleFileChange"
            drag
          >
            <div class="v3-upload-area">
              <n-icon size="32" :component="CloudUploadOutline" class="v3-upload-icon" />
              <div class="v3-upload-text">{{ t("keys.clickOrDragFile") }}</div>
              <div class="v3-upload-hint">{{ t("keys.onlyTxtFileSupported") }}</div>
            </div>
          </n-upload>
        </div>
      </div>

      <template #action>
        <div class="v3-modal-footer">
          <n-button size="small" @click="toggleInputMode">
            {{ inputMode === "text" ? t("keys.uploadFile") : t("keys.manualInput") }}
          </n-button>
          <div class="v3-modal-actions">
            <n-button size="small" @click="handleClose">{{ t("common.cancel") }}</n-button>
            <n-button
              type="primary"
              size="small"
              @click="handleSubmit"
              :loading="loading"
              :disabled="isSubmitDisabled()"
            >
              {{ t("common.add") }}
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

.v3-input-toggle {
  display: flex;
  gap: 8px;
  padding: 4px;
  background: var(--v3-surface-2);
  border-radius: var(--v3-radius);
}

.v3-toggle-btn {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 10px 16px;
  border: none;
  background: transparent;
  color: var(--v3-ink-2);
  font: 500 12px/1 var(--v3-sans);
  border-radius: 4px;
  cursor: pointer;
  transition: all 120ms;
}

.v3-toggle-btn:hover {
  background: var(--v3-surface);
  color: var(--v3-ink);
}

.v3-toggle-btn.active {
  background: var(--v3-surface);
  color: var(--v3-ink);
  box-shadow: 0 1px 3px oklch(0 0 0 / 0.1);
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

.v3-upload-wrapper {
  border: 1px dashed var(--v3-line-strong);
  border-radius: var(--v3-radius);
  overflow: hidden;
}

.v3-upload-area {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 48px 24px;
  background: var(--v3-surface);
  cursor: pointer;
  transition: all 120ms;
}

.v3-upload-area:hover {
  background: var(--v3-surface-2);
  border-color: var(--v3-accent);
}

.v3-upload-icon {
  color: var(--v3-ink-3);
  margin-bottom: 12px;
}

.v3-upload-text {
  font: 500 13px/1 var(--v3-sans);
  color: var(--v3-ink);
  margin-bottom: 4px;
}

.v3-upload-hint {
  font: 400 11px/1 var(--v3-sans);
  color: var(--v3-ink-3);
}

.v3-key-hint {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  background: var(--v3-surface-2);
  border-top: 1px solid var(--v3-line);
  font: 400 11.5px var(--v3-sans);
  color: var(--v3-ink-2);
}
.v3-key-hint__badge {
  font: 700 10px var(--v3-mono);
  padding: 1px 4px;
  border-radius: 3px;
}
.v3-key-hint__badge.ok {
  background: var(--v3-ok-soft);
  color: var(--v3-ok);
}
.v3-key-hint__badge.warn {
  background: oklch(0.96 0.05 80);
  color: oklch(0.5 0.15 80);
}

.v3-format-hint {
  font: 400 11.5px/1.5 var(--v3-sans);
  color: var(--v3-ink-3);
  margin-top: -8px;
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
