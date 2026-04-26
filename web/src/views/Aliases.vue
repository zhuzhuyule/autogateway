<script setup lang="ts">
import {
  aliasesApi,
  RESERVED_ALIASES,
  routingSettingsApi,
  type AliasCreatePayload,
  type ModelAliasRow,
  type RoutingSettings,
} from "@/api/aliases";
import { keysApi } from "@/api/keys";
import type { Group } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import {
  AddOutline,
  CloseOutline,
  LockClosedOutline,
  PencilOutline,
  RefreshOutline,
  SearchOutline,
  Trash,
} from "@vicons/ionicons5";
import {
  NIcon,
  NInputNumber,
  NModal,
  NSelect,
  NSpin,
  NSwitch,
  useDialog,
  useMessage,
  type SelectOption,
} from "naive-ui";
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();

const loading = ref(false);
const rows = ref<ModelAliasRow[]>([]);
const groups = ref<Group[]>([]);
const settings = ref<RoutingSettings>({
  Enabled: true,
  SimpleThreshold: 2000,
  ComplexThreshold: 8000,
});

// Group cache for label lookup
const groupNameById = computed<Record<number, string>>(() => {
  const m: Record<number, string> = {};
  for (const g of groups.value) if (g.id) m[g.id] = getGroupDisplayName(g);
  return m;
});

const groupOptions = computed<SelectOption[]>(() =>
  groups.value
    .filter(g => g.id)
    .map(g => ({
      label: `${getGroupDisplayName(g)} (${g.name})`,
      value: g.id as number,
    }))
);

interface GroupedAlias {
  alias: string;
  isReserved: boolean;
  members: ModelAliasRow[];
}

const grouped = computed<GroupedAlias[]>(() => {
  const map = new Map<string, GroupedAlias>();
  for (const r of rows.value) {
    const cur = map.get(r.alias) || {
      alias: r.alias,
      isReserved: r.is_reserved,
      members: [],
    };
    cur.isReserved = cur.isReserved || r.is_reserved;
    if (!(r.is_reserved && r.group_id === 0)) {
      cur.members.push(r);
    }
    map.set(r.alias, cur);
  }
  // Sort: reserved on top by RESERVED_ALIASES order, then alpha
  return Array.from(map.values()).sort((a, b) => {
    const ai = RESERVED_ALIASES.indexOf(a.alias as (typeof RESERVED_ALIASES)[number]);
    const bi = RESERVED_ALIASES.indexOf(b.alias as (typeof RESERVED_ALIASES)[number]);
    if (ai !== -1 && bi !== -1) return ai - bi;
    if (ai !== -1) return -1;
    if (bi !== -1) return 1;
    return a.alias.localeCompare(b.alias);
  });
});

async function loadAll() {
  loading.value = true;
  try {
    const [r, g, s] = await Promise.all([
      aliasesApi.list(),
      keysApi.getGroups(),
      routingSettingsApi.get(),
    ]);
    rows.value = (r as unknown as { data: ModelAliasRow[] }).data || [];
    groups.value = g || [];
    settings.value = (s as unknown as { data: RoutingSettings }).data || settings.value;
  } catch (e) {
    console.error(e);
    message.error(t("common.requestFailed"));
  } finally {
    loading.value = false;
  }
}

onMounted(() => loadAll());

// ----- editing -----
const showAdd = ref(false);
const addPayload = ref<AliasCreatePayload>({
  alias: "",
  group_id: 0,
  real_model: "",
  weight: 1,
  priority: 100,
});
const addPresetAlias = ref<string | null>(null);

function openAdd(presetAlias: string | null = null) {
  addPresetAlias.value = presetAlias;
  addPayload.value = {
    alias: presetAlias || "",
    group_id: 0,
    real_model: "",
    weight: 1,
    priority: 100,
  };
  showAdd.value = true;
}

async function submitAdd() {
  if (!addPayload.value.alias.trim() || !addPayload.value.real_model.trim() || !addPayload.value.group_id) {
    message.warning(t("v3.aliasAddIncomplete"));
    return;
  }
  try {
    await aliasesApi.create(addPayload.value);
    message.success(t("common.operationSuccess"));
    showAdd.value = false;
    await loadAll();
  } catch (e) {
    console.error(e);
    message.error(t("common.requestFailed"));
  }
}

