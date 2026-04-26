<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import {
  NAlert,
  NButton,
  NEmpty,
  NIcon,
  NInputNumber,
  NSelect,
  NSpin,
  NSwitch,
  NTag,
  useMessage,
  type SelectOption,
} from "naive-ui";
import { Save, Refresh, Add, Close, FlashOutline, PlayOutline } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";
import { getGroupList } from "@/api/dashboard";

const { t } = useI18n();

interface RouteConfig {
  enabled: boolean;
  simple_threshold: number;
  complex_threshold: number;
  group_mapping: Record<
    string,
    {
      simple_group: string;
      medium_group: string;
      complex_group: string;
    }
  >;
}

interface RouteAnalysis {
  level: string;
  estimated_tokens: number;
  has_vision: boolean;
  tool_count: number;
  message_count: number;
  has_tools: boolean;
  max_msg_length: number;
}

interface TestResult {
  target_group: string;
  analysis: RouteAnalysis | null;
  message?: string;
}

const PRESETS: Record<string, { simple: number; complex: number }> = {
  economy: { simple: 500, complex: 2000 },
  balanced: { simple: 2000, complex: 8000 },
  performance: { simple: 4000, complex: 16000 },
};

const AXIS_MAX = 20000;

const DEFAULT_TEST_BODY = JSON.stringify(
  {
    model: "gpt-4o",
    messages: [{ role: "user", content: "Hello, how are you?" }],
  },
  null,
  2
);

const message = useMessage();
const loading = ref(false);
const fetchLoading = ref(false);
const fetchError = ref(false);

const config = ref<RouteConfig>({
  enabled: false,
  simple_threshold: 2000,
  complex_threshold: 8000,
  group_mapping: {},
});

interface CatalogEntry {
  id: string;
  display_name: string;
  groups: string[];
}
const catalogModels = ref<CatalogEntry[]>([]);
const groupOptions = ref<SelectOption[]>([]);
const groupModelMap = ref<Record<string, string[]>>({});

const modelGroupsMap = computed<Record<string, string[]>>(() => {
  const map: Record<string, string[]> = {};
  for (const m of catalogModels.value) {
    map[m.id] = m.groups || [];
  }
  return map;
});

const newMappingGroup = ref<string | null>(null);
const newMappingSimple = ref<string | null>(null);
const newMappingMedium = ref<string | null>(null);
const newMappingComplex = ref<string | null>(null);

const modelOptions = computed<SelectOption[]>(() =>
  catalogModels.value.map(m => {
    const used = !!config.value.group_mapping[m.id];
    const suffix = m.groups.length ? ` · ${m.groups.length} 组` : "";
    return {
      label: `${m.id}${suffix}${used ? "  (已映射)" : ""}`,
      value: m.id,
      disabled: used,
    };
  })
);

const newMappingHintGroups = computed<string[]>(() =>
  newMappingGroup.value ? modelGroupsMap.value[newMappingGroup.value] || [] : []
);

const testModelKey = ref<string | null>(null);
const testBody = ref(DEFAULT_TEST_BODY);
const testLoading = ref(false);
const testResult = ref<TestResult | null>(null);
const testError = ref<string>("");

const mappingKeyOptions = computed<SelectOption[]>(() =>
  Object.keys(config.value.group_mapping).map(k => ({ label: k, value: k }))
);

const authHeader = computed(() => {
  const authKey = localStorage.getItem("authKey");
  return authKey ? `Bearer ${authKey}` : "";
});

onMounted(async () => {
  await Promise.all([fetchConfig(), fetchGroups(), fetchCatalog()]);
});

async function fetchCatalog() {
  try {
    const response = await fetch("/api/models", {
      headers: { Authorization: authHeader.value },
    });
    if (!response.ok) {return;}
    const result = await response.json();
    catalogModels.value = (result.data || []) as CatalogEntry[];
  } catch {
    // best-effort
  }
}

async function fetchConfig() {
  fetchLoading.value = true;
  fetchError.value = false;
  try {
    const response = await fetch("/api/auto-routing/config", {
      headers: {
        Authorization: authHeader.value,
        "Content-Type": "application/json",
      },
    });
    if (!response.ok) {throw new Error(`HTTP ${response.status}`);}
    const result = await response.json();
    if (result.success && result.config) {
      config.value = {
        enabled: result.config.enabled ?? false,
        simple_threshold: result.config.simple_threshold ?? 2000,
        complex_threshold: result.config.complex_threshold ?? 8000,
        group_mapping: result.config.group_mapping ?? {},
      };
    }
  } catch {
    fetchError.value = true;
    message.error(t("common.requestFailed"));
  } finally {
    fetchLoading.value = false;
  }
}

