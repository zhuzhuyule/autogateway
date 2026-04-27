<script setup lang="ts">
import {
  aliasesApi,
  RESERVED_ALIASES,
  routingSettingsApi,
  type ModelAliasRow,
  type RoutingSettings,
} from "@/api/aliases";
import { keysApi } from "@/api/keys";
import type { Group } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import {
  AddOutline,
  CloseOutline,
  HelpCircleOutline,
  LockClosedOutline,
  RefreshOutline,
  Trash,
} from "@vicons/ionicons5";
import {
  NIcon,
  NInputNumber,
  NSelect,
  NSpin,
  NSwitch,
  NTooltip,
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

// Defaults applied to all newly created mappings.
const DEFAULT_WEIGHT = 100;
const DEFAULT_PRIORITY = 0;

const groupNameById = computed<Record<number, string>>(() => {
  const m: Record<number, string> = {};
  for (const g of groups.value) if (g.id) m[g.id] = getGroupDisplayName(g);
  return m;
});

const groupOptions = computed<SelectOption[]>(() =>
  groups.value
    .filter(g => g.id && g.group_type !== "aggregate")
    .map(g => ({
      label: `${getGroupDisplayName(g)}`,
      value: g.id as number,
    }))
);

function modelOptionsForGroup(groupId: number | null): SelectOption[] {
  if (!groupId) return [];
  const g = groups.value.find(gr => gr.id === groupId);
  if (!g) return [];
  const raw = (g as unknown as { available_models?: unknown }).available_models;
  let arr: string[] = [];
  if (Array.isArray(raw)) {
    arr = raw.filter((m): m is string => typeof m === "string");
  } else if (typeof raw === "string" && raw.trim()) {
    try {
      const j = JSON.parse(raw);
      if (Array.isArray(j)) arr = j.filter((m): m is string => typeof m === "string");
    } catch {
      /* ignore */
    }
  }
  return arr.sort().map(m => ({ label: m, value: m }));
}

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
    if (!(r.is_reserved && r.group_id === 0)) cur.members.push(r);
    map.set(r.alias, cur);
  }
  return Array.from(map.values()).sort((a, b) => {
    const ai = RESERVED_ALIASES.indexOf(a.alias as (typeof RESERVED_ALIASES)[number]);
    const bi = RESERVED_ALIASES.indexOf(b.alias as (typeof RESERVED_ALIASES)[number]);
    if (ai !== -1 && bi !== -1) return ai - bi;
    if (ai !== -1) return -1;
    if (bi !== -1) return 1;
    return a.alias.localeCompare(b.alias);
  });
});

const totalMappings = computed(() =>
  rows.value.filter(r => !(r.is_reserved && r.group_id === 0)).length
);

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

// === Inline candidate add ===
interface AddDraft {
  alias: string;
  group_id: number | null;
  real_model: string;
}
const addDraftFor = ref<Record<string, AddDraft | null>>({});
function startAdd(alias: string) {
  addDraftFor.value[alias] = { alias, group_id: null, real_model: "" };
}
function cancelAdd(alias: string) {
  addDraftFor.value[alias] = null;
}
async function commitAdd(alias: string) {
  const d = addDraftFor.value[alias];
  if (!d || !d.group_id || !d.real_model.trim()) {
    message.warning(t("v5.alPickGroupModel"));
    return;
  }
  try {
    await aliasesApi.create({
      alias: alias,
      group_id: d.group_id,
      real_model: d.real_model.trim(),
      weight: DEFAULT_WEIGHT,
      priority: DEFAULT_PRIORITY,
      enabled: true,
    });
    addDraftFor.value[alias] = null;
    await loadAll();
  } catch (e) {
    console.error(e);
    message.error(t("common.requestFailed"));
  }
}