async function toggleEnabled(row: ModelAliasRow) {
  const next = !row.enabled;
  row.enabled = next;
  try {
    await aliasesApi.update(row.id, { enabled: next });
  } catch {
    row.enabled = !next;
    message.error(t("common.requestFailed"));
  }
}

async function updateWeight(row: ModelAliasRow, weight: number) {
  const old = row.weight;
  row.weight = weight;
  try {
    await aliasesApi.update(row.id, { weight });
  } catch {
    row.weight = old;
    message.error(t("common.requestFailed"));
  }
}

async function updatePriority(row: ModelAliasRow, priority: number) {
  const old = row.priority;
  row.priority = priority;
  try {
    await aliasesApi.update(row.id, { priority });
  } catch {
    row.priority = old;
    message.error(t("common.requestFailed"));
  }
}

function removeRow(row: ModelAliasRow) {
  dialog.warning({
    title: t("v3.aliasDeleteTitle"),
    content: t("v3.aliasDeleteConfirm", { alias: row.alias, model: row.real_model }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      try {
        await aliasesApi.remove(row.id);
        message.success(t("common.operationSuccess"));
        await loadAll();
      } catch {
        message.error(t("common.requestFailed"));
      }
    },
  });
}

// ----- settings -----
async function saveSettings() {
  try {
    const r = await routingSettingsApi.save({
      enabled: settings.value.Enabled,
      simple_threshold: settings.value.SimpleThreshold,
      complex_threshold: settings.value.ComplexThreshold,
    });
    settings.value = (r as unknown as { data: RoutingSettings }).data;
    message.success(t("common.operationSuccess"));
  } catch {
    message.error(t("common.requestFailed"));
  }
}

function totalWeight(members: ModelAliasRow[]): number {
  return members.reduce((s, m) => s + (m.enabled ? m.weight : 0), 0);
}

function pct(row: ModelAliasRow, members: ModelAliasRow[]): number {
  const total = totalWeight(members);
  if (!total || !row.enabled) return 0;
  return Math.round((row.weight / total) * 100);
}
</script>

