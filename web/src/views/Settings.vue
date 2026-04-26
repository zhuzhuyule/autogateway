<script setup lang="ts">
import { settingsApi, type Setting, type SettingCategory } from "@/api/settings";
import ProxyKeysInput from "@/components/common/ProxyKeysInput.vue";
import { HelpCircle, Save } from "@vicons/ionicons5";
import {
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NSwitch,
  NTooltip,
  useMessage,
  type FormItemRule,
} from "naive-ui";
import { ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

const settingList = ref<SettingCategory[]>([]);
const formRef = ref();
const form = ref<Record<string, string | number | boolean>>({});
const isSaving = ref(false);
const message = useMessage();

fetchSettings();

async function fetchSettings() {
  try {
    const data = await settingsApi.getSettings();
    settingList.value = data || [];
    initForm();
  } catch {
    message.error(t("settings.loadFailed"));
  }
}

function initForm() {
  form.value = settingList.value.reduce(
    (acc: Record<string, string | number | boolean>, category) => {
      category.settings?.forEach(setting => {
        acc[setting.key] = setting.value;
      });
      return acc;
    },
    {}
  );
}

async function handleSubmit() {
  if (isSaving.value) return;
  try {
    await formRef.value.validate();
    isSaving.value = true;
    await settingsApi.updateSettings(form.value);
    await fetchSettings();
  } finally {
    isSaving.value = false;
  }
}

function generateValidationRules(item: Setting): FormItemRule[] {
  const rules: FormItemRule[] = [];
  if (item.required) {
    const rule: FormItemRule = {
      required: true,
      message: t("settings.pleaseInput", { field: item.name }),
      trigger: ["input", "blur"],
    };
    if (item.type === "int") rule.type = "number";
    rules.push(rule);
  }
  if (item.type === "int" && item.min_value !== undefined && item.min_value !== null) {
    rules.push({
      validator: (_rule: FormItemRule, value: number) => {
        if (value === null || value === undefined) return true;
        if (
          item.min_value !== undefined &&
          item.min_value !== null &&
          value < item.min_value
        ) {
          return new Error(t("settings.minValueError", { value: item.min_value }));
        }
        return true;
      },
      trigger: ["input", "blur"],
    });
  }
  return rules;
}
</script>

<template>
  <div>
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.settings") }}</div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("nav.settings") || "Settings" }}
      <span class="v3-viewtitle__meta">{{ settingList.length }} categories</span>
    </h1>

    <n-form ref="formRef" :model="form" label-placement="top">
      <div class="v3-settings-grid">
        <div
          v-for="category in settingList"
          :key="category.category_name"
          class="v3-card"
        >
          <div class="v3-card__head">
            <div class="v3-card__title">{{ category.category_name }}</div>
          </div>
          <div class="v3-card__body">
            <div
              v-for="item in category.settings"
              :key="item.key"
              class="v3-set-row"
              style="flex-direction: column; align-items: stretch; gap: 6px"
            >
              <div
                style="
                  display: flex;
                  justify-content: space-between;
                  align-items: center;
                  gap: 12px;
                "
              >
                <div>
                  <div
                    class="v3-set-row__lbl"
                    style="display: flex; align-items: center; gap: 6px"
                  >
                    {{ item.name }}
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon
                          :component="HelpCircle"
                          :size="13"
                          style="cursor: help; color: var(--v3-ink-4)"
                        />
                      </template>
                      {{ item.description }}
                    </n-tooltip>
                  </div>
                  <div class="v3-set-row__sub">
                    <code
                      style="
                        font: 500 10.5px var(--v3-mono);
                        color: var(--v3-ink-3);
                        background: var(--v3-surface-2);
                        padding: 1px 5px;
                        border-radius: 3px;
                      "
                    >
                      {{ item.key }}
                    </code>
                    <span
                      v-if="item.required"
                      class="v3-chip v3-chip--warn"
                      style="margin-left: 6px"
                    >
                      required
                    </span>
                  </div>
                </div>
              </div>
              <n-form-item
                :path="item.key"
                :rule="generateValidationRules(item)"
                :show-label="false"
                style="margin: 0"
              >
                <n-input-number
                  v-if="item.type === 'int'"
                  v-model:value="form[item.key] as number"
                  :min="
                    item.min_value !== undefined && item.min_value >= 0
                      ? item.min_value
                      : undefined
                  "
                  :placeholder="t('settings.inputNumber') || 'Number…'"
                  clearable
                  style="width: 100%"
                  size="small"
                />
                <n-switch
                  v-else-if="item.type === 'bool'"
                  v-model:value="form[item.key] as boolean"
                  size="small"
                />
                <proxy-keys-input
                  v-else-if="item.key === 'proxy_keys'"
                  v-model="form[item.key] as string"
                  :placeholder="t('settings.inputContent') || 'Value…'"
                  size="small"
                />
                <n-input
                  v-else
                  v-model:value="form[item.key] as string"
                  :placeholder="t('settings.inputContent') || 'Value…'"
                  clearable
                  size="small"
                />
              </n-form-item>
            </div>
          </div>
        </div>
      </div>
    </n-form>

    <div
      v-if="settingList.length > 0"
      style="display: flex; justify-content: center; padding-top: 18px"
    >
      <button
        class="v3-btn v3-btn--accent v3-btn--lg"
        :disabled="isSaving"
        style="min-width: 220px"
        @click="handleSubmit"
      >
        <n-icon :component="Save" :size="13" />
        {{ isSaving ? t("settings.saving") || "Saving…" : t("settings.saveSettings") || "Save settings" }}
      </button>
    </div>
  </div>
</template>
