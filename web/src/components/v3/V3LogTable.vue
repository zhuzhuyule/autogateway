<script setup lang="ts">
import { logApi } from "@/api/logs";
import type { LogFilter, RequestLog } from "@/types/models";
import { copy as copyToClipboard } from "@/utils/clipboard";
import { maskKey } from "@/utils/display";
import {
  CloseOutline,
  CopyOutline,
  DownloadOutline,
  EyeOffOutline,
  EyeOutline,
  RefreshOutline,
  SearchOutline,
} from "@vicons/ionicons5";
import {
  NIcon,
  NModal,
  NPagination,
  NSelect,
  NSpin,
  useMessage,
  type SelectOption,
} from "naive-ui";
import { computed, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

interface LogRow extends RequestLog {
  is_key_visible: boolean;
}

const loading = ref(false);
const logs = ref<LogRow[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
const totalPages = computed(() => Math.ceil(total.value / pageSize.value));

const detailLog = ref<LogRow | null>(null);
const showDetail = ref(false);

const filters = ref<LogFilter>({
  page: 1,
  page_size: 20,
});

const successOptions: SelectOption[] = [
  { label: t("common.all") || "All", value: "" },
  { label: t("common.success") || "Success", value: "true" },
  { label: t("common.error") || "Error", value: "false" },
];
const requestTypeOptions: SelectOption[] = [
  { label: t("common.all") || "All", value: "" },
  { label: t("logs.retryRequest") || "Retry", value: "retry" },
  { label: t("logs.finalRequest") || "Final", value: "final" },
];

const successFilter = ref<string>("");
const requestTypeFilter = ref<string>("");
const groupFilter = ref<string>("");
const modelFilter = ref<string>("");
const errorFilter = ref<string>("");

let searchDebounce: number | undefined;
function applyFiltersDebounced() {
  clearTimeout(searchDebounce);
  searchDebounce = window.setTimeout(() => {
    page.value = 1;
    loadLogs();
  }, 250);
}

watch([successFilter, requestTypeFilter], () => {
  page.value = 1;
  loadLogs();
});
watch(page, () => loadLogs());

async function loadLogs() {
  loading.value = true;
  try {
    const params: LogFilter = {
      page: page.value,
      page_size: pageSize.value,
      group_name: groupFilter.value || undefined,
      model: modelFilter.value || undefined,
      error_contains: errorFilter.value || undefined,
      is_success:
        successFilter.value === ""
          ? undefined
          : successFilter.value === "true",
      request_type:
        requestTypeFilter.value === ""
          ? undefined
          : (requestTypeFilter.value as "retry" | "final"),
    };
    const r = await logApi.getLogs(params);
    if (r.code === 0 && r.data) {
      logs.value = r.data.items.map(item => ({ ...item, is_key_visible: false }));
      total.value = r.data.pagination.total_items;
    } else {
      logs.value = [];
      total.value = 0;
    }
  } catch (e) {
    console.error("load logs failed", e);
    logs.value = [];
    total.value = 0;
  } finally {
    loading.value = false;
  }
  void filters; // keep tsc happy
}

onMounted(() => loadLogs());

function statusClass(code: number): string {
  if (code >= 500) return "v3-log-status v3-log-status--5xx";
  if (code >= 400) return "v3-log-status v3-log-status--4xx";
  if (code >= 300) return "v3-log-status v3-log-status--3xx";
  return "v3-log-status v3-log-status--2xx";
}

function fmtTime(ts: string): string {
  if (!ts) return "—";
  return new Date(ts)
    .toLocaleString("zh-CN", { hour12: false })
    .replace(/\//g, "-");
}

function fmtMs(ms: number): string {
  if (ms == null) return "—";
  if (ms > 1000) return `${(ms / 1000).toFixed(2)}s`;
  return `${ms}ms`;
}

function tierForRequestType(rt: string): { cls: string; label: string } {
  if (rt === "retry") {
    return { cls: "v3-chip v3-chip--warn", label: t("logs.retryRequest") || "retry" };
  }
  return { cls: "v3-chip", label: t("logs.finalRequest") || "final" };
}

function showLogDetail(l: LogRow) {
  detailLog.value = l;
  showDetail.value = true;
}

function closeDetail() {
  showDetail.value = false;
  detailLog.value = null;
}

async function copyText(text: string) {
  const ok = await copyToClipboard(text);
  if (ok) message.success(t("logs.copiedToClipboard", { type: "" }) || "Copied");
  else message.error("Copy failed");
}

function exportLogs() {
  logApi.exportLogs({
    group_name: groupFilter.value || undefined,
    model: modelFilter.value || undefined,
    error_contains: errorFilter.value || undefined,
    is_success: successFilter.value === "" ? undefined : successFilter.value === "true",
    request_type:
      requestTypeFilter.value === ""
        ? undefined
        : (requestTypeFilter.value as "retry" | "final"),
  });
}
</script>

<template>
  <div class="v3-card">
    <!-- toolbar -->
    <div class="v3-gd__toolbar">
      <div class="v3-search" style="min-width: 180px">
        <n-icon :component="SearchOutline" :size="12" />
        <input
          v-model="modelFilter"
          :placeholder="t('logs.model') || 'Model'"
          @input="applyFiltersDebounced"
        />
      </div>
      <div class="v3-search" style="min-width: 180px">
        <n-icon :component="SearchOutline" :size="12" />
        <input
          v-model="groupFilter"
          :placeholder="t('logs.group') || 'Group'"
          @input="applyFiltersDebounced"
        />
      </div>
      <div class="v3-search" style="min-width: 200px">
        <n-icon :component="SearchOutline" :size="12" />
        <input
          v-model="errorFilter"
          :placeholder="t('logs.errorContains') || 'Error contains…'"
          @input="applyFiltersDebounced"
        />
      </div>
      <n-select
        v-model:value="successFilter"
        :options="successOptions"
        size="small"
        style="width: 110px"
      />
      <n-select
        v-model:value="requestTypeFilter"
        :options="requestTypeOptions"
        size="small"
        style="width: 110px"
      />
      <div class="v3-spacer">
        <button class="v3-btn" @click="loadLogs">
          <n-icon :component="RefreshOutline" :size="12" />
          {{ t("v3.refresh") }}
        </button>
        <button class="v3-btn" @click="exportLogs">
          <n-icon :component="DownloadOutline" :size="12" />
          {{ t("v3.exportAll") || "Export" }}
        </button>
      </div>
    </div>

    <n-spin :show="loading">
      <div class="v3-log-row v3-log-row--head">
        <div>{{ t("logs.time") || "Time" }}</div>
        <div>{{ t("logs.statusCode") || "Status" }}</div>
        <div>{{ t("logs.requestType") || "Type" }}</div>
        <div>{{ t("logs.duration") || "ms" }}</div>
        <div>{{ t("logs.path") || "Path · model · group" }}</div>
        <div></div>
      </div>
      <div
        v-for="l in logs"
        :key="l.id"
        class="v3-log-row"
        style="cursor: pointer"
        @click="showLogDetail(l)"
      >
        <div class="v3-log-row__t">{{ fmtTime(l.timestamp) }}</div>
        <div>
          <span :class="statusClass(l.status_code)">{{ l.status_code }}</span>
        </div>
        <div>
          <span :class="tierForRequestType(l.request_type).cls">
            {{ tierForRequestType(l.request_type).label }}
          </span>
        </div>
        <div
          class="tnum"
          :style="{
            color: l.duration_ms > 5000 ? 'var(--v3-danger)' : 'var(--v3-ink-2)',
            fontFamily: 'var(--v3-mono)',
            fontSize: '11.5px',
          }"
        >
          {{ fmtMs(l.duration_ms) }}
        </div>
        <div class="v3-log-msg">
          {{ l.request_path }}
          <span style="color: var(--v3-ink-3)">
            · {{ l.model || "—" }} · {{ l.parent_group_name || l.group_name || "—" }}
          </span>
        </div>
        <div style="text-align: right; white-space: nowrap">
          <button
            v-if="l.key_value"
            class="v3-btn v3-btn--ghost v3-btn--icon"
            :title="l.is_key_visible ? 'Hide key' : 'Show key'"
            @click.stop="l.is_key_visible = !l.is_key_visible"
          >
            <n-icon
              :component="l.is_key_visible ? EyeOffOutline : EyeOutline"
              :size="11"
            />
          </button>
          <button
            v-if="l.key_value"
            class="v3-btn v3-btn--ghost v3-btn--icon"
            title="Copy key"
            @click.stop="copyText(l.key_value!)"
          >
            <n-icon :component="CopyOutline" :size="11" />
          </button>
        </div>
      </div>
      <div
        v-if="!logs.length && !loading"
        style="
          padding: 28px 16px;
          text-align: center;
          color: var(--v3-ink-3);
          font-size: 12.5px;
        "
      >
        {{ t("logs.noLogs") || "No logs match your filters." }}
      </div>
    </n-spin>

    <div
      v-if="totalPages > 1"
      style="
        padding: 12px 16px;
        border-top: 1px solid var(--v3-line);
        display: flex;
        justify-content: space-between;
        align-items: center;
      "
    >
      <span class="mono" style="font-size: 11.5px; color: var(--v3-ink-3)">
        {{ total.toLocaleString() }} entries
      </span>
      <n-pagination
        v-model:page="page"
        :page-count="totalPages"
        :page-size="pageSize"
        :item-count="total"
      />
    </div>

    <!-- Detail modal -->
    <n-modal :show="showDetail" :mask-closable="false" @update:show="v => !v && closeDetail()">
      <div
        class="v3-card"
        style="
          width: 720px;
          max-width: calc(100vw - 32px);
          max-height: calc(100vh - 64px);
          display: flex;
          flex-direction: column;
        "
      >
        <div class="v3-card__head">
          <div style="flex: 1; min-width: 0">
            <div class="v3-card__title">{{ t("logs.detail") || "Log detail" }}</div>
            <div class="v3-card__sub">
              {{ detailLog?.request_path }}
            </div>
          </div>
          <button
            class="v3-btn v3-btn--ghost v3-btn--icon"
            @click="closeDetail"
          >
            <n-icon :component="CloseOutline" :size="13" />
          </button>
        </div>
        <div class="v3-card__body" style="overflow: auto; flex: 1">
          <div v-if="detailLog">
            <div class="v3-result-row">
              <span class="v3-result-row__k">Time</span>
              <span class="v3-result-row__v">{{ fmtTime(detailLog.timestamp) }}</span>
            </div>
            <div class="v3-result-row">
              <span class="v3-result-row__k">Status</span>
              <span class="v3-result-row__v">
                <span :class="statusClass(detailLog.status_code)">
                  {{ detailLog.status_code }}
                </span>
              </span>
            </div>
            <div class="v3-result-row">
              <span class="v3-result-row__k">Method</span>
              <span class="v3-result-row__v">
                {{ detailLog.is_success ? "ok" : "fail" }} · {{ fmtMs(detailLog.duration_ms) }}
              </span>
            </div>
            <div class="v3-result-row">
              <span class="v3-result-row__k">Group</span>
              <span class="v3-result-row__v">
                {{ detailLog.parent_group_name || detailLog.group_name || "—" }}
              </span>
            </div>
            <div class="v3-result-row">
              <span class="v3-result-row__k">Model</span>
              <span class="v3-result-row__v">{{ detailLog.model || "—" }}</span>
            </div>
            <div class="v3-result-row">
              <span class="v3-result-row__k">Source IP</span>
              <span class="v3-result-row__v">{{ detailLog.source_ip || "—" }}</span>
            </div>
            <div class="v3-result-row">
              <span class="v3-result-row__k">Upstream</span>
              <span class="v3-result-row__v" style="word-break: break-all">
                {{ detailLog.upstream_addr || "—" }}
              </span>
            </div>
            <div v-if="detailLog.key_value" class="v3-result-row">
              <span class="v3-result-row__k">Key</span>
              <span class="v3-result-row__v" style="font-family: var(--v3-mono)">
                {{ maskKey(detailLog.key_value) }}
              </span>
            </div>
            <div v-if="detailLog.error_message" style="margin-top: 14px">
              <div
                style="
                  font: 500 11px var(--v3-mono);
                  letter-spacing: 0.06em;
                  text-transform: uppercase;
                  color: var(--v3-ink-3);
                  margin-bottom: 6px;
                "
              >
                Error
              </div>
              <pre
                class="v3-code-box"
                style="white-space: pre-wrap; max-height: 200px; overflow: auto"
                >{{ detailLog.error_message }}</pre
              >
            </div>
            <div v-if="detailLog.request_body" style="margin-top: 14px">
              <div
                style="
                  font: 500 11px var(--v3-mono);
                  letter-spacing: 0.06em;
                  text-transform: uppercase;
                  color: var(--v3-ink-3);
                  margin-bottom: 6px;
                  display: flex;
                  align-items: center;
                  gap: 6px;
                "
              >
                Request body
                <button
                  class="v3-btn v3-btn--ghost v3-btn--sm"
                  @click="copyText(detailLog.request_body!)"
                >
                  <n-icon :component="CopyOutline" :size="11" />
                </button>
              </div>
              <pre
                class="v3-code-box"
                style="white-space: pre-wrap; max-height: 260px; overflow: auto"
                >{{ detailLog.request_body }}</pre
              >
            </div>
          </div>
        </div>
      </div>
    </n-modal>
  </div>
</template>
