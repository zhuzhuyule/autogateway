<script setup lang="ts">
import { findFreeModel, findProviderByUpstreams, isFree, FREE_PROVIDERS, type ModelTier } from "@/data/freeProviders";
import { freeModelsRef, getFreeStatus, lookupRegistry } from "@/api/freemodels";
import { keysApi } from "@/api/keys";
import type { Group } from "@/types/models";
import FreeBadge from "@/components/common/FreeBadge.vue";
import SpeedBadge from "@/components/common/SpeedBadge.vue";
import { RefreshOutline, SearchOutline } from "@vicons/ionicons5";
import { NIcon, NSpin, useMessage } from "naive-ui";
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

interface ModelItem {
  id: string;
  display_name: string;
  owned_by: string;
  groups: string[];
  /** 该模型是否已绑定到用户的某个 group (groups.length > 0). */
  configured: boolean;
}

interface AugmentedItem extends ModelItem {
  isFree: boolean;
  freeVariant: "full" | "trial" | null;
  tier?: ModelTier;
  /** 该模型涉及的 provider id 集合 (合并: row.groups 反查 + registry.provider). */
  providers: string[];
  /** UI 头像选哪个 provider — 取 providers[0] 或 row.owned_by. */
  freeProviderId?: string;
  hasVision: boolean;
  hasTools: boolean;
  isReasoning: boolean;
  contextHint: string | null;
  /** registry 直读的 speed 档位 (fast/balanced/slow), 缺失时 null. */
  speed: "fast" | "balanced" | "slow" | null;
}

/**
 * Capability flags 优先取自 FreeModels Registry; registry miss 时降级关键字推断,
 * 让 catalog 行的 chip 不至于全空。无客户端打分。
 */
function fallbackVision(id: string): boolean {
  const lower = id.toLowerCase();
  return (
    lower.includes("vision") ||
    lower.includes("4o") ||
    lower.includes("gpt-5") ||
    lower.includes("claude-3") ||
    lower.includes("claude-4") ||
    lower.includes("claude-sonnet") ||
    lower.includes("claude-opus") ||
    lower.includes("claude-haiku") ||
    lower.includes("gemini")
  );
}

function fallbackTools(id: string): boolean {
  const lower = id.toLowerCase();
  if (lower.includes("embedding") || lower.includes("whisper") || lower.includes("tts")) {
    return false;
  }
  if (lower.includes("base") || lower.includes("raw")) {
    return false;
  }
  return true;
}

const loading = ref(false);
const fetchError = ref(false);
const catalogData = ref<ModelItem[]>([]);
// 用户已配 groups — 用来反查每个 group_name → provider id (内置 freeProviders 反查)
const groupsCache = ref<Group[]>([]);
const groupNameToProvider = computed<Record<string, string>>(() => {
  const map: Record<string, string> = {};
  for (const g of groupsCache.value) {
    if (g.group_type === "aggregate") {
      continue;
    }
    const p = findProviderByUpstreams(g.upstreams || []);
    if (p) {
      map[g.name] = p.id;
    } else {
      // fallback: 用 group.name 子串去比对内置 provider id 列表
      const lower = g.name.toLowerCase();
      const hit = FREE_PROVIDERS.find(fp => lower.includes(fp.id));
      if (hit) {
        map[g.name] = hit.id;
      }
    }
  }
  return map;
});

async function loadGroups() {
  try {
    groupsCache.value = await keysApi.getGroups();
  } catch {
    groupsCache.value = [];
  }
}

const searchText = ref("");
const tierFilter = ref<ModelTier | "all">("all");
const providerFilter = ref<string | "all">("all");
const freeOnly = ref(false);
const configuredOnly = ref(false);

const authHeader = computed(() => {
  const k = localStorage.getItem("authKey");
  return k ? `Bearer ${k}` : "";
});

// 合并本地 catalog (/api/models) 和 FreeModels Registry, 按 bare modelId 去重。
// FreeModels CDN 的 modelId 都带 provider 前缀 ("bigmodel/glm-4-flash-250414"),
// 上游真实返回的 group.available_models 是裸 id ("glm-4-flash-250414") — 必须
// 用裸形式做 key 才能正确去重, 否则一条模型会被显示两次.
function bareId(id: string): string {
  const slash = id.indexOf("/");
  return slash >= 0 ? id.slice(slash + 1) : id;
}

