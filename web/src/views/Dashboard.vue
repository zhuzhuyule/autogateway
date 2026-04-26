<script setup lang="ts">
import { getDashboardStats } from "@/api/dashboard";
import EncryptionMismatchAlert from "@/components/EncryptionMismatchAlert.vue";
import LineChart from "@/components/LineChart.vue";
import SecurityAlert from "@/components/SecurityAlert.vue";
import V3Sparkline from "@/components/v3/V3Sparkline.vue";
import { V3_HEAT_DATA, V3_PROVIDER_DIR, V3_TOP_MODELS, pavClass } from "@/data/v3Catalog";
import type { DashboardStatsResponse, StatCard } from "@/types/models";
import { copy as copyToClipboard } from "@/utils/clipboard";
import { CopyOutline, DownloadOutline, RefreshOutline } from "@vicons/ionicons5";
import { NIcon, useMessage } from "naive-ui";
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

const stats = ref<DashboardStatsResponse | null>(null);
const loading = ref(true);

onMounted(async () => {
  try {
    const r = await getDashboardStats();
    stats.value = r.data;
  } catch (e) {
    console.error("dashboard stats failed", e);
  } finally {
    loading.value = false;
  }
});

function fmtNumber(v?: number): string {
  if (v == null) {return "—";}
  if (v >= 1_000_000) {return `${(v / 1_000_000).toFixed(1)}M`;}
  if (v >= 1_000) {return `${(v / 1_000).toFixed(1)}K`;}
  return v.toLocaleString();
}

function fmtTrend(card?: StatCard): string {
  if (!card || card.trend == null) {return "";}
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
      lbl: t("dashboard.requests24h") || "Requests · 24h",
      val: fmtNumber(s?.request_count?.value),
      sub: s?.request_count ? `${fmtTrend(s.request_count)} vs prev` : "—",
      up: s?.request_count?.trend_is_growth ?? null,
      spark: fakeSpark,
      color: "var(--v3-ok)",
    },
    {
      lbl: t("dashboard.rpm10Min") || "RPM · 10m",
      val: s?.rpm ? s.rpm.value.toFixed(1) : "—",
      sub: s?.rpm ? `${fmtTrend(s.rpm)}` : "—",
      up: s?.rpm?.trend_is_growth ?? null,
      spark: [3, 4, 5, 5, 6, 7, 7, 9],
      color: "var(--v3-accent)",
    },
    {
      lbl: t("dashboard.totalKeys") || "Active keys",
      val: fmtNumber(s?.key_count?.value),
      sub: s?.key_count?.sub_value
        ? `${s.key_count.value ?? 0} active · ${s.key_count.sub_value} invalid`
        : "—",
      spark: [4, 5, 5, 6, 7, 7, 8, 9],
      color: "var(--v3-accent)",
    },
    {
      lbl: t("dashboard.errorRate24h") || "Error rate",
      val: s?.error_rate ? `${s.error_rate.value.toFixed(2)}%` : "—",
      sub: s?.error_rate ? `${fmtTrend(s.error_rate)}` : "—",
      up: s?.error_rate?.trend_is_growth ?? null,
      spark: [2, 2, 3, 2, 4, 5, 3, 3],
      color: "var(--v3-danger)",
    },
  ];
});

const totalCalls = computed(() =>
  V3_TOP_MODELS.reduce((acc, m) => acc + m.calls, 0).toLocaleString()
);

function tierColor(tier: string): string {
  return tier === "simple"
    ? "var(--v3-ok)"
    : tier === "medium"
      ? "var(--v3-warn)"
      : "var(--v3-danger)";
}

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
  if (ok) {message.success(t("common.copySuccess") || "Copied");}
  else {message.error("Copy failed");}
}

function refresh() {
  window.location.reload();
}
</script>

