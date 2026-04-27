<script setup lang="ts">
import { keysApi } from "@/api/keys";
import AddSubGroupModal from "@/components/keys/AddSubGroupModal.vue";
import EditSubGroupWeightModal from "@/components/keys/EditSubGroupWeightModal.vue";
import type { Group, SubGroupInfo } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import {
  AddOutline,
  CreateOutline,
  EyeOutline,
  SearchOutline,
  Trash,
} from "@vicons/ionicons5";
import { NIcon, NSpin, useDialog } from "naive-ui";
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

interface Props {
  selectedGroup: Group | null;
  subGroups?: SubGroupInfo[];
  groups?: Group[];
  loading?: boolean;
}

interface Emits {
  (e: "refresh"): void;
  (e: "group-select", groupId: number): void;
}

const props = withDefaults(defineProps<Props>(), {
  subGroups: () => [],
  groups: () => [],
  loading: false,
});
const emit = defineEmits<Emits>();

const dialog = useDialog();

const search = ref("");
const statusFilter = ref<"all" | "active" | "disabled" | "unavailable">("all");

const showAdd = ref(false);
const showEdit = ref(false);
const editing = ref<SubGroupInfo | null>(null);

interface SubGroupRow extends SubGroupInfo {
  percentage: number;
  status: "active" | "disabled" | "unavailable";
}
function statusOf(sg: SubGroupInfo): "active" | "disabled" | "unavailable" {
  if (sg.weight === 0) {
    return "disabled";
  }
  if (sg.weight > 0 && sg.active_keys === 0) {
    return "unavailable";
  }
  return "active";
}

const rows = computed<SubGroupRow[]>(() => {
  const list = props.subGroups || [];
  const total = list.reduce((s, sg) => s + sg.weight, 0);
  return list
    .map(sg => ({
      ...sg,
      percentage: total > 0 ? Math.round((sg.weight / total) * 100) : 0,
      status: statusOf(sg),
    }))
    .sort((a, b) => b.weight - a.weight);
});

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase();
  return rows.value.filter(r => {
    if (q) {
      const name = (r.group.name || "").toLowerCase();
      const display = (r.group.display_name || "").toLowerCase();
      if (!name.includes(q) && !display.includes(q)) return false;
    }
    if (statusFilter.value !== "all" && r.status !== statusFilter.value) return false;
    return true;
  });
});

function shortFor(g: Group): string {
  const src = g.display_name || g.name || "?";
  return src.replace(/[^A-Za-z0-9]/g, "").slice(0, 2).toUpperCase() || "??";
}

function avatarClass(g: Group): string {
  if (g.channel_type === "anthropic") return "v3-pav-anthropic";
  if (g.channel_type === "gemini") return "v3-pav-google";
  const lower = (g.name || "").toLowerCase();
  for (const key of [
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
  ]) {
    if (lower.includes(key)) return `v3-pav-${key}`;
  }
  return "v3-pav-default";
}

function statusChip(s: SubGroupRow["status"]): { cls: string; label: string } {
  if (s === "active")
    return {
      cls: "v3-chip v3-chip--ok",
      label: t("subGroups.statusActive") || "Active",
    };
  if (s === "disabled")
    return {
      cls: "v3-chip v3-chip--warn",
      label: t("subGroups.statusDisabled") || "Disabled",
    };
  return {
    cls: "v3-chip v3-chip--danger",
    label: t("subGroups.statusUnavailable") || "Unavailable",
  };
}

function fmtN(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return n.toString();
}

function openEdit(sg: SubGroupInfo) {
  editing.value = sg;
  showEdit.value = true;
}

function removeSub(sg: SubGroupInfo) {
  if (!props.selectedGroup?.id || !sg.group.id) return;
  const aggId = props.selectedGroup.id;
  const subId = sg.group.id;
  const d = dialog.warning({
    title: t("subGroups.removeSubGroup") || "Remove sub-group",
    content:
      t("subGroups.confirmRemoveSubGroup", { name: getGroupDisplayName(sg) }) ||
      `Remove "${getGroupDisplayName(sg)}" from this aggregate?`,
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      d.loading = true;
      try {
        await keysApi.deleteSubGroup(aggId, subId);
        emit("refresh");
      } finally {
        d.loading = false;
      }
    },
  });
}