interface GroupRow {
  name: string;
  display_name?: string;
  available_models?: unknown;
}

function parseModels(raw: unknown): string[] {
  if (Array.isArray(raw)) {return raw.filter((m): m is string => typeof m === "string");}
  if (typeof raw === "string" && raw.trim().length > 0) {
    try {
      const arr = JSON.parse(raw);
      return Array.isArray(arr) ? arr.filter((m): m is string => typeof m === "string") : [];
    } catch {
      return [];
    }
  }
  return [];
}

async function fetchGroups() {
  try {
    const response = await getGroupList();
    const list = (response as unknown as { data: GroupRow[] }).data || [];
    const map: Record<string, string[]> = {};
    groupOptions.value = list.map(g => {
      const models = parseModels(g.available_models);
      map[g.name] = models;
      const display = g.display_name ? `${g.display_name} (${g.name})` : g.name;
      const suffix = models.length > 0 ? ` · ${models.length} ${t("autoroute.modelsSuffix")}` : "";
      return { label: display + suffix, value: g.name };
    });
    groupModelMap.value = map;
  } catch {
    // tolerate
  }
}

function groupModelHint(groupName: string | null | undefined, max = 3): string {
  if (!groupName) {return "";}
  const models = groupModelMap.value[groupName] || [];
  if (models.length === 0) {return "";}
  const sample = models.slice(0, max).join(", ");
  const more = models.length > max ? ` +${models.length - max}` : "";
  return `${sample}${more}`;
}