<template>
  <div>
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.aliases") }}</div>
      <div class="v3-viewhead__actions">
        <button class="v3-btn" @click="loadAll">
          <n-icon :component="RefreshOutline" :size="12" />
          {{ t("v3.refresh") }}
        </button>
        <button class="v3-btn v3-btn--accent" @click="openAdd(null)">
          <n-icon :component="AddOutline" :size="12" />
          {{ t("v3.aliasAddBtn") }}
        </button>
      </div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("v3.aliasesTitle") }}
      <span class="v3-viewtitle__meta">
        {{ rows.filter(r => !(r.is_reserved && r.group_id === 0)).length }} mappings
      </span>
    </h1>
    <div class="v3-viewhead__sub" style="margin: -8px 0 16px">
      {{ t("v3.aliasesDesc") }}
    </div>

    <!-- Threshold settings -->
    <div class="v3-thresh-card" style="margin-bottom: 16px">
      <div style="display: flex; align-items: center; gap: 14px; margin-bottom: 4px">
        <div style="font: 600 13px var(--v3-sans)">
          {{ t("v3.complexityThresholds") }}
        </div>
        <span style="font: 400 11.5px var(--v3-mono); color: var(--v3-ink-3)">
          {{ t("v3.complexityThresholdsSub") }}
        </span>
        <span style="margin-left: auto; display: flex; align-items: center; gap: 8px">
          <span style="font: 500 11px var(--v3-mono); color: var(--v3-ink-3)">
            {{ settings.Enabled ? t("v3.routingActive") : t("v3.routingPassthrough") }}
          </span>
          <n-switch v-model:value="settings.Enabled" @update:value="saveSettings" />
        </span>
      </div>
      <div
        style="
          display: grid;
          grid-template-columns: 1fr 1fr auto;
          gap: 12px;
          margin-top: 16px;
          align-items: end;
        "
      >
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
            simple &lt; n tokens
          </div>
          <n-input-number
            v-model:value="settings.SimpleThreshold"
            :min="1"
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
            complex &gt;= n tokens
          </div>
          <n-input-number
            v-model:value="settings.ComplexThreshold"
            :min="1"
            :step="100"
            style="width: 100%"
          />
        </div>
        <button class="v3-btn v3-btn--accent" @click="saveSettings">
          {{ t("v3.save") }}
        </button>
      </div>
    </div>

    <!-- Aliases groups -->
    <n-spin :show="loading">
      <div
        v-for="grp in grouped"
        :key="grp.alias"
        class="v3-card"
        style="margin-bottom: 12px"
      >
        <div class="v3-card__head">
          <div style="flex: 1; min-width: 0">
            <div
              class="v3-card__title"
              style="display: flex; align-items: center; gap: 8px"
            >
              <code
                style="
                  font: 600 13px var(--v3-mono);
                  background: var(--v3-surface-2);
                  padding: 2px 7px;
                  border-radius: 4px;
                "
              >
                {{ grp.alias }}
              </code>
              <n-icon
                v-if="grp.isReserved"
                :component="LockClosedOutline"
                :size="12"
                style="color: var(--v3-warn)"
                :title="t('v3.aliasReservedHint')"
              />
              <span class="v3-chip" v-if="grp.isReserved">{{ t("v3.aliasReserved") }}</span>
              <span class="v3-card__sub" style="margin: 0 0 0 4px">
                {{ grp.members.length }} {{ t("v3.aliasMembers") }}
              </span>
            </div>
          </div>
          <button class="v3-btn v3-btn--sm" @click="openAdd(grp.alias)">
            <n-icon :component="AddOutline" :size="11" />
            {{ t("v3.aliasAddMember") }}
          </button>
        </div>

        <table v-if="grp.members.length" class="v3-ktable">
          <thead>
            <tr>
              <th>{{ t("v3.aliasGroupCol") }}</th>
              <th>{{ t("v3.aliasModelCol") }}</th>
              <th style="min-width: 200px">{{ t("v3.aliasWeightShare") }}</th>
              <th>{{ t("v3.aliasPriority") }}</th>
              <th>{{ t("v3.aliasEnabled") }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in grp.members" :key="r.id">
              <td>{{ groupNameById[r.group_id] || `#${r.group_id}` }}</td>
              <td>
                <code
                  style="
                    font: 500 12px var(--v3-mono);
                    background: var(--v3-surface-2);
                    padding: 2px 5px;
                    border-radius: 3px;
                  "
                  >{{ r.real_model }}</code
                >
              </td>
              <td>
                <div style="display: flex; align-items: center; gap: 8px">
                  <n-input-number
                    :value="r.weight"
                    :min="1"
                    size="small"
                    style="width: 80px"
                    @update:value="v => updateWeight(r, v ?? 1)"
                  />
                  <span class="v3-weight-bar" style="width: 80px">
                    <i
                      style="background: var(--v3-accent)"
                      :style="{ width: `${Math.max(pct(r, grp.members), 4)}%` }"
                    />
                  </span>
                  <span
                    class="mono tnum"
                    style="font-size: 11.5px; color: var(--v3-ink-3); min-width: 32px"
                  >
                    {{ pct(r, grp.members) }}%
                  </span>
                </div>
              </td>
              <td>
                <n-input-number
                  :value="r.priority"
                  :min="1"
                  size="small"
                  style="width: 90px"
                  @update:value="v => updatePriority(r, v ?? 100)"
                />
              </td>
              <td>
                <n-switch :value="r.enabled" @update:value="() => toggleEnabled(r)" />
              </td>
              <td style="text-align: right">
                <button
                  class="v3-btn v3-btn--ghost v3-btn--sm v3-btn--danger"
                  @click="removeRow(r)"
                >
                  <n-icon :component="Trash" :size="11" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
        <div
          v-else
          style="
            padding: 22px 16px;
            text-align: center;
            color: var(--v3-ink-3);
            font-size: 12.5px;
          "
        >
          {{ t("v3.aliasNoMembers") }}
        </div>
      </div>
    </n-spin>

    <!-- Weight bar styles (mirrors V3SubGroupTable) -->
    <style scoped>
      .v3-weight-bar {
        height: 6px;
        background: var(--v3-surface-3);
        border-radius: 3px;
        overflow: hidden;
        display: inline-block;
      }
      .v3-weight-bar > i {
        display: block;
        height: 100%;
        border-radius: 3px;
        transition: width 0.3s ease;
      }
    </style>

    <!-- Add modal -->
    <n-modal v-model:show="showAdd" :mask-closable="false">
      <div
        class="v3-card"
        style="width: 480px; max-width: calc(100vw - 32px); padding: 0"
      >
        <div class="v3-card__head">
          <div style="flex: 1">
            <div class="v3-card__title">
              {{ addPresetAlias ? t("v3.aliasAddMember") : t("v3.aliasAddBtn") }}
            </div>
            <div class="v3-card__sub">
              {{ addPresetAlias ? `→ ${addPresetAlias}` : t("v3.aliasAddSub") }}
            </div>
          </div>
          <button
            class="v3-btn v3-btn--ghost v3-btn--icon"
            @click="showAdd = false"
          >
            <n-icon :component="CloseOutline" :size="13" />
          </button>
        </div>
        <div class="v3-card__body" style="display: grid; gap: 12px">
          <div v-if="!addPresetAlias">
            <div class="v3-intake__paste-lbl" style="margin-bottom: 6px">
              {{ t("v3.aliasNameLabel") }}
            </div>
            <input
              v-model="addPayload.alias"
              :placeholder="t('v3.aliasNamePlaceholder')"
              style="
                width: 100%;
                padding: 6px 9px;
                border: 1px solid var(--v3-line);
                border-radius: 5px;
                font: 500 12px var(--v3-sans);
                background: var(--v3-surface);
                color: var(--v3-ink);
              "
            />
          </div>
          <div>
            <div class="v3-intake__paste-lbl" style="margin-bottom: 6px">
              {{ t("v3.aliasGroupLabel") }}
            </div>
            <n-select
              v-model:value="addPayload.group_id"
              :options="groupOptions"
              filterable
              :placeholder="t('v3.aliasGroupPlaceholder')"
            />
          </div>
          <div>
            <div class="v3-intake__paste-lbl" style="margin-bottom: 6px">
              {{ t("v3.aliasModelLabel") }}
            </div>
            <input
              v-model="addPayload.real_model"
              :placeholder="t('v3.aliasModelPlaceholder')"
              style="
                width: 100%;
                padding: 6px 9px;
                border: 1px solid var(--v3-line);
                border-radius: 5px;
                font: 500 12px var(--v3-mono);
                background: var(--v3-surface);
                color: var(--v3-ink);
              "
            />
          </div>
          <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px">
            <div>
              <div class="v3-intake__paste-lbl" style="margin-bottom: 6px">
                {{ t("v3.aliasWeightCol") || "Weight" }}
              </div>
              <n-input-number v-model:value="addPayload.weight" :min="1" />
            </div>
            <div>
              <div class="v3-intake__paste-lbl" style="margin-bottom: 6px">
                {{ t("v3.aliasPriority") }}
              </div>
              <n-input-number v-model:value="addPayload.priority" :min="1" />
            </div>
          </div>
        </div>
        <div
          style="
            padding: 12px 16px;
            border-top: 1px solid var(--v3-line);
            background: var(--v3-surface-2);
            display: flex;
            justify-content: flex-end;
            gap: 8px;
          "
        >
          <button class="v3-btn" @click="showAdd = false">
            {{ t("common.cancel") }}
          </button>
          <button class="v3-btn v3-btn--accent" @click="submitAdd">
            <n-icon :component="AddOutline" :size="12" />
            {{ t("common.confirm") }}
          </button>
        </div>
      </div>
    </n-modal>
  </div>
</template>