// === New alias card ===
const newCardOpen = ref(false);
const newCardName = ref("");
const newCardCandidates = ref<AddDraft[]>([]);
function openNewCard() {
  newCardOpen.value = true;
  newCardName.value = "";
  newCardCandidates.value = [{ alias: "", group_id: null, real_model: "" }];
}
function cancelNewCard() {
  newCardOpen.value = false;
  newCardName.value = "";
  newCardCandidates.value = [];
}
function addCandidateRow() {
  newCardCandidates.value.push({ alias: "", group_id: null, real_model: "" });
}
function removeCandidateRow(i: number) {
  newCardCandidates.value.splice(i, 1);
  if (!newCardCandidates.value.length) {
    newCardCandidates.value.push({ alias: "", group_id: null, real_model: "" });
  }
}
async function commitNewCard() {
  const name = newCardName.value.trim();
  if (!name) {
    message.warning(t("v5.alNameRequired"));
    return;
  }
  const valid = newCardCandidates.value.filter(c => c.group_id && c.real_model.trim());
  if (!valid.length) {
    message.warning(t("v5.alPickGroupModel"));
    return;
  }
  let ok = 0;
  let fail = 0;
  for (const c of valid) {
    try {
      await aliasesApi.create({
        alias: name,
        group_id: c.group_id as number,
        real_model: c.real_model.trim(),
        weight: DEFAULT_WEIGHT,
        priority: DEFAULT_PRIORITY,
        enabled: true,
      });
      ok += 1;
    } catch (e) {
      console.error(e);
      fail += 1;
    }
  }
  if (ok > 0) message.success(t("v5.maCreated", { ok, fail }));
  else message.error(t("v5.maAllFailed"));
  cancelNewCard();
  await loadAll();
}

// === Remove single mapping ===
function removeMapping(row: ModelAliasRow) {
  dialog.warning({
    title: t("v3.aliasDeleteTitle"),
    content: t("v3.aliasDeleteConfirm", { alias: row.alias, model: row.real_model }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      try {
        await aliasesApi.remove(row.id);
        await loadAll();
      } catch {
        message.error(t("common.requestFailed"));
      }
    },
  });
}

// === Cascade delete entire alias ===
function removeWholeAlias(alias: string) {
  const members = rows.value.filter(
    r => r.alias === alias && !(r.is_reserved && r.group_id === 0)
  );
  if (!members.length) return;
  dialog.warning({
    title: t("v5.alDeleteAliasTitle"),
    content: t("v5.alDeleteAliasConfirm", { alias, n: members.length }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      let fail = 0;
      for (const m of members) {
        try {
          await aliasesApi.remove(m.id);
        } catch {
          fail += 1;
        }
      }
      if (fail > 0) message.error(t("common.requestFailed"));
      await loadAll();
    },
  });
}

