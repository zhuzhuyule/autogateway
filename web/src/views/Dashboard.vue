<script setup lang="ts">
import { getDashboardStats, getGroupList } from "@/api/dashboard";
import { keysApi } from "@/api/keys";
import EncryptionMismatchAlert from "@/components/EncryptionMismatchAlert.vue";
import LineChart from "@/components/LineChart.vue";
import SecurityAlert from "@/components/SecurityAlert.vue";
import V3Sparkline from "@/components/v3/V3Sparkline.vue";
import { V3_PROVIDER_DIR, pavClass } from "@/data/v3Catalog";
import type { DashboardStatsResponse, Group, StatCard } from "@/types/models";
import { copy as copyToClipboard } from "@/utils/clipboard";
import { CopyOutline, DownloadOutline, RefreshOutline } from "@vicons/ionicons5";
import { NIcon, useMessage } from "naive-ui";
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

const stats = ref<DashboardStatsResponse | null>(null);
const loading = ref(true);

interface ChannelGroup {
  name: string;
  display_name?: string;
  channel_type?: string;
  available_models?: unknown;
}

const groupList = ref<ChannelGroup[]>([]);
const groupStatsMap = ref<Record<string, { req24h: number; failed: number }>>({});

onMounted(() => {
  loadAll();
});

async function loadAll() {
  loading.value = true;
  try {
    await Promise.all([loadStats(), loadGroupsAndStats()]);
  } finally {
    loading.value = false;
  }
}

async function loadStats() {
  try {
    const r = await getDashboardStats();
    stats.value = r.data;
  } catch (e) {
    console.error("dashboard stats failed", e);
  }
}

async function loadGroupsAndStats() {
  try {
    const r = await getGroupList();
    groupList.value = (r as unknown as { data: ChannelGroup[] }).data || [];
    // best-effort: pull per-group stats so Top models has real numbers
    const allGroups = await keysApi.getGroups();
    const promises = allGroups
      .filter(g => g.id != null && g.group_type !== "aggregate")
      .map(async (g: Group) => {
        try {
          const s = await keysApi.getGroupStats(g.id!);
          groupStatsMap.value[g.name] = {
            req24h: s.stats_24_hour.total_requests,
            failed: s.stats_24_hour.failed_requests,
          };
        } catch {
          /* tolerate */
        }
      });
    await Promise.all(promises);
  } catch (e) {
    console.error("group list / stats failed", e);
  }
}

function refresh() {
  loadAll();
  message.success(t("v3.refresh"));
}