const mergedRows = computed<ModelItem[]>(() => {
  const out = new Map<string, ModelItem>();
  for (const r of catalogData.value) {
    const key = bareId(r.id).toLowerCase();
    out.set(key, { ...r, configured: (r.groups || []).length > 0 });
  }
  const env = freeModelsRef.value;
  if (env?.models) {
    for (const m of env.models) {
      const bare = bareId(m.modelId);
      const key = bare.toLowerCase();
      if (out.has(key)) {
        continue; // 重复 → 以本地为准 (有 groups), 不再追加
      }
      out.set(key, {
        id: bare,
        display_name: m.name || bare,
        owned_by: m.provider || "",
        groups: [],
        configured: false,
      });
    }
  }
  return Array.from(out.values());
});

const augmented = computed<AugmentedItem[]>(() =>
  mergedRows.value.map(row => {
    // Registry 单一可信源:能 hit 就直接用,字段都齐全(isFree / isMultimodal /
    // hasToolUse / isReasoning / contextLabel / tier / speed)。
    const reg = lookupRegistry(undefined, row.id);
    const status = getFreeStatus(undefined, row.id);

    // 真实 providers: row.groups 反查 + registry.provider 合并 (并集)
    const providers = new Set<string>();
    for (const gName of row.groups || []) {
      const pid = groupNameToProvider.value[gName];
      if (pid) {
        providers.add(pid);
      }
    }
    if (reg?.provider) {
      providers.add(reg.provider);
    }
    // freeProviderId 用作 UI 头像/标签 — 取第一个 provider, fallback registry/free
    const free = findFreeModel(row.id);
    const providerId =
      [...providers][0] || reg?.provider || free?.providerId || row.owned_by || undefined;

    const isFreeFlag =
      status === "full" || status === "trial"
        ? true
        : status === "paid"
          ? false
          : free
            ? true
            : isFree(undefined, row.id) === true;

    const freeVariant: "full" | "trial" | null =
      status === "trial" ? "trial" : status === "full" || isFreeFlag ? "full" : null;

    // tier(老 fast/balanced/max 三档)— registry 的 speed 优先映射, 否则 free.tier
    let tier: ModelTier | undefined = free?.tier;
    if (reg?.speed === "fast") {
      tier = "fast";
    } else if (reg?.speed === "slow") {
      tier = "max";
    } else if (reg?.speed === "balanced") {
      tier = "balanced";
    }

    return {
      ...row,
      isFree: isFreeFlag,
      freeVariant,
      tier,
      providers: [...providers],
      freeProviderId: providerId,
      hasVision: reg ? reg.isMultimodal : fallbackVision(row.id),
      hasTools: reg ? reg.hasToolUse : fallbackTools(row.id),
      isReasoning: reg ? reg.isReasoning : false,
      contextHint: reg?.contextLabel || null,
      speed: (reg?.speed as "fast" | "balanced" | "slow" | null) || null,
    };
  })
);

const filtered = computed<AugmentedItem[]>(() => {
  const q = searchText.value.trim().toLowerCase();
  return augmented.value
    .filter(row => {
      if (
        q &&
        !row.id.toLowerCase().includes(q) &&
        !(row.display_name || "").toLowerCase().includes(q)
      ) {
        return false;
      }
      if (freeOnly.value && !row.isFree) {
        return false;
      }
      if (configuredOnly.value && !row.configured) {
        return false;
      }
      if (tierFilter.value !== "all" && row.tier !== tierFilter.value) {
        return false;
      }
      if (providerFilter.value !== "all" && !row.providers.includes(providerFilter.value)) {
        return false;
      }
      return true;
    })
    .sort((a, b) => {
      if (a.isFree !== b.isFree) {
        return a.isFree ? -1 : 1;
      }
      return a.id.localeCompare(b.id);
    });
});

const freeCount = computed(() => augmented.value.filter(r => r.isFree).length);

