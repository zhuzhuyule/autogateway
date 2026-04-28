<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { Group, SubGroupInfo } from "@/types/models";
import { CloseOutline } from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NForm,
  NFormItem,
  NIcon,
  NInputNumber,
  NModal,
  useMessage,
  type FormRules,
} from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  subGroup: SubGroupInfo | null;
  aggregateGroup: Group | null;
  subGroups: SubGroupInfo[];
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const formRef = ref();

const formData = reactive<{
  weight: number;
}>({
  weight: 0,
});

const previewPercentage = computed(() => {
  if (!props.subGroups || !props.subGroup) {
    return 0;
  }

  const totalWeight = props.subGroups.reduce((sum, sg) => {
    if (sg.group.id === props.subGroup?.group.id) {
      return sum + formData.weight;
    }
    return sum + sg.weight;
  }, 0);

  return totalWeight > 0 ? Math.round((formData.weight / totalWeight) * 100) : 0;
});

const rules: FormRules = {
  weight: [
    {
      validator: (_rule, value) => {
        if (value === null || value === undefined || value === "") {
          return new Error(t("keys.enterWeight"));
        }
        if (value < 0) {
          return new Error(t("keys.weightCannotBeNegative"));
        }
        if (value > 1000) {
          return new Error(t("keys.weightMaxExceeded"));
        }
        return true;
      },
      trigger: ["blur", "input"],
    },
  ],
};

watch(
  () => [props.show, props.subGroup] as const,
  ([show, subGroup]) => {
    if (show && subGroup) {
      formData.weight = subGroup.weight;
    }
  },
  { immediate: true }
);

function handleClose() {
  emit("update:show", false);
}

async function handleSubmit() {
  if (loading.value || !props.subGroup || !props.aggregateGroup) {
    return;
  }

  try {
    await formRef.value?.validate();

    loading.value = true;

    if (!props.aggregateGroup?.id) {
      message.error(t("keys.invalidAggregateGroup"));
      return;
    }

    const subGroupId = props.subGroup.group.id;
    if (!subGroupId) {
      message.error("no subGroupId");
      return;
    }

    await keysApi.updateSubGroupWeight(props.aggregateGroup.id, subGroupId, formData.weight);

    emit("success");
    handleClose();
  } finally {
    loading.value = false;
  }
}

function adjustWeight(delta: number) {
  const newWeight = Math.max(0, Math.min(1000, formData.weight + delta));
  formData.weight = newWeight;
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="v3-modal">
    <n-card
      class="v3-modal-card"
      :title="t('keys.editWeight')"
      :bordered="false"
      size="huge"
      role="dialog"
      aria-modal="true"
    >
      <template #header-extra>
        <n-button quaternary circle size="small" @click="handleClose" class="v3-modal-close">
          <template #icon>
            <n-icon :component="CloseOutline" />
          </template>
        </n-button>
      </template>

      <div class="v3-modal-body">
        <div class="v3-sub-group-info">
          <div class="v3-sub-group-header">
            <span class="v3-sub-group-name">
              {{ subGroup?.group.display_name || subGroup?.group.name }}
            </span>
          </div>
          <div class="v3-sub-group-meta">
            <span class="v3-meta-item">
              {{ t("keys.groupId") }}:
              <strong>{{ subGroup?.group.id }}</strong>
            </span>
            <span class="v3-meta-item">
              {{ t("keys.currentWeight") }}:
              <strong>{{ subGroup?.weight }}</strong>
            </span>
          </div>
        </div>

        <n-form
          ref="formRef"
          :model="formData"
          :rules="rules"
          label-placement="left"
          label-width="100px"
        >
          <n-form-item :label="t('keys.newWeight')" path="weight" class="v3-weight-form-item">
            <div class="v3-weight-input-row">
              <n-input-number
                v-model:value="formData.weight"
                :min="0"
                :max="1000"
                :precision="0"
                :placeholder="t('keys.enterWeight')"
                class="v3-weight-input"
              />
              <div class="v3-quick-adjust">
                <n-button
                  size="tiny"
                  @click="adjustWeight(-10)"
                  :disabled="formData.weight <= 0"
                  class="v3-adjust-btn"
                >
                  -10
                </n-button>
                <n-button
                  size="tiny"
                  @click="adjustWeight(-1)"
                  :disabled="formData.weight <= 0"
                  class="v3-adjust-btn"
                >
                  -1
                </n-button>
                <n-button
                  size="tiny"
                  @click="adjustWeight(1)"
                  :disabled="formData.weight >= 1000"
                  class="v3-adjust-btn"
                >
                  +1
                </n-button>
                <n-button
                  size="tiny"
                  @click="adjustWeight(10)"
                  :disabled="formData.weight >= 1000"
                  class="v3-adjust-btn"
                >
                  +10
                </n-button>
              </div>
            </div>
          </n-form-item>
        </n-form>

        <div class="v3-preview-card">
          <div class="v3-preview-header">
            <span class="v3-preview-label">{{ t("keys.previewPercentage") }}</span>
            <span class="v3-preview-value">{{ previewPercentage }}%</span>
          </div>
          <div class="v3-preview-bar">
            <div class="v3-preview-fill" :style="{ width: `${previewPercentage}%` }" />
          </div>
          <div class="v3-preview-note">{{ t("keys.weightPreviewNote") }}</div>
        </div>
      </div>

      <template #action>
        <div class="v3-modal-footer">
          <div />
          <div class="v3-modal-actions">
            <n-button size="small" @click="handleClose">{{ t("common.cancel") }}</n-button>
            <n-button type="primary" size="small" @click="handleSubmit" :loading="loading">
              {{ t("common.confirm") }}
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

.v3-sub-group-info {
  padding: 14px 16px;
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius);
}

.v3-sub-group-header {
  margin-bottom: 10px;
}

.v3-sub-group-name {
  font: 600 15px/1.2 var(--v3-sans);
  color: var(--v3-ink);
}

.v3-sub-group-meta {
  display: flex;
  gap: 20px;
  flex-wrap: wrap;
}

.v3-meta-item {
  font: 400 12px/1 var(--v3-sans);
  color: var(--v3-ink-3);
}

.v3-meta-item strong {
  color: var(--v3-ink);
  font-weight: 600;
}

.v3-weight-form-item {
  margin-bottom: 0;
}

.v3-weight-input-row {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
}

.v3-weight-input {
  flex: 1;
}

.v3-quick-adjust {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
}

.v3-adjust-btn {
  min-width: 32px;
  font: 500 11px/1 var(--v3-mono);
}

.v3-preview-card {
  padding: 14px 16px;
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius);
}

.v3-preview-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 10px;
}

.v3-preview-label {
  font: 500 12px/1 var(--v3-sans);
  color: var(--v3-ink-2);
}

.v3-preview-value {
  font: 700 20px/1 var(--v3-sans);
  color: var(--v3-accent);
}

.v3-preview-bar {
  height: 6px;
  background: var(--v3-surface-3);
  border-radius: 3px;
  overflow: hidden;
  margin-bottom: 10px;
}

.v3-preview-fill {
  height: 100%;
  background: var(--v3-accent);
  border-radius: 3px;
  transition: width 200ms ease;
}

.v3-preview-note {
  font: 400 11px/1.3 var(--v3-sans);
  color: var(--v3-ink-3);
  font-style: italic;
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

@media (max-width: 500px) {
  .v3-weight-input-row {
    flex-direction: column;
    align-items: stretch;
  }

  .v3-quick-adjust {
    justify-content: center;
  }
}
</style>
