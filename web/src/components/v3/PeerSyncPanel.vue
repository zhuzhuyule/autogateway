<script setup lang="ts">
import {
  syncApi,
  upgradeApi,
  type SyncPeer,
  type SyncLog,
  type SyncConfig,
  type VersionInfo,
  type UpgradeStatus,
} from "@/api/sync";
import {
  Add,
  ArrowUpCircle,
  CheckmarkCircle,
  CloseCircle,
  Create,
  Refresh,
  Time,
  Trash,
  WarningOutline,
} from "@vicons/ionicons5";
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NModal,
  NPopconfirm,
  NSpace,
  NSwitch,
  NTag,
  NTime,
  useDialog,
  useMessage,
} from "naive-ui";
import { computed, h, onMounted, onUnmounted, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();

// 全局同步配置 (顶部卡片管理)
const config = ref<SyncConfig>({ sync_enabled: false, sync_key: "" });
const configSaving = ref(false);

const peers = ref<SyncPeer[]>([]);
const loading = ref(false);
const myVersion = ref<VersionInfo | null>(null);

const showModal = ref(false);
const editingPeer = ref<Partial<SyncPeer> | null>(null);
const formRef = ref();

const logDrawer = ref(false);
const logPeer = ref<SyncPeer | null>(null);
const logRows = ref<SyncLog[]>([]);
const logLoading = ref(false);

const upgradeStatus = ref<UpgradeStatus | null>(null);
let upgradePoller: number | null = null;

function parseStatus(s: string): { kind: "ok" | "off" | "warn" | "rej"; reason?: string } {
  if (s === "connected") return { kind: "ok" };
  if (s.startsWith("warning:")) return { kind: "warn", reason: s.slice("warning:".length) };
  if (s.startsWith("rejected:")) return { kind: "rej", reason: s.slice("rejected:".length) };
  return { kind: "off" };
}

function versionBadge(
  peerVer: string | undefined
): "unknown" | "match" | "diff" | "incompat" {
  if (!peerVer || !myVersion.value) return "unknown";
  const mine = myVersion.value.version;
  if (peerVer === mine) return "match";
  const majorOf = (v: string) => v.replace(/^v/, "").split(".")[0];
  if (majorOf(peerVer) !== majorOf(mine)) return "incompat";
  return "diff";
}

function versionBadgeForPeer(peerVer: string): "higher" | "lower" | "equal" | "unknown" {
  if (!myVersion.value) return "unknown";
  const partsM = myVersion.value.version.replace(/^v/, "").split(".").map(Number);
  const partsP = peerVer.replace(/^v/, "").split(".").map(Number);
  for (let i = 0; i < 3; i++) {
    const a = partsP[i] ?? 0;
    const b = partsM[i] ?? 0;
    if (a > b) return "higher";
    if (a < b) return "lower";
  }
  return "equal";
}

const columns = computed(() => [
  {
    title: t("sync.peerName"),
    key: "name",
    render(row: SyncPeer) {
      return h("div", { style: "display:flex;flex-direction:column;gap:2px" }, [
        h("div", { style: "font-weight:500" }, row.name),
        h("div", { style: "color:var(--text-color-3);font-size:12px" }, row.url),
      ]);
    },
  },
  {
    title: t("sync.status"),
    key: "status",
    render(row: SyncPeer) {
      const st = parseStatus(row.status);
      const map = {
        ok: { type: "success" as const, icon: CheckmarkCircle, label: t("sync.statusConnected") },
        off: { type: "default" as const, icon: CloseCircle, label: t("sync.statusDisconnected") },
        warn: { type: "warning" as const, icon: WarningOutline, label: t("sync.statusWarning") },
        rej: { type: "error" as const, icon: CloseCircle, label: t("sync.statusRejected") },
      };
      const item = map[st.kind];
      return h(
        NTag,
        { type: item.type, size: "small" },
        {
          default: () => item.label + (st.reason ? `: ${st.reason}` : ""),
          icon: () => h(NIcon, { component: item.icon }),
        }
      );
    },
  },
  {
    title: t("sync.peerVersion"),
    key: "peer_version",
    render(row: SyncPeer) {
      const b = versionBadge(row.peer_version);
      const cfg = {
        unknown: { type: "default" as const, label: "—" },
        match: { type: "success" as const, label: row.peer_version || "" },
        diff: { type: "warning" as const, label: row.peer_version || "" },
        incompat: { type: "error" as const, label: row.peer_version || "" },
      };
      const c = cfg[b];
      return h(NTag, { type: c.type, size: "small" }, { default: () => c.label });
    },
  },
  {
    title: t("sync.lastSync"),
    key: "last_synced_at",
    render(row: SyncPeer) {
      if (!row.last_synced_at) {
        return h("span", { style: "color:var(--text-color-3)" }, t("sync.never"));
      }
      return h(NTime, { time: new Date(row.last_synced_at), type: "relative" });
    },
  },
  {
    title: t("common.actions"),
    key: "actions",
    render(row: SyncPeer) {
      const canUpgrade =
        versionBadge(row.peer_version) === "diff" &&
        !!myVersion.value &&
        !!row.peer_version;
      return h(NSpace, { size: 4 }, {
        default: () => [
          canUpgrade &&
            h(
              NButton,
              {
                size: "small",
                type: "primary",
                tertiary: true,
                onClick: () => confirmRemoteUpgrade(row),
                title: t("upgrade.remoteUpgradeTip", { v: myVersion.value!.version }),
              },
              {
                icon: () => h(NIcon, null, { default: () => h(ArrowUpCircle) }),
                default: () => myVersion.value!.version,
              }
            ),
          h(
            NButton,
            { size: "small", tertiary: true, onClick: () => openLogs(row), title: t("sync.viewHistory") },
            { icon: () => h(NIcon, null, { default: () => h(Time) }) }
          ),
          h(
            NButton,
            { size: "small", tertiary: true, onClick: () => handleEdit(row), title: t("common.edit") },
            { icon: () => h(NIcon, null, { default: () => h(Create) }) }
          ),
          h(
            NPopconfirm,
            { onPositiveClick: () => handleDelete(row.id) },
            {
              trigger: () =>
                h(
                  NButton,
                  { size: "small", type: "error", tertiary: true, title: t("common.delete") },
                  { icon: () => h(NIcon, null, { default: () => h(Trash) }) }
                ),
              default: () => t("common.confirmDelete"),
            }
          ),
        ].filter(Boolean),
      });
    },
  },
]);

function confirmRemoteUpgrade(_row: SyncPeer) {
  message.info(t("upgrade.remoteNotYetImplemented"));
}

async function triggerLocalUpgrade() {
  if (!myVersion.value) return;
  const candidates = peers.value
    .map(p => p.peer_version)
    .filter((v): v is string => !!v)
    .filter(v => versionBadgeForPeer(v) === "higher");
  const target = candidates.sort().pop();
  if (!target) {
    message.info(t("upgrade.noTargetFound"));
    return;
  }
  dialog.warning({
    title: t("upgrade.confirmTitle"),
    content: t("upgrade.confirmBody", { from: myVersion.value.version, to: target }),
    positiveText: t("upgrade.confirmYes"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      try {
        await upgradeApi.request(target, "self");
        message.success(t("upgrade.requestSent"));
        await loadUpgradeStatus();
      } catch (err: any) {
        message.error(err.response?.data?.error || t("upgrade.requestFailed"));
      }
    },
  });
}

async function loadUpgradeStatus() {
  try {
    upgradeStatus.value = await upgradeApi.status();
  } catch {
    upgradeStatus.value = null;
  }
}

const logColumns = computed(() => [
  {
    title: t("sync.logTime"),
    key: "timestamp",
    width: 160,
    render(row: SyncLog) {
      return h(NTime, { time: new Date(row.timestamp), type: "relative" });
    },
  },
  {
    title: t("sync.logAction"),
    key: "action",
    width: 80,
    render(row: SyncLog) {
      return h(
        NTag,
        { size: "small", type: row.action === "push" ? "info" : "default" },
        { default: () => (row.action === "push" ? "↑ push" : "↓ pull") }
      );
    },
  },
  {
    title: t("sync.logStatus"),
    key: "status",
    width: 90,
    render(row: SyncLog) {
      return h(
        NTag,
        { size: "small", type: row.status === "success" ? "success" : "error" },
        { default: () => (row.status === "success" ? "✓" : "✗") + " " + row.status }
      );
    },
  },
  {
    title: t("sync.logDetails"),
    key: "details",
    render(row: SyncLog) {
      return h("span", { style: "color:var(--text-color-2)" }, row.error_message || row.details || "—");
    },
  },
]);

async function loadConfig() {
  try {
    const c = await syncApi.getConfig();
    // 防御: 若后端 200 但返回 "" / null / HTML fallback (vite 代理失效场景),
    // 不要把 config.value 改成非对象, 否则 <n-switch v-model> 会炸
    if (c && typeof c === "object") {
      config.value = {
        sync_enabled: !!c.sync_enabled,
        sync_key: c.sync_key ?? "",
      };
    }
  } catch {
    // 拉失败时保留默认 disabled 状态
  }
}

async function saveConfig() {
  configSaving.value = true;
  try {
    await syncApi.updateConfig(config.value);
    message.success(t("sync.configSaved"));
  } catch (err: any) {
    message.error(err.response?.data?.error || t("common.saveFailed"));
  } finally {
    configSaving.value = false;
  }
}

async function loadPeers() {
  loading.value = true;
  try {
    peers.value = await syncApi.getPeers();
  } catch (err: any) {
    message.error(err.response?.data?.error || t("sync.loadFailed"));
  } finally {
    loading.value = false;
  }
}

async function loadVersion() {
  try {
    myVersion.value = await syncApi.getVersion();
  } catch {
    /* 版本接口失败不阻断, 徽章只显示 unknown */
  }
}

async function copyFingerprint() {
  if (!myVersion.value) return;
  try {
    await navigator.clipboard.writeText(myVersion.value.fingerprint);
    message.success(t("common.copySuccess"));
  } catch {
    message.error(t("common.copyFailed") || "Copy failed");
  }
}

async function openLogs(peer: SyncPeer) {
  logPeer.value = peer;
  logDrawer.value = true;
  logLoading.value = true;
  try {
    logRows.value = await syncApi.getLogs({ peer_id: peer.id, limit: 100 });
  } catch (err: any) {
    message.error(err.response?.data?.error || t("sync.loadFailed"));
  } finally {
    logLoading.value = false;
  }
}

onMounted(() => {
  loadVersion();
  loadConfig();
  loadPeers();
  loadUpgradeStatus();
  upgradePoller = window.setInterval(() => {
    if (upgradeStatus.value?.pending) loadUpgradeStatus();
  }, 5000);
});

onUnmounted(() => {
  if (upgradePoller !== null) window.clearInterval(upgradePoller);
});

function handleAdd() {
  editingPeer.value = {
    id: Date.now().toString(36) + Math.random().toString(36).substring(2),
    name: "",
    url: "",
    sync_key: "",
    role: "client",
    pinned_fingerprint: "",
  };
  showModal.value = true;
}

function handleEdit(peer: SyncPeer) {
  editingPeer.value = { ...peer };
  showModal.value = true;
}

async function handleDelete(id: string) {
  try {
    await syncApi.deletePeer(id);
    message.success(t("common.deleteSuccess"));
    loadPeers();
  } catch (err: any) {
    message.error(err.response?.data?.error || t("common.deleteFailed"));
  }
}

async function handleSave() {
  if (!editingPeer.value) return;
  try {
    await formRef.value?.validate();
    if (peers.value.some(p => p.id === editingPeer.value?.id)) {
      await syncApi.updatePeer(editingPeer.value.id as string, editingPeer.value);
    } else {
      await syncApi.createPeer(editingPeer.value);
    }
    message.success(t("common.saveSuccess"));
    showModal.value = false;
    loadPeers();
  } catch (err: any) {
    message.error(err.response?.data?.error || t("common.saveFailed"));
  }
}
</script>

<template>
  <n-card class="v3-card" :title="t('sync.peerSync')" style="margin-bottom: 24px">
    <template #header-extra>
      <span v-if="myVersion" style="color:var(--text-color-3);font-size:12px">
        {{ t("sync.myVersion") }}:
        <strong>{{ myVersion.version }}</strong>
        · schema <code style="font-size:11px">{{ myVersion.schema_hash }}</code>
      </span>
    </template>

    <!-- 本机身份指纹 — 用户复制给对端用 -->
    <div v-if="myVersion" class="v3-sync-identity">
      <div class="v3-sync-identity__label">{{ t("sync.myIdentity") }}</div>
      <div class="v3-sync-identity__fp" :title="myVersion.public_key">
        <code>{{ myVersion.fingerprint }}</code>
        <n-button size="tiny" tertiary @click="copyFingerprint">
          {{ t("common.copy") }}
        </n-button>
      </div>
      <div class="v3-sync-identity__hint">{{ t("sync.identityHint") }}</div>
    </div>

    <!-- 全局同步配置: enable + secret (合并自原 Settings 页) -->
    <div class="v3-sync-config">
      <div class="v3-sync-config__row">
        <div class="v3-sync-config__label">
          <div class="v3-sync-config__title">{{ t("sync.enable") }}</div>
          <div class="v3-sync-config__hint">{{ t("sync.enableHint") }}</div>
        </div>
        <n-switch v-model:value="config.sync_enabled" />
      </div>
      <div class="v3-sync-config__row" v-if="config.sync_enabled">
        <div class="v3-sync-config__label">
          <div class="v3-sync-config__title">{{ t("sync.syncSecret") }}</div>
          <div class="v3-sync-config__hint">{{ t("sync.syncSecretHint") }}</div>
        </div>
        <n-input
          v-model:value="config.sync_key"
          type="password"
          show-password-on="click"
          style="max-width: 320px"
          :placeholder="t('sync.syncSecretPlaceholder')"
        />
      </div>
      <div class="v3-sync-config__actions">
        <n-button type="primary" :loading="configSaving" @click="saveConfig">
          {{ t("common.save") }}
        </n-button>
      </div>
    </div>

    <!-- 升级 pending banner -->
    <n-alert
      v-if="upgradeStatus?.pending"
      :type="(upgradeStatus.waiting_secs ?? 0) > 60 ? 'error' : 'info'"
      style="margin: 16px 0"
      :title="t('upgrade.pendingTitle', { v: upgradeStatus.request?.target_version || '?' })"
    >
      <div>{{ t("upgrade.pendingBody", { s: upgradeStatus.waiting_secs ?? 0 }) }}</div>
      <div v-if="(upgradeStatus.waiting_secs ?? 0) > 60" style="margin-top: 8px; color: var(--error-color)">
        ⚠️ {{ t("upgrade.watcherMaybeMissing") }}
      </div>
    </n-alert>

    <!-- Peers 列表 -->
    <div class="v3-sync-peers-head">
      <div class="v3-sync-peers-head__title">{{ t("sync.peers") }}</div>
      <n-space>
        <n-button @click="loadPeers" tertiary circle :title="t('common.refresh')">
          <template #icon><n-icon><Refresh /></n-icon></template>
        </n-button>
        <n-button
          v-if="peers.length > 0"
          tertiary
          type="warning"
          @click="triggerLocalUpgrade"
          :title="t('upgrade.localUpgradeTip')"
        >
          <template #icon><n-icon><ArrowUpCircle /></n-icon></template>
          {{ t("upgrade.localUpgradeBtn") }}
        </n-button>
        <n-button type="primary" @click="handleAdd">
          <template #icon><n-icon><Add /></n-icon></template>
          {{ t("sync.addPeer") }}
        </n-button>
      </n-space>
    </div>

    <n-data-table
      :columns="columns"
      :data="peers"
      :loading="loading"
      :bordered="false"
      size="small"
    >
      <template #empty>
        <n-empty :description="t('sync.noPeers')" />
      </template>
    </n-data-table>

    <!-- 新增/编辑 Peer 弹窗 (简化: 删 api_keys 开关 + 红色警告 + 手输确认) -->
    <n-modal
      v-model:show="showModal"
      preset="card"
      style="width: 520px"
      :title="t('sync.peerEditTitle')"
    >
      <n-form ref="formRef" :model="editingPeer || {}" label-placement="left" label-width="100">
        <n-form-item
          :label="t('sync.peerName')"
          path="name"
          :rule="{ required: true, message: t('common.fieldRequired') }"
        >
          <n-input v-model:value="editingPeer!.name" placeholder="e.g. prod-server-1" />
        </n-form-item>
        <n-form-item
          :label="t('sync.peerUrl')"
          path="url"
          :rule="{ required: true, message: t('common.fieldRequired') }"
        >
          <n-input v-model:value="editingPeer!.url" placeholder="http://peer-ip:port" />
        </n-form-item>
        <n-form-item
          :label="t('sync.syncKey')"
          path="sync_key"
          :rule="{ required: true, message: t('common.fieldRequired') }"
        >
          <n-input
            v-model:value="editingPeer!.sync_key"
            type="password"
            show-password-on="click"
            :placeholder="t('sync.syncKeyHint')"
          />
        </n-form-item>
        <n-form-item :label="t('sync.pinnedFingerprint')" path="pinned_fingerprint">
          <n-input
            v-model:value="editingPeer!.pinned_fingerprint"
            :placeholder="t('sync.pinnedFingerprintPlaceholder')"
          />
        </n-form-item>
        <div style="font-size: 12px; color: var(--text-color-3); margin-bottom: 12px; line-height: 1.5">
          {{ t("sync.pinnedFingerprintHint") }}
        </div>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showModal = false">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" @click="handleSave">{{ t("common.save") }}</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 历史抽屉 -->
    <n-drawer v-model:show="logDrawer" :width="640" placement="right">
      <n-drawer-content
        :title="t('sync.historyTitle') + (logPeer ? ` — ${logPeer.name}` : '')"
        closable
      >
        <n-data-table
          :columns="logColumns"
          :data="logRows"
          :loading="logLoading"
          :bordered="false"
          size="small"
          :max-height="600"
        >
          <template #empty>
            <n-empty :description="t('sync.noHistory')" />
          </template>
        </n-data-table>
      </n-drawer-content>
    </n-drawer>
  </n-card>
</template>

<style scoped>
.v3-sync-config {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 12px 16px;
  margin-bottom: 8px;
  background: var(--v3-surface-2, rgba(0, 0, 0, 0.02));
  border-radius: 8px;
}
.v3-sync-config__row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
}
.v3-sync-config__label {
  flex: 1;
  min-width: 0;
}
.v3-sync-config__title {
  font-weight: 500;
  font-size: 13px;
}
.v3-sync-config__hint {
  font-size: 12px;
  color: var(--text-color-3);
  margin-top: 2px;
}
.v3-sync-config__actions {
  display: flex;
  justify-content: flex-end;
}
.v3-sync-peers-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 4px 8px 4px;
}
.v3-sync-peers-head__title {
  font-weight: 500;
  font-size: 14px;
}
.v3-sync-identity {
  padding: 12px 16px;
  margin-bottom: 12px;
  border-radius: 8px;
  background: var(--v3-surface-2, rgba(0, 0, 0, 0.02));
  border-left: 3px solid var(--primary-color, #18a058);
}
.v3-sync-identity__label {
  font-size: 12px;
  color: var(--text-color-3);
  font-weight: 500;
  margin-bottom: 4px;
}
.v3-sync-identity__fp {
  display: flex;
  align-items: center;
  gap: 10px;
}
.v3-sync-identity__fp code {
  font: 600 14px var(--v3-mono, ui-monospace);
  letter-spacing: 0.5px;
  background: var(--code-color, rgba(0, 0, 0, 0.04));
  padding: 4px 10px;
  border-radius: 4px;
}
.v3-sync-identity__hint {
  font-size: 11px;
  color: var(--text-color-3);
  margin-top: 6px;
}
</style>
