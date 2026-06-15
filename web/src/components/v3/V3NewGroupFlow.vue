<script setup lang="ts">
// 添加 Provider — 智能识别 + 折叠 summary 设计 (P11.28).
//
// 设计原则:
//   1. 把决策疲劳降到最低: 不让用户填一堆字段, 自动填 + 默认折叠 summary.
//   2. 80% 场景 = "我有 Key, 加进去就行": 粘 Key 自动识别 provider + 自动选
//      推荐 testModel + 创建.
//   3. 全程支持用户改动 (展开 summary 改 name/baseUrl/testModel/channelType).
//   4. Key 可选, 不填可创建空 group 再去 detail 页补.
//
// 流程:
//   选 Provider (搜索 / 推荐 chip / 官方 / 手动配置)
//        +
//   贴 Key (可选, 触发智能识别自动选 provider)
//        ↓ 选了 provider 后展开 summary
//   确认 (默认折叠成一行 [展开编辑] / 改字段)
//        ↓
//   创建 → emit success → sidebar 选中新 group + 跳 keys tab
import { keysApi } from "@/api/keys";
import { detectFromText, type KeyDetection } from "@/data/keyDetector";
import {
  FREE_PROVIDERS,
  OFFICIAL_PROVIDERS,
  FREE_MODELS,
  bootstrapExposedModels,
  isRecommended,
  type FreeProvider,
} from "@/data/freeProviders";
import type { Group } from "@/types/models";
import { CheckmarkOutline, CloseOutline, KeyOutline, OpenOutline } from "@vicons/ionicons5";
import { NIcon, NModal } from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import ProviderLogo from "@/components/common/ProviderLogo.vue";
import { hasProviderLogo } from "@/data/providerLogos";

const { t } = useI18n();
const router = useRouter();
const route = useRoute();

interface Props {
  show: boolean;
  existingGroupNames?: string[];
}
interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success", group: Group): void;
}
const props = withDefaults(defineProps<Props>(), {
  existingGroupNames: () => [],
});
const emit = defineEmits<Emits>();

// ============================================================================
// State
// ============================================================================

const picked = ref<FreeProvider | null>(null);

// 表单字段 — 选了模板自动填, 始终可见可改. 没选模板时整段 hidden.
const formName = ref("");
const formBaseUrl = ref("");
const formChannelType = ref<FreeProvider["channelType"]>("openai");
const formTestModel = ref("");
const keysInput = ref("");

// UI state
const providerSearch = ref("");
const showAllFree = ref(false); // 默认只显示推荐, 点 "更多" 展开全部 free
const submitting = ref(false);

// ============================================================================
// Provider catalog & filtering
// ============================================================================

interface CatalogItem {
  provider: FreeProvider;
  kind: "official" | "recommended" | "free" | "custom";
}

// 哨兵 — 代表"自定义/passthrough"入口; 不来自任何 provider 数据集
const CUSTOM_SENTINEL: FreeProvider = {
  id: "__custom__",
  name: "Custom",
  recommendedGroupName: "custom",
  recommendedDisplayName: "Custom",
  description: "",
  baseUrl: "",
  channelType: "openai",
  testModel: "",
  models: [],
  freeTier: "",
  signupUrl: "",
  docsUrl: "",
  upstreamHosts: [],
  verifiedAt: "",
};

// "推荐" 优先: 有 isRecommended model + verifiedAt 较新的 free provider
const recommendedIds = computed(() => {
  const ids = new Set<string>();
  for (const m of FREE_MODELS) {
    if (m.recommended) {
      ids.add(m.providerId);
    }
  }
  return ids;
});

const officialCatalog = computed<CatalogItem[]>(() =>
  OFFICIAL_PROVIDERS.map(p => ({ provider: p, kind: "official" as const })),
);

const recommendedCatalog = computed<CatalogItem[]>(() =>
  FREE_PROVIDERS.filter(p => recommendedIds.value.has(p.id)).map(p => ({
    provider: p,
    kind: "recommended" as const,
  })),
);

