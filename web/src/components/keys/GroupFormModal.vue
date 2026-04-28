<script setup lang="ts">
import { getGroupList } from "@/api/dashboard";
import { keysApi } from "@/api/keys";
import { settingsApi } from "@/api/settings";
import ProxyKeysInput from "@/components/common/ProxyKeysInput.vue";
import {
  FREE_PROVIDERS,
  bootstrapExposedModels,
  findProviderByUpstreams,
  isFree,
  type FreeProvider,
} from "@/data/freeProviders";
import type { Group, GroupConfigOption, UpstreamInfo } from "@/types/models";
import {
  Add,
  Close,
  CopyOutline,
  HelpCircleOutline,
  OpenOutline,
  RefreshOutline,
  Remove,
  RocketOutline,
} from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NEmpty,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSwitch,
  NTag,
  NTooltip,
  useMessage,
  type FormRules,
} from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  group?: Group | null;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success", value: Group): void;
  (e: "switchToGroup", groupId: number): void;
}

// 配置项类型
interface ConfigItem {
  key: string;
  value: number | string | boolean;
}

// Header规则类型
interface HeaderRuleItem {
  key: string;
  value: string;
  action: "set" | "remove";
}

const props = withDefaults(defineProps<Props>(), {
  group: null,
});

const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const formRef = ref();
const modelRedirectTip = `{
  "gpt-5": "gpt-5-2025-08-07",
  "gemini-2.5-flash": "gemini-2.5-flash-preview-09-2025"
}`;

// 表单数据接口
interface GroupFormData {
  name: string;
  display_name: string;
  description: string;
  upstreams: UpstreamInfo[];
  channel_type: "anthropic" | "gemini" | "openai" | "openai-response";
  sort: number;
  test_model: string;
  validation_endpoint: string;
  param_overrides: string;
  model_redirect_rules: string;
  model_redirect_strict: boolean;
  config: Record<string, number | string | boolean>;
  configItems: ConfigItem[];
  header_rules: HeaderRuleItem[];
  proxy_keys: string;
  group_type?: string;
}

// 表单数据
const formData = reactive<GroupFormData>({
  name: "",
  display_name: "",
  description: "",
  upstreams: [
    {
      url: "",
      weight: 1,
    },
  ] as UpstreamInfo[],
  channel_type: "openai",
  sort: 1,
  test_model: "",
  validation_endpoint: "",
  param_overrides: "",
  model_redirect_rules: "",
  model_redirect_strict: false,
  config: {},
  configItems: [] as ConfigItem[],
  header_rules: [] as HeaderRuleItem[],
  proxy_keys: "",
  group_type: "standard",
});

const channelTypeOptions = ref<{ label: string; value: string }[]>([]);
const configOptions = ref<GroupConfigOption[]>([]);
const channelTypesFetched = ref(false);
const configOptionsFetched = ref(false);

// 跟踪用户是否已手动修改过字段（仅在新增模式下使用）
const userModifiedFields = ref({
  test_model: false,
  upstream: false,
});

// Free provider 快速预填
const providerSearch = ref("");
const providerPanelExpanded = ref<string[]>(["picker"]);
const usedProviderIds = ref<Set<string>>(new Set());

// 上游真实模型列表(从 group.available_models 加载;可手动刷新)
const availableModels = ref<string[]>([]);
const modelsRefreshLoading = ref(false);
const modelsRefreshedAt = ref<string | null>(null);
const modelsListExpanded = ref<string[]>([]);
const modelsListSearch = ref("");

const formProviderId = computed<string | undefined>(
  () => findProviderByUpstreams(formData.upstreams)?.id
);

function modelIsFree(modelId: string): boolean {
  return isFree(formProviderId.value, modelId) === true;
}

const filteredAvailableModels = computed(() => {
  const q = modelsListSearch.value.trim().toLowerCase();
  const list = availableModels.value.filter(m => !q || m.toLowerCase().includes(q));
  // 免费在前 + 按 id 排序
  return list.sort((a, b) => {
    const fa = modelIsFree(a);
    const fb = modelIsFree(b);
    if (fa !== fb) {
      return fa ? -1 : 1;
    }
    return a.localeCompare(b);
  });
});

function refreshedAtDisplay(iso: string | null): string {
  if (!iso) {
    return "";
  }
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

async function copyModelId(modelId: string) {
  try {
    await navigator.clipboard.writeText(modelId);
    message.success(t("keys.modelIdCopied"));
  } catch {
    message.error(t("common.requestFailed"));
  }
}

const testModelOptions = computed(() => {
  // 合并 freeProviders 内置示例(用于新建尚未拉取时给点提示) + 真实拉取
  const merged = new Set<string>();
  if (availableModels.value.length === 0 && !props.group) {
    // 创建模式且未拉取过,从 channel_type 默认值给一个占位
    const placeholder = formData.test_model;
    if (placeholder) {
      merged.add(placeholder);
    }
  }
  availableModels.value.forEach(m => merged.add(m));
  // 当前已选值始终保留为合法选项
  if (formData.test_model) {
    merged.add(formData.test_model);
  }
  return Array.from(merged).map(id => ({ label: id, value: id }));
});

const filteredProviders = computed<FreeProvider[]>(() => {
  const q = providerSearch.value.trim().toLowerCase();
  if (!q) {
    return FREE_PROVIDERS;
  }
  return FREE_PROVIDERS.filter(p =>
    [p.id, p.name, p.description, p.freeTier, ...p.models].some(s => s.toLowerCase().includes(q))
  );
});

async function refreshUsedProviders() {
  try {
    const response = await getGroupList();
    const list =
      (response as unknown as { data?: Array<{ upstreams?: Array<{ url?: string }> }> }).data || [];
    const ids = new Set<string>();
    for (const g of list) {
      const matched = findProviderByUpstreams(g.upstreams || []);
      if (matched) {
        ids.add(matched.id);
      }
    }
    usedProviderIds.value = ids;
  } catch {
    // 忽略,徽标仅做提示用
  }
}

const showProviderPicker = computed(() => !props.group && formData.group_type !== "aggregate");

function applyProvider(p: FreeProvider) {
  const alreadyUsed = usedProviderIds.value.has(p.id);
  formData.channel_type = p.channelType;
  formData.name = alreadyUsed ? `${p.recommendedGroupName}-2` : p.recommendedGroupName;
  formData.display_name = p.recommendedDisplayName;
  formData.description = `${p.name} · ${p.freeTier}`;
  formData.test_model = p.testModel;
  formData.upstreams = [{ url: p.baseUrl, weight: 1 }];
  formData.validation_endpoint = "";
  // 重置"用户已修改"标记,避免 channel_type watcher 又覆盖回 channel 默认值
  userModifiedFields.value = { test_model: true, upstream: true };
  providerPanelExpanded.value = [];
  if (alreadyUsed) {
    message.warning(t("keys.providerAlreadyUsed", { name: p.name }));
  } else {
    message.success(t("keys.providerApplied", { name: p.name }));
  }
}

// 根据渠道类型动态生成占位符提示
const testModelPlaceholder = computed(() => {
  switch (formData.channel_type) {
    case "openai":
    case "openai-response":
      return "gpt-4.1-nano";
    case "gemini":
      return "gemini-2.0-flash-lite";
    case "anthropic":
      return "claude-3-haiku-20240307";
    default:
      return t("keys.enterModelName");
  }
});

const upstreamPlaceholder = computed(() => {
  switch (formData.channel_type) {
    case "openai":
    case "openai-response":
      return "https://api.openai.com";
    case "gemini":
      return "https://generativelanguage.googleapis.com";
    case "anthropic":
      return "https://api.anthropic.com";
    default:
      return t("keys.enterUpstreamUrl");
  }
});

const validationEndpointPlaceholder = computed(() => {
  switch (formData.channel_type) {
    case "openai":
      return "/v1/chat/completions";
    case "openai-response":
      return "/v1/responses";
    case "anthropic":
      return "/v1/messages";
    case "gemini":
      return ""; // Gemini 不显示此字段
    default:
      return t("keys.enterValidationPath");
  }
});

// 表单验证规则
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
  test_model: [
    {
      required: true,
      message: t("keys.enterTestModel"),
      trigger: ["blur", "input"],
    },
  ],
  upstreams: [
    {
      type: "array",
      min: 1,
      message: t("keys.atLeastOneUpstream"),
      trigger: ["blur", "change"],
    },
  ],
};