function exportSnapshot() {
  const snapshot = {
    generated_at: new Date().toISOString(),
    stats: stats.value,
    top_models: topModels.value,
    groups: groupList.value.map(g => ({
      name: g.name,
      display_name: g.display_name,
      channel_type: g.channel_type,
      stats_24h: groupStatsMap.value[g.name] || null,
    })),
  };
  const blob = new Blob([JSON.stringify(snapshot, null, 2)], {
    type: "application/json",
  });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `autogateway-snapshot-${Date.now()}.json`;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

function fmtNumber(v?: number): string {
  if (v == null) return "—";
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`;
  return v.toLocaleString();
}

function fmtTrend(card?: StatCard): string {
  if (!card || card.trend == null) return "";
  const sign = card.trend >= 0 ? "+" : "";
  return `${sign}${card.trend.toFixed(1)}%`;
}

interface KpiSpec {
  lbl: string;
  val: string;
  sub: string;
  up?: boolean | null;
  spark: number[];
  color: string;
}

const fakeSpark = [4, 6, 5, 8, 12, 9, 11, 14];

const kpis = computed<KpiSpec[]>(() => {
  const s = stats.value;
  return [
    {
      lbl: t("v3.requests24h"),
      val: fmtNumber(s?.request_count?.value),
      sub: s?.request_count ? `${fmtTrend(s.request_count)} vs prev` : "—",
      up: s?.request_count?.trend_is_growth ?? null,
      spark: fakeSpark,
      color: "var(--v3-ok)",
    },
    {
      lbl: t("v3.rpm10m"),
      val: s?.rpm ? s.rpm.value.toFixed(1) : "—",
      sub: s?.rpm ? fmtTrend(s.rpm) : "—",
      up: s?.rpm?.trend_is_growth ?? null,
      spark: [3, 4, 5, 5, 6, 7, 7, 9],
      color: "var(--v3-accent)",
    },
    {
      lbl: t("v3.activeKeys"),
      val: fmtNumber(s?.key_count?.value),
      sub:
        s?.key_count != null
          ? t("v3.keysActiveInvalid", {
              active: s.key_count.value ?? 0,
              invalid: s.key_count.sub_value ?? 0,
            })
          : "—",
      spark: [4, 5, 5, 6, 7, 7, 8, 9],
      color: "var(--v3-accent)",
    },
    {
      lbl: t("v3.errorRate"),
      val: s?.error_rate ? `${s.error_rate.value.toFixed(2)}%` : "—",
      sub: s?.error_rate ? fmtTrend(s.error_rate) : "—",
      up: s?.error_rate?.trend_is_growth ?? null,
      spark: [2, 2, 3, 2, 4, 5, 3, 3],
      color: "var(--v3-danger)",
    },
  ];
});

interface TopModelRow {
  id: string;
  calls: number;
  providers: string[];
  tier: "simple" | "medium" | "complex";
}

function parseModels(raw: unknown): string[] {
  if (Array.isArray(raw)) {
    return raw.filter((m): m is string => typeof m === "string");
  }
  if (typeof raw === "string" && raw.trim().length > 0) {
    try {
      const arr = JSON.parse(raw);
      return Array.isArray(arr)
        ? arr.filter((m): m is string => typeof m === "string")
        : [];
    } catch {
      return [];
    }
  }
  return [];
}

function inferProvider(groupName: string, channel?: string): string {
  const lower = (groupName || "").toLowerCase();
  for (const p of [
    "groq",
    "cerebras",
    "openrouter",
    "together",
    "cloudflare",
    "mistral",
    "google",
    "gemini",
    "cohere",
    "github",
    "anthropic",
  ]) {
    if (lower.includes(p)) return p === "gemini" ? "google" : p;
  }
  if (channel === "anthropic") return "anthropic";
  if (channel === "gemini") return "google";
  return "default";
}

function inferTier(modelId: string): "simple" | "medium" | "complex" {
  const id = modelId.toLowerCase();
  if (
    id.includes("flash") ||
    id.includes("haiku") ||
    id.includes("8b") ||
    id.includes("instant") ||
    id.includes("mini") ||
    id.includes("small")
  ) {
    return "simple";
  }
  if (
    id.includes("pro") ||
    id.includes("opus") ||
    id.includes("sonnet") ||
    id.includes("70b") ||
    id.includes("405b") ||
    id.includes("4o") ||
    id.includes("o1") ||
    id.includes("r1")
  ) {
    return "complex";
  }
  return "medium";
}

const topModels = computed<TopModelRow[]>(() => {
  // Aggregate model id -> { calls, providers }
  const acc = new Map<string, { calls: number; providers: Set<string> }>();
  for (const g of groupList.value) {
    const provider = inferProvider(g.name, g.channel_type);
    const groupCalls = groupStatsMap.value[g.name]?.req24h ?? 0;
    const models = parseModels(g.available_models);
    if (models.length === 0) continue;
    // Distribute group calls evenly across its models (rough proxy)
    const per = Math.max(0, Math.round(groupCalls / models.length));
    for (const m of models) {
      const entry = acc.get(m) || { calls: 0, providers: new Set<string>() };
      entry.calls += per;
      entry.providers.add(provider);
      acc.set(m, entry);
    }
  }
  return Array.from(acc.entries())
    .map(([id, v]) => ({
      id,
      calls: v.calls,
      providers: Array.from(v.providers),
      tier: inferTier(id),
    }))
    .sort((a, b) => b.calls - a.calls)
    .slice(0, 6);
});

const totalCalls = computed(() => {
  const total = topModels.value.reduce((s, m) => s + m.calls, 0);
  return total > 0 ? `${total.toLocaleString()} req` : "—";
});

function tierColor(tier: string): string {
  return tier === "simple"
    ? "var(--v3-ok)"
    : tier === "medium"
      ? "var(--v3-warn)"
      : "var(--v3-danger)";
}

interface ProviderLineupRow {
  name: string;
  groupCount: number;
  req24h: number;
  failed: number;
}

const providerLineup = computed<ProviderLineupRow[]>(() => {
  const acc = new Map<string, ProviderLineupRow>();
  for (const g of groupList.value) {
    const provider = inferProvider(g.name, g.channel_type);
    if (provider === "default") continue;
    const stats = groupStatsMap.value[g.name];
    const row = acc.get(provider) || {
      name: provider,
      groupCount: 0,
      req24h: 0,
      failed: 0,
    };
    row.groupCount += 1;
    row.req24h += stats?.req24h ?? 0;
    row.failed += stats?.failed ?? 0;
    acc.set(provider, row);
  }
  return Array.from(acc.values()).sort((a, b) => b.req24h - a.req24h);
});

const endpoints = computed(() => {
  const base = `${window.location.protocol}//${window.location.host}`;
  return [
    { channel: "openai", url: `${base}/openai/v1` },
    { channel: "anthropic", url: `${base}/anthropic/v1` },
    { channel: "gemini", url: `${base}/gemini/v1beta` },
  ];
});

