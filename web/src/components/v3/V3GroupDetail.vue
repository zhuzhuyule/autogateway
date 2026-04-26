<script setup lang="ts">
import { keysApi } from "@/api/keys";
import KeyCreateDialog from "@/components/keys/KeyCreateDialog.vue";
import KeyDeleteDialog from "@/components/keys/KeyDeleteDialog.vue";
import GroupCopyModal from "@/components/keys/GroupCopyModal.vue";
import GroupFormModal from "@/components/keys/GroupFormModal.vue";
import { findProviderByUpstreams } from "@/data/freeProviders";
import type {
  APIKey,
  Group,
  GroupStatsResponse,
  KeyStatus,
} from "@/types/models";
import { appState, triggerSyncOperationRefresh } from "@/utils/app-state";
import { copy as copyToClipboard } from "@/utils/clipboard";
import { getGroupDisplayName, maskKey } from "@/utils/display";
import {
  AddOutline,
  CheckmarkCircle,
  CloseOutline,
  CopyOutline,
  EyeOffOutline,
  EyeOutline,
  LockClosedOutline,
  OpenOutline,
  PencilOutline,
  RefreshOutline,
  RemoveCircleOutline,
  SearchOutline,
  Trash,
} from "@vicons/ionicons5";
import { NIcon, NPagination, NSpin, useDialog, useMessage } from "naive-ui";
import { computed, h, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

interface Props {
  group: Group;
}

interface Emits {
  (e: "refresh"): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const message = useMessage();
const dialog = useDialog();

const stats = ref<GroupStatsResponse | null>(null);
const statsLoading = ref(false);

interface KeyRow extends APIKey {
  is_visible: boolean;
}

const keys = ref<KeyRow[]>([]);
const keysLoading = ref(false);
const search = ref("");
const statusFilter = ref<"all" | "active" | "invalid">("all");
const page = ref(1);
const pageSize = ref(15);
const total = ref(0);
const totalPages = ref(0);
const showAddKey = ref(false);
const showDeleteKey = ref(false);
const showEditGroup = ref(false);
const showCopyGroup = ref(false);

const matchedProvider = computed(() =>
  findProviderByUpstreams(props.group?.upstreams || [])
);

const proxyUrl = computed(() => {
  if (!props.group) return "";
  const channel = props.group.channel_type;
  const path =
    channel === "anthropic"
      ? "/anthropic/v1/messages"
      : channel === "gemini"
        ? `/gemini/v1beta/models/${props.group.test_model || "gemini-2.5-flash"}:generateContent`
        : "/openai/v1/chat/completions";
  if (props.group.is_system) {
    return `/proxy${path}`;
  }
  return `/proxy/${props.group.name}${path}`;
});

const groupAvatarShort = computed(() => {
  if (!props.group) return "??";
  const src = props.group.display_name || props.group.name;
  return src.replace(/[^A-Za-z0-9]/g, "").slice(0, 2).toUpperCase() || "??";
});

const groupAvatarClass = computed(() => {
  const g = props.group;
  if (!g) return "v3-pav-default";
  if (g.channel_type === "anthropic") return "v3-pav-anthropic";
  if (g.channel_type === "gemini") return "v3-pav-google";
  if (g.is_system) return "v3-pav-default";
  const lower = g.name.toLowerCase();
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
});

async function loadStats() {
  if (!props.group?.id) {
    stats.value = null;
    return;
  }
  statsLoading.value = true;
  try {
    stats.value = await keysApi.getGroupStats(props.group.id);
  } catch (e) {
    console.error(e);
  } finally {
    statsLoading.value = false;
  }
}

async function loadKeys() {
  if (!props.group?.id) {
    keys.value = [];
    return;
  }
  keysLoading.value = true;
  try {
    const res = await keysApi.getGroupKeys({
      group_id: props.group.id,
      page: page.value,
      page_size: pageSize.value,
      status: statusFilter.value === "all" ? undefined : (statusFilter.value as KeyStatus),
      key_value: search.value.trim() || undefined,
    });
    keys.value = (res.items || []).map(k => ({
      ...k,
      is_visible: false,
    })) as KeyRow[];
    total.value = res.pagination.total_items;
    totalPages.value = res.pagination.total_pages;
  } finally {
    keysLoading.value = false;
  }
}

watch(
  () => props.group?.id,
  () => {
    page.value = 1;
    statusFilter.value = "all";
    search.value = "";
    loadStats();
    loadKeys();
  },
  { immediate: true }
);

watch([page, statusFilter], () => loadKeys());

watch(
  () => appState.groupDataRefreshTrigger,
  () => {
    if (
      appState.lastCompletedTask &&
      props.group?.name === appState.lastCompletedTask.groupName
    ) {
      loadStats();
      loadKeys();
    }
  }
);

let searchTimer: number | undefined;
function onSearchInput() {
  clearTimeout(searchTimer);
  searchTimer = window.setTimeout(() => {
    page.value = 1;
    loadKeys();
  }, 250);
}

function refreshAll() {
  loadStats();
  loadKeys();
}

// inline notes editing
const editingNoteId = ref<number | null>(null);
const editingNoteText = ref("");
const savingNotesId = ref<number | null>(null);

function startEditNotes(k: KeyRow) {
  editingNoteId.value = k.id;
  editingNoteText.value = k.notes || "";
}

function cancelNotes() {
  editingNoteId.value = null;
  editingNoteText.value = "";
}

async function saveNotes(k: KeyRow) {
  const trimmed = editingNoteText.value.trim();
  if (trimmed === (k.notes || "")) {
    cancelNotes();
    return;
  }
  savingNotesId.value = k.id;
  const previous = k.notes;
  // optimistic update
  k.notes = trimmed;
  try {
    await keysApi.updateKeyNotes(k.id, trimmed);
    message.success(t("keys.notesUpdated") || "Notes updated");
    cancelNotes();
  } catch (e) {
    k.notes = previous;
    console.error("update notes failed", e);
    message.error(t("common.requestFailed"));
  } finally {
    savingNotesId.value = null;
  }
}

function focusEditingInput(el: unknown, id?: number) {
  if (!el || !id || id !== editingNoteId.value) return;
  if (el instanceof HTMLInputElement) el.focus();
}

async function copyText(value: string, msg = "Copied") {
  const ok = await copyToClipboard(value);
  if (ok) message.success(msg);
  else message.error("Copy failed");
}

function toggleVisible(k: KeyRow) {
  k.is_visible = !k.is_visible;
}

function displayKey(k: KeyRow): string {
  return k.is_visible ? k.key_value : maskKey(k.key_value);
}

function statusChipClass(s: KeyStatus): string {
  if (s === "active") return "v3-chip v3-chip--ok";
  if (s === "invalid") return "v3-chip v3-chip--danger";
  return "v3-chip";
}

function statusLabel(s: KeyStatus): string {
  if (s === "active") return t("keys.valid") || "Valid";
  if (s === "invalid") return t("keys.invalid") || "Invalid";
  return "—";
}

async function copyKey(k: KeyRow) {
  await copyText(k.key_value, t("keys.keyCopied") || "Key copied");
}

let testingMsg: ReturnType<typeof message.info> | null = null;
async function testKey(k: KeyRow) {
  if (!props.group?.id || !k.key_value || testingMsg) return;
  testingMsg = message.info(t("keys.testingKey") || "Testing key…", { duration: 0 });
  try {
    const r = await keysApi.testKeys(props.group.id, k.key_value);
    const cur = r.results?.[0];
    if (cur?.is_valid) {
      message.success(t("keys.testFinished") || "Key OK");
    } else {
      message.error(cur?.error || t("keys.testFailed") || "Key failed");
    }
    refreshAll();
    triggerSyncOperationRefresh(props.group.name, "TEST_SINGLE");
  } finally {
    testingMsg?.destroy();
    testingMsg = null;
  }
}

function deleteKey(k: KeyRow) {
  if (!props.group?.id) return;
  const groupId = props.group.id;
  const groupName = props.group.name;
  const d = dialog.warning({
    title: t("keys.deleteKey") || "Delete key",
    content: t("keys.confirmDeleteKey", { key: maskKey(k.key_value) }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      d.loading = true;
      try {
        await keysApi.deleteKeys(groupId, k.key_value);
        refreshAll();
        triggerSyncOperationRefresh(groupName, "DELETE_SINGLE");
      } finally {
        d.loading = false;
      }
    },
  });
}

function restoreKey(k: KeyRow) {
  if (!props.group?.id) return;
  const groupId = props.group.id;
  const groupName = props.group.name;
  const d = dialog.warning({
    title: t("keys.restoreKey") || "Restore key",
    content: t("keys.confirmRestoreKey", { key: maskKey(k.key_value) }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      d.loading = true;
      try {
        await keysApi.restoreKeys(groupId, k.key_value);
        refreshAll();
        triggerSyncOperationRefresh(groupName, "RESTORE_SINGLE");
      } finally {
        d.loading = false;
      }
    },
  });
}

function validateAll(scope: "all" | "active" | "invalid" = "all") {
  if (!props.group?.id) return;
  const groupId = props.group.id;
  keysApi.validateGroupKeys(groupId, scope === "all" ? undefined : scope).then(() => {
    localStorage.removeItem("last_closed_task");
    appState.taskPollingTrigger++;
  });
}

function clearInvalid() {
  if (!props.group?.id) return;
  const groupId = props.group.id;
  const groupName = props.group.name;
  const d = dialog.warning({
    title: t("keys.clearKeys") || "Clear invalid keys",
    content: t("keys.confirmClearInvalidKeys") || "Clear all invalid keys in this group?",
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      d.loading = true;
      try {
        const { data } = await keysApi.clearAllInvalidKeys(groupId);
        message.success(data?.message || t("keys.clearSuccess") || "Cleared");
        refreshAll();
        triggerSyncOperationRefresh(groupName, "CLEAR_ALL_INVALID");
      } finally {
        d.loading = false;
      }
    },
  });
}

function deleteGroup() {
  if (!props.group?.id) return;
  const id = props.group.id;
  const name = props.group.name;
  const d = dialog.warning({
    title: t("keys.deleteGroup") || "Delete group",
    content: t("keys.confirmDeleteGroup", { name }) || `Delete group "${name}"?`,
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      d.loading = true;
      try {
        await keysApi.deleteGroup(id);
        emit("refresh");
      } finally {
        d.loading = false;
      }
    },
  });
}