// 监听弹窗显示状态
watch(
  () => props.show,
  show => {
    if (show) {
      if (!channelTypesFetched.value) {
        fetchChannelTypes();
      }
      if (!configOptionsFetched.value) {
        fetchGroupConfigOptions();
      }
      resetForm();
      if (props.group) {
        loadGroupData();
      } else {
        // 仅创建模式下,实时刷新"已添加"徽标
        refreshUsedProviders();
      }
    }
  }
);

// 监听渠道类型变化，在新增模式下智能更新默认值
watch(
  () => formData.channel_type,
  (_newChannelType, oldChannelType) => {
    if (!props.group && oldChannelType) {
      // 仅在新增模式且不是初始设置时处理
      // 检查测试模型是否应该更新（为空或是旧渠道类型的默认值）
      if (
        !userModifiedFields.value.test_model ||
        formData.test_model === getOldDefaultTestModel(oldChannelType)
      ) {
        formData.test_model = testModelPlaceholder.value;
        userModifiedFields.value.test_model = false;
      }

      // 检查第一个上游地址是否应该更新
      if (
        formData.upstreams.length > 0 &&
        (!userModifiedFields.value.upstream ||
          formData.upstreams[0].url === getOldDefaultUpstream(oldChannelType))
      ) {
        formData.upstreams[0].url = upstreamPlaceholder.value;
        userModifiedFields.value.upstream = false;
      }
    }
  }
);

// 获取旧渠道类型的默认值（用于比较）
function getOldDefaultTestModel(channelType: string): string {
  switch (channelType) {
    case "openai":
    case "openai-response":
      return "gpt-4.1-nano";
    case "gemini":
      return "gemini-2.0-flash-lite";
    case "anthropic":
      return "claude-3-haiku-20240307";
    default:
      return "";
  }
}

function getOldDefaultUpstream(channelType: string): string {
  switch (channelType) {
    case "openai":
    case "openai-response":
      return "https://api.openai.com";
    case "gemini":
      return "https://generativelanguage.googleapis.com";
    case "anthropic":
      return "https://api.anthropic.com";
    default:
      return "";
  }
}

// 重置表单
function resetForm() {
  const isCreateMode = !props.group;
  const defaultChannelType = "openai";

  // 先设置渠道类型，这样 computed 属性能正确计算默认值
  formData.channel_type = defaultChannelType;

  Object.assign(formData, {
    name: "",
    display_name: "",
    description: "",
    upstreams: [
      {
        url: isCreateMode ? upstreamPlaceholder.value : "",
        weight: 1,
      },
    ],
    channel_type: defaultChannelType,
    sort: 1,
    test_model: isCreateMode ? testModelPlaceholder.value : "",
    validation_endpoint: "",
    param_overrides: "",
    model_redirect_rules: "",
    model_redirect_strict: false,
    config: {},
    configItems: [],
    header_rules: [],
    proxy_keys: "",
    group_type: "standard",
  });

  // 重置用户修改状态追踪
  if (isCreateMode) {
    userModifiedFields.value = {
      test_model: false,
      upstream: false,
    };
  }
}

// 加载分组数据（编辑模式）
function loadGroupData() {
  if (!props.group) {
    return;
  }

  const configItems = Object.entries(props.group.config || {}).map(([key, value]) => {
    return {
      key,
      value,
    };
  });
  Object.assign(formData, {
    name: props.group.name || "",
    display_name: props.group.display_name || "",
    description: props.group.description || "",
    upstreams: props.group.upstreams?.length
      ? [...props.group.upstreams]
      : [{ url: "", weight: 1 }],
    channel_type: props.group.channel_type || "openai",
    sort: props.group.sort || 1,
    test_model: props.group.test_model || "",
    validation_endpoint: props.group.validation_endpoint || "",
    param_overrides: JSON.stringify(props.group.param_overrides || {}, null, 2),
    model_redirect_rules: JSON.stringify(props.group.model_redirect_rules || {}, null, 2),
    model_redirect_strict: props.group.model_redirect_strict || false,
    config: {},
    configItems,
    header_rules: (props.group.header_rules || []).map((rule: HeaderRuleItem) => ({
      key: rule.key || "",
      value: rule.value || "",
      action: (rule.action as "set" | "remove") || "set",
    })),
    proxy_keys: props.group.proxy_keys || "",
    group_type: props.group.group_type || "standard",
  });

  // 编辑模式下,从 group.available_models 加载缓存的真实模型列表
  const cached = (props.group as unknown as { available_models?: unknown }).available_models;
  if (Array.isArray(cached)) {
    availableModels.value = cached.filter((m): m is string => typeof m === "string");
  } else if (typeof cached === "string" && cached.length > 0) {
    try {
      const arr = JSON.parse(cached);
      availableModels.value = Array.isArray(arr)
        ? arr.filter((m): m is string => typeof m === "string")
        : [];
    } catch {
      availableModels.value = [];
    }
  } else {
    availableModels.value = [];
  }
  modelsRefreshedAt.value =
    (props.group as unknown as { models_refreshed_at?: string | null }).models_refreshed_at || null;
}

