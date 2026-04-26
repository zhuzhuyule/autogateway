<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { FREE_PROVIDERS, type FreeProvider } from "@/data/freeProviders";
import type { Group } from "@/types/models";
import {
  CheckmarkOutline,
  CloseOutline,
  KeyOutline,
  OpenOutline,
  PlayOutline,
} from "@vicons/ionicons5";
import { NIcon, NModal, useMessage } from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

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
    }
  }
);

const step = computed<1 | 2 | 3>(() => {
  if (!picked.value) return 1;
  if (!pasted.value.trim()) return 2;
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
    while (props.existingGroupNames.includes(`${name}-${i}`)) i += 1;
    name = `${name}-${i}`;
  }
  groupName.value = name;
}

function openKeyPage() {
  if (picked.value?.signupUrl) {
    window.open(picked.value.signupUrl, "_blank", "noopener");
  }
}

function badgeLabel(badge?: FreeProvider["badge"]) {
  if (badge === "fast") return "⚡ fast";
  if (badge === "high-quota") return "high quota";
  if (badge === "multi-model") return "multi-model";
  return "";
}

function badgeClass(badge?: FreeProvider["badge"]) {
  if (badge === "fast") return "v3-pc__badge v3-pc__badge--fast";
  if (badge === "high-quota") return "v3-pc__badge v3-pc__badge--high";
  if (badge === "multi-model") return "v3-pc__badge v3-pc__badge--multi";
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
  if (known.includes(id)) return `v3-pav v3-pav-${id}`;
  if (id.includes("google")) return "v3-pav v3-pav-google";
  if (id.includes("github")) return "v3-pav v3-pav-github";
  return "v3-pav v3-pav-default";
}

async function ensureGroupExists(): Promise<Group> {
  // create the group with provider's recommended config
  if (!picked.value) {
    throw new Error("no provider picked");
  }
  const p = picked.value;
  const submit: Partial<Group> = {
    name: groupName.value || p.recommendedGroupName,
    display_name: p.recommendedDisplayName,
    description: p.description,
    channel_type: p.channelType,
    upstreams: [{ url: p.baseUrl, weight: 1 }],
    test_model: p.testModel,
    sort: 0,
    validation_endpoint: "",
    param_overrides: {},
    model_redirect_rules: {},
    model_redirect_strict: false,
    config: {},
    header_rules: [],
    proxy_keys: "",
  };
  return await keysApi.createGroup(submit);
}

async function testOnly() {
  if (!picked.value || !pasted.value.trim()) return;
  testing.value = true;
  testResults.value = null;
  try {
    // we need a group_id to call testKeys; create the group first if needed
    // since testKeys requires existing group, we create-then-test in one go
    const g = await ensureGroupExists();
    if (!g.id) throw new Error("group create returned no id");
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
  if (!picked.value || !pasted.value.trim()) return;
  submitting.value = true;
  try {
    const g = await ensureGroupExists();
    if (!g.id) throw new Error("group create returned no id");
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
              <strong style="color: var(--v3-ok)">{{ picked.freeTier }}</strong
              >. {{ t("v3.getKeyHint") }}
            </div>
          </div>
          <button class="v3-btn v3-btn--accent v3-btn--lg" @click="openKeyPage">
            <n-icon :component="OpenOutline" :size="14" />
            {{ t("v3.openProvider", { provider: picked.name }) }}
          </button>
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
            <div
              v-if="testResults"
              class="mono"
              style="font-size: 11px; color: var(--v3-ink-2)"
            >
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
          <span class="v3-chip">
            {{ FREE_PROVIDERS.length }} {{ t("v3.providersAvailable") }}
          </span>
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
              <span
                :class="pavClassFor(prov.id) + ' v3-pc__logo'"
                style="font-size: 11px"
              >
                {{ prov.name.replace(/[^A-Za-z0-9]/g, "").slice(0, 2).toUpperCase() }}
              </span>
              <div style="flex: 1; min-width: 0">
                <div class="v3-pc__name">{{ prov.name }}</div>
                <div class="v3-pc__free">★ {{ prov.freeTier }}</div>
              </div>
              <span v-if="prov.badge" :class="badgeClass(prov.badge)">
                {{ badgeLabel(prov.badge) }}
              </span>
            </div>
            <div class="v3-pc__desc">{{ prov.description }}</div>
            <div class="v3-pc__foot">
              <button
                class="v3-btn v3-btn--sm"
                style="flex: 1"
                @click.stop="pickProvider(prov)"
              >
                <n-icon
                  v-if="picked?.id === prov.id"
                  :component="CheckmarkOutline"
                  :size="11"
                />
                {{ picked?.id === prov.id ? t("v3.selected") : t("v3.useThis") }}
              </button>
              <a
                :href="prov.signupUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="v3-btn v3-btn--sm"
                @click.stop
              >
                <n-icon :component="OpenOutline" :size="11" />
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
  padding: 16px;
  overflow: auto;
  flex: 1;
}
.v3-ngf__foot {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 16px;
  border-top: 1px solid var(--v3-line);
  background: var(--v3-surface-2);
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