function exportKeys(scope: "all" | "active" | "invalid") {
  if (!props.group?.id) return;
  keysApi.exportKeys(props.group.id, scope);
}

function fmtFailRate(failed?: number, total?: number): string {
  if (!total) return "0%";
  return `${(((failed || 0) / total) * 100).toFixed(1)}%`;
}

function fmtMs(ms: number): string {
  if (!ms) return "—";
  if (ms > 1000) return `${(ms / 1000).toFixed(2)}s`;
  return `${ms}ms`;
}

function formatRelative(date?: string): string {
  if (!date) return t("keys.never") || "never";
  const diffSec = Math.floor((Date.now() - new Date(date).getTime()) / 1000);
  if (diffSec < 60) return `${diffSec}s ago`;
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  return `${Math.floor(diffSec / 86400)}d ago`;
}

function refreshFromCreate() {
  showAddKey.value = false;
  refreshAll();
  if (props.group?.name) {
    triggerSyncOperationRefresh(props.group.name, "BATCH_ADD");
  }
}

function refreshFromDelete() {
  showDeleteKey.value = false;
  refreshAll();
  if (props.group?.name) {
    triggerSyncOperationRefresh(props.group.name, "BATCH_DELETE");
  }
}

function onGroupUpdated() {
  showEditGroup.value = false;
  emit("refresh");
}