function onSuccess() {
  showAdd.value = false;
  showEdit.value = false;
  editing.value = null;
  emit("refresh");
}

function viewSubGroup(id?: number) {
  if (id) emit("group-select", id);
}
</script>

<template>
  <div class="v3-sgt">
    <!-- Toolbar -->
    <div class="v3-gd__toolbar">
      <button class="v3-btn v3-btn--accent" @click="showAdd = true">
        <n-icon :component="AddOutline" :size="12" />
        {{ t("subGroups.addSubGroup") || "Add sub-group" }}
      </button>
      <div class="v3-spacer">
        <select
          v-model="statusFilter"
          class="v3-btn v3-btn--sm"
          style="padding-right: 24px"
        >
          <option value="all">{{ t("common.all") || "All" }}</option>
          <option value="active">
            {{ t("subGroups.statusActive") || "Active" }}
          </option>
          <option value="disabled">
            {{ t("subGroups.statusDisabled") || "Disabled" }}
          </option>
          <option value="unavailable">
            {{ t("subGroups.statusUnavailable") || "Unavailable" }}
          </option>
        </select>
        <div class="v3-search">
          <n-icon :component="SearchOutline" :size="12" />
          <input
            v-model="search"
            :placeholder="t('keys.searchByName') || 'Filter sub-groups…'"
          />
        </div>
      </div>
    </div>

    <!-- Table -->
    <n-spin :show="loading">
      <div style="overflow: auto">
        <table class="v3-ktable">
          <thead>
            <tr>
              <th>Sub-group</th>
              <th style="min-width: 200px">Weight share</th>
              <th>Keys</th>
              <th>Active / Invalid</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in filtered" :key="row.group.id">
              <td>
                <div style="display: flex; align-items: center; gap: 10px">
                  <span
                    class="v3-pav"
                    :class="avatarClass(row.group)"
                    style="width: 28px; height: 28px; border-radius: 6px; font-size: 10px"
                  >
                    {{ shortFor(row.group) }}
                  </span>
                  <div style="min-width: 0">
                    <div
                      style="
                        font: 500 12.5px var(--v3-sans);
                        color: var(--v3-ink);
                        margin-bottom: 4px;
                      "
                    >
                      {{ getGroupDisplayName(row) }}
                    </div>
                    <div
                      style="
                        font: 400 10.5px/1 var(--v3-mono);
                        color: var(--v3-ink-3);
                      "
                    >
                      #{{ row.group.name }} · {{ row.group.channel_type }}
                    </div>
                  </div>
                </div>
              </td>
              <td>
                <div style="display: flex; align-items: center; gap: 8px">
                  <span
                    class="mono tnum"
                    style="font-size: 11.5px; color: var(--v3-ink); min-width: 38px"
                  >
                    w {{ row.weight }}
                  </span>
                  <span class="v3-weight-bar">
                    <i
                      :class="{
                        'v3-weight-bar__fill--active': row.status === 'active',
                        'v3-weight-bar__fill--warn': row.status === 'disabled',
                        'v3-weight-bar__fill--danger': row.status === 'unavailable',
                      }"
                      :style="{ width: `${Math.max(row.percentage, 4)}%` }"
                    />
                  </span>
                  <span
                    class="mono tnum"
                    style="font-size: 11.5px; color: var(--v3-ink-3); min-width: 32px"
                  >
                    {{ row.percentage }}%
                  </span>
                </div>
              </td>
              <td class="tnum mono">{{ fmtN(row.total_keys) }}</td>
              <td>
                <span
                  class="mono tnum"
                  style="color: var(--v3-ok); font-weight: 600"
                >
                  {{ fmtN(row.active_keys) }}
                </span>
                <span style="color: var(--v3-ink-4); margin: 0 6px">/</span>
                <span
                  class="mono tnum"
                  :style="{
                    color: row.invalid_keys ? 'var(--v3-danger)' : 'var(--v3-ink-3)',
                    fontWeight: row.invalid_keys ? 600 : 500,
                  }"
                >
                  {{ fmtN(row.invalid_keys) }}
                </span>
              </td>
              <td>
                <span :class="statusChip(row.status).cls">
                  <span
                    class="v3-dot"
                    :class="{
                      'v3-dot--ok': row.status === 'active',
                      'v3-dot--warn': row.status === 'disabled',
                      'v3-dot--danger': row.status === 'unavailable',
                    }"
                  />
                  {{ statusChip(row.status).label }}
                </span>
              </td>
              <td style="text-align: right; white-space: nowrap">
                <button
                  class="v3-btn v3-btn--ghost v3-btn--sm"
                  :title="t('common.view') || 'View'"
                  @click="viewSubGroup(row.group.id)"
                >
                  <n-icon :component="EyeOutline" :size="12" />
                </button>
                <button
                  class="v3-btn v3-btn--ghost v3-btn--sm"
                  :title="t('subGroups.editWeight') || 'Edit weight'"
                  @click="openEdit(row)"
                >
                  <n-icon :component="CreateOutline" :size="12" />
                </button>
                <button
                  class="v3-btn v3-btn--ghost v3-btn--sm v3-btn--danger"
                  :title="t('subGroups.removeSubGroup') || 'Remove'"
                  @click="removeSub(row)"
                >
                  <n-icon :component="Trash" :size="12" />
                </button>
              </td>
            </tr>
            <tr v-if="!filtered.length">
              <td
                colspan="6"
                style="
                  padding: 28px 16px;
                  text-align: center;
                  color: var(--v3-ink-3);
                  font-size: 12.5px;
                "
              >
                {{
                  rows.length
                    ? t("keys.noMatchingKeys") || "No sub-groups match the filter."
                    : t("subGroups.noSubGroups") ||
                      "No sub-groups in this aggregate yet."
                }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </n-spin>

    <div class="v3-sgt__foot">
      <span>
        {{
          t("subGroups.totalSubGroups", { total: filtered.length }) ||
          `${filtered.length} sub-groups`
        }}
        <template v-if="filtered.length !== rows.length">
          / {{ rows.length }}
        </template>
      </span>
      <span>
        {{ t("subGroups.sortedByWeight") || "Sorted by weight ↓" }}
      </span>
    </div>

    <!-- Modals -->
    <add-sub-group-modal
      v-if="selectedGroup?.id"
      v-model:show="showAdd"
      :aggregate-group="selectedGroup"
      :existing-sub-groups="subGroups || []"
      :groups="groups || []"
      @success="onSuccess"
    />
    <edit-sub-group-weight-modal
      v-if="editing && selectedGroup?.id"
      v-model:show="showEdit"
      :aggregate-group="selectedGroup"
      :sub-group="editing"
      :sub-groups="subGroups || []"
      @success="onSuccess"
      @update:show="
        show => {
          if (!show) editing = null;
        }
      "
    />
  </div>
</template>

<style scoped>
.v3-sgt {
  display: flex;
  flex-direction: column;
}

.v3-weight-bar {
  flex: 1;
  height: 6px;
  background: var(--v3-surface-3);
  border-radius: 3px;
  overflow: hidden;
  display: inline-block;
  min-width: 80px;
}
.v3-weight-bar > i {
  display: block;
  height: 100%;
  border-radius: 3px;
  transition: width 0.3s ease;
}
.v3-weight-bar__fill--active {
  background: var(--v3-ok);
}
.v3-weight-bar__fill--warn {
  background: var(--v3-warn);
}
.v3-weight-bar__fill--danger {
  background: repeating-linear-gradient(
    45deg,
    var(--v3-danger),
    var(--v3-danger) 6px,
    oklch(0.55 0.16 25) 6px,
    oklch(0.55 0.16 25) 12px
  );
}

.v3-sgt__foot {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 16px;
  border-top: 1px solid var(--v3-line);
  background: var(--v3-surface-2);
  font: 400 11.5px/1.3 var(--v3-mono);
  color: var(--v3-ink-3);
}
</style>