const restFreeCatalog = computed<CatalogItem[]>(() =>
  FREE_PROVIDERS.filter(p => !recommendedIds.value.has(p.id)).map(p => ({
    provider: p,
    kind: "free" as const,
  })),
);

// 搜索时不分组, 一起显示匹配的; 没搜索时按 推荐/官方/其余 分组
const hasSearch = computed(() => providerSearch.value.trim().length > 0);

function matchesSearch(p: FreeProvider): boolean {
  if (!hasSearch.value) {
    return true;
  }
  const q = providerSearch.value.trim().toLowerCase();
  return (
    p.id.toLowerCase().includes(q) ||
    p.name.toLowerCase().includes(q) ||
    p.recommendedDisplayName.toLowerCase().includes(q)
  );
}

const searchResults = computed<CatalogItem[]>(() => {
  if (!hasSearch.value) {
    return [];
  }
  const all = [...officialCatalog.value, ...recommendedCatalog.value, ...restFreeCatalog.value];
  return all.filter(item => matchesSearch(item.provider));
});

// ============================================================================
// 智能识别 — 粘 key 时自动选 provider
// ============================================================================

const smartDetection = computed<KeyDetection | null>(() => {
  const k = keysInput.value.trim();
  if (!k) {
    return null;
  }
  return detectFromText(k);
});

// 用户粘 key 时, 如果识别出 provider 且高置信度且当前没选 picked, 自动选
watch(smartDetection, det => {
  if (!det || picked.value) {
    return;
  }
  if (det.confidence !== "high") {
    return;
  }
  const provider = [...FREE_PROVIDERS, ...OFFICIAL_PROVIDERS].find(p => p.id === det.providerId);
  if (provider) {
    pickProvider(provider);
  }
});

const detectedKeyCount = computed(() => {
  return keysInput.value
    .split(/[\n,]/)
    .map(k => k.trim())
    .filter(Boolean).length;
});

// ============================================================================
// 选 provider — 触发自动填字段
// ============================================================================

// 选 provider: 全部表单字段重置成模板推荐值. 即使用户之前改过 name/baseUrl,
// 也会覆盖 — 切换 provider 是主动行为, 期望全新开始.
function pickProvider(p: FreeProvider) {
  picked.value = p;
  // name 防重: 已存在的名字递增 -2, -3 ...
  let name = p.recommendedGroupName;
  if (props.existingGroupNames.includes(name)) {
    let i = 2;
    while (props.existingGroupNames.includes(`${name}-${i}`)) {
      i += 1;
    }
    name = `${name}-${i}`;
  }
  formName.value = name;
  formBaseUrl.value = p.baseUrl;
  formChannelType.value = p.channelType;
  formTestModel.value = firstRecommendedModel(p);
}

// custom 模式 — 选了哨兵 provider
const isCustomMode = computed(() => picked.value?.id === "__custom__");

// 进入 custom 模式: 把哨兵设为 picked, 填默认名, 清空 baseUrl/testModel 等待用户填
function pickCustom() {
  picked.value = CUSTOM_SENTINEL;
  let name = "custom";
  if (props.existingGroupNames.includes(name)) {
    let i = 2;
    while (props.existingGroupNames.includes(`${name}-${i}`)) {
      i += 1;
    }
    name = `${name}-${i}`;
  }
  formName.value = name;
  formBaseUrl.value = "";
  formChannelType.value = "openai";
  formTestModel.value = "";
}

// 第一个推荐模型 — 优先 isRecommended, 没有就 freeProviders 自带 testModel
function firstRecommendedModel(p: FreeProvider): string {
  const rec = FREE_MODELS.find(m => m.recommended && m.providerId === p.id);
  if (rec) {
    return rec.modelId;
  }
  return p.testModel;
}

// 当前 picked 的可选模型清单 (用于 testModel 下拉)
const availableModelsForPicked = computed(() => {
  const p = picked.value;
  if (!p) {
    return [];
  }
  const list = new Set<string>([...p.models, ...(p.paidModels ?? [])]);
  return Array.from(list);
});

// ============================================================================
// Reset on open
// ============================================================================

