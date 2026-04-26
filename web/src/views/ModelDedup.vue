<script setup lang="ts">
import { CheckmarkCircle, CloseOutline, RefreshOutline } from "@vicons/ionicons5";
import { NIcon, NInput, NModal, NSpin, useMessage } from "naive-ui";
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

interface DedupSuggestion {
  model_name: string;
  source_groups: string[];
  suggested_aggregate_name: string;
}

const loading = ref(false);
const fetchError = ref(false);
const suggestions = ref<DedupSuggestion[]>([]);
const showConfirm = ref(false);
const selected = ref<DedupSuggestion | null>(null);
const aggregateName = ref("");
const submitting = ref(false);

const authHeader = computed(() => {
  const k = localStorage.getItem("authKey");
  return k ? `Bearer ${k}` : "";
});

const hasData = computed(() => suggestions.value.length > 0);

onMounted(() => fetchSuggestions());

async function fetchSuggestions() {
  loading.value = true;
  fetchError.value = false;
  try {
    const r = await fetch("/api/dedup/suggestions", {
      headers: { Authorization: authHeader.value },
    });
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    suggestions.value = await r.json();
  } catch {
    fetchError.value = true;
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
}

function openConfirm(s: DedupSuggestion) {
  selected.value = s;
  aggregateName.value = s.suggested_aggregate_name;
  showConfirm.value = true;
}

function closeConfirm() {
  showConfirm.value = false;
  selected.value = null;
  aggregateName.value = "";
}

async function createAggregate() {
  if (!selected.value || !aggregateName.value.trim()) {
    message.warning(t("dedup.pleaseEnterAggregateName"));
    return;
  }
  submitting.value = true;
  try {
    const r = await fetch("/api/dedup/create", {
      method: "POST",
      headers: {
        Authorization: authHeader.value,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        model_name: selected.value.model_name,
        aggregate_name: aggregateName.value,
        source_groups: selected.value.source_groups,
      }),
    });
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    message.success(t("common.operationSuccess"));
    closeConfirm();
    await fetchSuggestions();
  } catch {
    message.error(t("common.requestFailed"));
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div>
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">CONSOLE / MODEL DEDUP</div>
      <div class="v3-viewhead__actions">
        <button class="v3-btn" @click="fetchSuggestions">
          <n-icon :component="RefreshOutline" :size="12" />
          {{ t("common.refresh") || "Refresh" }}
        </button>
      </div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("dedup.title") || "Model dedup" }}
      <span class="v3-viewtitle__meta">{{ suggestions.length }} suggestions</span>
    </h1>

    <div
      v-if="fetchError"
      class="v3-empty-hint"
      style="
        background: var(--v3-danger-soft);
        color: var(--v3-danger);
        margin-bottom: 12px;
      "
    >
      {{ t("dedup.loadFailed") || "Failed to load suggestions" }}
    </div>

    <n-spin :show="loading">
      <div class="v3-card">
        <div class="v3-card__head">
          <div>
            <div class="v3-card__title">
              {{ t("dedup.title") || "Aggregate suggestions" }}
            </div>
            <div class="v3-card__sub">
              Models served by multiple groups can be unified behind one aggregate
            </div>
          </div>
        </div>
        <table v-if="hasData" class="v3-ktable">
          <thead>
            <tr>
              <th>{{ t("dedup.modelName") || "Model" }}</th>
              <th>{{ t("dedup.sourceGroups") || "Source groups" }}</th>
              <th>{{ t("dedup.suggestedName") || "Suggested aggregate" }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in suggestions" :key="row.model_name">
              <td>
                <code
                  style="
                    font: 500 12px var(--v3-mono);
                    background: var(--v3-surface-2);
                    padding: 2px 6px;
                    border-radius: 3px;
                  "
                >
                  {{ row.model_name }}
                </code>
              </td>
              <td>
                <div style="display: flex; gap: 4px; flex-wrap: wrap">
                  <span
                    v-for="g in row.source_groups.slice(0, 4)"
                    :key="g"
                    class="v3-chip v3-chip--info"
                  >
                    {{ g }}
                  </span>
                  <span
                    v-if="row.source_groups.length > 4"
                    style="font: 400 10.5px var(--v3-mono); color: var(--v3-ink-3)"
                  >
                    +{{ row.source_groups.length - 4 }}
                  </span>
                </div>
              </td>
              <td
                style="
                  font: 500 12px var(--v3-mono);
                  color: var(--v3-ink);
                  word-break: break-all;
                "
              >
                {{ row.suggested_aggregate_name }}
              </td>
              <td style="text-align: right">
                <button class="v3-btn v3-btn--accent v3-btn--sm" @click="openConfirm(row)">
                  <n-icon :component="CheckmarkCircle" :size="11" />
                  {{ t("dedup.createAggregate") || "Create aggregate" }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
        <div
          v-else-if="!loading"
          style="
            padding: 32px 16px;
            text-align: center;
            color: var(--v3-ink-3);
            font-size: 12.5px;
          "
        >
          {{ t("dedup.noSuggestions") || "No dedup suggestions right now." }}
        </div>
      </div>
    </n-spin>

    <!-- Confirmation modal -->
    <n-modal v-model:show="showConfirm" :mask-closable="false">
      <div
        class="v3-card"
        style="width: 480px; max-width: calc(100vw - 32px); padding: 0"
      >
        <div class="v3-card__head">
          <div>
            <div class="v3-card__title">
              {{ t("dedup.createAggregateTitle") || "Create aggregate" }}
            </div>
            <div class="v3-card__sub">
              {{ selected?.model_name }} → unified group
            </div>
          </div>
          <button
            class="v3-btn v3-btn--ghost v3-btn--icon"
            style="margin-left: auto"
            @click="closeConfirm"
          >
            <n-icon :component="CloseOutline" :size="13" />
          </button>
        </div>
        <div class="v3-card__body">
          <div
            class="v3-empty-hint"
            style="background: var(--v3-info-soft); color: var(--v3-ink-2)"
          >
            {{
              t("dedup.createAggregateConfirm", { model: selected?.model_name }) ||
              `Create an aggregate group for ${selected?.model_name} that fans out across all source groups.`
            }}
          </div>
          <div style="margin-top: 14px">
            <div class="v3-intake__paste-lbl" style="margin-bottom: 6px">
              {{ t("dedup.aggregateNamePlaceholder") || "Aggregate name" }}
            </div>
            <n-input
              v-model:value="aggregateName"
              :placeholder="t('dedup.aggregateNamePlaceholder') || 'aggregate-name'"
              size="medium"
            />
          </div>
        </div>
        <div
          style="
            padding: 12px 16px;
            border-top: 1px solid var(--v3-line);
            display: flex;
            justify-content: flex-end;
            gap: 8px;
            background: var(--v3-surface-2);
          "
        >
          <button class="v3-btn" @click="closeConfirm">
            {{ t("common.cancel") || "Cancel" }}
          </button>
          <button
            class="v3-btn v3-btn--accent"
            :disabled="submitting"
            @click="createAggregate"
          >
            <n-icon :component="CheckmarkCircle" :size="12" />
            {{ submitting ? "Saving…" : t("common.confirm") || "Confirm" }}
          </button>
        </div>
      </div>
    </n-modal>
  </div>
</template>