function onGroupCopied() {
  showCopyGroup.value = false;
  emit("refresh");
}
</script>

<template>
  <section class="v3-gd">
    <!-- Header -->
    <div class="v3-gd__head">
      <span
        class="v3-pav"
        :class="groupAvatarClass"
        style="width: 36px; height: 36px; border-radius: 7px; font-size: 12px"
      >
        {{ groupAvatarShort }}
      </span>
      <div style="flex: 1; min-width: 0">
        <div class="v3-gd__title">
          {{ getGroupDisplayName(group) }}
          <n-icon v-if="group.is_system" :component="LockClosedOutline" :size="12" />
        </div>
        <div class="v3-gd__path">
          <span class="v3-chip v3-chip--info">{{ group.channel_type }}</span>
          <code>POST {{ proxyUrl }}</code>
          <button
            class="v3-btn v3-btn--ghost v3-btn--sm"
            @click="copyText(proxyUrl, 'Endpoint copied')"
          >
            <n-icon :component="CopyOutline" :size="11" />
          </button>
        </div>
      </div>
      <a
        v-if="matchedProvider?.signupUrl"
        :href="matchedProvider.signupUrl"
        target="_blank"
        rel="noopener"
        class="v3-btn"
      >
        <n-icon :component="OpenOutline" :size="12" />
        {{ t("keys.getMoreKeys") || "Get more keys" }}
      </a>
      <button v-if="!group.is_system" class="v3-btn" @click="showEditGroup = true">
        <n-icon :component="PencilOutline" :size="12" /> {{ t("common.edit") || "Edit" }}
      </button>
      <button class="v3-btn" @click="showCopyGroup = true">
        <n-icon :component="CopyOutline" :size="12" /> {{ t("keys.copyGroup") || "Copy" }}
      </button>
      <button v-if="!group.is_system" class="v3-btn v3-btn--danger" @click="deleteGroup">
        <n-icon :component="Trash" :size="12" />
      </button>
    </div>

    <!-- Stats -->
    <div class="v3-kvg">
      <div>
        <div class="v3-kvg__lbl">Keys</div>
        <div class="v3-kvg__val tnum">
          {{ (stats?.key_stats.total_keys ?? 0).toLocaleString() }}
        </div>
        <div class="v3-kvg__sub">
          {{ stats?.key_stats.active_keys ?? 0 }} valid ·
          {{ stats?.key_stats.invalid_keys ?? 0 }} invalid
        </div>
      </div>
      <div>
        <div class="v3-kvg__lbl">24h req</div>
        <div class="v3-kvg__val tnum">
          {{ (stats?.stats_24_hour.total_requests ?? 0).toLocaleString() }}
        </div>
        <div
          class="v3-kvg__sub"
          :style="{
            color: stats?.stats_24_hour.failed_requests
              ? 'var(--v3-danger)'
              : undefined,
          }"
        >
          {{ stats?.stats_24_hour.failed_requests ?? 0 }} fail ·
          {{
            fmtFailRate(
              stats?.stats_24_hour.failed_requests,
              stats?.stats_24_hour.total_requests
            )
          }}
        </div>
      </div>
      <div>
        <div class="v3-kvg__lbl">7d req</div>
        <div class="v3-kvg__val tnum">
          {{ (stats?.stats_7_day.total_requests ?? 0).toLocaleString() }}
        </div>
        <div class="v3-kvg__sub">
          {{ stats?.stats_7_day.failed_requests ?? 0 }} fail
        </div>
      </div>
      <div>
        <div class="v3-kvg__lbl">30d req</div>
        <div class="v3-kvg__val tnum">
          {{ (stats?.stats_30_day.total_requests ?? 0).toLocaleString() }}
        </div>
        <div class="v3-kvg__sub">
          {{ stats?.stats_30_day.failed_requests ?? 0 }} fail
        </div>
      </div>
    </div>

    <!-- Toolbar -->
    <div class="v3-gd__toolbar">
      <button class="v3-btn v3-btn--accent" @click="showAddKey = true">
        <n-icon :component="AddOutline" :size="12" />
        {{ t("keys.addKey") || "Add key" }}
      </button>
      <button class="v3-btn" @click="validateAll('all')">
        <n-icon :component="CheckmarkCircle" :size="12" />
        {{ t("keys.validateAllKeys") || "Test all" }}
      </button>
      <button class="v3-btn" @click="validateAll('invalid')">
        <n-icon :component="RefreshOutline" :size="12" />
        {{ t("keys.validateInvalidKeys") || "Recheck invalid" }}
      </button>
      <button class="v3-btn v3-btn--danger" @click="clearInvalid">
        <n-icon :component="Trash" :size="12" />
        {{ t("keys.clearAllInvalidKeys") || "Clear invalid" }}
      </button>
      <button class="v3-btn" @click="showDeleteKey = true">
        <n-icon :component="RemoveCircleOutline" :size="12" />
        {{ t("keys.batchDelete") || "Batch delete" }}
      </button>
      <button class="v3-btn" @click="exportKeys('all')">
        <n-icon :component="OpenOutline" :size="12" />
        {{ t("keys.exportAllKeys") || "Export all" }}
      </button>
      <div class="v3-spacer">
        <select
          v-model="statusFilter"
          class="v3-btn v3-btn--sm"
          style="padding-right: 24px"
        >
          <option value="all">{{ t("common.all") || "All" }}</option>
          <option value="active">{{ t("keys.valid") || "Valid only" }}</option>
          <option value="invalid">{{ t("keys.invalid") || "Invalid only" }}</option>
        </select>
        <div class="v3-search">
          <n-icon :component="SearchOutline" :size="12" />
          <input
            v-model="search"
            :placeholder="t('keys.searchKeyPlaceholder') || 'Filter keys…'"
            @input="onSearchInput"
          />
        </div>
      </div>
    </div>

    <!-- Key table -->
    <n-spin :show="keysLoading">
      <div style="overflow: auto">
        <table class="v3-ktable">
          <thead>
            <tr>
              <th>Key</th>
              <th>Status</th>
              <th>24h req</th>
              <th>Failures</th>
              <th>Last used</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="k in keys" :key="k.id">
              <td>
                <span class="v3-k-mask">
                  {{ displayKey(k) }}
                  <button
                    class="v3-btn v3-btn--ghost v3-btn--icon"
                    :title="k.is_visible ? 'Hide' : 'Show'"
                    @click="toggleVisible(k)"
                  >
                    <n-icon
                      :component="k.is_visible ? EyeOffOutline : EyeOutline"
                      :size="12"
                    />
                  </button>
                  <button
                    class="v3-btn v3-btn--ghost v3-btn--icon"
                    title="Copy"
                    @click="copyKey(k)"
                  >
                    <n-icon :component="CopyOutline" :size="12" />
                  </button>
                </span>
                <div
                  v-if="editingNoteId === k.id"
                  style="margin-top: 6px; display: flex; gap: 6px; align-items: center"
                >
                  <input
                    ref="el => focusEditingInput(el, k.id)"
                    v-model="editingNoteText"
                    class="v3-search"
                    :placeholder="t('v3.notesPlaceholder')"
                    style="
                      flex: 1;
                      padding: 4px 8px;
                      font: 500 12px var(--v3-sans);
                      color: var(--v3-ink);
                      border: 1px solid var(--v3-line);
                      background: var(--v3-surface);
                      border-radius: 5px;
                      min-width: 160px;
                    "
                    @keyup.enter="saveNotes(k)"
                    @keyup.esc="cancelNotes"
                    @click.stop
                  />
                  <button
                    class="v3-btn v3-btn--accent v3-btn--sm"
                    :disabled="savingNotesId === k.id"
                    @click.stop="saveNotes(k)"
                  >
                    <n-icon :component="CheckmarkCircle" :size="11" />
                  </button>
                  <button
                    class="v3-btn v3-btn--ghost v3-btn--sm"
                    @click.stop="cancelNotes"
                  >
                    <n-icon :component="CloseOutline" :size="11" />
                  </button>
                </div>
                <div
                  v-else
                  style="
                    margin-top: 4px;
                    display: flex;
                    align-items: center;
                    gap: 6px;
                  "
                >
                  <span
                    v-if="k.notes"
                    style="font: 400 11px/1.3 var(--v3-sans); color: var(--v3-ink-3)"
                  >
                    {{ k.notes }}
                  </span>
                  <span
                    v-else
                    style="font: 400 11px/1.3 var(--v3-sans); color: var(--v3-ink-4)"
                  >
                    —
                  </span>
                  <button
                    class="v3-btn v3-btn--ghost v3-btn--icon"
                    :title="t('v3.editNotes')"
                    @click.stop="startEditNotes(k)"
                  >
                    <n-icon :component="PencilOutline" :size="11" />
                  </button>
                </div>
              </td>
              <td>
                <span :class="statusChipClass(k.status)">
                  <span
                    class="v3-dot"
                    :class="{
                      'v3-dot--ok': k.status === 'active',
                      'v3-dot--danger': k.status === 'invalid',
                    }"
                  />
                  {{ statusLabel(k.status) }}
                </span>
              </td>
              <td class="tnum mono">{{ k.request_count?.toLocaleString() ?? 0 }}</td>
              <td>
                <div style="display: flex; align-items: center; gap: 8px">
                  <span
                    class="v3-k-fail-bar"
                    :class="{ 'v3-k-fail-bar--ok': (k.failure_count || 0) === 0 }"
                  >
                    <i
                      :style="{
                        width: `${Math.min(100, (k.failure_count || 0) * 18)}%`,
                      }"
                    />
                  </span>
                  <span
                    class="mono tnum"
                    style="font-size: 11.5px; color: var(--v3-ink-3)"
                  >
                    {{ k.failure_count || 0 }} fail
                  </span>
                </div>
              </td>
              <td
                class="mono"
                style="color: var(--v3-ink-3); font-size: 11.5px"
              >
                {{ formatRelative(k.last_used_at) }}
              </td>
              <td style="text-align: right">
                <button
                  class="v3-btn v3-btn--ghost v3-btn--sm"
                  title="Test key"
                  @click="testKey(k)"
                >
                  <n-icon :component="CheckmarkCircle" :size="12" />
                </button>
                <button
                  v-if="k.status === 'invalid'"
                  class="v3-btn v3-btn--ghost v3-btn--sm"
                  title="Restore"
                  @click="restoreKey(k)"
                >
                  <n-icon :component="RefreshOutline" :size="12" />
                </button>
                <button
                  class="v3-btn v3-btn--ghost v3-btn--sm v3-btn--danger"
                  title="Delete"
                  @click="deleteKey(k)"
                >
                  <n-icon :component="Trash" :size="12" />
                </button>
              </td>
            </tr>
            <tr v-if="!keys.length">
              <td
                colspan="6"
                style="
                  padding: 28px 16px;
                  text-align: center;
                  color: var(--v3-ink-3);
                  font-size: 12.5px;
                "
              >
                {{ t("keys.noKeys") || "No keys in this group yet." }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </n-spin>

    <div
      v-if="totalPages > 1"
      style="
        padding: 12px 16px;
        border-top: 1px solid var(--v3-line);
        display: flex;
        justify-content: flex-end;
      "
    >
      <n-pagination
        v-model:page="page"
        :page-count="totalPages"
        :page-size="pageSize"
        :item-count="total"
      />
    </div>

    <!-- Modals -->
    <key-create-dialog
      v-model:show="showAddKey"
      :group-id="group.id ?? 0"
      :group-name="group.name"
      @success="refreshFromCreate"
    />
    <key-delete-dialog
      v-model:show="showDeleteKey"
      :group-id="group.id ?? 0"
      :group-name="group.name"
      @success="refreshFromDelete"
    />
    <group-form-modal
      v-model:show="showEditGroup"
      :group="group"
      @success="onGroupUpdated"
    />
    <group-copy-modal
      v-model:show="showCopyGroup"
      :source-group="group"
      @success="onGroupCopied"
    />
  </section>
</template>