watch(
  () => props.show,
  v => {
    if (!v) {
      return;
    }
    picked.value = null;
    formName.value = "";
    formBaseUrl.value = "";
    formChannelType.value = "openai";
    formTestModel.value = "";
    keysInput.value = "";
    providerSearch.value = "";
    showAllFree.value = false;
    submitting.value = false;
    remotelyTakenNames.value = new Set();
  },
);

// ============================================================================
// 创建可行性 — name + baseUrl 填了即可
// ============================================================================

// 后端拒绝过的名字 (DUPLICATE_RESOURCE) — props.existingGroupNames 只含 active
// groups, 看不到 soft-deleted 占位. 后端 partial unique 拒绝时把这次的 name 记
// 进黑名单, form warning 立即显示, 用户改名提交不再撞同一堵墙.
const remotelyTakenNames = ref<Set<string>>(new Set());

// 重名实时检测 — active groups (前端可见) + 后端拒绝过的 (soft-deleted 等).
// 命中显示 warning + disable 创建按钮.
const nameExists = computed(() => {
  const n = formName.value.trim();
  if (!n) {
    return false;
  }
  return props.existingGroupNames.includes(n) || remotelyTakenNames.value.has(n);
});

const canCreate = computed(
  () =>
    picked.value !== null &&
    formName.value.trim().length > 0 &&
    formBaseUrl.value.trim().length > 0 &&
    !nameExists.value,
);

// ============================================================================
// 创建
// ============================================================================

function close() {
  emit("update:show", false);
}

function openSignupPage() {
  if (picked.value?.signupUrl) {
    window.open(picked.value.signupUrl, "_blank", "noopener");
  }
}