<template>
  <div>
    <encryption-mismatch-alert />
    <security-alert v-if="stats?.security_warnings?.length" :warnings="stats.security_warnings" />

    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">CONSOLE / DASHBOARD</div>
      <div class="v3-viewhead__actions">
        <button class="v3-btn" @click="refresh">
          <n-icon :component="RefreshOutline" :size="12" />
          Refresh
        </button>
        <button class="v3-btn">
          <n-icon :component="DownloadOutline" :size="12" />
          Export 24h
        </button>
      </div>
    </div>
    <h1 class="v3-viewtitle">Operations</h1>

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
            <div class="v3-tm-head__title">Top models · 24h</div>
            <div class="v3-tm-head__sub">by request volume · across all groups</div>
          </div>
          <div class="v3-tm-head__sub">{{ totalCalls }} total</div>
          <div class="v3-tm-head__actions">
            <button class="v3-btn v3-btn--sm">View catalog →</button>
          </div>
        </div>
        <div v-for="(m, i) in V3_TOP_MODELS" :key="m.id + i" class="v3-tm-row">
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
              <span class="v3-tm-row__sub">
                avg {{ m.avgMs }}ms · err {{ (m.errors * 100).toFixed(1) }}%
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
                {{ V3_PROVIDER_DIR[p]?.short || "?" }}
              </span>
            </div>
            <div style="font: 400 10.5px/1 var(--v3-mono); color: var(--v3-ink-3); margin-top: 6px">
              {{ m.providers.length }} provider{{ m.providers.length > 1 ? "s" : "" }}
            </div>
          </div>
          <div>
            <div class="v3-tm-row__count">{{ (m.calls / 1000).toFixed(1) }}k</div>
            <div class="v3-tm-row__count-sub">requests</div>
          </div>
          <div>
            <div class="v3-tm-row__bar">
              <i
                :style="{
                  width: `${(m.calls / V3_TOP_MODELS[0].calls) * 100}%`,
                  background: tierColor(m.tier),
                }"
              />
            </div>
            <div class="v3-tm-row__count-sub" style="text-align: right; margin-top: 5px">
              {{ ((m.calls / V3_TOP_MODELS[0].calls) * 100).toFixed(0) }}%
            </div>
          </div>
          <div style="width: 48px; height: 22px">
            <v3-sparkline :data="m.trend" :color="tierColor(m.tier)" />
          </div>
        </div>
      </div>

      <!-- Right column -->
      <div style="display: flex; flex-direction: column; gap: 16px">
        <!-- Heat strip -->
        <div class="v3-heat-card">
          <div style="display: flex; align-items: baseline; gap: 10px; margin-bottom: 12px">
            <div class="v3-card__title">Provider activity · 24h</div>
            <div class="v3-card__sub">30-min buckets · red = incident</div>
          </div>
          <div v-for="r in V3_HEAT_DATA" :key="r.name" class="v3-heat-row">
            <div class="v3-heat-row__name">
              <span
                class="v3-pav"
                :class="`v3-pav-${r.name}`"
                style="width: 16px; height: 16px; font-size: 8px"
              >
                {{ V3_PROVIDER_DIR[r.name]?.short || "?" }}
              </span>
              {{ r.name }}
            </div>
            <div class="v3-heat-grid">
              <div v-for="(v, i) in r.cells" :key="i" class="v3-heat-cell" :data-h="v" />
            </div>
          </div>
          <div class="v3-heat-legend">
            less
            <span class="v3-heat-cell" style="width: 10px; height: 10px" />
            <span class="v3-heat-cell" data-h="1" style="width: 10px; height: 10px" />
            <span class="v3-heat-cell" data-h="2" style="width: 10px; height: 10px" />
            <span class="v3-heat-cell" data-h="3" style="width: 10px; height: 10px" />
            <span class="v3-heat-cell" data-h="4" style="width: 10px; height: 10px" />
            more
            <span style="margin-left: 16px">incident</span>
            <span class="v3-heat-cell" data-h="e" style="width: 10px; height: 10px" />
          </div>
        </div>

        <!-- Endpoint quick-paste -->
        <div class="v3-card">
          <div class="v3-card__head">
            <div>
              <div class="v3-card__title">Endpoint quick-paste</div>
              <div class="v3-card__sub">drop on any SDK base_url</div>
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
          <div class="v3-card__title">Request volume · last 24h</div>
          <div class="v3-card__sub">grouped per channel</div>
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