const tierPills: Array<{ k: ModelTier | "all"; label: string }> = [
  { k: "all", label: t("modelcatalog.allTiers") || "All tiers" },
  { k: "fast", label: t("modelcatalog.tierFast") || "Fast" },
  { k: "balanced", label: t("modelcatalog.tierBalanced") || "Balanced" },
  { k: "max", label: t("modelcatalog.tierMax") || "Max" },
];

function tierChipClass(tier?: ModelTier): string {
  if (tier === "fast") {
    return "v3-chip v3-chip--ok";
  }
  if (tier === "balanced") {
    return "v3-chip v3-chip--warn";
  }
  if (tier === "max") {
    return "v3-chip v3-chip--danger";
  }
  return "v3-chip";
}

function providerShort(id?: string): string {
  if (!id) {
    return "—";
  }
  const p = FREE_PROVIDERS.find(x => x.id === id);
  if (p) {
    return p.name
      .replace(/[^A-Za-z0-9]/g, "")
      .slice(0, 2)
      .toUpperCase();
  }
  return id.slice(0, 2).toUpperCase();
}

function providerName(id?: string): string {
  if (!id) {
    return "—";
  }
  return FREE_PROVIDERS.find(x => x.id === id)?.name || id;
}

function pavClassFor(providerId?: string): string {
  if (!providerId) {
    return "v3-pav v3-pav-default";
  }
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
  if (known.includes(providerId)) {
    return `v3-pav v3-pav-${providerId}`;
  }
  if (providerId.includes("google")) {
    return "v3-pav v3-pav-google";
  }
  if (providerId.includes("github")) {
    return "v3-pav v3-pav-github";
  }
  if (providerId.includes("hugging")) {
    return "v3-pav v3-pav-cohere";
  }
  return "v3-pav v3-pav-default";
}

onMounted(async () => {
  await Promise.all([fetchCatalog(), loadGroups()]);
});