async function copyText(value: string) {
  const ok = await copyToClipboard(value);
  if (ok) message.success(t("common.copySuccess") || "Copied");
  else message.error("Copy failed");
}
</script>

<template>
  <div>
    <encryption-mismatch-alert />
    <security-alert v-if="stats?.security_warnings?.length" :warnings="stats.security_warnings" />

    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.dashboard") }}</div>
      <div class="v3-viewhead__actions">
        <button class="v3-btn" @click="refresh">
          <n-icon :component="RefreshOutline" :size="12" />
          {{ t("v3.refresh") }}
        </button>
        <button class="v3-btn" @click="exportSnapshot">
          <n-icon :component="DownloadOutline" :size="12" />
          {{ t("v3.export24h") }}
        </button>
      </div>
    </div>
    <h1 class="v3-viewtitle">{{ t("v3.operations") }}</h1>

    <!-- KPI row -->
    <div class="v3-kpi-row">
      <div v-for="k in kpis" :key="k.lbl" class="v3-kpi">
        <div class="v3-kpi__lbl">{{ k.lbl }}</div>
        <div class="v3-kpi__val">{{ k.val }}</div>
        <div
          class="v3-kpi__sub"
          :class="{
            'v3-kpi__sub--up': k.up === true,
            'v3-kpi__sub--down': k.up === false,
          }"
        >
          {{ k.sub }}
        </div>
        <div class="v3-kpi__spark">
          <v3-sparkline :data="k.spark" :color="k.color" />
        </div>
      </div>
    </div>

    <!-- Top models + Heat / Endpoints -->
    <div class="v3-dash-grid">
      <!-- Top models -->
      <div class="v3-tm-card">
        <div class="v3-tm-head">
          <div>
            <div class="v3-tm-head__title">{{ t("v3.topModels") }}</div>
            <div class="v3-tm-head__sub">{{ t("v3.topModelsSub") }}</div>
          </div>
          <div class="v3-tm-head__sub">{{ totalCalls }}</div>
          <div class="v3-tm-head__actions">
            <button class="v3-btn v3-btn--sm" @click="$router.push({ name: 'model-catalog' })">
              {{ t("v3.viewCatalog") }}
            </button>
          </div>
        </div>
        <div
          v-for="(m, i) in topModels"
          :key="m.id + i"
          class="v3-tm-row"
        >
          <div class="v3-tm-row__rank">#{{ i + 1 }}</div>
          <div>
            <div class="v3-tm-row__name">{{ m.id }}</div>
            <div style="display: flex; align-items: center; gap: 8px; margin-top: 5px">
              <span
                class="v3-chip"
                :style="{ borderColor: tierColor(m.tier), color: tierColor(m.tier) }"
              >
                {{ m.tier }}
              </span>
            </div>
          </div>
          <div>
            <div class="v3-tm-row__provs">
              <span
                v-for="p in m.providers"
                :key="p"
                class="v3-tm-row__pchip"
                :class="pavClass(p)"
              >
                {{ V3_PROVIDER_DIR[p]?.short || p.slice(0, 2).toUpperCase() }}
              </span>
            </div>
            <div style="font: 400 10.5px/1 var(--v3-mono); color: var(--v3-ink-3); margin-top: 6px">
              {{ t("v3.nProviders", { n: m.providers.length }) }}
            </div>
          </div>
          <div>
            <div class="v3-tm-row__count">{{ fmtNumber(m.calls) }}</div>
            <div class="v3-tm-row__count-sub">{{ t("v3.req24h") }}</div>
          </div>
          <div>
            <div class="v3-tm-row__bar">
              <i
                :style="{
                  width: `${topModels[0]?.calls ? (m.calls / topModels[0].calls) * 100 : 0}%`,
                  background: tierColor(m.tier),
                }"
              />
            </div>
            <div class="v3-tm-row__count-sub" style="text-align: right; margin-top: 5px">
              {{
                topModels[0]?.calls
                  ? ((m.calls / topModels[0].calls) * 100).toFixed(0)
                  : 0
              }}%
            </div>
          </div>
          <div style="width: 48px; height: 22px; opacity: 0.45">
            <v3-sparkline
              :data="[1, 2, 2, 3, 4, 4, 5, 6]"
              :color="tierColor(m.tier)"
            />
          </div>
        </div>
        <div
          v-if="!topModels.length"
          style="
            padding: 28px 16px;
            text-align: center;
            color: var(--v3-ink-3);
            font-size: 12.5px;
          "
        >
          {{ t("v3.noKeys") }}
        </div>
      </div>

      <!-- Right column -->
      <div style="display: flex; flex-direction: column; gap: 16px">
        <!-- Provider line-up (replaces mock heat strip until backend ships /api/dashboard/activity) -->
        <div class="v3-heat-card">
          <div style="display: flex; align-items: baseline; gap: 10px; margin-bottom: 12px">
            <div class="v3-card__title">{{ t("v3.providerLineup") }}</div>
            <div class="v3-card__sub">{{ t("v3.providerLineupSub") }}</div>
          </div>
          <div
            v-for="p in providerLineup"
            :key="p.name"
            style="
              display: grid;
              grid-template-columns: 28px 1fr auto auto;
              gap: 12px;
              align-items: center;
              padding: 8px 0;
              border-bottom: 1px solid var(--v3-line);
            "
          >
            <span
              class="v3-pav"
              :class="pavClass(p.name)"
              style="width: 24px; height: 24px; border-radius: 5px; font-size: 9px"
            >
              {{ V3_PROVIDER_DIR[p.name]?.short || p.name.slice(0, 2).toUpperCase() }}
            </span>
            <div>
              <div style="font: 500 12px var(--v3-sans); color: var(--v3-ink)">
                {{ V3_PROVIDER_DIR[p.name]?.name || p.name }}
              </div>
              <div style="font: 400 10.5px var(--v3-mono); color: var(--v3-ink-3); margin-top: 3px">
                {{ p.groupCount }} {{ p.groupCount > 1 ? "groups" : "group" }}
              </div>
            </div>
            <div class="tnum mono" style="font-size: 12px; text-align: right">
              {{ fmtNumber(p.req24h) }}
              <div style="font: 400 10px var(--v3-mono); color: var(--v3-ink-4); margin-top: 3px">
                {{ t("v3.req24h") }}
              </div>
            </div>
            <span
              class="v3-chip"
              :class="
                p.failed === 0
                  ? 'v3-chip--ok'
                  : p.req24h && p.failed / p.req24h > 0.05
                    ? 'v3-chip--danger'
                    : 'v3-chip--warn'
              "
            >
              <span
                class="v3-dot"
                :class="
                  p.failed === 0
                    ? 'v3-dot--ok'
                    : p.req24h && p.failed / p.req24h > 0.05
                      ? 'v3-dot--danger'
                      : 'v3-dot--warn'
                "
              />
              {{ p.failed === 0 ? "ok" : `${p.failed} fail` }}
            </span>
          </div>
          <div
            v-if="!providerLineup.length"
            style="padding: 16px 0; text-align: center; color: var(--v3-ink-3); font-size: 12px"
          >
            —
          </div>
        </div>

        <!-- Endpoint quick-paste -->
        <div class="v3-card">
          <div class="v3-card__head">
            <div>
              <div class="v3-card__title">{{ t("v3.endpointPaste") }}</div>
              <div class="v3-card__sub">{{ t("v3.endpointPasteSub") }}</div>
            </div>
          </div>
          <div class="v3-card__body">
            <div class="v3-endpoints">
              <div v-for="ep in endpoints" :key="ep.channel" class="v3-ep-row">
                <span class="v3-ep-row__channel">{{ ep.channel }}</span>
                <span class="v3-ep-row__url">{{ ep.url }}</span>
                <button class="v3-btn v3-btn--ghost v3-btn--sm" @click="copyText(ep.url)">
                  <n-icon :component="CopyOutline" :size="11" />
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Time-series chart (existing component) -->
    <div class="v3-card">
      <div class="v3-card__head">
        <div>
          <div class="v3-card__title">{{ t("v3.requestVolume24h") }}</div>
          <div class="v3-card__sub">{{ t("v3.requestVolume24hSub") }}</div>
        </div>
      </div>
      <div class="v3-card__body">
        <line-chart class="v3-dash-chart" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.v3-dash-chart {
  width: 100%;
}
</style>