// === Settings ===
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
        <button class="v3-btn v3-btn--accent" @click="openNewCard">
          <n-icon :component="AddOutline" :size="12" />
          {{ t("v5.alNewAlias") }}
        </button>
      </div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("v3.aliasesTitle") }}
      <span class="v3-viewtitle__meta">
        {{ t("v5.alMappings", { n: totalMappings }) }}
      </span>
    </h1>
    <div class="v3-viewhead__sub" style="margin: -8px 0 16px">
      {{ t("v3.aliasesDesc") }}
    </div>

    <!-- Threshold settings (kept) -->
    <div class="v3-thresh-card" style="margin-bottom: 16px">
      <div style="display: flex; align-items: center; gap: 14px; margin-bottom: 4px">
        <div style="font: 600 13px var(--v3-sans)">{{ t("v3.complexityThresholds") }}</div>
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
          <div class="v5-al-tinylbl">simple &lt; n tokens</div>
          <n-input-number
            v-model:value="settings.SimpleThreshold"
            :min="1"
            :step="100"
            style="width: 100%"
          />
        </div>
        <div>
          <div class="v5-al-tinylbl">complex &gt;= n tokens</div>
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

    <!-- Cards grid -->
    <n-spin :show="loading">
      <div class="v5-alias-grid">
        <!-- New alias draft card -->
        <div v-if="newCardOpen" class="v5-alias-card v5-alias-card--draft">
          <div class="v5-alias-card__head">
            <input
              v-model="newCardName"
              :placeholder="t('v3.aliasNamePlaceholder')"
              class="v5-alias-card__name-input"
              @keyup.enter="commitNewCard"
            />
            <n-tooltip>
              <template #trigger>
                <button class="v5-keycard__iconbtn" @click="cancelNewCard">
                  <n-icon :component="CloseOutline" :size="14" />
                </button>
              </template>
              {{ t("common.cancel") }}
            </n-tooltip>
          </div>
          <div class="v5-alias-card__body">
            <div
              v-for="(c, i) in newCardCandidates"
              :key="i"
              class="v5-alias-card__draft-row"
            >
              <n-select
                v-model:value="c.group_id"
                :options="groupOptions"
                filterable
                :placeholder="t('v3.aliasGroupPlaceholder')"
                size="small"
                style="flex: 1; min-width: 140px"
                @update:value="() => (c.real_model = '')"
              />
              <n-select
                v-model:value="c.real_model"
                :options="modelOptionsForGroup(c.group_id)"
                tag
                filterable
                :placeholder="t('v3.aliasModelPlaceholder')"
                size="small"
                style="flex: 1; min-width: 140px"
                :disabled="!c.group_id"
              />
              <n-tooltip v-if="newCardCandidates.length > 1">
                <template #trigger>
                  <button class="v5-keycard__iconbtn" @click="removeCandidateRow(i)">
                    <n-icon :component="CloseOutline" :size="14" />
                  </button>
                </template>
                {{ t("common.delete") }}
              </n-tooltip>
            </div>
            <button class="v5-alias-card__add-btn" @click="addCandidateRow">
              <n-icon :component="AddOutline" :size="11" />
              {{ t("v5.alAddCandidate") }}
            </button>
          </div>
          <div class="v5-alias-card__foot">
            <span class="v5-alias-card__defaults">{{ t("v5.alDefaults") }}</span>
            <button class="v3-btn v3-btn--accent v3-btn--sm" @click="commitNewCard">
              {{ t("common.save") || "Save" }}
            </button>
          </div>
        </div>

        <!-- Existing alias cards -->
        <div
          v-for="grp in grouped"
          :key="grp.alias"
          :class="['v5-alias-card', grp.isReserved ? 'v5-alias-card--reserved' : '']"
        >
          <div class="v5-alias-card__head">
            <code class="v5-alias-card__name">{{ grp.alias }}</code>
            <n-tooltip v-if="grp.isReserved">
              <template #trigger>
                <span class="v5-alias-card__lock">
                  <n-icon :component="LockClosedOutline" :size="11" />
                </span>
              </template>
              {{ t("v3.aliasReservedHint") }}
            </n-tooltip>
            <span class="v5-alias-card__count">
              {{ t("v5.alNCandidates", { n: grp.members.length }) }}
            </span>
            <n-tooltip v-if="!grp.isReserved && grp.members.length">
              <template #trigger>
                <button
                  class="v5-keycard__iconbtn v5-keycard__iconbtn--danger"
                  style="margin-left: auto"
                  @click="removeWholeAlias(grp.alias)"
                >
                  <n-icon :component="Trash" :size="14" />
                </button>
              </template>
              {{ t("v5.alDeleteAlias") }}
            </n-tooltip>
          </div>
          <div class="v5-alias-card__body">
            <span
              v-for="m in grp.members"
              :key="m.id"
              class="v5-alias-card__cand"
            >
              <span class="v5-alias-card__cand-grp">
                {{ groupNameById[m.group_id] || `#${m.group_id}` }}
              </span>
              <span class="v5-alias-card__cand-arrow">→</span>
              <code class="v5-alias-card__cand-model">{{ m.real_model }}</code>
              <n-tooltip>
                <template #trigger>
                  <button
                    class="v5-keycard__iconbtn v5-keycard__iconbtn--danger"
                    @click="removeMapping(m)"
                  >
                    <n-icon :component="CloseOutline" :size="12" />
                  </button>
                </template>
                {{ t("v5.alRemoveCandidate") }}
              </n-tooltip>
            </span>

            <!-- Inline add -->
            <template v-if="addDraftFor[grp.alias]">
              <div class="v5-alias-card__draft-row">
                <n-select
                  v-model:value="addDraftFor[grp.alias]!.group_id"
                  :options="groupOptions"
                  filterable
                  :placeholder="t('v3.aliasGroupPlaceholder')"
                  size="small"
                  style="flex: 1; min-width: 140px"
                  @update:value="() => (addDraftFor[grp.alias]!.real_model = '')"
                />
                <n-select
                  v-model:value="addDraftFor[grp.alias]!.real_model"
                  :options="modelOptionsForGroup(addDraftFor[grp.alias]!.group_id)"
                  tag
                  filterable
                  :placeholder="t('v3.aliasModelPlaceholder')"
                  size="small"
                  style="flex: 1; min-width: 140px"
                  :disabled="!addDraftFor[grp.alias]!.group_id"
                />
                <n-tooltip>
                  <template #trigger>
                    <button
                      class="v5-keycard__iconbtn v5-keycard__iconbtn--ok"
                      @click="commitAdd(grp.alias)"
                    >
                      <n-icon :component="AddOutline" :size="14" />
                    </button>
                  </template>
                  {{ t("common.save") }}
                </n-tooltip>
                <n-tooltip>
                  <template #trigger>
                    <button class="v5-keycard__iconbtn" @click="cancelAdd(grp.alias)">
                      <n-icon :component="CloseOutline" :size="14" />
                    </button>
                  </template>
                  {{ t("common.cancel") }}
                </n-tooltip>
              </div>
            </template>
            <button
              v-else
              class="v5-alias-card__add-btn"
              @click="startAdd(grp.alias)"
            >
              <n-icon :component="AddOutline" :size="11" />
              {{ t("v5.alAddCandidate") }}
            </button>
          </div>
        </div>

        <div
          v-if="!grouped.length && !newCardOpen"
          class="v5-empty"
          style="grid-column: 1 / -1"
        >
          <div class="v5-empty__icon">
            <n-icon :component="HelpCircleOutline" :size="22" />
          </div>
          <div class="v5-empty__title">{{ t("v5.alEmpty") }}</div>
          <div class="v5-empty__sub">{{ t("v5.alEmptySub") }}</div>
          <button class="v3-btn v3-btn--accent" style="margin-top: 8px" @click="openNewCard">
            <n-icon :component="AddOutline" :size="12" />
            {{ t("v5.alNewAlias") }}
          </button>
        </div>
      </div>
    </n-spin>
  </div>