async function fetchCatalog() {
  loading.value = true;
  fetchError.value = false;
  try {
    const response = await fetch("/api/models", {
      headers: { Authorization: authHeader.value },
    });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const result = await response.json();
    catalogData.value = result.data || [];
  } catch (e) {
    fetchError.value = true;
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <div>
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.models") }}</div>
      <div class="v3-viewhead__actions">
        <div class="v3-search">
          <n-icon :component="SearchOutline" :size="12" />
          <input
            v-model="searchText"
            :placeholder="t('modelcatalog.searchPlaceholder') || 'Filter models…'"
          />
        </div>
        <button class="v3-btn" @click="fetchCatalog">
          <n-icon :component="RefreshOutline" :size="12" />
          {{ t("common.refresh") || "Refresh" }}
        </button>
      </div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("modelcatalog.title") || "Model catalog" }}
      <span class="v3-viewtitle__meta">
        {{ filtered.length }} / {{ catalogData.length }} entries · {{ freeCount }} free
      </span>
    </h1>

    <!-- Filter pills -->
    <div style="display: flex; flex-wrap: wrap; gap: 6px; margin: -4px 0 12px; align-items: center">
      <span
        v-for="p in tierPills"
        :key="p.k"
        class="v3-pill"
        :class="{ 'v3-pill--active': tierFilter === p.k }"
        @click="tierFilter = p.k"
      >
        {{ p.label }}
      </span>
      <span style="color: var(--v3-line); margin: 0 4px">|</span>
      <span class="v3-pill" :class="{ 'v3-pill--active': freeOnly }" @click="freeOnly = !freeOnly">
        <FreeBadge compact :size="11" />
        <span style="margin-left: 4px">{{ t("modelcatalog.freeOnly") || "Free only" }}</span>
      </span>
      <span
        class="v3-pill"
        :class="{ 'v3-pill--active': configuredOnly }"
        @click="configuredOnly = !configuredOnly"
      >
        {{ t("modelcatalog.onlyConfigured") || "已配置" }}
      </span>
      <span style="color: var(--v3-line); margin: 0 4px">|</span>
      <span
        class="v3-pill"
        :class="{ 'v3-pill--active': providerFilter === 'all' }"
        @click="providerFilter = 'all'"
      >
        {{ t("modelcatalog.allProviders") || "All providers" }}
      </span>
      <span
        v-for="prov in FREE_PROVIDERS"
        :key="prov.id"
        class="v3-pill"
        :class="{ 'v3-pill--active': providerFilter === prov.id }"
        @click="providerFilter = prov.id"
      >
        {{ prov.name }}
      </span>
    </div>

    <div
      v-if="fetchError"
      class="v3-empty-hint"
      style="background: var(--v3-danger-soft); color: var(--v3-danger); margin-bottom: 12px"
    >
      {{ t("modelcatalog.loadFailed") || "Failed to load model catalog" }}
    </div>

    <n-spin :show="loading">
      <div class="v3-card">
        <div class="v3-model-row v3-model-row--head">
          <div />
          <div>{{ t("modelcatalog.modelId") || "Model" }}</div>
          <div>{{ t("modelcatalog.ownedBy") || "Owned by" }}</div>
          <div>{{ t("modelcatalog.groups") || "Groups" }}</div>
          <div style="text-align: right">
            {{ t("modelcatalog.tier") || "Tier" }}
          </div>
        </div>
        <div
          v-for="row in filtered"
          :key="`${row.id}-${row.freeProviderId || ''}`"
          class="v3-model-row"
        >
          <div>
            <span
              :class="pavClassFor(row.freeProviderId)"
              style="width: 24px; height: 24px; border-radius: 5px; font-size: 9px"
            >
              {{ providerShort(row.freeProviderId) || providerShort(row.owned_by) || "?" }}
            </span>
          </div>
          <div>
            <div class="v3-model-row__name">{{ row.id }}</div>
            <div
              style="display: flex; gap: 6px; margin-top: 5px; align-items: center; flex-wrap: wrap"
            >
              <FreeBadge
                v-if="row.freeVariant"
                :variant="row.freeVariant"
                :size="11"
              />
              <SpeedBadge v-if="row.speed" :speed="row.speed" :size="11" />
              <span v-if="row.contextHint" class="v3-chip">ctx {{ row.contextHint }}</span>
              <span v-if="row.hasTools" class="v3-chip">tools</span>
              <span v-if="row.hasVision" class="v3-chip v3-chip--info">vision</span>
              <span v-if="row.isReasoning" class="v3-chip v3-chip--warn">reasoning</span>
              <span
                v-if="row.freeProviderId"
                style="font: 400 10.5px var(--v3-mono); color: var(--v3-ink-3)"
              >
                via {{ providerName(row.freeProviderId) }}
              </span>
            </div>
          </div>
          <div style="font: 500 12px var(--v3-sans); color: var(--v3-ink-2); word-break: break-all">
            {{ row.owned_by || "—" }}
          </div>
          <div style="display: flex; gap: 4px; flex-wrap: wrap">
            <span
              v-if="!row.configured"
              class="v3-chip v3-chip--warn"
              :title="t('modelcatalog.registryOnlyTip') || 'FreeModels 注册表知道此模型,但你尚未配置对应 group'"
            >
              {{ t("modelcatalog.registryOnly") || "registry only" }}
            </span>
            <span
              v-for="g in (row.groups || []).slice(0, 4)"
              :key="g"
              class="v3-chip v3-chip--info"
            >
              {{ g }}
            </span>
            <span
              v-if="row.groups && row.groups.length > 4"
              style="font: 400 10.5px var(--v3-mono); color: var(--v3-ink-3)"
            >
              +{{ row.groups.length - 4 }}
            </span>
          </div>
          <div style="text-align: right">
            <span v-if="row.tier" :class="tierChipClass(row.tier)">{{ row.tier }}</span>
            <span v-else style="color: var(--v3-ink-4); font-family: var(--v3-mono)">—</span>
          </div>
        </div>
        <div
          v-if="!filtered.length && !loading"
          style="padding: 32px 16px; text-align: center; color: var(--v3-ink-3); font-size: 12.5px"
        >
          {{ t("modelcatalog.noData") || "No models match your filter." }}
        </div>
      </div>
    </n-spin>
  </div>
</template>
