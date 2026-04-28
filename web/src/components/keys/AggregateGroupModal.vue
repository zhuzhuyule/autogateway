<script setup lang="ts">
import { keysApi } from "@/api/keys";
import ProxyKeysInput from "@/components/common/ProxyKeysInput.vue";
import { type ChannelType, type Group } from "@/types/models";
import { CloseOutline } from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  useMessage,
  type FormRules,
} from "naive-ui";
import { reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  group?: Group | null;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success", value: Group): void;
}

const props = withDefaults(defineProps<Props>(), {
  group: null,
});

const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const formRef = ref();

const channelTypeOptions = [
  { label: "OpenAI", value: "openai" as ChannelType },
  { label: "OpenAI Response", value: "openai-response" as ChannelType },
  { label: "Gemini", value: "gemini" as ChannelType },
  { label: "Anthropic", value: "anthropic" as ChannelType },
];

const defaultFormData = {
  name: "",
  display_name: "",
  description: "",
  channel_type: "openai" as ChannelType,
  sort: 1,
  proxy_keys: "",
};

const formData = reactive({ ...defaultFormData });

const rules: FormRules = {
  name: [
    {
      required: true,
      message: t("keys.enterGroupName"),
      trigger: ["blur", "input"],
    },
    {
      pattern: /^[a-z0-9_-]{1,100}$/,
      message: t("keys.groupNamePattern"),
      trigger: ["blur", "input"],
    },
  ],
  channel_type: [
    {
      required: true,
      message: t("keys.selectChannelType"),
      trigger: ["blur", "change"],
    },
  ],
};

watch(
  () => props.show,
  show => {
    if (show) {
      if (props.group) {
        loadGroupData();
      } else {
        resetForm();
      }
    }
  }
);

function resetForm() {
  Object.assign(formData, defaultFormData);
}

function loadGroupData() {
  if (!props.group) {
    return;
  }

  Object.assign(formData, {
    name: props.group.name || "",
    display_name: props.group.display_name || "",
    description: props.group.description || "",
    channel_type: props.group.channel_type || "openai",
    sort: props.group.sort || 1,
    proxy_keys: props.group.proxy_keys || "",
  });
}

function handleClose() {
  emit("update:show", false);
}

async function handleSubmit() {
  if (loading.value) {
    return;
  }

  try {
    await formRef.value?.validate();

    loading.value = true;

    const submitData = {
      name: formData.name,
      display_name: formData.display_name,
      description: formData.description,
      channel_type: formData.channel_type,
      sort: formData.sort,
      proxy_keys: formData.proxy_keys,
      group_type: "aggregate" as const,
    };

    let result: Group;
    if (props.group) {
      if (!props.group?.id) {
        message.error(t("keys.invalidGroup"));
        return;
      }
      result = await keysApi.updateGroup(props.group.id, submitData);
    } else {
      result = await keysApi.createGroup(submitData);
    }

    emit("success", result);
    handleClose();
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="v3-modal">
    <n-card
      class="v3-modal-card"
      :title="group ? t('keys.editAggregateGroup') : t('keys.createAggregateGroup')"
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
        <n-form
          ref="formRef"
          :model="formData"
          :rules="rules"
          label-placement="left"
          label-width="100px"
          require-mark-placement="right-hanging"
        >
          <div class="v3-form-section">
            <h4 class="v3-form-section-title">{{ t("keys.basicInfo") }}</h4>

            <n-form-item :label="t('keys.groupName')" path="name" class="v3-form-item">
              <n-input
                v-model:value="formData.name"
                :placeholder="t('keys.groupNamePlaceholder')"
                clearable
                class="v3-input"
              />
            </n-form-item>

            <n-form-item :label="t('keys.displayName')" class="v3-form-item">
              <n-input
                v-model:value="formData.display_name"
                :placeholder="t('keys.displayNamePlaceholder')"
                clearable
                class="v3-input"
              />
            </n-form-item>

            <n-form-item :label="t('keys.channelType')" path="channel_type" class="v3-form-item">
              <n-select
                v-model:value="formData.channel_type"
                :options="channelTypeOptions"
                :placeholder="t('keys.selectChannelType')"
                :disabled="!!props.group"
                class="v3-select"
              />
            </n-form-item>

            <n-form-item :label="t('keys.sortOrder')" class="v3-form-item">
              <n-input-number
                v-model:value="formData.sort"
                :placeholder="t('keys.sortValue')"
                class="v3-input-number"
              />
            </n-form-item>

            <n-form-item :label="t('keys.proxyKeys')" class="v3-form-item">
              <proxy-keys-input v-model="formData.proxy_keys" class="v3-input" />
            </n-form-item>

            <n-form-item :label="t('common.description')" class="v3-form-item">
              <n-input
                v-model:value="formData.description"
                type="textarea"
                placeholder=""
                :rows="2"
                :autosize="{ minRows: 2, maxRows: 5 }"
                class="v3-input"
              />
            </n-form-item>
          </div>
        </n-form>
      </div>

      <template #action>
        <div class="v3-modal-footer">
          <div />
          <div class="v3-modal-actions">
            <n-button size="small" @click="handleClose">{{ t("common.cancel") }}</n-button>
            <n-button type="primary" size="small" @click="handleSubmit" :loading="loading">
              {{ group ? t("common.update") : t("common.create") }}
            </n-button>
          </div>
        </div>
      </template>
    </n-card>
  </n-modal>
</template>

<style scoped>
.v3-modal {
  width: 560px;
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
  gap: 4px;
}

.v3-form-section-title {
  font: 600 13px/1.2 var(--v3-sans);
  color: var(--v3-ink);
  margin: 0 0 16px 0;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--v3-line);
}

.v3-form-item {
  margin-bottom: 16px;
}

.v3-input {
  width: 100%;
}

.v3-select {
  width: 100%;
}

.v3-input-number {
  width: 100%;
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