async function handleSubmit() {
  loading.value = true;
  try {
    const response = await fetch("/api/auto-routing/config", {
      method: "POST",
      headers: {
        Authorization: authHeader.value,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(config.value),
    });
    if (!response.ok) {throw new Error(`HTTP ${response.status}`);}
    message.success(t("common.operationSuccess"));
  } catch {
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
}

function applyPreset(name: keyof typeof PRESETS) {
  const p = PRESETS[name];
  config.value.simple_threshold = p.simple;
  config.value.complex_threshold = p.complex;
  message.success(t("autoroute.presetApplied"));
}

function activePreset(): string | null {
  for (const [name, p] of Object.entries(PRESETS)) {
    if (
      p.simple === config.value.simple_threshold &&
      p.complex === config.value.complex_threshold
    ) {
      return name;
    }
  }
  return null;
}

function pickedModelAutofill(modelId: string | null) {
  if (!modelId) {return;}
  const groups = modelGroupsMap.value[modelId];
  if (!groups || groups.length === 0) {return;}
  const pick = (idx: number) => groups[Math.min(idx, groups.length - 1)];
  if (!newMappingSimple.value) {newMappingSimple.value = pick(0);}
  if (!newMappingMedium.value) {newMappingMedium.value = pick(Math.floor(groups.length / 2));}
  if (!newMappingComplex.value) {newMappingComplex.value = pick(groups.length - 1);}
  if (groups.length > 0) {message.info(t("autoroute.autoFilledFromCatalog"));}
}

function addMapping() {
  if (!newMappingGroup.value) {
    message.warning(t("autoroute.pleaseEnterModelName"));
    return;
  }
  if (config.value.group_mapping[newMappingGroup.value]) {
    message.warning(t("autoroute.modelAlreadyExists"));
    return;
  }
  config.value.group_mapping[newMappingGroup.value] = {
    simple_group: newMappingSimple.value ?? "",
    medium_group: newMappingMedium.value ?? "",
    complex_group: newMappingComplex.value ?? "",
  };
  newMappingGroup.value = null;
  newMappingSimple.value = null;
  newMappingMedium.value = null;
  newMappingComplex.value = null;
}

function removeMapping(key: string) {
  delete config.value.group_mapping[key];
}

async function runTest() {
  testError.value = "";
  testResult.value = null;
  if (!testModelKey.value) {
    testError.value = t("autoroute.pleaseEnterModelName");
    return;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(testBody.value);
  } catch {
    testError.value = t("autoroute.testInvalidJSON");
    return;
  }
  testLoading.value = true;
  try {
    const response = await fetch("/api/auto-routing/test", {
      method: "POST",
      headers: {
        Authorization: authHeader.value,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        group_name: testModelKey.value,
        request_body: parsed,
      }),
    });
    if (!response.ok) {throw new Error(`HTTP ${response.status}`);}
    const result = await response.json();
    if (!result.success) {throw new Error(result.error || "test failed");}
    testResult.value = {
      target_group: result.target_group,
      analysis: result.analysis,
      message: result.message,
    };
  } catch (e) {
    testError.value = (e as Error).message;
  } finally {
    testLoading.value = false;
  }
}

const tiers = [
  {
    id: "simple" as const,
    title: "Simple",
    hint: "fastest & cheapest",
    rule: () => `tokens < ${config.value.simple_threshold.toLocaleString()} · few tools`,
    color: "var(--v3-ok)",
  },
  {
    id: "medium" as const,
    title: "Medium",
    hint: "balanced",
    rule: () =>
      `${config.value.simple_threshold.toLocaleString()}–${config.value.complex_threshold.toLocaleString()} tok`,
    color: "var(--v3-warn)",
  },
  {
    id: "complex" as const,
    title: "Complex",
    hint: "flagship",
    rule: () => `> ${config.value.complex_threshold.toLocaleString()} tok · vision · 4+ tools`,
    color: "var(--v3-danger)",
  },
];

const mappingEntries = computed(() =>
  Object.entries(config.value.group_mapping).map(([model, m]) => ({
    model,
    simple: m.simple_group,
    medium: m.medium_group,
    complex: m.complex_group,
  }))
);

function setMappingGroup(
  model: string,
  tier: "simple" | "medium" | "complex",
  value: string | null
) {
  const entry = config.value.group_mapping[model];
  if (!entry) {return;}
  if (tier === "simple") {entry.simple_group = value ?? "";}
  if (tier === "medium") {entry.medium_group = value ?? "";}
  if (tier === "complex") {entry.complex_group = value ?? "";}
}
</script>

<template>
  <div>
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.auto") }}</div>
      <div class="v3-viewhead__actions">
        <span class="v3-routing-state">
          <span
            class="v3-state-dot"
            :style="{ background: config.enabled ? 'var(--v3-ok)' : 'var(--v3-ink-4)' }"
          />
          {{ config.enabled ? t("v3.routingActive") : t("v3.routingPassthrough") }}
        </span>
        <n-switch v-model:value="config.enabled" />
        <button class="v3-btn" @click="fetchConfig">
          <n-icon :component="Refresh" :size="12" />
          {{ t("common.refresh") }}
        </button>
      </div>
    </div>
    <h1 class="v3-viewtitle">{{ t("autoroute.title") }}</h1>
    <div class="v3-viewhead__sub" style="margin: -8px 0 16px">
      Cheap path for simple prompts. Flagship only when it pays off. Each model can be mapped to a
      different group per tier.
    </div>

    <n-alert
      v-if="fetchError"
      type="error"
      :title="t('common.error')"
      closable
      @close="fetchError = false"
      style="margin-bottom: 14px"
    >
      {{ t("autoroute.loadConfigFailed") }}
    </n-alert>

    <!-- Threshold axis -->
    <div class="v3-thresh-card">
      <div style="display: flex; align-items: center; gap: 14px; margin-bottom: 4px">
        <div style="font: 600 13px var(--v3-sans)">Complexity thresholds</div>
        <span style="font: 400 11.5px var(--v3-mono); color: var(--v3-ink-3)">
          token count → tier
        </span>
        <div style="margin-left: auto; display: flex; gap: 6px">
          <button
            v-for="p in Object.keys(PRESETS)"
            :key="p"
            class="v3-btn v3-btn--sm"
            :class="{ 'v3-btn--accent': activePreset() === p }"
            @click="applyPreset(p as keyof typeof PRESETS)"
          >
            {{ p }}
            <span style="font: 500 10px var(--v3-mono); margin-left: 4px; opacity: 0.65">
              {{ PRESETS[p].simple }}/{{ PRESETS[p].complex }}
            </span>
          </button>
        </div>
      </div>
      <div class="v3-thresh-axis">
        <div class="v3-thresh-cap" style="left: 0">0 tok</div>
        <div class="v3-thresh-cap" style="right: 0">{{ AXIS_MAX.toLocaleString() }} tok</div>
        <div
          class="v3-thresh-track"
          :style="{
            background: `linear-gradient(to right, var(--v3-ok) 0%, var(--v3-ok) ${(config.simple_threshold / AXIS_MAX) * 100}%, var(--v3-warn) ${(config.simple_threshold / AXIS_MAX) * 100}%, var(--v3-warn) ${(config.complex_threshold / AXIS_MAX) * 100}%, var(--v3-danger) ${(config.complex_threshold / AXIS_MAX) * 100}%, var(--v3-danger) 100%)`,
          }"
        />
        <div
          class="v3-thresh-stop"
          :style="{ left: `${(config.simple_threshold / AXIS_MAX) * 100}%` }"
        />
        <div
          class="v3-thresh-tick"
          :style="{ left: `${(config.simple_threshold / AXIS_MAX) * 100}%` }"
        >
          {{ config.simple_threshold.toLocaleString() }}
        </div>
        <div
          class="v3-thresh-stop"
          :style="{ left: `${(config.complex_threshold / AXIS_MAX) * 100}%` }"
        />
        <div
          class="v3-thresh-tick"
          :style="{ left: `${(config.complex_threshold / AXIS_MAX) * 100}%` }"
        >
          {{ config.complex_threshold.toLocaleString() }}
        </div>
      </div>
      <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 18px">
        <div>
          <div
            style="
              font: 500 10px/1 var(--v3-mono);
              letter-spacing: 0.1em;
              text-transform: uppercase;
              color: var(--v3-ink-3);
              margin-bottom: 6px;
            "
          >
            {{ t("autoroute.simpleThreshold") }}
          </div>
          <n-input-number
            v-model:value="config.simple_threshold"
            :min="0"
            :step="100"
            style="width: 100%"
          />
        </div>
        <div>
          <div
            style="
              font: 500 10px/1 var(--v3-mono);
              letter-spacing: 0.1em;
              text-transform: uppercase;
              color: var(--v3-ink-3);
              margin-bottom: 6px;
            "
          >
            {{ t("autoroute.complexThreshold") }}
          </div>
          <n-input-number
            v-model:value="config.complex_threshold"
            :min="0"
            :step="100"
            style="width: 100%"
          />
        </div>
      </div>
    </div>

    <!-- Three tier columns -->
    <div class="v3-tier-board" style="margin-top: 16px">
      <div v-for="(t2, ti) in tiers" :key="t2.id" class="v3-tier" :class="`v3-tier--${t2.id}`">
        <div class="v3-tier__band" />
        <div class="v3-tier__head">
          <div class="v3-tier__icon">{{ String(ti + 1).padStart(2, "0") }}</div>
          <div style="flex: 1">
            <div class="v3-tier__title">
              {{ t2.title }}
              <span style="font: 400 11px var(--v3-mono); color: var(--v3-ink-3); margin-left: 6px">
                >· {{ t2.hint }}</span
              </span>
            </div>
            <div class="v3-tier__rule">{{ t2.rule() }}</div>
          </div>
        </div>
        <div class="v3-tier__divider" />
        <div class="v3-tier__lbl-row">
          <div class="v3-tier__lbl">Mapped models ({{ mappingEntries.length }})</div>
          <span class="v3-tier__lbl" style="color: var(--v3-ink-4)">
            <n-icon :component="FlashOutline" :size="11" />
            per-model group
          </span>
        </div>
        <div class="v3-chain">
          <div v-for="entry in mappingEntries" :key="entry.model + t2.id" class="v3-chain__item">
            <div style="min-width: 0; grid-column: 1 / -1">
              <div class="v3-chain__rank">{{ entry.model }}</div>
              <div style="margin-top: 6px">
                <n-select
                  :value="entry[t2.id]"
                  :options="groupOptions"
                  :placeholder="`${t2.title} group`"
                  filterable
                  clearable
                  tag
                  size="small"
                  @update:value="v => setMappingGroup(entry.model, t2.id, v)"
                />
                <div
                  v-if="groupModelHint(entry[t2.id])"
                  style="
                    font: 400 10.5px/1.4 var(--v3-mono);
                    color: var(--v3-ink-3);
                    margin-top: 5px;
                  "
                >
                  {{ groupModelHint(entry[t2.id]) }}
                </div>
              </div>
            </div>
          </div>
          <div v-if="!mappingEntries.length" class="v3-empty-hint">
            No model mappings yet — add one below.
          </div>
        </div>
      </div>
    </div>

    <!-- Add new mapping card -->
    <div class="v3-card" style="margin-top: 16px">
      <div class="v3-card__head">
        <div>
          <div class="v3-card__title">
            <n-icon :component="Add" :size="13" />
            {{ t("autoroute.addNewMapping") }}
          </div>
          <div class="v3-card__sub">
            Map a model name to one group per tier. Auto-routing picks the tier from token count.
          </div>
        </div>
      </div>
      <div class="v3-card__body">
        <div
          style="
            display: grid;
            grid-template-columns: 1.4fr 1fr 1fr 1fr auto auto;
            gap: 8px;
            align-items: center;
          "
        >
          <n-select
            v-model:value="newMappingGroup"
            :options="modelOptions"
            :placeholder="t('autoroute.selectModelPlaceholder')"
            filterable
            clearable
            tag
            size="small"
            @update:value="pickedModelAutofill"
          />
          <n-select
            v-model:value="newMappingSimple"
            :options="groupOptions"
            :placeholder="t('autoroute.simpleGroup')"
            filterable
            clearable
            tag
            size="small"
          />
          <n-select
            v-model:value="newMappingMedium"
            :options="groupOptions"
            :placeholder="t('autoroute.mediumGroup')"
            filterable
            clearable
            tag
            size="small"
          />
          <n-select
            v-model:value="newMappingComplex"
            :options="groupOptions"
            :placeholder="t('autoroute.complexGroup')"
            filterable
            clearable
            tag
            size="small"
          />
          <button class="v3-btn v3-btn--accent v3-btn--sm" @click="addMapping">
            <n-icon :component="Add" :size="12" />
            {{ t("common.add") }}
          </button>
          <button
            class="v3-btn v3-btn--sm"
            @click="
              () => {
                newMappingGroup = null;
                newMappingSimple = null;
                newMappingMedium = null;
                newMappingComplex = null;
              }
            "
          >
            <n-icon :component="Close" :size="12" />
          </button>
        </div>
        <div
          v-if="newMappingHintGroups.length"
          style="font: 400 11px/1.4 var(--v3-mono); color: var(--v3-ink-3); margin-top: 8px"
        >
          {{ t("autoroute.modelGroupsHint", { groups: newMappingHintGroups.join(", ") }) }}
        </div>
        <div
          v-if="!mappingEntries.length"
          style="
            display: flex;
            justify-content: center;
            padding: 16px;
            font-size: 12.5px;
            color: var(--v3-ink-3);
          "
        >
          <n-empty :description="t('autoroute.noMappings')" size="small" />
        </div>
      </div>
    </div>

    <!-- Existing mappings table view -->
    <div v-if="mappingEntries.length" class="v3-card" style="margin-top: 16px">
      <div class="v3-card__head">
        <div>
          <div class="v3-card__title">{{ t("autoroute.groupMappings") }}</div>
          <div class="v3-card__sub">{{ mappingEntries.length }} mapped models</div>
        </div>
      </div>
      <table class="v3-ktable">
        <thead>
          <tr>
            <th>Model</th>
            <th>Simple</th>
            <th>Medium</th>
            <th>Complex</th>
            <th/>
          </tr>
        </thead>
        <tbody>
          <tr v-for="entry in mappingEntries" :key="entry.model">
            <td>
              <code
                style="
                  font: 500 12px var(--v3-mono);
                  background: var(--v3-surface-2);
                  padding: 2px 6px;
                  border-radius: 3px;
                "
              >{{ entry.model }}</code
              </code>
            </td>
            <td>
              <span class="v3-chip v3-chip--ok">{{ entry.simple || "—" }}</span>
            </td>
            <td>
              <span class="v3-chip v3-chip--warn">{{ entry.medium || "—" }}</span>
            </td>
            <td>
              <span class="v3-chip v3-chip--danger">{{ entry.complex || "—" }}</span>
            </td>
            <td style="text-align: right">
              <button
                class="v3-btn v3-btn--ghost v3-btn--sm v3-btn--danger"
                @click="removeMapping(entry.model)"
              >
                <n-icon :component="Close" :size="11" />
                {{ t("common.delete") }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Route tester -->
    <div class="v3-card" style="margin-top: 16px">
      <div class="v3-card__head">
        <div>
          <div class="v3-card__title">{{ t("autoroute.routeTester") }}</div>
          <div class="v3-card__sub">{{ t("autoroute.routeTesterTip") }}</div>
        </div>
      </div>
      <div class="v3-card__body">
        <div class="v3-tester">
          <div class="v3-tester__panel">
            <div
              style="
                display: flex;
                justify-content: space-between;
                align-items: center;
                margin-bottom: 8px;
              "
            >
              <span
                style="
                  font: 500 11px var(--v3-mono);
                  letter-spacing: 0.06em;
                  text-transform: uppercase;
                  color: var(--v3-ink-3);
                "
              >Request body</span
              </span>
              <button
                class="v3-btn v3-btn--accent v3-btn--sm"
                :disabled="testLoading"
                @click="runTest"
              >
                <n-icon :component="PlayOutline" :size="11" />
                {{ testLoading ? "Running…" : t("autoroute.runTest") }}
              </button>
            </div>
            <div style="margin-bottom: 8px">
              <n-select
                v-model:value="testModelKey"
                :options="mappingKeyOptions"
                :placeholder="t('autoroute.modelNamePlaceholder')"
                filterable
                size="small"
              />
            </div>
            <textarea
              v-model="testBody"
              spellcheck="false"
              style="
                width: 100%;
                min-height: 180px;
                background: oklch(0.18 0.018 245);
                color: oklch(0.92 0.008 250);
                padding: 12px 14px;
                border-radius: 6px;
                border: 0;
                outline: 0;
                resize: vertical;
                font: 500 12px/1.6 var(--v3-mono);
              "
            />
          </div>
          <div class="v3-tester__panel">
            <div
              style="
                font: 500 11px var(--v3-mono);
                letter-spacing: 0.06em;
                text-transform: uppercase;
                color: var(--v3-ink-3);
                margin-bottom: 8px;
              "
            >
              Decision
            </div>
            <n-spin :show="testLoading">
              <div v-if="testError" style="color: var(--v3-danger); font-size: 12px">
                {{ testError }}
              </div>
              <div v-else-if="testResult && testResult.analysis">
                <div class="v3-result-row">
                  <span class="v3-result-row__k">Target group</span>
                  <span class="v3-result-row__v">
                    <span class="v3-chip v3-chip--info">{{ testResult.target_group }}</span>
                  </span>
                </div>
                <div class="v3-result-row">
                  <span class="v3-result-row__k">{{ t("v3.routedTier") }}</span>
                  <span class="v3-result-row__v">
                    <span
                      class="v3-chip"
                      :class="{
                        'v3-chip--ok': testResult.analysis.level === 'simple',
                        'v3-chip--warn': testResult.analysis.level === 'medium',
                        'v3-chip--danger': testResult.analysis.level === 'complex',
                      }"
                    >{{ testResult.analysis.level }}</span
                    </span>
                  </span>
                </div>
                <div class="v3-result-row">
                  <span class="v3-result-row__k">Estimated tokens</span>
                  <span class="v3-result-row__v tnum">
                    {{ testResult.analysis.estimated_tokens }}
                  </span>
                </div>
                <div class="v3-result-row">
                  <span class="v3-result-row__k">Tool count</span>
                  <span class="v3-result-row__v tnum">{{ testResult.analysis.tool_count }}</span>
                </div>
                <div class="v3-result-row">
                  <span class="v3-result-row__k">Has vision</span>
                  <span class="v3-result-row__v">
                    {{ testResult.analysis.has_vision ? "yes" : "no" }}
                  </span>
                </div>
                <div class="v3-result-row">
                  <span class="v3-result-row__k">Message count</span>
                  <span class="v3-result-row__v tnum">{{ testResult.analysis.message_count }}</span>
                </div>
              </div>
              <div
                v-else-if="testResult"
                style="font-size: 12px; color: var(--v3-ink-3); padding: 16px 0; text-align: center"
              >
                {{ t("autoroute.testNoMapping") }}
              </div>
              <div
                v-else
                style="font-size: 12px; color: var(--v3-ink-4); padding: 24px 0; text-align: center"
              >
                Run test to see how the request is routed.
              </div>
            </n-spin>
          </div>
        </div>
      </div>
    </div>

    <div style="display: flex; justify-content: center; padding: 18px 0 4px">
      <button
        class="v3-btn v3-btn--accent v3-btn--lg"
        :disabled="loading || fetchLoading"
        @click="handleSubmit"
        style="min-width: 220px"
      >
        <n-icon :component="Save" :size="13" />
        {{ loading ? "Saving…" : t("common.save") }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.v3-routing-state {
  font: 500 11px/1 var(--v3-mono);
  letter-spacing: 0.06em;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--v3-ink-3);
  text-transform: uppercase;
}
.v3-state-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  display: inline-block;
}
.v3-card .v3-ktable th {
  background: var(--v3-surface-2);
}
.v3-card .v3-ktable {
  width: 100%;
}
</style>
