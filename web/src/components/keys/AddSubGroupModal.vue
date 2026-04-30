<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { Group, SubGroupInfo } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import { AddOutline, CloseOutline } from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NFormItem,
  NIcon,
  NInputNumber,
  NModal,
  NSelect,
  useMessage,
} from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  aggregateGroup: Group | null;
  existingSubGroups: SubGroupInfo[];
  groups: Group[];
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
}

interface SubGroupItem {
  group_id: number | null;
  weight: number;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const formRef = ref();

const formData = reactive<{
  sub_groups: SubGroupItem[];
}>({
  sub_groups: [{ group_id: null, weight: 1 }],
});

const getAvailableOptions = computed(() => {
  if (!props.aggregateGroup?.channel_type) {
    return [];
  }

  const existingIds = props.existingSubGroups.map(sg => sg.group.id);

  return props.groups
    .filter(group => {
      if (group.group_type === "aggregate") {
        return false;
      }

      if (group.channel_type !== props.aggregateGroup?.channel_type) {
        return false;
      }

      if (props.aggregateGroup?.id && group.id === props.aggregateGroup.id) {
        return false;
      }

      if (group.id && existingIds.includes(group.id)) {
        return false;
      }

      return true;
    })
    .map(group => ({
      label: getGroupDisplayName(group),
      value: group?.id,
    }));
});

const getOptionsForItems = computed(() => {
  const selectedIds = formData.sub_groups.map(sg => sg.group_id).filter(Boolean);

  return formData.sub_groups.map((currentItem, currentIndex) => {
    const otherSelectedIds = selectedIds.filter((_id, index) => index !== currentIndex);

    return getAvailableOptions.value.filter(option => {
      if (option.value === currentItem.group_id) {
        return true;
      }
      return !otherSelectedIds.includes(option?.value || 0);
    });
  });
});

function getOptionsForItem(index: number) {
  return getOptionsForItems.value[index] || [];
}

watch(
  () => props.show,
  show => {
    if (show) {
      resetForm();
    }
  }
);

function resetForm() {
  formData.sub_groups = [{ group_id: null, weight: 1 }];
}

function addSubGroupItem() {
  formData.sub_groups.push({ group_id: null, weight: 1 });
}

function removeSubGroupItem(index: number) {
  if (formData.sub_groups.length > 1) {
    formData.sub_groups.splice(index, 1);
  }
}

function handleClose() {
  emit("update:show", false);
}

async function handleSubmit() {
  if (loading.value || !props.aggregateGroup?.id) {
    return;
  }

  try {
    await formRef.value?.validate();
    loading.value = true;

    const validSubGroups = formData.sub_groups.filter(sg => sg.group_id !== null);

    if (validSubGroups.length === 0) {
      message.error(t("keys.atLeastOneSubGroup"));
      return;
    }

    await keysApi.addSubGroups(
      props.aggregateGroup.id,
      validSubGroups as { group_id: number; weight: number }[]
    );

    emit("success");
    handleClose();
  } finally {
    loading.value = false;
  }
}

const canAddMore = computed(() => {
  return formData.sub_groups.length < getAvailableOptions.value.length;
});
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="v3-modal">
    <n-card
      class="v3-modal-card"
      :title="t('keys.addSubGroup')"
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
        <div class="v3-form-section">
          <div class="v3-form-section-header">
            <h4 class="v3-form-section-title">{{ t("keys.selectSubGroups") }}</h4>
            <span class="v3-chip">
              {{ t("keys.channelType") }}: {{ aggregateGroup?.channel_type?.toUpperCase() }}
            </span>
          </div>

          <div class="v3-sub-group-list">
            <div
              v-for="(item, index) in formData.sub_groups"
              :key="index"
              class="v3-sub-group-item"
            >
              <span class="v3-sub-group-index">{{ index + 1 }}</span>

              <n-form-item
                class="v3-sub-group-select"
                :path="`sub_groups[${index}].group_id`"
                :show-feedback="false"
              >
                <n-select
                  v-model:value="item.group_id"
                  :options="getOptionsForItem(index)"
                  :placeholder="t('keys.selectSubGroup')"
                  clearable
                />
              </n-form-item>

              <n-form-item
                class="v3-sub-group-weight"
                :path="`sub_groups[${index}].weight`"
                :show-feedback="false"
              >
                <n-input-number
                  v-model:value="item.weight"
                  :min="0"
                  :max="1000"
                  :placeholder="t('keys.enterWeight')"
                />
              </n-form-item>

              <n-button
                @click="removeSubGroupItem(index)"
                quaternary
                circle
                size="small"
                class="v3-sub-group-delete"
                :disabled="formData.sub_groups.length <= 1"
              >
                <template #icon>
                  <n-icon :component="CloseOutline" />
                </template>
              </n-button>
            </div>
          </div>

          <div class="v3-add-item-section">
            <n-button v-if="canAddMore" @click="addSubGroupItem" dashed class="v3-add-btn">
              <template #icon>
                <n-icon :component="AddOutline" />
              </template>
              {{ t("keys.addMoreSubGroup") }}
            </n-button>
            <div v-else class="v3-no-more-tip">
              {{ t("keys.noMoreAvailableGroups") }}
            </div>
          </div>
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
  width: 680px;
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
}

.v3-form-section {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.v3-form-section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--v3-line);
}

.v3-form-section-title {
  font: 600 13px/1.2 var(--v3-sans);
  color: var(--v3-ink);
  margin: 0;
}

.v3-sub-group-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.v3-sub-group-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px;
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius);
}

/* 让 NForm-item 不再贡献顶部/底部 feedback 空间, 以保证整行视觉垂直居中 */
.v3-sub-group-item :deep(.n-form-item) {
  display: flex;
  align-items: center;
}

.v3-sub-group-item :deep(.n-form-item-blank) {
  min-height: 0;
  padding-top: 0;
  padding-bottom: 0;
  display: flex;
  align-items: center;
}

.v3-sub-group-index {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: var(--v3-surface-3);
  color: var(--v3-ink-2);
  font: 600 11px/24px var(--v3-mono);
  text-align: center;
  flex-shrink: 0;
  align-self: center;
}

.v3-sub-group-select {
  flex: 1;
  min-width: 180px;
  margin: 0;
}

.v3-sub-group-weight {
  width: 100px;
  flex-shrink: 0;
  margin: 0;
}

.v3-sub-group-delete {
  flex-shrink: 0;
  opacity: 0.6;
}

.v3-sub-group-delete:hover:not(:disabled) {
  opacity: 1;
}

.v3-sub-group-delete:disabled {
  opacity: 0.3;
}

.v3-add-item-section {
  margin-top: 4px;
}

.v3-add-btn {
  width: 100%;
  justify-content: center;
}

.v3-no-more-tip {
  text-align: center;
  color: var(--v3-ink-3);
  font: 400 12px/1.4 var(--v3-sans);
  padding: 16px;
  background: var(--v3-surface-2);
  border: 1px dashed var(--v3-line);
  border-radius: var(--v3-radius);
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

@media (max-width: 600px) {
  .v3-sub-group-item {
    flex-direction: column;
    align-items: stretch;
    gap: 10px;
  }

  .v3-sub-group-index {
    align-self: flex-start;
  }

  .v3-sub-group-weight {
    width: 100%;
  }
}
</style>
