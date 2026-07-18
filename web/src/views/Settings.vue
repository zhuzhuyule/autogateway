<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { settingsApi, type Setting, type SettingCategory } from "@/api/settings";
import ProxyKeysInput from "@/components/common/ProxyKeysInput.vue";
import V3BackupCard from "@/components/v3/V3BackupCard.vue";
import { ChevronDown, ChevronForward, HelpCircle, Save } from "@vicons/ionicons5";
import {
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NSelect,
  NSwitch,
  NTooltip,
  useMessage,
  type FormItemRule,
  type SelectOption,
} from "naive-ui";
import { ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

const settingList = ref<SettingCategory[]>([]);
const formRef = ref();
const form = ref<Record<string, string | number | boolean>>({});
const isSaving = ref(false);
const message = useMessage();

// 每个分类的展开态(分类折叠面板)。默认只展开第一个,其余收起以省屏。
const expanded = ref<Record<string, boolean>>({});
// 错误归因分组下拉的可选项(现有分组名),让 llm_error_triage_group 不用手打。
const groupOptions = ref<SelectOption[]>([]);

fetchSettings();
fetchGroups();

async function fetchSettings() {
  try {
    const data = await settingsApi.getSettings();
    settingList.value = data || [];
    initForm();
    initExpanded();
  } catch {
    message.error(t("settings.loadFailed"));
  }
}

async function fetchGroups() {
  try {
    const groups = await keysApi.listGroups();
    groupOptions.value = groups.map(g => ({
      label: g.display_name ? `${g.display_name} (${g.name})` : g.name,
      value: g.name,
    }));
  } catch {
    // 拉分组失败不阻塞设置页; 下拉为空时用户仍可在别处配置分组后再来选。
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

function initExpanded() {
  const next: Record<string, boolean> = {};
  settingList.value.forEach((category, i) => {
    // 保留用户已有的展开态; 首次加载默认只展开第一个分类。
    next[category.category_name] = expanded.value[category.category_name] ?? i === 0;
  });
  expanded.value = next;
}

function toggleCategory(name: string) {
  expanded.value[name] = !expanded.value[name];
}

// 归因「模型」字段给个具体示例占位, 比通用 "Value…" 好懂。
function placeholderFor(item: Setting): string {
  if (item.key === "llm_error_triage_model") {
    return "如 gpt-4o-mini";
  }
  return t("settings.inputContent") || "Value…";
}

async function handleSubmit() {
  if (isSaving.value) {
    return;
  }
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
    if (item.type === "int") {
      rule.type = "number";
    }
    rules.push(rule);
  }
  if (item.type === "int" && item.min_value !== undefined && item.min_value !== null) {
    rules.push({
      validator: (_rule: FormItemRule, value: number) => {
        if (value === null || value === undefined) {
          return true;
        }
        if (item.min_value !== undefined && item.min_value !== null && value < item.min_value) {
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

    <V3BackupCard style="margin-bottom: 24px" />

    <n-form ref="formRef" :model="form" label-placement="top">
      <div class="v3-settings-stack">
        <div v-for="category in settingList" :key="category.category_name" class="v3-card set-cat">
          <button
            type="button"
            class="set-cat__head"
            :aria-expanded="expanded[category.category_name]"
            @click="toggleCategory(category.category_name)"
          >
            <span class="v3-card__title">{{ category.category_name }}</span>
            <span class="set-cat__meta">
              <span class="set-cat__count">{{ category.settings?.length || 0 }}</span>
              <n-icon
                :component="expanded[category.category_name] ? ChevronDown : ChevronForward"
                :size="16"
                class="set-cat__chev"
              />
            </span>
          </button>
          <div v-show="expanded[category.category_name]" class="set-cat__body">
            <div class="set-grid">
              <div v-for="item in category.settings" :key="item.key" class="set-item">
                <div class="set-item__label">
                <span class="set-item__name">{{ item.name }}</span>
                <n-tooltip trigger="hover" placement="top" style="max-width: 320px">
                  <template #trigger>
                    <n-icon :component="HelpCircle" :size="14" class="set-item__help" />
                  </template>
                  <div>{{ item.description }}</div>
                  <div class="set-item__key">{{ item.key }}</div>
                </n-tooltip>
                <span v-if="item.required" class="v3-chip v3-chip--warn">required</span>
              </div>
              <div class="set-item__ctrl">
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
                  <n-select
                    v-else-if="item.key === 'llm_error_triage_group'"
                    v-model:value="form[item.key] as string"
                    :options="groupOptions"
                    filterable
                    clearable
                    :placeholder="t('settings.selectGroup') || '选择一个分组'"
                    size="small"
                    style="width: 100%"
                  />
                  <n-input
                    v-else
                    v-model:value="form[item.key] as string"
                    :placeholder="placeholderFor(item)"
                    clearable
                    size="small"
                  />
                </n-form-item>
                </div>
              </div>
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
        {{
          isSaving
            ? t("settings.saving") || "Saving…"
            : t("settings.saveSettings") || "Save settings"
        }}
      </button>
    </div>
  </div>
</template>

<style scoped>
/* 分类折叠面板 —— 单列堆叠, 每个分类一张 v3-card, head 可点击折叠 */
.v3-settings-stack {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.set-cat {
  padding: 0;
  overflow: hidden;
}
.set-cat__head {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 13px 16px;
  background: none;
  border: none;
  cursor: pointer;
  text-align: left;
  color: inherit;
  font: inherit;
}
.set-cat__head:hover {
  background: var(--v3-surface-2);
}
.set-cat__meta {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--v3-ink-4);
}
.set-cat__count {
  font: 600 11px var(--v3-mono);
  color: var(--v3-ink-3);
  background: var(--v3-surface-2);
  padding: 1px 7px;
  border-radius: 999px;
}
.set-cat__chev {
  transition: transform 0.15s ease;
}
.set-cat__body {
  padding: 4px 16px 10px;
  border-top: 1px solid var(--v3-line);
}

/* 响应式多列网格: 分类内设置项按列宽自适应排成 2-3 列(宽屏), 窄屏自然回退单列。
   每项控件占满所在列, 根治"名称在左、控件在右、中间一大片空白 + 整行拉满全宽"。 */
.set-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  column-gap: 32px;
}
.set-item {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 6px;
  padding: 11px 0;
  min-width: 0;
  border-bottom: 1px solid var(--v3-line);
}
.set-item__label {
  display: flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
}
.set-item__name {
  font-size: 13px;
  color: var(--v3-ink-1, inherit);
}
.set-item__help {
  cursor: help;
  color: var(--v3-ink-4);
  flex-shrink: 0;
}
.set-item__key {
  margin-top: 5px;
  font: 500 10.5px var(--v3-mono);
  opacity: 0.6;
}
.set-item__ctrl {
  width: 100%;
}
</style>