// 调用 /api/groups/:id/refresh-models 拉上游真实模型列表
async function refreshModels() {
  if (!props.group?.id) {
    message.warning(t("keys.refreshModelsRequiresSave"));
    return;
  }
  modelsRefreshLoading.value = true;
  try {
    const response = await fetch(`/api/groups/${props.group.id}/refresh-models`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${localStorage.getItem("authKey") || ""}`,
        "Content-Type": "application/json",
      },
    });
    const result = await response.json();
    if (!response.ok || result.code !== 0) {
      throw new Error(result.message || `HTTP ${response.status}`);
    }
    const list = (result.data?.models || []) as string[];
    availableModels.value = list;
    modelsRefreshedAt.value = new Date().toISOString();
    if (list.length > 0) {
      modelsListExpanded.value = ["models"];
    }
    message.success(t("keys.refreshModelsSuccess", { n: list.length }));
  } catch (e) {
    message.error((e as Error).message);
  } finally {
    modelsRefreshLoading.value = false;
  }
}

async function fetchChannelTypes() {
  const options = (await settingsApi.getChannelTypes()) || [];
  channelTypeOptions.value =
    options?.map((type: string) => ({
      label: type,
      value: type,
    })) || [];
  channelTypesFetched.value = true;
}

// 添加上游地址
function addUpstream() {
  formData.upstreams.push({
    url: "",
    weight: 1,
  });
}

// 删除上游地址
function removeUpstream(index: number) {
  if (formData.upstreams.length > 1) {
    formData.upstreams.splice(index, 1);
  } else {
    message.warning(t("keys.atLeastOneUpstream"));
  }
}

async function fetchGroupConfigOptions() {
  const options = await keysApi.getGroupConfigOptions();
  configOptions.value = options || [];
  configOptionsFetched.value = true;
}

// 添加配置项
function addConfigItem() {
  formData.configItems.push({
    key: "",
    value: "",
  });
}

// 删除配置项
function removeConfigItem(index: number) {
  formData.configItems.splice(index, 1);
}

// 添加Header规则
function addHeaderRule() {
  formData.header_rules.push({
    key: "",
    value: "",
    action: "set",
  });
}

// 删除Header规则
function removeHeaderRule(index: number) {
  formData.header_rules.splice(index, 1);
}

// 规范化Header Key到Canonical格式（模拟HTTP标准）
function canonicalHeaderKey(key: string): string {
  if (!key) {
    return key;
  }
  return key
    .split("-")
    .map(part => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase())
    .join("-");
}

// 验证Header Key唯一性（使用Canonical格式对比）
function validateHeaderKeyUniqueness(
  rules: HeaderRuleItem[],
  currentIndex: number,
  key: string
): boolean {
  if (!key.trim()) {
    return true;
  }

  const canonicalKey = canonicalHeaderKey(key.trim());
  return !rules.some(
    (rule, index) => index !== currentIndex && canonicalHeaderKey(rule.key.trim()) === canonicalKey
  );
}

// 当配置项的key改变时，设置默认值
function handleConfigKeyChange(index: number, key: string) {
  const option = configOptions.value.find(opt => opt.key === key);
  if (option) {
    formData.configItems[index].value = option.default_value;
  }
}

const getConfigOption = (key: string) => {
  return configOptions.value.find(opt => opt.key === key);
};

// 关闭弹窗
function handleClose() {
  emit("update:show", false);
}

// 提交表单
async function handleSubmit() {
  if (loading.value) {
    return;
  }

  try {
    await formRef.value?.validate();

    loading.value = true;

    // 验证 JSON 格式
    let paramOverrides = {};
    if (formData.param_overrides) {
      try {
        paramOverrides = JSON.parse(formData.param_overrides);
      } catch {
        message.error(t("keys.invalidJsonFormat"));
        return;
      }
    }

    // 验证模型重定向规则 JSON 格式
    let modelRedirectRules = {};
    if (formData.model_redirect_rules) {
      try {
        modelRedirectRules = JSON.parse(formData.model_redirect_rules);

        // Validate rule format
        for (const [key, value] of Object.entries(modelRedirectRules)) {
          if (typeof key !== "string" || typeof value !== "string") {
            message.error(t("keys.modelRedirectInvalidFormat"));
            return;
          }
          if (key.trim() === "" || (value as string).trim() === "") {
            message.error(t("keys.modelRedirectEmptyModel"));
            return;
          }
        }
      } catch {
        message.error(t("keys.modelRedirectInvalidJson"));
        return;
      }
    }

    // 将configItems转换为config对象
    const config: Record<string, number | string | boolean> = {};
    formData.configItems.forEach((item: ConfigItem) => {
      if (item.key && item.key.trim()) {
        const option = configOptions.value.find(opt => opt.key === item.key);
        if (option && typeof option.default_value === "number" && typeof item.value === "string") {
          const numValue = Number(item.value);
          config[item.key] = isNaN(numValue) ? 0 : numValue;
        } else {
          config[item.key] = item.value;
        }
      }
    });

    // 构建提交数据
    const submitData = {
      name: formData.name,
      display_name: formData.display_name,
      description: formData.description,
      upstreams: formData.upstreams.filter((upstream: UpstreamInfo) => upstream.url.trim()),
      channel_type: formData.channel_type,
      sort: formData.sort,
      test_model: formData.test_model,
      validation_endpoint: formData.validation_endpoint,
      param_overrides: paramOverrides,
      model_redirect_rules: modelRedirectRules,
      model_redirect_strict: formData.model_redirect_strict,
      config,
      header_rules: formData.header_rules
        .filter((rule: HeaderRuleItem) => rule.key.trim())
        .map((rule: HeaderRuleItem) => ({
          key: rule.key.trim(),
          value: rule.value,
          action: rule.action,
        })),
      proxy_keys: formData.proxy_keys,
    };

    let res: Group;
    if (props.group?.id) {
      // 编辑模式 — 不动 model_routing_mode / exposed_models, 由专门的 UI 控制
      res = await keysApi.updateGroup(props.group.id, submitData);
    } else {
      // 新建模式 — 默认 specified + 自动暴露 (已知 provider 取免费集,
      // 未知 provider 仅暴露用户填的 testModel)
      const provider = findProviderByUpstreams(submitData.upstreams) as
        | FreeProvider
        | undefined;
      const newGroupData = {
        ...submitData,
        model_routing_mode: "specified" as const,
        exposed_models: bootstrapExposedModels(provider, submitData.test_model),
      };
      res = await keysApi.createGroup(newGroupData);
    }

    emit("success", res);
    // 如果是新建模式，发出切换到新分组的事件
    if (!props.group?.id && res.id) {
      emit("switchToGroup", res.id);
    }
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
      :title="group ? t('keys.editGroup') : t('keys.createGroup')"
      :bordered="false"
      size="huge"
      role="dialog"
      aria-modal="true"
    >
      <template #header-extra>
        <n-button quaternary circle @click="handleClose" class="v3-modal-close">
          <template #icon>
            <n-icon :component="Close" />
          </template>
        </n-button>
      </template>

      <div class="v3-modal-body">
        <n-form
          ref="formRef"
          :model="formData"
          :rules="rules"
          label-placement="left"
          label-width="120px"
          require-mark-placement="right-hanging"
          class="group-form"
        >
          <!-- 免费 Provider 快速预填(仅创建标准分组时显示) -->
          <div v-if="showProviderPicker" class="form-section provider-picker">
            <n-collapse v-model:expanded-names="providerPanelExpanded">
              <n-collapse-item name="picker">
                <template #header>
                  <div class="provider-picker-header">
                    <n-icon :component="RocketOutline" class="provider-picker-icon" />
                    <span class="section-title" style="margin: 0">
                      {{ t("keys.useFreeProvider") }}
                    </span>
                    <n-tag size="small" type="success" round style="margin-left: 8px">
                      {{ t("keys.providerCount", { n: FREE_PROVIDERS.length }) }}
                    </n-tag>
                  </div>
                </template>
                <p class="provider-picker-tip">{{ t("keys.providerPickerTip") }}</p>
                <n-input
                  v-model:value="providerSearch"
                  :placeholder="t('keys.providerSearchPlaceholder')"
                  clearable
                  style="margin-bottom: 12px"
                />
                <div class="provider-grid">
                  <div
                    v-for="p in filteredProviders"
                    :key="p.id"
                    class="provider-card"
                    :class="{ 'provider-card-used': usedProviderIds.has(p.id) }"
                    @click="applyProvider(p)"
                  >
                    <div class="provider-card-head">
                      <span class="provider-card-name">{{ p.name }}</span>
                      <div class="provider-card-tags">
                        <n-tag
                          v-if="usedProviderIds.has(p.id)"
                          size="tiny"
                          type="success"
                          :bordered="false"
                        >
                          {{ t("keys.providerAdded") }}
                        </n-tag>
                        <n-tag
                          v-if="p.badge"
                          size="tiny"
                          :type="
                            p.badge === 'fast'
                              ? 'warning'
                              : p.badge === 'high-quota'
                                ? 'success'
                                : 'info'
                          "
                        >
                          {{ t(`keys.providerBadge_${p.badge.replace("-", "_")}`) }}
                        </n-tag>
                      </div>
                    </div>
                    <div class="provider-card-tier">{{ p.freeTier }}</div>
                    <a
                      :href="p.signupUrl"
                      target="_blank"
                      rel="noopener noreferrer"
                      class="provider-card-signup"
                      @click.stop
                    >
                      <n-icon :component="OpenOutline" />
                      {{ t("keys.providerSignup") }}
                    </a>
                  </div>
                </div>
              </n-collapse-item>
            </n-collapse>
          </div>

          <!-- 基础信息 -->
          <div class="form-section">
            <h4 class="section-title">{{ t("keys.basicInfo") }}</h4>

            <!-- Group name and display name on the same row -->
            <div class="form-row">
              <n-form-item :label="t('keys.groupName')" path="name" class="form-item-half">
                <template #label>
                  <div class="form-label-with-tooltip">
                    {{ t("keys.groupName") }}
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon :component="HelpCircleOutline" class="help-icon" />
                      </template>
                      {{ t("keys.groupNameTooltip") }}
                    </n-tooltip>
                  </div>
                </template>
                <n-input v-model:value="formData.name" placeholder="gemini" />
              </n-form-item>

              <n-form-item
                :label="t('keys.displayName')"
                path="display_name"
                class="form-item-half"
              >
                <template #label>
                  <div class="form-label-with-tooltip">
                    {{ t("keys.displayName") }}
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon :component="HelpCircleOutline" class="help-icon" />
                      </template>
                      {{ t("keys.displayNameTooltip") }}
                    </n-tooltip>
                  </div>
                </template>
                <n-input v-model:value="formData.display_name" placeholder="Google Gemini" />
              </n-form-item>
            </div>

            <!-- Channel type and sort order on the same row -->
            <div class="form-row">
              <n-form-item
                :label="t('keys.channelType')"
                path="channel_type"
                class="form-item-half"
              >
                <template #label>
                  <div class="form-label-with-tooltip">
                    {{ t("keys.channelType") }}
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon :component="HelpCircleOutline" class="help-icon" />
                      </template>
                      {{ t("keys.channelTypeTooltip") }}
                    </n-tooltip>
                  </div>
                </template>
                <n-select
                  v-model:value="formData.channel_type"
                  :options="channelTypeOptions"
                  :placeholder="t('keys.selectChannelType')"
                />
              </n-form-item>

              <n-form-item :label="t('keys.sortOrder')" path="sort" class="form-item-half">
                <template #label>
                  <div class="form-label-with-tooltip">
                    {{ t("keys.sortOrder") }}
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon :component="HelpCircleOutline" class="help-icon" />
                      </template>
                      {{ t("keys.sortOrderTooltip") }}
                    </n-tooltip>
                  </div>
                </template>
                <n-input-number
                  v-model:value="formData.sort"
                  :min="0"
                  :placeholder="t('keys.sortValue')"
                  style="width: 100%"
                />
              </n-form-item>
            </div>

            <!-- Test model and test path on the same row -->
            <div class="form-row">
              <n-form-item :label="t('keys.testModel')" path="test_model" class="form-item-half">
                <template #label>
                  <div class="form-label-with-tooltip">
                    {{ t("keys.testModel") }}
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon :component="HelpCircleOutline" class="help-icon" />
                      </template>
                      {{ t("keys.testModelTooltip") }}
                    </n-tooltip>
                  </div>
                </template>
                <div style="display: flex; gap: 6px; width: 100%">
                  <n-select
                    v-model:value="formData.test_model"
                    :options="testModelOptions"
                    :placeholder="testModelPlaceholder"
                    filterable
                    tag
                    clearable
                    style="flex: 1"
                    @update:value="() => !props.group && (userModifiedFields.test_model = true)"
                  />
                  <n-tooltip trigger="hover" :disabled="!!props.group">
                    <template #trigger>
                      <n-button
                        size="small"
                        :loading="modelsRefreshLoading"
                        :disabled="!props.group"
                        @click="refreshModels"
                        style="flex-shrink: 0"
                      >
                        <template #icon>
                          <n-icon :component="RefreshOutline" />
                        </template>
                      </n-button>
                    </template>
                    {{ t("keys.refreshModelsRequiresSave") }}
                  </n-tooltip>
                </div>
              </n-form-item>

              <n-form-item
                :label="t('keys.testPath')"
                path="validation_endpoint"
                class="form-item-half"
                v-if="formData.channel_type !== 'gemini'"
              >
                <template #label>
                  <div class="form-label-with-tooltip">
                    {{ t("keys.testPath") }}
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon :component="HelpCircleOutline" class="help-icon" />
                      </template>
                      <div>
                        {{ t("keys.testPathTooltip1") }}
                        <br />
                        • OpenAI: /v1/chat/completions
                        <br />
                        • OpenAI Response: /v1/responses
                        <br />
                        • Anthropic: /v1/messages
                        <br />
                        {{ t("keys.testPathTooltip2") }}
                      </div>
                    </n-tooltip>
                  </div>
                </template>
                <n-input
                  v-model:value="formData.validation_endpoint"
                  :placeholder="
                    validationEndpointPlaceholder || t('keys.optionalCustomValidationPath')
                  "
                />
              </n-form-item>

              <!-- When gemini channel, test path is hidden, need placeholder div to keep layout -->
              <div v-else class="form-item-half" />
            </div>

            <!-- Proxy keys -->
            <n-form-item :label="t('keys.proxyKeys')" path="proxy_keys">
              <template #label>
                <div class="form-label-with-tooltip">
                  {{ t("keys.proxyKeys") }}
                  <n-tooltip trigger="hover" placement="top">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon" />
                    </template>
                    {{ t("keys.proxyKeysTooltip") }}
                  </n-tooltip>
                </div>
              </template>
              <proxy-keys-input
                v-model="formData.proxy_keys"
                :placeholder="t('keys.multiKeysPlaceholder')"
                size="medium"
              />
            </n-form-item>

            <!-- Description takes full row -->
            <n-form-item :label="t('common.description')" path="description">
              <template #label>
                <div class="form-label-with-tooltip">
                  {{ t("common.description") }}
                  <n-tooltip trigger="hover" placement="top">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon" />
                    </template>
                    {{ t("keys.descriptionTooltip") }}
                  </n-tooltip>
                </div>
              </template>
              <n-input
                v-model:value="formData.description"
                type="textarea"
                placeholder=""
                :rows="1"
                :autosize="{ minRows: 1, maxRows: 5 }"
                style="resize: none"
              />
            </n-form-item>
          </div>

          <!-- Upstream available models -->
          <div class="form-section upstream-models-section" style="margin-top: 10px">
            <n-collapse v-model:expanded-names="modelsListExpanded">
              <n-collapse-item name="models">
                <template #header>
                  <div class="upstream-models-header">
                    <span class="section-title" style="margin: 0; padding: 0; border: none">
                      {{ t("keys.upstreamModelsList") }}
                    </span>
                    <n-tag size="tiny" type="info" :bordered="false">
                      {{ availableModels.length }}
                    </n-tag>
                    <span v-if="modelsRefreshedAt" class="hint" style="margin-left: 8px">
                      {{ t("keys.lastRefreshed", { at: refreshedAtDisplay(modelsRefreshedAt) }) }}
                    </span>
                  </div>
                </template>

                <div v-if="availableModels.length === 0" class="empty-models-hint">
                  <n-empty
                    size="small"
                    :description="
                      props.group
                        ? t('keys.upstreamModelsEmpty')
                        : t('keys.refreshModelsRequiresSave')
                    "
                  />
                </div>

                <template v-else>
                  <div class="models-toolbar">
                    <n-input
                      v-model:value="modelsListSearch"
                      :placeholder="t('keys.searchModelPlaceholder')"
                      clearable
                      style="width: 240px"
                    />
                    <n-button
                      size="small"
                      :loading="modelsRefreshLoading"
                      :disabled="!props.group"
                      @click="refreshModels"
                    >
                      <template #icon>
                        <n-icon :component="RefreshOutline" />
                      </template>
                      {{ t("common.refresh") }}
                    </n-button>
                  </div>

                  <div class="models-grid">
                    <div v-for="m in filteredAvailableModels" :key="m" class="model-item">
                      <span
                        v-if="modelIsFree(m)"
                        class="model-item-free"
                        :title="t('modelcatalog.freeTag')"
                      >🆓</span>
                      <span class="model-item-id" :title="m">{{ m }}</span>
                      <button
                        class="model-item-copy"
                        :title="t('common.copy')"
                        @click="copyModelId(m)"
                      >
                        <n-icon :component="CopyOutline" :size="14" />
                      </button>
                    </div>
                  </div>
                </template>
              </n-collapse-item>
            </n-collapse>
          </div>

          <!-- Upstream addresses -->
          <div class="form-section" style="margin-top: 10px">
            <h4 class="section-title">{{ t("keys.upstreamAddresses") }}</h4>
            <n-form-item
              v-for="(upstream, index) in formData.upstreams"
              :key="index"
              :label="`${t('keys.upstream')} ${index + 1}`"
              :path="`upstreams[${index}].url`"
              :rule="{
                required: true,
                message: '',
                trigger: ['blur', 'input'],
              }"
            >
              <template #label>
                <div class="form-label-with-tooltip">
                  {{ t("keys.upstream") }} {{ index + 1 }}
                  <n-tooltip trigger="hover" placement="top">
                    <template #trigger>
                      <n-icon :component="HelpCircleOutline" class="help-icon" />
                    </template>
                    {{ t("keys.upstreamTooltip") }}
                  </n-tooltip>
                </div>
              </template>
              <div class="upstream-row">
                <div class="upstream-url">
                  <n-input
                    v-model:value="upstream.url"
                    :placeholder="upstreamPlaceholder"
                    @input="
                      () => !props.group && index === 0 && (userModifiedFields.upstream = true)
                    "
                  />
                </div>
                <div class="upstream-weight">
                  <span class="weight-label">{{ t("keys.weight") }}</span>
                  <n-tooltip trigger="hover" placement="top" style="width: 100%">
                    <template #trigger>
                      <n-input-number
                        v-model:value="upstream.weight"
                        :min="0"
                        :placeholder="t('keys.weight')"
                        style="width: 100%"
                      />
                    </template>
                    {{ t("keys.weightTooltip") }}
                  </n-tooltip>
                </div>
                <div class="upstream-actions">
                  <n-button
                    v-if="formData.upstreams.length > 1"
                    @click="removeUpstream(index)"
                    type="error"
                    quaternary
                    circle
                    size="small"
                  >
                    <template #icon>
                      <n-icon :component="Remove" />
                    </template>
                  </n-button>
                </div>
              </div>
            </n-form-item>

            <n-form-item>
              <n-button @click="addUpstream" dashed style="width: 100%">
                <template #icon>
                  <n-icon :component="Add" />
                </template>
                {{ t("keys.addUpstream") }}
              </n-button>
            </n-form-item>
          </div>

          <!-- Advanced configuration -->
          <div class="form-section" style="margin-top: 10px">
            <n-collapse>
              <n-collapse-item name="advanced">
                <template #header>{{ t("keys.advancedConfig") }}</template>
                <div class="config-section">
                  <h5 class="config-title-with-tooltip">
                    {{ t("keys.groupConfig") }}
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                      </template>
                      {{ t("keys.groupConfigTooltip") }}
                    </n-tooltip>
                  </h5>

                  <div class="config-items">
                    <n-form-item
                      v-for="(configItem, index) in formData.configItems"
                      :key="index"
                      class="config-item-row"
                      :label="`${t('keys.config')} ${index + 1}`"
                      :path="`configItems[${index}].key`"
                      :rule="{
                        required: true,
                        message: '',
                        trigger: ['blur', 'change'],
                      }"
                    >
                      <template #label>
                        <div class="form-label-with-tooltip">
                          {{ t("keys.config") }} {{ index + 1 }}
                          <n-tooltip trigger="hover" placement="top">
                            <template #trigger>
                              <n-icon :component="HelpCircleOutline" class="help-icon" />
                            </template>
                            {{ t("keys.configTooltip") }}
                          </n-tooltip>
                        </div>
                      </template>
                      <div class="config-item-content">
                        <div class="config-select">
                          <n-select
                            v-model:value="configItem.key"
                            :options="
                              configOptions.map(opt => ({
                                label: opt.name,
                                value: opt.key,
                                disabled:
                                  formData.configItems
                                    .map((item: ConfigItem) => item.key)
                                    ?.includes(opt.key) && opt.key !== configItem.key,
                              }))
                            "
                            :placeholder="t('keys.selectConfigParam')"
                            @update:value="value => handleConfigKeyChange(index, value)"
                            clearable
                          />
                        </div>
                        <div class="config-value">
                          <n-tooltip trigger="hover" placement="top">
                            <template #trigger>
                              <n-input-number
                                v-if="typeof configItem.value === 'number'"
                                v-model:value="configItem.value"
                                :placeholder="t('keys.paramValue')"
                                :precision="0"
                                style="width: 100%"
                              />
                              <n-switch
                                v-else-if="typeof configItem.value === 'boolean'"
                                v-model:value="configItem.value"
                                size="small"
                              />
                              <n-input
                                v-else
                                v-model:value="configItem.value"
                                :placeholder="t('keys.paramValue')"
                              />
                            </template>
                            {{
                              getConfigOption(configItem.key)?.description ||
                              t("keys.setConfigValue")
                            }}
                          </n-tooltip>
                        </div>
                        <div class="config-actions">
                          <n-button
                            @click="removeConfigItem(index)"
                            type="error"
                            quaternary
                            circle
                            size="small"
                          >
                            <template #icon>
                              <n-icon :component="Remove" />
                            </template>
                          </n-button>
                        </div>
                      </div>
                    </n-form-item>
                  </div>

                  <div style="margin-top: 12px; padding-left: 120px">
                    <n-button
                      @click="addConfigItem"
                      dashed
                      style="width: 100%"
                      :disabled="formData.configItems.length >= configOptions.length"
                    >
                      <template #icon>
                        <n-icon :component="Add" />
                      </template>
                      {{ t("keys.addConfigParam") }}
                    </n-button>
                  </div>
                </div>

                <div class="config-section">
                  <h5 class="config-title-with-tooltip">
                    {{ t("keys.customHeaders") }}
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                      </template>
                      <div>
                        {{ t("keys.headerRulesTooltip1") }}
                        <br />
                        {{ t("keys.supportedVariables") }}：
                        <br />
                        • ${CLIENT_IP} - {{ t("keys.clientIpVar") }}
                        <br />
                        • ${GROUP_NAME} - {{ t("keys.groupNameVar") }}
                        <br />
                        • ${API_KEY} - {{ t("keys.apiKeyVar") }}
                        <br />
                        • ${TIMESTAMP_MS} - {{ t("keys.timestampMsVar") }}
                        <br />
                        • ${TIMESTAMP_S} - {{ t("keys.timestampSVar") }}
                      </div>
                    </n-tooltip>
                  </h5>

                  <div class="header-rules-items">
                    <n-form-item
                      v-for="(headerRule, index) in formData.header_rules"
                      :key="index"
                      class="header-rule-row"
                      :label="`${t('keys.header')} ${index + 1}`"
                    >
                      <template #label>
                        <div class="form-label-with-tooltip">
                          {{ t("keys.header") }} {{ index + 1 }}
                          <n-tooltip trigger="hover" placement="top">
                            <template #trigger>
                              <n-icon :component="HelpCircleOutline" class="help-icon" />
                            </template>
                            {{ t("keys.headerTooltip") }}
                          </n-tooltip>
                        </div>
                      </template>
                      <div class="header-rule-content">
                        <div class="header-name">
                          <n-input
                            v-model:value="headerRule.key"
                            :placeholder="t('keys.headerName')"
                            :status="
                              !validateHeaderKeyUniqueness(
                                formData.header_rules,
                                index,
                                headerRule.key
                              )
                                ? 'error'
                                : undefined
                            "
                          />
                          <div
                            v-if="
                              !validateHeaderKeyUniqueness(
                                formData.header_rules,
                                index,
                                headerRule.key
                              )
                            "
                            class="error-message"
                          >
                            {{ t("keys.duplicateHeader") }}
                          </div>
                        </div>
                        <div class="header-value" v-if="headerRule.action === 'set'">
                          <n-input
                            v-model:value="headerRule.value"
                            :placeholder="t('keys.headerValuePlaceholder')"
                          />
                        </div>
                        <div class="header-value removed-placeholder" v-else>
                          <span class="removed-text">{{ t("keys.willRemoveFromRequest") }}</span>
                        </div>
                        <div class="header-action">
                          <n-tooltip trigger="hover" placement="top">
                            <template #trigger>
                              <n-switch
                                v-model:value="headerRule.action"
                                :checked-value="'remove'"
                                :unchecked-value="'set'"
                                size="small"
                              />
                            </template>
                            {{ t("keys.removeToggleTooltip") }}
                          </n-tooltip>
                        </div>
                        <div class="header-actions">
                          <n-button
                            @click="removeHeaderRule(index)"
                            type="error"
                            quaternary
                            circle
                            size="small"
                          >
                            <template #icon>
                              <n-icon :component="Remove" />
                            </template>
                          </n-button>
                        </div>
                      </div>
                    </n-form-item>
                  </div>

                  <div style="margin-top: 12px; padding-left: 120px">
                    <n-button @click="addHeaderRule" dashed style="width: 100%">
                      <template #icon>
                        <n-icon :component="Add" />
                      </template>
                      {{ t("keys.addHeader") }}
                    </n-button>
                  </div>
                </div>

                <!-- 模型重定向配置 -->
                <div v-if="formData.group_type !== 'aggregate'" class="config-section">
                  <n-form-item path="model_redirect_strict">
                    <template #label>
                      <div class="form-label-with-tooltip">
                        {{ t("keys.modelRedirectPolicy") }}
                        <n-tooltip trigger="hover" placement="top">
                          <template #trigger>
                            <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                          </template>
                          {{ t("keys.modelRedirectPolicyTooltip") }}
                        </n-tooltip>
                      </div>
                    </template>
                    <div style="display: flex; align-items: center; gap: 12px">
                      <n-switch v-model:value="formData.model_redirect_strict" />
                      <span style="font-size: 14px; color: #666">
                        {{
                          formData.model_redirect_strict
                            ? t("keys.modelRedirectStrictMode")
                            : t("keys.modelRedirectLooseMode")
                        }}
                      </span>
                    </div>
                    <template #feedback>
                      <div style="font-size: 12px; color: #999; margin: 4px 0">
                        <div v-if="formData.model_redirect_strict" style="color: #f5a623">
                          ⚠️ {{ t("keys.modelRedirectStrictWarning") }}
                        </div>
                        <div v-else style="color: #52c41a">
                          ✅ {{ t("keys.modelRedirectLooseInfo") }}
                        </div>
                      </div>
                    </template>
                  </n-form-item>

                  <n-form-item path="model_redirect_rules">
                    <template #label>
                      <div class="form-label-with-tooltip">
                        {{ t("keys.modelRedirectRules") }}
                        <n-tooltip trigger="hover" placement="top">
                          <template #trigger>
                            <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                          </template>
                          {{ t("keys.modelRedirectRulesTooltip") }}
                        </n-tooltip>
                      </div>
                    </template>
                    <n-input
                      v-model:value="formData.model_redirect_rules"
                      type="textarea"
                      :placeholder="modelRedirectTip"
                      :rows="4"
                    />
                    <template #feedback>
                      <div style="font-size: 14px; color: #999">
                        {{ t("keys.modelRedirectRulesDescription") }}
                      </div>
                    </template>
                  </n-form-item>
                </div>

                <div class="config-section">
                  <n-form-item path="param_overrides">
                    <template #label>
                      <div class="form-label-with-tooltip">
                        {{ t("keys.paramOverrides") }}
                        <n-tooltip trigger="hover" placement="top">
                          <template #trigger>
                            <n-icon :component="HelpCircleOutline" class="help-icon config-help" />
                          </template>
                          {{ t("keys.paramOverridesTooltip") }}
                        </n-tooltip>
                      </div>
                    </template>
                    <n-input
                      v-model:value="formData.param_overrides"
                      type="textarea"
                      placeholder='{"temperature": 0.7}'
                      :rows="4"
                    />
                  </n-form-item>
                </div>
              </n-collapse-item>
            </n-collapse>
          </div>
        </n-form>
      </div>

      <template #action>
        <div class="v3-modal-footer">
          <div />
          <div class="v3-modal-actions">
            <n-button size="small" @click="handleClose" :disabled="loading">
              {{ t("common.cancel") }}
            </n-button>
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
  width: 800px;
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

.form-section {
  margin-top: 20px;
}

.section-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--v3-text-primary);
  margin: 0 0 16px 0;
  padding-bottom: 8px;
  border-bottom: 2px solid var(--v3-line);
}

.provider-picker {
  margin-top: 0;
}

.provider-picker-header {
  display: flex;
  align-items: center;
  gap: 6px;
}

.provider-picker-icon {
  font-size: 18px;
  color: var(--v3-primary);
}

.provider-picker-tip {
  color: var(--v3-text-secondary);
  font-size: 13px;
  margin: 0 0 12px 0;
}

.provider-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
  gap: 8px;
}

.provider-card {
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius-sm);
  padding: 6px 10px;
  cursor: pointer;
  transition:
    border-color 0.15s,
    box-shadow 0.15s,
    transform 0.05s;
  background: var(--v3-surface);
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.provider-card:hover {
  border-color: var(--v3-primary);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
}

.provider-card:active {
  transform: scale(0.99);
}

.provider-card-used {
  background: var(--v3-primary-bg);
  border-style: dashed;
}

.provider-card-used .provider-card-name::after {
  content: " ✓";
  color: var(--v3-primary);
  font-weight: 700;
}

.provider-card-tags {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.upstream-models-section {
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius-md);
  padding: 6px 12px;
}

/* 去掉 n-collapse-item 内容区默认 padding,避免 header/toolbar 间一大块空白 */
.upstream-models-section :deep(.n-collapse-item__content-inner) {
  padding-top: 4px !important;
}

.upstream-models-header {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.empty-models-hint {
  padding: 4px 0;
}

.models-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 8px;
}

.models-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 4px;
  max-height: 360px;
  overflow-y: auto;
  padding: 2px;
}

.model-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius-sm);
  background: var(--v3-surface);
  font-size: 12px;
  transition:
    border-color 0.15s,
    background 0.15s;
}

.model-item:hover {
  border-color: var(--v3-primary);
  background: var(--v3-primary-bg);
}

.model-item-free {
  font-size: 13px;
  line-height: 1;
  flex-shrink: 0;
  cursor: help;
}

.model-item-id {
  font-family: var(--v3-mono, monospace);
  font-size: 12px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1;
  min-width: 0;
}

.model-item-copy {
  border: 0;
  background: transparent;
  padding: 2px 4px;
  border-radius: 4px;
  cursor: pointer;
  color: var(--v3-ink-3);
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  transition: all 120ms;
}
.model-item-copy:hover {
  background: var(--v3-accent-soft);
  color: var(--v3-accent);
}

.hint {
  color: var(--v3-text-secondary);
  font-size: 12px;
}

.provider-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 4px;
}

.provider-card-name {
  font-weight: 600;
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.provider-card-tier {
  font-size: 11px;
  color: var(--v3-primary);
  line-height: 1.3;
}

.provider-card-signup {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  font-size: 11px;
  color: var(--v3-text-secondary);
  text-decoration: none;
  margin-top: 2px;
}

.provider-card-signup:hover {
  color: var(--v3-primary);
  text-decoration: underline;
}

:deep(.n-form-item-label) {
  font-weight: 500;
}

:deep(.n-form-item-blank) {
  flex-grow: 1;
}

:deep(.n-input) {
  --n-border-radius: var(--v3-radius-sm);
}

:deep(.n-select) {
  --n-border-radius: var(--v3-radius-sm);
}

:deep(.n-input-number) {
  --n-border-radius: var(--v3-radius-sm);
}

:deep(.n-card-header) {
  border-bottom: 1px solid var(--v3-line);
  padding: 10px 20px;
}

:deep(.n-card__content) {
  max-height: calc(100vh - 68px - 61px - 50px);
  overflow-y: auto;
}

:deep(.n-card__footer) {
  border-top: 1px solid var(--v3-line);
  padding: 10px 15px;
}

:deep(.n-form-item-feedback-wrapper) {
  min-height: 10px;
}

.config-section {
  margin-top: 16px;
}

.config-title {
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--v3-text-primary);
  margin: 0 0 12px 0;
}

.form-label {
  margin-left: 25px;
  margin-right: 10px;
  height: 34px;
  line-height: 34px;
  font-weight: 500;
}

/* Tooltip related styles */
.form-label-with-tooltip {
  display: flex;
  align-items: center;
  gap: 6px;
}

.help-icon {
  color: var(--v3-text-tertiary);
  font-size: 14px;
  cursor: help;
  transition: color 0.2s ease;
}

.help-icon:hover {
  color: var(--v3-primary);
}

.section-title-with-tooltip {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
}

.section-help {
  font-size: 16px;
}

.collapse-header-with-tooltip {
  display: flex;
  align-items: center;
  gap: 6px;
  font-weight: 500;
}

.collapse-help {
  font-size: 14px;
}

.config-title-with-tooltip {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--v3-text-primary);
  margin: 0 0 12px 0;
}

.config-help {
  font-size: 13px;
}

/* Enhanced form styles */
:deep(.n-form-item-label) {
  font-weight: 500;
  color: var(--v3-text-primary);
}

:deep(.n-input) {
  --n-border-radius: var(--v3-radius-sm);
  --n-border: 1px solid var(--v3-line);
  --n-border-hover: 1px solid var(--v3-primary);
  --n-border-focus: 1px solid var(--v3-primary);
  --n-box-shadow-focus: 0 0 0 2px var(--v3-primary-bg);
}

:deep(.n-select) {
  --n-border-radius: var(--v3-radius-sm);
}

:deep(.n-input-number) {
  --n-border-radius: var(--v3-radius-sm);
}

:deep(.n-button) {
  --n-border-radius: var(--v3-radius-sm);
}

/* Tooltip styles */
:deep(.n-tooltip__trigger) {
  display: inline-flex;
  align-items: center;
}

:deep(.n-tooltip) {
  --n-font-size: 13px;
  --n-border-radius: var(--v3-radius-sm);
}

:deep(.n-tooltip .n-tooltip__content) {
  max-width: 320px;
  line-height: 1.5;
}

:deep(.n-tooltip .n-tooltip__content div) {
  white-space: pre-line;
}

/* Collapse panel style optimization */
:deep(.n-collapse-item__header) {
  font-weight: 500;
  color: var(--v3-text-primary);
}

:deep(.n-collapse-item) {
  --n-title-padding: 16px 0;
}

:deep(.n-base-selection-label) {
  height: 40px;
}

/* 表单行布局 */
.form-row {
  display: flex;
  gap: 20px;
  align-items: flex-start;
}

.form-item-half {
  flex: 1;
  width: 50%;
}

/* 上游地址行布局 */
.upstream-row {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
}

.upstream-url {
  flex: 1;
}

.upstream-weight {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 140px;
}

.weight-label {
  font-weight: 500;
  color: var(--v3-text-primary);
  white-space: nowrap;
}

.upstream-actions {
  flex: 0 0 32px;
  display: flex;
  justify-content: center;
}

/* 配置项行布局 */
.config-item-row {
  margin-bottom: 12px;
}

.config-item-content {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
}

.config-select {
  flex: 0 0 200px;
}

.config-value {
  flex: 1;
}

.config-actions {
  flex: 0 0 32px;
  display: flex;
  justify-content: center;
}

@media (max-width: 768px) {
  .group-form-card {
    width: 100vw !important;
  }

  .group-form {
    width: auto !important;
  }

  .form-row {
    flex-direction: column;
    gap: 0;
  }

  .form-item-half {
    width: 100%;
  }

  .section-title {
    font-size: 0.9rem;
  }

  .upstream-row,
  .config-item-content {
    flex-direction: column;
    gap: 8px;
    align-items: stretch;
  }

  .upstream-weight {
    flex: 1;
    flex-direction: column;
    align-items: flex-start;
  }

  .config-value {
    flex: 1;
  }

  .upstream-actions,
  .config-actions {
    justify-content: flex-end;
  }
}

/* Header规则相关样式 */
.header-rule-row {
  margin-bottom: 12px;
}

.header-rule-content {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  width: 100%;
}

.header-name {
  flex: 0 0 200px;
  position: relative;
}

.header-value {
  flex: 1;
  display: flex;
  align-items: center;
  min-height: 34px;
}

.header-value.removed-placeholder {
  justify-content: center;
  background-color: var(--v3-surface);
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius-sm);
  padding: 0 12px;
}

.removed-text {
  color: var(--v3-text-tertiary);
  font-style: italic;
  font-size: 13px;
}

.header-action {
  flex: 0 0 50px;
  display: flex;
  justify-content: center;
  align-items: center;
  height: 34px;
}

.header-actions {
  flex: 0 0 32px;
  display: flex;
  justify-content: center;
  align-items: flex-start;
  height: 34px;
}

.error-message {
  position: absolute;
  top: 100%;
  left: 0;
  font-size: 12px;
  color: var(--v3-error);
  margin-top: 2px;
}

/* Header rule related styles */
.header-rule-row {
  margin-bottom: 12px;
}

.header-rule-content {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  width: 100%;
}

.header-name {
  flex: 0 0 200px;
  position: relative;
}

.header-value {
  flex: 1;
  display: flex;
  align-items: center;
  min-height: 34px;
}

.header-action {
  flex: 0 0 50px;
  display: flex;
  justify-content: center;
  align-items: center;
  height: 34px;
}

.header-actions {
  flex: 0 0 32px;
  display: flex;
  justify-content: center;
  align-items: flex-start;
  height: 34px;
}

@media (max-width: 768px) {
  .v3-modal-card {
    width: 100vw !important;
  }

  .group-form {
    width: auto !important;
  }

  .form-row {
    flex-direction: column;
    gap: 0;
  }

  .form-item-half {
    width: 100%;
  }

  .section-title {
    font-size: 0.9rem;
  }

  .upstream-row,
  .config-item-content {
    flex-direction: column;
    gap: 8px;
    align-items: stretch;
  }

  .upstream-weight {
    flex: 1;
    flex-direction: column;
    align-items: flex-start;
  }

  .config-value {
    flex: 1;
  }

  .upstream-actions,
  .config-actions {
    justify-content: flex-end;
  }

  .header-rule-content {
    flex-direction: column;
    gap: 8px;
    align-items: stretch;
  }

  .header-name,
  .header-value {
    flex: 1;
  }

  .header-actions {
    justify-content: flex-end;
  }
}
</style>
