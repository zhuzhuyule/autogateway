<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { detectFromText, type KeyDetection } from "@/data/keyDetector";
import {
  FREE_PROVIDERS,
  FREE_MODELS,
  bootstrapExposedModels,
  displayModelName,
  type FreeProvider,
} from "@/data/freeProviders";
import type { Group } from "@/types/models";
import {
  CheckmarkOutline,
  CloseOutline,
  FlashOutline,
  KeyOutline,
  OpenOutline,
  PlayOutline,
} from "@vicons/ionicons5";
import { NIcon, NModal, useMessage } from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import FreeBadge from "@/components/common/FreeBadge.vue";

const { t } = useI18n();
const message = useMessage();

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

const picked = ref<FreeProvider | null>(null);
const groupName = ref("");
const pasted = ref("");
const submitting = ref(false);
const testing = ref(false);
const testResults = ref<{ ok: number; fail: number } | null>(null);

// Model selection
const showPaid = ref(false);
const selectedModel = ref<string>("");

watch(picked, p => {
  if (!p) {
    selectedModel.value = "";
    return;
  }
  const freeIds = FREE_MODELS.filter(m => m.providerId === p.id).map(m => m.modelId);
  selectedModel.value = freeIds[0] ?? p.testModel;
  showPaid.value = false;
});

const visibleModels = computed(() => {
  if (!picked.value) {
    return [] as Array<{ id: string; isFree: boolean; tier?: string }>;
  }
  const p = picked.value;
  const freeSet = new Set(FREE_MODELS.filter(m => m.providerId === p.id).map(m => m.modelId));
  const freeList = FREE_MODELS.filter(m => m.providerId === p.id).map(m => ({
    id: m.modelId,
    isFree: true,
    tier: m.tier as string | undefined,
  }));
  if (!showPaid.value) {
    return freeList;
  }
  const paidList = (p.paidModels ?? [])
    .filter(id => !freeSet.has(id))
    .map(id => ({ id, isFree: false, tier: undefined as string | undefined }));
  return [...freeList, ...paidList];
});

// Smart detection
const smartInput = ref("");
const smartDetection = computed<KeyDetection | null>(() => detectFromText(smartInput.value));

function applySmartDetection() {
  if (!smartDetection.value) {
    return;
  }
  const provider = FREE_PROVIDERS.find(p => p.id === smartDetection.value!.providerId);
  if (!provider) {
    return;
  }
  pickProvider(provider);
  pasted.value = smartInput.value;
  smartInput.value = "";
}

watch(
  () => props.show,
  v => {
    if (v) {
      picked.value = null;
      groupName.value = "";
      pasted.value = "";
      submitting.value = false;
      testing.value = false;
      testResults.value = null;
      smartInput.value = "";
    }
  }
);

const step = computed<1 | 2 | 3>(() => {
  if (!picked.value) {
    return 1;
  }
  if (!pasted.value.trim()) {
    return 2;
  }
  return 3;
});

const detectedKeys = computed(() => {
  return pasted.value
    .split(/[\n,]/)
    .map(k => k.trim())
    .filter(Boolean);
});

const keyCountLabel = computed(() => detectedKeys.value.length);

function close() {
  emit("update:show", false);
}

function pickProvider(p: FreeProvider) {
  picked.value = p;
  testResults.value = null;
  // ensure unique group name
  let name = p.recommendedGroupName;
  if (props.existingGroupNames.includes(name)) {
    let i = 2;
    while (props.existingGroupNames.includes(`${name}-${i}`)) {
      i += 1;
    }
    name = `${name}-${i}`;
  }
  groupName.value = name;
}

function openKeyPage() {
  if (picked.value?.signupUrl) {
    window.open(picked.value.signupUrl, "_blank", "noopener");
  }
}

