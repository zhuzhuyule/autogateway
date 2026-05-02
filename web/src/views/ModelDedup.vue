<script setup lang="ts">
import { CheckmarkCircle, CloseOutline, RefreshOutline } from "@vicons/ionicons5";
import { NCheckbox, NIcon, NInput, NModal, NSpin, useMessage } from "naive-ui";
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

interface DedupCandidate {
  group_id: number;
  group_name: string;
  real_model: string;
}

interface DedupSuggestion {
  model_name: string;
  suggested_alias: string;
  candidates: DedupCandidate[];
}

const loading = ref(false);
const fetchError = ref(false);
const suggestions = ref<DedupSuggestion[]>([]);
const showConfirm = ref(false);
const selected = ref<DedupSuggestion | null>(null);
const aliasName = ref("");
const checked = ref<Record<string, boolean>>({});
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
    if (!r.ok) {
      throw new Error(`HTTP ${r.status}`);
    }
    suggestions.value = await r.json();
  } catch {
    fetchError.value = true;
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
}

function candKey(c: DedupCandidate): string {
  return `${c.group_id}::${c.real_model}`;
}

function openConfirm(s: DedupSuggestion) {
  selected.value = s;
  aliasName.value = s.suggested_alias || s.model_name;
  const next: Record<string, boolean> = {};
  for (const c of s.candidates) next[candKey(c)] = true;
  checked.value = next;
  showConfirm.value = true;
}

function closeConfirm() {
  showConfirm.value = false;
  selected.value = null;
  aliasName.value = "";
  checked.value = {};
}

async function createAlias() {
  if (!selected.value || !aliasName.value.trim()) {
    message.warning(t("dedup.pleaseEnterAlias"));
    return;
  }
  const picked = selected.value.candidates.filter((c) => checked.value[candKey(c)]);
  if (picked.length === 0) {
    message.warning(t("dedup.selectAtLeastOne"));
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
        alias: aliasName.value.trim(),
        candidates: picked.map((c) => ({
          group_id: c.group_id,
          real_model: c.real_model,
        })),
      }),
    });
    if (!r.ok) {
      throw new Error(`HTTP ${r.status}`);
    }
    const data = await r.json();
    const created = data?.created ?? 0;
    const failures: string[] = data?.failures ?? [];
    message.success(t("dedup.createdN", { n: created }));
    if (failures.length > 0) {
      message.warning(t("dedup.partialFailures", { n: failures.length }));
    }
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
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.dedup") }}</div>
      <div class="v3-viewhead__actions">
        <button class="v3-btn" @click="fetchSuggestions">
          <n-icon :component="RefreshOutline" :size="12" />
          {{ t("v3.refresh") }}
        </button>
      </div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("dedup.title") }}
      <span class="v3-viewtitle__meta">{{ suggestions.length }}</span>
    </h1>

    <div
      v-if="fetchError"
      class="v3-empty-hint"
      style="background: var(--v3-danger-soft); color: var(--v3-danger); margin-bottom: 12px"
    >
      {{ t("dedup.loadFailed") }}
    </div>

    <n-spin :show="loading">
      <div class="v3-card">
        <div class="v3-card__head">
          <div>
            <div class="v3-card__title">{{ t("dedup.title") }}</div>
            <div class="v3-card__sub">{{ t("dedup.subtitle") }}</div>
          </div>
        </div>
        <table v-if="hasData" class="v3-ktable">
          <thead>
            <tr>
              <th>{{ t("dedup.modelName") }}</th>
              <th>{{ t("dedup.candidates") }}</th>
              <th>{{ t("dedup.suggestedAlias") }}</th>
              <th />
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
                    v-for="c in row.candidates.slice(0, 4)"
                    :key="candKey(c)"
                    class="v3-chip v3-chip--info"
                    :title="`${c.group_name} → ${c.real_model}`"
                  >
                    {{ c.group_name }}
                  </span>
                  <span
                    v-if="row.candidates.length > 4"
                    style="font: 400 10.5px var(--v3-mono); color: var(--v3-ink-3)"
                  >
                    +{{ row.candidates.length - 4 }}
                  </span>
                </div>
              </td>
              <td
                style="font: 500 12px var(--v3-mono); color: var(--v3-ink); word-break: break-all"
              >
                {{ row.suggested_alias }}
              </td>
              <td style="text-align: right">
                <button class="v3-btn v3-btn--accent v3-btn--sm" @click="openConfirm(row)">
                  <n-icon :component="CheckmarkCircle" :size="11" />
                  {{ t("dedup.createAlias") }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
        <div
          v-else-if="!loading"
          style="padding: 32px 16px; text-align: center; color: var(--v3-ink-3); font-size: 12.5px"
        >
          {{ t("dedup.noSuggestions") }}
        </div>
      </div>
    </n-spin>

    <!-- Confirmation modal -->
    <n-modal v-model:show="showConfirm" :mask-closable="false">
      <div class="v3-card" style="width: 520px; max-width: calc(100vw - 32px); padding: 0">
        <div class="v3-card__head">
          <div>
            <div class="v3-card__title">{{ t("dedup.createAliasTitle") }}</div>
            <div class="v3-card__sub">{{ selected?.model_name }}</div>
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
            {{ t("dedup.createAliasConfirm", { model: selected?.model_name }) }}
          </div>

          <div style="margin-top: 14px">
            <div class="v3-intake__paste-lbl" style="margin-bottom: 6px">
              {{ t("dedup.suggestedAlias") }}
            </div>
            <n-input
              v-model:value="aliasName"
              :placeholder="t('dedup.aliasPlaceholder')"
              size="medium"
            />
          </div>

          <div style="margin-top: 14px">
            <div class="v3-intake__paste-lbl" style="margin-bottom: 6px">
              {{ t("dedup.candidates") }}
            </div>
            <div style="display: flex; flex-direction: column; gap: 6px">
              <label
                v-for="c in selected?.candidates ?? []"
                :key="candKey(c)"
                style="
                  display: flex;
                  align-items: center;
                  gap: 8px;
                  padding: 8px 10px;
                  border: 1px solid var(--v3-line);
                  border-radius: 4px;
                  background: var(--v3-surface-2);
                  cursor: pointer;
                "
              >
                <n-checkbox v-model:checked="checked[candKey(c)]" />
                <span style="font: 500 12.5px var(--v3-sans); color: var(--v3-ink)">
                  {{ c.group_name }}
                </span>
                <span style="color: var(--v3-ink-3); font-size: 11px">→</span>
                <code
                  style="
                    font: 500 12px var(--v3-mono);
                    color: var(--v3-ink-2);
                    background: var(--v3-surface);
                    padding: 1px 6px;
                    border-radius: 3px;
                  "
                >
                  {{ c.real_model }}
                </code>
              </label>
            </div>
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
          <button class="v3-btn v3-btn--accent" :disabled="submitting" @click="createAlias">
            <n-icon :component="CheckmarkCircle" :size="12" />
            {{ submitting ? "Saving…" : t("common.confirm") || "Confirm" }}
          </button>
        </div>
      </div>
    </n-modal>
  </div>
</template>