async function createProvider() {
  if (!canCreate.value || submitting.value) {
    return;
  }
  submitting.value = true;
  try {
    const p = picked.value;
    // 走 picked(非 custom): specified 模式 + bootstrap exposed_models;
    // 走 custom 哨兵 / 无 picked: passthrough, 等 detail 页拉真实 /v1/models 后再决定 expose 啥.
    const isCustom = isCustomMode.value;
    const providerConfig: Record<string, number> = {};
    if (!isCustom && p?.rpmLimit !== undefined && p.rpmLimit > 0) {
      providerConfig.rpm_limit = p.rpmLimit;
    }
    if (!isCustom && p?.rpdLimit !== undefined && p.rpdLimit > 0) {
      providerConfig.rpd_limit = p.rpdLimit;
    }
    const submit: Partial<Group> = {
      name: formName.value.trim(),
      // 选了模板优先用 provider 的 display name (含空格/大小写好看); custom 走 group name
      display_name: isCustom ? formName.value.trim() : (p?.recommendedDisplayName || formName.value.trim()),
      description: isCustom ? "" : (p?.description || ""),
      channel_type: formChannelType.value,
      upstreams: [{ url: formBaseUrl.value.trim(), weight: 1 }],
      test_model: formTestModel.value || "",
      sort: 0,
      validation_endpoint: "",
      param_overrides: {},
      model_redirect_rules: {},
      model_redirect_strict: false,
      config: providerConfig,
      header_rules: [],
      proxy_keys: "",
      model_routing_mode: isCustom ? "passthrough" : "specified",
      exposed_models: isCustom ? [] : bootstrapExposedModels(p!, formTestModel.value),
    };
    // staticModels 类 (LongCat): 直接把已知清单写入 available_models, 免去之后 refresh 404
    if (!isCustom && p?.staticModels) {
      submit.available_models = [...p.models, ...(p.paidModels ?? [])];
    }
    const g = await keysApi.createGroup(submit);
    if (!g.id) {
      throw new Error("group create returned no id");
    }
    // Keys 可选 — 填了就异步入库 (后端会验证 + 上报 status)
    if (keysInput.value.trim()) {
      await keysApi.addKeysAsync(g.id, keysInput.value);
    }
    // success toast 由 http interceptor 自动弹 (POST 默认开), 这里不重复
    emit("success", g);
    // 创建后跳 keys tab — 新 group 通常要看 key 状态 / 补加 key. detail
    // 页通过 URL ?tab=keys 切换. 不替换 path, replace query 保持当前路由.
    router.replace({ query: { ...route.query, tab: "keys" } }).catch(() => { /* nav guard cancel ok */ });
    close();
  } catch (e) {
    console.error("create failed", e);
    // error toast 已由 http interceptor 处理 (响应 body 的 message 透传).
    // 这里只做表单状态修正: 后端 DUPLICATE 时把这次名字加进 remotelyTaken
    // 黑名单, 用户看到 Warning 提示, 改名后立刻清除.
    const err = e as { response?: { status?: number; data?: { code?: string } } };
    const code = err?.response?.data?.code;
    if (err?.response?.status === 409 || code === "DUPLICATE_RESOURCE") {
      remotelyTakenNames.value = new Set([...remotelyTakenNames.value, formName.value.trim()]);
    }
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <n-modal :show="show" :mask-closable="false" @update:show="v => !v && close()">
    <div class="v3-card v3-ngf">
      <div class="v3-card__head">
        <div style="flex: 1; min-width: 0">
          <div class="v3-card__title">{{ t("v3.newGroupTitle") }}</div>
          <div class="v3-card__sub">{{ t("v3.newGroupSubV2") }}</div>
        </div>
        <button class="v3-btn v3-btn--ghost v3-btn--icon" @click="close">
          <n-icon :component="CloseOutline" :size="13" />
        </button>
      </div>

      <div class="v3-ngf__body">
        <!-- ============== Section 1: Provider 选择 ============== -->
        <section class="v3-ngf__section">
          <div class="v3-ngf__section-head">
            <span class="v3-ngf__section-title">{{ t("v3.sectionProvider") }}</span>
            <input
              v-model="providerSearch"
              class="v3-ngf__search"
              :placeholder="t('v3.searchProviderPlaceholder')"
              spellcheck="false"
            />
          </div>

          <!-- 搜索结果 (有 query 时只显示这个) -->
          <div v-if="hasSearch">
            <div v-if="searchResults.length" class="v3-ngf__chips">
              <button
                v-for="item in searchResults"
                :key="`s-${item.kind}-${item.provider.id}`"
                type="button"
                class="v3-pc-chip"
                :class="{ 'v3-pc-chip--selected': picked?.id === item.provider.id }"
                :title="`${item.provider.name} · ${item.provider.freeTier}`"
                @click="pickProvider(item.provider)"
              >
                <ProviderLogo
                  :hint="hasProviderLogo(item.provider.id) ? item.provider.id : item.provider.name"
                  :host="item.provider.baseUrl"
                  :fallback-initial="item.provider.name"
                  :size="18"
                  style="border-radius: 4px"
                />
                <span class="v3-pc-chip__name">{{ item.provider.name }}</span>
                <span
                  class="v3-pc-chip__tag"
                  :class="
                    item.kind === 'official'
                      ? 'v3-pc-chip__tag--official'
                      : item.kind === 'recommended'
                        ? 'v3-pc-chip__tag--recommended'
                        : 'v3-pc-chip__tag--free'
                  "
                >
                  {{
                    item.kind === "official"
                      ? t("v3.officialChip")
                      : item.kind === "recommended"
                        ? "⭐"
                        : t("v3.freeChip")
                  }}
                </span>
              </button>
            </div>
            <div v-else class="v3-ngf__empty">{{ t("v3.searchNoMatch") }}</div>
          </div>

          <!-- 无搜索: 推荐 + 官方 + (展开后) 其余 -->
          <template v-else>
            <div v-if="recommendedCatalog.length" class="v3-ngf__group">
              <div class="v3-ngf__group-label">⭐ {{ t("v3.recommended") }}</div>
              <div class="v3-ngf__chips">
                <button
                  v-for="item in recommendedCatalog"
                  :key="`r-${item.provider.id}`"
                  type="button"
                  class="v3-pc-chip"
                  :class="{ 'v3-pc-chip--selected': picked?.id === item.provider.id }"
                  :title="`${item.provider.name} · ${item.provider.freeTier}`"
                  @click="pickProvider(item.provider)"
                >
                  <ProviderLogo
                    :hint="hasProviderLogo(item.provider.id) ? item.provider.id : item.provider.name"
                    :host="item.provider.baseUrl"
                    :fallback-initial="item.provider.name"
                    :size="18"
                    style="border-radius: 4px"
                  />
                  <span class="v3-pc-chip__name">{{ item.provider.name }}</span>
                </button>
              </div>
            </div>

            <div class="v3-ngf__group">
              <div class="v3-ngf__group-label">{{ t("v3.officialProviders") }}</div>
              <div class="v3-ngf__chips">
                <button
                  v-for="item in officialCatalog"
                  :key="`o-${item.provider.id}`"
                  type="button"
                  class="v3-pc-chip"
                  :class="{ 'v3-pc-chip--selected': picked?.id === item.provider.id }"
                  :title="item.provider.name"
                  @click="pickProvider(item.provider)"
                >
                  <ProviderLogo
                    :hint="hasProviderLogo(item.provider.id) ? item.provider.id : item.provider.name"
                    :host="item.provider.baseUrl"
                    :fallback-initial="item.provider.name"
                    :size="18"
                    style="border-radius: 4px"
                  />
                  <span class="v3-pc-chip__name">{{ item.provider.name }}</span>
                </button>
              </div>
            </div>

            <!-- 自定义入口卡片 — 接任意 OpenAI/Anthropic/Gemini 兼容端点 -->
            <div class="v3-ngf__group">
              <div class="v3-ngf__group-label">{{ t("v3.customProviderGroupLabel") }}</div>
              <div class="v3-ngf__chips">
                <button
                  type="button"
                  class="v3-pc-chip v3-pc-chip--custom"
                  :class="{ 'v3-pc-chip--selected': isCustomMode }"
                  :title="t('v3.customProviderDesc')"
                  @click="pickCustom"
                >
                  <span class="v3-pc-chip__custom-icon">⚙️</span>
                  <span class="v3-pc-chip__name">{{ t("v3.customProviderTitle") }}</span>
                  <span class="v3-pc-chip__tag v3-pc-chip__tag--custom">
                    {{ t("v3.customChip") }}
                  </span>
                </button>
              </div>
            </div>

            <div v-if="!showAllFree && restFreeCatalog.length" class="v3-ngf__more">
              <button class="v3-btn v3-btn--ghost v3-btn--sm" @click="showAllFree = true">
                {{ t("v3.showMoreFree", { n: restFreeCatalog.length }) }}
              </button>
            </div>
            <div v-if="showAllFree && restFreeCatalog.length" class="v3-ngf__group">
              <div class="v3-ngf__group-label">{{ t("v3.otherFree") }}</div>
              <div class="v3-ngf__chips">
                <button
                  v-for="item in restFreeCatalog"
                  :key="`f-${item.provider.id}`"
                  type="button"
                  class="v3-pc-chip"
                  :class="{ 'v3-pc-chip--selected': picked?.id === item.provider.id }"
                  :title="`${item.provider.name} · ${item.provider.freeTier}`"
                  @click="pickProvider(item.provider)"
                >
                  <ProviderLogo
                    :hint="hasProviderLogo(item.provider.id) ? item.provider.id : item.provider.name"
                    :host="item.provider.baseUrl"
                    :fallback-initial="item.provider.name"
                    :size="18"
                    style="border-radius: 4px"
                  />
                  <span class="v3-pc-chip__name">{{ item.provider.name }}</span>
                </button>
              </div>
            </div>

          </template>
        </section>

        <!-- ============== Section 2: API Keys (可选) ============== -->
        <section class="v3-ngf__section">
          <div class="v3-ngf__section-head">
            <span class="v3-ngf__section-title">
              {{ t("v3.sectionKeys") }}
              <span class="v3-ngf__optional">{{ t("v3.optional") }}</span>
            </span>
            <span v-if="detectedKeyCount > 0" class="v3-ngf__key-count">
              {{ t("v3.detectedKeys", { n: detectedKeyCount }) }}
            </span>
          </div>
          <textarea
            v-model="keysInput"
            spellcheck="false"
            class="v3-ngf__keys-textarea"
            :placeholder="t('v3.keysPlaceholderV2')"
          />
          <!-- 智能识别提示 (粘了 key 但还没自动选 provider 时) -->
          <div
            v-if="smartDetection && smartDetection.confidence !== 'high' && !picked"
            class="v3-ngf__detect"
          >
            <span class="v3-ngf__detect-badge">?</span>
            <span>{{ t("v3.smartLowConfidence", { name: smartDetection.providerName }) }}</span>
            <button
              class="v3-btn v3-btn--accent v3-btn--sm"
              style="margin-left: auto"
              @click="
                () => {
                  const p = [...FREE_PROVIDERS, ...OFFICIAL_PROVIDERS].find(x => x.id === smartDetection?.providerId);
                  if (p) pickProvider(p);
                }
              "
            >
              {{ t("v3.smartApply") }}
            </button>
          </div>
        </section>

        <!-- ============== Section 3: 详情 (始终展开, 3 行 + custom 时 base URL) ============== -->
        <section v-if="picked" class="v3-ngf__section v3-ngf__details">
          <div class="v3-ngf__section-head">
            <span class="v3-ngf__section-title">{{ t("v3.sectionDetails") }}</span>
            <button
              v-if="picked?.signupUrl"
              class="v3-btn v3-btn--ghost v3-btn--sm"
              style="margin-left: auto"
              @click="openSignupPage"
            >
              <n-icon :component="OpenOutline" :size="11" />
              {{ t("v3.getKey") }}
            </button>
          </div>

          <div class="v3-ngf__form">
            <!-- 分组名称 + 实时重名 warning -->
            <div class="v3-ngf__form-row">
              <label class="v3-ngf__form-lbl">{{ t("v3.formGroupName") }} *</label>
              <div class="v3-ngf__name-wrap">
                <input
                  v-model="formName"
                  class="v3-ngf__form-input"
                  :class="{ 'v3-ngf__form-input--err': nameExists }"
                  spellcheck="false"
                />
                <span v-if="nameExists" class="v3-ngf__warn">
                  ⚠ {{ t("v3.groupNameTaken") }}
                </span>
              </div>
            </div>

            <!-- 渠道类型 -->
            <div class="v3-ngf__form-row">
              <label class="v3-ngf__form-lbl">{{ t("v3.formChannelType") }}</label>
              <select v-model="formChannelType" class="v3-ngf__form-input">
                <option value="openai">{{ t("v3.channelOpenAICompat") }}</option>
                <option value="openai-response">{{ t("v3.channelOpenAIResponseCompat") }}</option>
                <option value="anthropic">{{ t("v3.channelAnthropicCompat") }}</option>
                <option value="gemini">{{ t("v3.channelGeminiCompat") }}</option>
              </select>
            </div>

            <!-- Base URL 始终展示, 选了模板自动回填, 用户可改 (e.g. 自架代理) -->
            <div class="v3-ngf__form-row">
              <label class="v3-ngf__form-lbl">{{ t("v3.formBaseUrl") }} *</label>
              <input v-model="formBaseUrl" class="v3-ngf__form-input" spellcheck="false" />
            </div>

            <!-- 测试模型: custom 模式下自由输入, 否则从已知模型清单 select -->
            <div class="v3-ngf__form-row">
              <label class="v3-ngf__form-lbl">
                {{ t("v3.formTestModel") }}
                <span class="v3-ngf__form-optional">{{ t("v3.optional") }}</span>
              </label>
              <input
                v-if="isCustomMode"
                v-model="formTestModel"
                class="v3-ngf__form-input"
                spellcheck="false"
                :placeholder="t('v3.customTestModelPlaceholder')"
              />
              <select v-else v-model="formTestModel" class="v3-ngf__form-input">
                <option
                  v-for="m in availableModelsForPicked"
                  :key="m"
                  :value="m"
                >
                  {{ m }}{{ picked && isRecommended(picked.id, m) ? " ⭐" : "" }}
                </option>
              </select>
            </div>
          </div>
        </section>
      </div>

      <div class="v3-ngf__foot">
        <span class="v3-ngf__hint">{{ t("v3.newGroupFootHint") }}</span>
        <div class="v3-ngf__foot-btns">
          <button class="v3-btn" @click="close">{{ t("common.cancel") }}</button>
          <button
            class="v3-btn v3-btn--accent"
            :disabled="!canCreate || submitting"
            :title="!canCreate ? t('v3.cantCreateHint') : ''"
            @click="createProvider"
          >
            <n-icon v-if="!submitting" :component="CheckmarkOutline" :size="12" />
            <n-icon v-else :component="KeyOutline" :size="12" />
            {{ submitting ? t("v3.saving") : t("v3.createProvider") }}
          </button>
        </div>
      </div>
    </div>
  </n-modal>
</template>

<style scoped>
.v3-ngf {
  width: 720px;
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 64px);
  display: flex;
  flex-direction: column;
  background: var(--v3-card-bg, #fff);
}

.v3-ngf__body {
  padding: 14px 18px;
  overflow: auto;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

/* ===== Sections (Provider / Keys / Summary) ===== */
.v3-ngf__section {
  padding: 12px 14px;
  background: var(--v3-soft, #fafafa);
  border: 1px solid var(--v3-line);
  border-radius: 10px;
}
.v3-ngf__section-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.v3-ngf__section-title {
  font: 600 12px var(--v3-sans);
  color: var(--v3-ink);
  letter-spacing: 0.02em;
}
.v3-ngf__optional {
  font: 400 9.5px var(--v3-mono);
  color: var(--v3-ink-3);
  background: var(--v3-line);
  padding: 1px 5px;
  border-radius: 3px;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  margin-left: 6px;
}
.v3-ngf__key-count {
  margin-left: auto;
  font: 400 11px var(--v3-mono);
  color: var(--v3-ok);
}

/* ===== Provider 搜索 + chip groups ===== */
.v3-ngf__search {
  margin-left: auto;
  width: 220px;
  font: 500 12px var(--v3-sans);
  padding: 5px 9px;
  background: var(--v3-card-bg, #fff);
  border: 1px solid var(--v3-line);
  border-radius: 5px;
  outline: none;
}
.v3-ngf__search:focus {
  border-color: var(--v3-accent);
}
.v3-ngf__group {
  margin-bottom: 10px;
}
.v3-ngf__group:last-child {
  margin-bottom: 0;
}
.v3-ngf__group-label {
  font: 500 10.5px var(--v3-mono);
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--v3-ink-3);
  margin-bottom: 6px;
}
.v3-ngf__chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.v3-pc-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 8px 4px 5px;
  background: var(--v3-card-bg, #fff);
  border: 1px solid var(--v3-line);
  border-radius: 16px;
  cursor: pointer;
  font: 500 12px var(--v3-sans);
  color: var(--v3-ink);
  transition: all 140ms ease;
  outline: none;
}
.v3-pc-chip:hover {
  border-color: var(--v3-accent);
  background: oklch(from var(--v3-accent) 98% 0.02 h);
}
.v3-pc-chip--selected {
  border-color: var(--v3-accent);
  background: oklch(from var(--v3-accent) 95% 0.04 h);
  box-shadow: 0 0 0 2px oklch(from var(--v3-accent) l c h / 0.15);
}
.v3-pc-chip__name {
  white-space: nowrap;
}
.v3-pc-chip__tag {
  font: 500 9px var(--v3-mono);
  letter-spacing: 0.04em;
  padding: 1px 4px;
  border-radius: 3px;
  text-transform: uppercase;
}
.v3-pc-chip__tag--free {
  color: var(--v3-ok);
  background: oklch(from var(--v3-ok) l c h / 0.1);
}
.v3-pc-chip__tag--official {
  color: var(--v3-accent);
  background: oklch(from var(--v3-accent) l c h / 0.1);
}
.v3-pc-chip__tag--recommended {
  color: #c97c0c;
  background: rgba(201, 124, 12, 0.12);
}
.v3-pc-chip__tag--custom {
  color: var(--v3-ink-3);
  background: var(--v3-line);
}
.v3-pc-chip__custom-icon {
  font-size: 14px;
  line-height: 1;
}

.v3-ngf__more {
  margin-top: 6px;
  display: flex;
  justify-content: center;
}
.v3-ngf__empty {
  font: 400 12px var(--v3-sans);
  color: var(--v3-ink-3);
  padding: 16px 0;
  text-align: center;
}

/* ===== Keys textarea ===== */
.v3-ngf__keys-textarea {
  width: 100%;
  min-height: 70px;
  resize: vertical;
  background: var(--v3-card-bg, #fff);
  border: 1px solid var(--v3-line);
  border-radius: 5px;
  padding: 7px 9px;
  outline: none;
  font: 500 12px/1.5 var(--v3-mono);
  color: var(--v3-ink);
  transition: border-color 140ms;
}
.v3-ngf__keys-textarea:focus {
  border-color: var(--v3-accent);
}

/* 智能识别 - 低置信度提示 */
.v3-ngf__detect {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  padding: 6px 10px;
  background: oklch(from var(--v3-accent) 96% 0.04 h);
  border: 1px solid oklch(from var(--v3-accent) l c h / 0.2);
  border-radius: 6px;
  font: 400 11.5px var(--v3-sans);
  color: var(--v3-ink);
}
.v3-ngf__detect-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font: 700 10px var(--v3-mono);
  background: oklch(from var(--v3-accent) l c h / 0.18);
  color: var(--v3-accent);
  border-radius: 50%;
}

/* ===== Details section (always-expanded form) ===== */
.v3-ngf__details {
  background: oklch(from var(--v3-ok) 98% 0.02 h);
  border-color: oklch(from var(--v3-ok) l c h / 0.25);
}

/* Name input + 重名 warning 包装 (label 一格, 输入框+warning 一格) */
.v3-ngf__name-wrap {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.v3-ngf__form-input--err {
  border-color: var(--v3-danger);
  background: oklch(from var(--v3-danger) 98% 0.02 h);
}
.v3-ngf__form-input--err:focus {
  border-color: var(--v3-danger);
}
.v3-ngf__warn {
  font: 500 11px var(--v3-sans);
  color: var(--v3-danger);
}

/* ===== Form layout ===== */
.v3-ngf__form {
  display: grid;
  grid-template-columns: 100px 1fr;
  gap: 8px 12px;
  align-items: center;
  padding-top: 4px;
}
.v3-ngf__form-row {
  display: contents;
}
.v3-ngf__form-lbl {
  font: 500 11.5px var(--v3-sans);
  color: var(--v3-ink-3);
  letter-spacing: 0.02em;
}
/* 行内 "可选" mini chip - 在 label 后小字标注 */
.v3-ngf__form-optional {
  display: inline-block;
  font: 400 9.5px var(--v3-mono);
  color: var(--v3-ink-3);
  background: var(--v3-line);
  padding: 1px 4px;
  border-radius: 3px;
  margin-left: 4px;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}
.v3-ngf__form-input {
  font: 500 12px var(--v3-mono);
  padding: 6px 9px;
  background: var(--v3-card-bg, #fff);
  border: 1px solid var(--v3-line);
  border-radius: 5px;
  outline: none;
  color: var(--v3-ink);
  transition: border-color 140ms;
}
.v3-ngf__form-input:focus {
  border-color: var(--v3-accent);
}

/* ===== Footer ===== */
.v3-ngf__foot {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 18px;
  border-top: 1px solid var(--v3-line);
  background: var(--v3-card-bg, #fff);
}
.v3-ngf__hint {
  font: 400 11.5px var(--v3-sans);
  color: var(--v3-ink-3);
}
.v3-ngf__foot-btns {
  margin-left: auto;
  display: flex;
  gap: 8px;
}
</style>
