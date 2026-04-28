<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { Group } from "@/types/models";
import { appState } from "@/utils/app-state";
import { getGroupDisplayName } from "@/utils/display";
import { CopyOutline, DocumentTextOutline } from "@vicons/ionicons5";
import { NButton, NCard, NIcon, NModal, NRadio, NRadioGroup, NSpace, useMessage } from "naive-ui";
import { computed, ref, watchEffect } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  sourceGroup: Group | null;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success", group: Group): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);

const formData = ref<{
  copyKeys: "all" | "valid_only" | "none";
}>({
  copyKeys: "all",
});

const modalVisible = computed({
  get: () => props.show,
  set: (value: boolean) => emit("update:show", value),
});

watchEffect(() => {
  if (props.show) {
    resetForm();
  }
});

function resetForm() {
  formData.value = {
    copyKeys: "all",
  };
}

function generateNewGroupName(): string {
  if (!props.sourceGroup) {
    return "";
  }
  const baseName = props.sourceGroup.name;
  return `${baseName}_copy`;
}

async function handleCopy() {
  if (!props.sourceGroup?.id) {
    message.error(t("keys.sourceGroupNotExist"));
    return;
  }

  loading.value = true;
  try {
    const copyData = {
      copy_keys: formData.value.copyKeys,
    };
    const result = await keysApi.copyGroup(props.sourceGroup.id, copyData);

    if (formData.value.copyKeys !== "none") {
      message.success(
        t("keys.copyGroupWithKeysSuccess", {
          groupName: result.group.display_name || result.group.name,
        })
      );
      appState.taskPollingTrigger++;
    } else {
      message.success(
        t("keys.copyGroupSuccess", { groupName: result.group.display_name || result.group.name })
      );
    }

    emit("success", result.group);
    modalVisible.value = false;
  } finally {
    loading.value = false;
  }
}

function handleCancel() {
  modalVisible.value = false;
}
</script>

<template>
  <n-modal :show="modalVisible" @update:show="handleCancel" class="v3-modal">
    <n-card
      class="v3-modal-card"
      :title="
        t('keys.copyGroupTitle', { groupName: sourceGroup ? getGroupDisplayName(sourceGroup) : '' })
      "
      :bordered="false"
      size="huge"
      role="dialog"
      aria-modal="true"
    >
      <template #header-extra>
        <n-button quaternary circle size="small" @click="handleCancel" class="v3-modal-close">
          <template #icon>
            <n-icon :component="DocumentTextOutline" />
          </template>
        </n-button>
      </template>

      <div class="v3-modal-body">
        <div class="v3-copy-preview">
          <div class="v3-copy-preview-label">{{ t("keys.newGroupNameLabel") }}</div>
          <code class="v3-copy-preview-value">{{ generateNewGroupName() }}</code>
        </div>

        <div class="v3-radio-group">
          <div class="v3-radio-group-label">{{ t("keys.keyHandling") }}</div>
          <n-radio-group v-model:value="formData.copyKeys" name="copyKeys">
            <n-space vertical>
              <n-radio value="all" class="v3-radio-item">
                <div class="v3-radio-content">
                  <span class="v3-radio-title">{{ t("keys.copyAllKeys") }}</span>
                </div>
              </n-radio>
              <n-radio value="valid_only" class="v3-radio-item">
                <div class="v3-radio-content">
                  <span class="v3-radio-title">{{ t("keys.copyValidKeysOnly") }}</span>
                </div>
              </n-radio>
              <n-radio value="none" class="v3-radio-item">
                <div class="v3-radio-content">
                  <span class="v3-radio-title">{{ t("keys.dontCopyKeys") }}</span>
                </div>
              </n-radio>
            </n-space>
          </n-radio-group>
        </div>
      </div>

      <template #action>
        <div class="v3-modal-footer">
          <div />
          <div class="v3-modal-actions">
            <n-button size="small" @click="handleCancel" :disabled="loading">
              {{ t("common.cancel") }}
            </n-button>
            <n-button type="primary" size="small" @click="handleCopy" :loading="loading">
              <template #icon>
                <n-icon :component="CopyOutline" />
              </template>
              {{ t("keys.confirmCopy") }}
            </n-button>
          </div>
        </div>
      </template>
    </n-card>
  </n-modal>
</template>

<style scoped>
.v3-modal {
  width: 480px;
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
  gap: 20px;
}

.v3-copy-preview {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  background: var(--v3-ok-soft);
  border: 1px solid oklch(0.78 0.1 145);
  border-radius: var(--v3-radius);
}

.v3-copy-preview-label {
  font: 500 11px/1 var(--v3-sans);
  color: oklch(0.4 0.13 145);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.v3-copy-preview-value {
  font: 500 12px/1 var(--v3-mono);
  color: oklch(0.4 0.13 145);
  background: oklch(1 0 0 / 0.5);
  padding: 3px 8px;
  border-radius: 4px;
}

.v3-radio-group {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.v3-radio-group-label {
  font: 500 12px/1 var(--v3-sans);
  color: var(--v3-ink-2);
}

.v3-radio-item {
  margin: 0;
}

.v3-radio-content {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.v3-radio-title {
  font: 500 13px/1.3 var(--v3-sans);
  color: var(--v3-ink);
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