// Favicon support
const FAVICON_DOMAIN_MAP: Record<string, string> = {
  openai: "openai.com",
  gemini: "gemini.google.com",
  anthropic: "anthropic.com",
  google: "google.com",
  groq: "groq.com",
  cerebras: "cerebras.ai",
  openrouter: "openrouter.ai",
  together: "together.ai",
  cloudflare: "cloudflare.com",
  mistral: "mistral.ai",
  cohere: "cohere.com",
  github: "github.com",
  siliconflow: "siliconflow.cn",
  zhipu: "zhipuai.cn",
  nvidia: "nvidia.com",
  gitee: "gitee.com",
  modelscope: "modelscope.cn",
  longcat: "longcat.chat",
  xfyun: "xfyun.cn",
  aihubmix: "aihubmix.com",
  kilo: "kilo.ai",
  llm7: "llm7.io",
};

function faviconFor(id: string): string {
  const domain = FAVICON_DOMAIN_MAP[id];
  if (domain) {
    return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(domain)}&sz=64`;
  }
  return "";
}
const faviconErr = ref<Record<string, boolean>>({});
function onFaviconErr(id: string) {
  faviconErr.value[id] = true;
}

function rateTag(p: FreeProvider): string {
  return p.freeTier
    .replace(/requests?\/day/gi, "/day")
    .replace(/tokens?\/day/gi, "tk/day")
    .replace(/\s+/g, " ")
    .trim();
}

interface ExtraTag {
  label: string;
  cls: string;
}

function extraTags(p: FreeProvider): ExtraTag[] {
  const tags: ExtraTag[] = [];
  const cnt = FREE_MODELS.filter(m => m.providerId === p.id).length;
  if (cnt > 0) {
    tags.push({ label: `${cnt} 免费模型`, cls: "v3-pc__tag--count" });
  }
  if (p.rpm) {
    tags.push({ label: p.rpm, cls: "v3-pc__tag--limit" });
  }
  if (p.rpd) {
    tags.push({ label: p.rpd, cls: "v3-pc__tag--limit" });
  }
  if (p.concurrent) {
    tags.push({ label: p.concurrent, cls: "v3-pc__tag--limit" });
  }
  if (p.context) {
    tags.push({ label: p.context, cls: "v3-pc__tag--spec" });
  }
  if (p.highlights) {
    for (const h of p.highlights) {
      tags.push({ label: h, cls: "v3-pc__tag--feat" });
    }
  }
  return tags;
}

function featTags(p: FreeProvider): string[] {
  if (!p.description) {
    return [];
  }
  const seen = new Set(extraTags(p).map(t => t.label));
  return p.description
    .split(/[,，/|·、]/)
    .map(s => s.trim())
    .filter(s => s && !seen.has(s));
}

function badgeLabel(badge?: FreeProvider["badge"]) {
  if (badge === "fast") {
    return "⚡ fast";
  }
  if (badge === "high-quota") {
    return "high quota";
  }
  if (badge === "multi-model") {
    return "multi-model";
  }
  return "";
}

function badgeClass(badge?: FreeProvider["badge"]) {
  if (badge === "fast") {
    return "v3-pc__badge v3-pc__badge--fast";
  }
  if (badge === "high-quota") {
    return "v3-pc__badge v3-pc__badge--high";
  }
  if (badge === "multi-model") {
    return "v3-pc__badge v3-pc__badge--multi";
  }
  return "v3-pc__badge";
}

function pavClassFor(id: string) {
  const known = [
    "groq",
    "cerebras",
    "openrouter",
    "together",
    "cloudflare",
    "mistral",
    "google",
    "cohere",
    "github",
    "anthropic",
  ];
  if (known.includes(id)) {
    return `v3-pav v3-pav-${id}`;
  }
  if (id.includes("google")) {
    return "v3-pav v3-pav-google";
  }
  if (id.includes("github")) {
    return "v3-pav v3-pav-github";
  }
  return "v3-pav v3-pav-default";
}

async function ensureGroupExists(): Promise<Group> {
  // create the group with provider's recommended config
  if (!picked.value) {
    throw new Error("no provider picked");
  }
  const p = picked.value;
  const testModel = selectedModel.value || p.testModel;
  const submit: Partial<Group> = {
    name: groupName.value || p.recommendedGroupName,
    display_name: p.recommendedDisplayName,
    description: p.description,
    channel_type: p.channelType,
    upstreams: [{ url: p.baseUrl, weight: 1 }],
    test_model: testModel,
    sort: 0,
    validation_endpoint: "",
    param_overrides: {},
    model_redirect_rules: {},
    model_redirect_strict: false,
    config: {},
    header_rules: [],
    proxy_keys: "",
    // 默认 specified + 自动暴露已知免费模型 (含用户选的 testModel)
    model_routing_mode: "specified",
    exposed_models: bootstrapExposedModels(p, testModel),
  };
  // 上游无 /v1/models 端点的 provider (e.g. LongCat) — 直接把静态清单
  // 写入 available_models,免去之后 refresh 时 404。
  if (p.staticModels) {
    submit.available_models = [...p.models, ...(p.paidModels ?? [])];
  }
  return await keysApi.createGroup(submit);
}

async function testOnly() {
  if (!picked.value || !pasted.value.trim()) {
    return;
  }
  testing.value = true;
  testResults.value = null;
  try {
    // we need a group_id to call testKeys; create the group first if needed
    // since testKeys requires existing group, we create-then-test in one go
    const g = await ensureGroupExists();
    if (!g.id) {
      throw new Error("group create returned no id");
    }
    const r = await keysApi.testKeys(g.id, pasted.value);
    const ok = r.results.filter(x => x.is_valid).length;
    const fail = r.results.length - ok;
    testResults.value = { ok, fail };
    if (ok > 0) {
      message.success(t("v3.testResultMsg", { ok, fail }));
    } else {
      message.warning(t("v3.testResultMsg", { ok, fail }));
    }
    // emit success so parent refreshes group list (group already created)
    emit("success", g);
  } catch (e) {
    console.error("test failed", e);
    message.error(t("common.requestFailed"));
  } finally {
    testing.value = false;
  }
}

async function testAndSave() {
  if (!picked.value || !pasted.value.trim()) {
    return;
  }
  submitting.value = true;
  try {
    const g = await ensureGroupExists();
    if (!g.id) {
      throw new Error("group create returned no id");
    }
    await keysApi.addKeysAsync(g.id, pasted.value);
    message.success(t("common.operationSuccess"));
    emit("success", g);
    close();
  } catch (e) {
    console.error("save failed", e);
    message.error(t("common.requestFailed"));
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
          <div class="v3-card__title">
            {{ picked ? `${t("v3.newGroupTitle")} · ${picked.name}` : t("v3.newGroupTitle") }}
          </div>
          <div class="v3-card__sub">
            {{ picked ? t("v3.newGroupSubPicked") : t("v3.newGroupSubPick") }}
          </div>
        </div>
        <button class="v3-btn v3-btn--ghost v3-btn--icon" @click="close">
          <n-icon :component="CloseOutline" :size="13" />
        </button>
      </div>

      <!-- Step indicator -->
      <div class="v3-intake__steps">
        <div
          class="v3-step"
          :class="{
            'v3-step--active': step === 1,
            'v3-step--done': step > 1,
          }"
        >
          <div class="v3-step__n">
            <n-icon v-if="step > 1" :component="CheckmarkOutline" :size="12" />
            <span v-else>1</span>
          </div>
          <div>
            <div class="v3-step__title">{{ t("v3.step1Title") }}</div>
            <div class="v3-step__sub">{{ t("v3.step1Sub") }}</div>
          </div>
        </div>
        <div
          class="v3-step"
          :class="{
            'v3-step--active': step === 2,
            'v3-step--done': step > 2,
          }"
        >
          <div class="v3-step__n">
            <n-icon v-if="step > 2" :component="CheckmarkOutline" :size="12" />
            <span v-else>2</span>
          </div>
          <div>
            <div class="v3-step__title">{{ t("v3.step2Title") }}</div>
            <div class="v3-step__sub">{{ t("v3.step2Sub") }}</div>
          </div>
        </div>
        <div class="v3-step" :class="{ 'v3-step--active': step === 3 }">
          <div class="v3-step__n">3</div>
          <div>
            <div class="v3-step__title">{{ t("v3.step3Title") }}</div>
            <div class="v3-step__sub">{{ t("v3.step3Sub") }}</div>
          </div>
        </div>
      </div>

      <div class="v3-ngf__body">
        <!-- GetKey callout (after picking provider) -->
        <div v-if="picked" class="v3-getkey">
          <div class="v3-getkey__icon">
            <n-icon :component="KeyOutline" :size="18" />
          </div>
          <div>
            <div class="v3-getkey__title">
              {{ t("v3.getKeyTitle", { provider: picked.name }) }}
            </div>
            <div class="v3-getkey__sub">
              {{ t("v3.getKeyOpensIn") }}
              <code>{{ picked.signupUrl }}</code>
              · {{ t("v3.freeTier") }}:
              <strong style="color: var(--v3-ok)">{{ picked.freeTier }}</strong>
              . {{ t("v3.getKeyHint") }}
            </div>
          </div>
          <button class="v3-btn v3-btn--accent v3-btn--lg" @click="openKeyPage">
            <n-icon :component="OpenOutline" :size="14" />
            {{ t("v3.openProvider", { provider: picked.name }) }}
          </button>
        </div>

        <!-- Model selector -->
        <div v-if="picked && visibleModels.length" class="v3-msel">
          <div class="v3-msel__head">
            <span class="v3-msel__label">{{ t("v3.modelConfig") }}</span>
            <button
              class="v3-btn v3-btn--sm v3-msel__toggle"
              :class="{ 'v3-msel__toggle--active': showPaid }"
              @click="showPaid = !showPaid"
            >
              {{ showPaid ? t("v3.allModels") : t("v3.freeOnly") }}
            </button>
          </div>
          <div class="v3-msel__list">
            <button
              v-for="m in visibleModels"
              :key="m.id"
              class="v3-msel__chip"
              :class="{
                'v3-msel__chip--selected': selectedModel === m.id,
                'v3-msel__chip--paid': !m.isFree,
              }"
              @click="selectedModel = m.id"
              :title="m.id"
            >
              <span class="v3-msel__chip-id">
                {{ displayModelName(picked ?? undefined, m.id) }}
                <FreeBadge v-if="m.isFree" />
              </span>
              <span v-if="!m.isFree" class="v3-msel__chip-badge v3-msel__chip-badge--paid">paid</span>
              <span v-if="m.tier" class="v3-msel__chip-tier">{{ m.tier }}</span>
            </button>
          </div>
        </div>

        <!-- Paste textarea (after picking provider) -->
        <div v-if="picked" class="v3-intake__paste">
          <div class="v3-intake__paste-lbl">
            <n-icon :component="KeyOutline" :size="11" />
            {{ t("v3.pasteLbl") }}
          </div>
          <textarea
            v-model="pasted"
            spellcheck="false"
            :placeholder="`${picked.recommendedGroupName.slice(0, 3)}_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n${picked.recommendedGroupName.slice(0, 3)}_yyyyyyyyyyyyyyyyyyyyyyyyyyyyyy`"
            style="
              width: 100%;
              min-height: 100px;
              resize: vertical;
              background: transparent;
              border: 0;
              outline: 0;
              font: 500 12px/1.5 var(--v3-mono);
              color: var(--v3-ink);
            "
          />
          <div class="v3-intake__paste-foot">
            <span class="v3-intake__paste-hint">
              {{ t("v3.detectedKeys", { n: keyCountLabel }) }}
            </span>
            <div v-if="testResults" class="mono" style="font-size: 11px; color: var(--v3-ink-2)">
              <span style="color: var(--v3-ok)">{{ testResults.ok }} ok</span>
              ·
              <span :style="{ color: testResults.fail ? 'var(--v3-danger)' : 'var(--v3-ink-3)' }">
                {{ testResults.fail }} fail
              </span>
            </div>
            <div style="margin-left: auto; display: flex; gap: 8px">
              <button
                class="v3-btn"
                :disabled="testing || submitting || !pasted.trim()"
                @click="testOnly"
              >
                <n-icon :component="PlayOutline" :size="12" />
                {{ testing ? t("v3.testing") : t("v3.testOnly") }}
              </button>
              <button
                class="v3-btn v3-btn--accent"
                :disabled="submitting || testing || !pasted.trim()"
                @click="testAndSave"
              >
                <n-icon :component="CheckmarkOutline" :size="12" />
                {{ submitting ? t("v3.saving") : t("v3.testAndSave") }}
              </button>
            </div>
          </div>
        </div>

        <!-- Smart detect bar (hidden once provider is picked) -->
        <div v-if="!picked" class="v3-sdet">
          <div class="v3-sdet__head">
            <n-icon :component="FlashOutline" :size="12" style="color: var(--v3-accent)" />
            <span class="v3-sdet__title">{{ t("v3.smartDetect") }}</span>
          </div>
          <div class="v3-sdet__body">
            <textarea
              v-model="smartInput"
              class="v3-sdet__input"
              :placeholder="t('v3.smartDetectPlaceholder')"
              rows="2"
              spellcheck="false"
            />
            <div class="v3-sdet__result" :class="{ 'v3-sdet__result--visible': smartDetection }">
              <template v-if="smartDetection">
                <span
                  class="v3-sdet__badge"
                  :class="
                    smartDetection.confidence === 'high'
                      ? 'v3-sdet__badge--high'
                      : 'v3-sdet__badge--med'
                  "
                >
                  {{ smartDetection.confidence === "high" ? "✓ 高置信度" : "? 低置信度" }}
                </span>
                <span class="v3-sdet__name">{{ smartDetection.providerName }}</span>
                <span class="v3-sdet__hint">{{ smartDetection.hint }}</span>
                <button
                  class="v3-btn v3-btn--accent v3-btn--sm"
                  style="margin-left: auto"
                  @click="applySmartDetection"
                >
                  {{ t("v3.smartApply") }} →
                </button>
              </template>
              <template v-else-if="smartInput.trim()">
                <span class="v3-sdet__hint">{{ t("v3.smartNoMatch") }}</span>
              </template>
            </div>
          </div>
        </div>

        <!-- Provider catalog grid -->
        <div
          style="
            margin-top: 18px;
            display: flex;
            align-items: center;
            gap: 10px;
            margin-bottom: 12px;
          "
        >
          <div
            style="
              font: 500 11px/1 var(--v3-mono);
              letter-spacing: 0.1em;
              text-transform: uppercase;
              color: var(--v3-ink-3);
            "
          >
            {{ picked ? t("v3.orPickAnother") : t("v3.chooseProvider") }}
          </div>
          <div style="flex: 1; height: 1px; background: var(--v3-line)" />
          <span class="v3-chip">{{ FREE_PROVIDERS.length }} {{ t("v3.providersAvailable") }}</span>
        </div>
        <div class="v3-pc-grid">
          <div
            v-for="prov in FREE_PROVIDERS"
            :key="prov.id"
            class="v3-pc"
            :class="{ 'v3-pc--selected': picked?.id === prov.id }"
            @click="pickProvider(prov)"
          >
            <div class="v3-pc__head">
              <div class="v3-pc__logo-wrapper">
                <img
                  v-if="faviconFor(prov.id) && !faviconErr[prov.id]"
                  :src="faviconFor(prov.id)"
                  alt=""
                  @error="onFaviconErr(prov.id)"
                  class="v3-pc__favicon"
                />
                <span v-else :class="pavClassFor(prov.id) + ' v3-pc__logo'">
                  {{
                    prov.name
                      .replace(/[^A-Za-z0-9]/g, "")
                      .slice(0, 2)
                      .toUpperCase()
                  }}
                </span>
              </div>
              <div style="flex: 1; min-width: 0">
                <div class="v3-pc__name">{{ prov.name }}</div>
              </div>
              <span v-if="prov.badge" :class="badgeClass(prov.badge)">
                {{ badgeLabel(prov.badge) }}
              </span>
            </div>
            <div class="v3-pc__tags">
              <span class="v3-pc__tag v3-pc__tag--rate">★ {{ rateTag(prov) }}</span>
              <span
                v-for="x in extraTags(prov)"
                :key="`x-${x.label}`"
                class="v3-pc__tag"
                :class="x.cls"
              >{{ x.label }}</span>
              <span v-for="t in featTags(prov)" :key="t" class="v3-pc__tag">{{ t }}</span>
            </div>
            <div class="v3-pc__foot">
              <button
                class="v3-btn v3-btn--sm"
                style="flex: 1; height: 26px; font-size: 11px"
                @click.stop="pickProvider(prov)"
              >
                {{ picked?.id === prov.id ? t("v3.selected") : t("v3.useThis") }}
              </button>
              <a
                :href="prov.signupUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="v3-btn v3-btn--sm"
                style="height: 26px; font-size: 11px"
                @click.stop
              >
                <n-icon :component="OpenOutline" :size="10" />
                {{ t("v3.getKey") }}
              </a>
            </div>
          </div>
        </div>
      </div>

      <div class="v3-ngf__foot">
        <span style="font: 400 11.5px var(--v3-sans); color: var(--v3-ink-3)">
          {{ t("v3.newGroupFootHint") }}
        </span>
        <button class="v3-btn" style="margin-left: auto" @click="close">
          {{ t("common.cancel") }}
        </button>
      </div>
    </div>
  </n-modal>
</template>

<style scoped>
.v3-ngf {
  width: 920px;
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 64px);
  display: flex;
  flex-direction: column;
}
.v3-intake__steps {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  padding: 14px 16px;
  background: var(--v3-surface-2);
  border-bottom: 1px solid var(--v3-line);
}
.v3-ngf__body {
  padding: 14px;
  overflow: auto;
  flex: 1;
}
.v3-pc-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 10px;
}
.v3-pc {
  background: var(--v3-surface);
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius-md);
  padding: 10px 12px;
  cursor: pointer;
  transition: all 120ms;
  display: flex;
  flex-direction: column;
}
.v3-pc:hover {
  border-color: var(--v3-line-strong);
  box-shadow: var(--v3-shadow-sm);
}
.v3-pc--selected {
  border-color: var(--v3-accent);
  background: var(--v3-accent-soft);
}
.v3-pc__head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}
.v3-pc__logo-wrapper {
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
}
.v3-pc__favicon {
  width: 24px;
  height: 24px;
  object-fit: contain;
}
.v3-pc__logo {
  width: 28px;
  height: 28px;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
}
.v3-pc__name {
  font: 700 13px var(--v3-sans);
  color: var(--v3-ink);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.v3-pc__tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-bottom: 10px;
  flex: 1;
  align-content: flex-start;
}
.v3-pc__tag {
  font: 500 10px/1.4 var(--v3-mono);
  padding: 2px 6px;
  border-radius: 4px;
  background: var(--v3-surface-2);
  color: var(--v3-ink-2);
  border: 1px solid var(--v3-line);
  white-space: nowrap;
}
.v3-pc__tag--rate {
  background: var(--v3-ok-soft);
  color: var(--v3-ok);
  border-color: transparent;
  font-weight: 700;
}
.v3-pc__tag--count {
  background: var(--v3-accent-soft);
  color: var(--v3-accent);
  border-color: transparent;
  font-weight: 600;
}
.v3-pc__tag--limit {
  background: var(--v3-info-soft);
  color: var(--v3-info);
  border-color: transparent;
}
.v3-pc__tag--spec {
  background: transparent;
  color: var(--v3-ink);
  border-color: var(--v3-line-strong, var(--v3-line));
  font-weight: 600;
}
.v3-pc__tag--feat {
  background: transparent;
  color: var(--v3-ink-2);
}
.v3-pc__foot {
  display: flex;
  gap: 6px;
  margin-top: auto;
}
.v3-pc__badge {
  font: 700 8.5px var(--v3-mono);
  padding: 1px 4px;
  border-radius: 3px;
  text-transform: uppercase;
}
.v3-pc__badge--fast {
  background: var(--v3-ok-soft);
  color: var(--v3-ok);
}
.v3-pc__badge--high {
  background: var(--v3-accent-soft);
  color: var(--v3-accent);
}
.v3-pc__badge--multi {
  background: var(--v3-info-soft);
  color: var(--v3-info);
}

.v3-ngf__foot {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 16px;
  border-top: 1px solid var(--v3-line);
  background: var(--v3-surface-2);
}

/* Smart detect */
.v3-sdet {
  margin-top: 4px;
  border: 1px solid var(--v3-accent);
  border-radius: var(--v3-radius-md);
  overflow: hidden;
  background: var(--v3-accent-soft);
}
.v3-sdet__head {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border-bottom: 1px solid oklch(from var(--v3-accent) l c h / 0.15);
}
.v3-sdet__title {
  font: 600 11px/1 var(--v3-mono);
  color: var(--v3-accent);
  text-transform: uppercase;
  letter-spacing: 0.08em;
}
.v3-sdet__body {
  display: flex;
  flex-direction: column;
}
.v3-sdet__input {
  width: 100%;
  padding: 8px 12px;
  background: transparent;
  border: none;
  outline: none;
  font: 500 12px/1.5 var(--v3-mono);
  color: var(--v3-ink);
  resize: none;
}
.v3-sdet__input::placeholder {
  color: var(--v3-ink-4);
}
.v3-sdet__result {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  min-height: 32px;
  border-top: 1px solid oklch(from var(--v3-accent) l c h / 0.12);
  font: 400 11.5px var(--v3-sans);
  color: var(--v3-ink-2);
}
.v3-sdet__badge {
  font: 700 9px/1 var(--v3-mono);
  padding: 2px 5px;
  border-radius: 3px;
  text-transform: uppercase;
}
.v3-sdet__badge--high {
  background: var(--v3-ok-soft);
  color: var(--v3-ok);
}
.v3-sdet__badge--med {
  background: var(--v3-warn-soft, oklch(0.96 0.05 80));
  color: var(--v3-warn, oklch(0.6 0.15 80));
}
.v3-sdet__name {
  font: 600 12px var(--v3-sans);
  color: var(--v3-ink);
}
.v3-sdet__hint {
  font: 400 11px var(--v3-mono);
  color: var(--v3-ink-3);
}

/* Model selector */
.v3-msel {
  margin-bottom: 10px;
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius-md);
  background: var(--v3-surface-2);
  overflow: hidden;
}
.v3-msel__head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 12px;
  border-bottom: 1px solid var(--v3-line);
}
.v3-msel__label {
  font: 600 11px/1 var(--v3-mono);
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--v3-ink-3);
  flex: 1;
}
.v3-msel__toggle {
  color: var(--v3-ink-2);
}
.v3-msel__toggle--active {
  color: var(--v3-accent);
  border-color: var(--v3-accent);
}
.v3-msel__list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 8px 12px;
}
.v3-msel__chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 8px;
  border: 1px solid var(--v3-line);
  border-radius: 20px;
  background: var(--v3-surface);
  cursor: pointer;
  font: 400 11px var(--v3-mono);
  color: var(--v3-ink-2);
  transition: all 80ms;
  white-space: nowrap;
}
.v3-msel__chip:hover {
  border-color: var(--v3-accent);
  color: var(--v3-accent);
}
.v3-msel__chip--selected {
  border-color: var(--v3-accent);
  background: var(--v3-accent-soft);
  color: var(--v3-accent);
  font-weight: 600;
}
.v3-msel__chip--paid {
  border-style: dashed;
}
.v3-msel__chip-id { white-space: nowrap; }
.v3-msel__chip-badge {
  font-size: 9px;
  padding: 1px 3px;
  border-radius: 3px;
}
.v3-msel__chip-badge--free {
  background: var(--v3-ok-soft);
  color: var(--v3-ok);
}
.v3-msel__chip-badge--paid {
  background: var(--v3-surface);
  color: var(--v3-ink-3);
  border: 1px solid var(--v3-line);
}
.v3-msel__chip-tier {
  font: 400 9px var(--v3-mono);
  color: var(--v3-ink-3);
  text-transform: uppercase;
}

@media (max-width: 700px) {
  .v3-intake__steps {
    grid-template-columns: 1fr;
  }
  .v3-getkey {
    grid-template-columns: 36px 1fr;
  }
  .v3-getkey > .v3-btn {
    grid-column: 1 / -1;
  }
}
</style>