</template>

<style scoped>
.v5-al-tinylbl {
  font: 500 10px/1 var(--v3-mono);
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--v3-ink-3);
  margin-bottom: 6px;
}

.v5-alias-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 12px;
}

.v5-alias-card {
  background: var(--v3-surface);
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius-md);
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  transition: all 120ms;
}
.v5-alias-card:hover {
  border-color: var(--v3-line-strong);
  box-shadow: var(--v3-shadow-sm);
}
.v5-alias-card--reserved {
  border-color: oklch(from var(--v3-warn) l c h / 0.32);
  background: oklch(from var(--v3-warn) l c h / 0.04);
}
.v5-alias-card--draft {
  border-color: var(--v3-info);
  background: var(--v3-info-soft);
}

.v5-alias-card__head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.v5-alias-card__name {
  font: 600 13px var(--v3-mono);
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  padding: 3px 9px;
  border-radius: 5px;
  color: var(--v3-ink);
}
.v5-alias-card__name-input {
  flex: 1;
  font: 600 13px var(--v3-mono);
  background: var(--v3-surface);
  border: 1px solid var(--v3-line);
  padding: 5px 10px;
  border-radius: 5px;
  color: var(--v3-ink);
  outline: none;
}
.v5-alias-card__name-input:focus {
  border-color: var(--v3-info);
}
.v5-alias-card__lock {
  display: inline-flex;
  align-items: center;
  color: var(--v3-warn);
}
.v5-alias-card__count {
  font: 500 11px/1 var(--v3-mono);
  color: var(--v3-ink-3);
}

.v5-alias-card__body {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.v5-alias-card__cand {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  padding: 5px 8px;
  font: 500 12px var(--v3-sans);
  color: var(--v3-ink-2);
  flex-wrap: wrap;
}
.v5-alias-card__cand-grp {
  color: var(--v3-ink);
}
.v5-alias-card__cand-arrow {
  color: var(--v3-ink-4);
}
.v5-alias-card__cand-model {
  font: 500 11.5px var(--v3-mono);
  color: var(--v3-ink);
  flex: 1;
  min-width: 0;
}

.v5-alias-card__draft-row {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.v5-alias-card__add-btn {
  background: transparent;
  border: 1px dashed var(--v3-line);
  color: var(--v3-ink-3);
  cursor: pointer;
  padding: 6px 10px;
  border-radius: 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 5px;
  font: 500 12px var(--v3-sans);
  transition: all 120ms;
}
.v5-alias-card__add-btn:hover {
  border-color: var(--v3-info);
  color: var(--v3-info);
}

.v5-alias-card__foot {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  padding-top: 8px;
  border-top: 1px dashed var(--v3-line);
}
.v5-alias-card__defaults {
  font: 400 11px var(--v3-sans);
  color: var(--v3-ink-3);
}
</style>
