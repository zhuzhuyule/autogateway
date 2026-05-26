<script setup lang="ts">
import { syncApi, upgradeApi, type SyncPeer, type SyncLog, type VersionInfo, type UpgradeStatus } from "@/api/sync";
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

const peers = ref<SyncPeer[]>([]);
const loading = ref(false);
const myVersion = ref<VersionInfo | null>(null);

// P9.2 升级状态轮询
const upgradeStatus = ref<UpgradeStatus | null>(null);
let upgradePoller: number | null = null;

const showModal = ref(false);
const editingPeer = ref<Partial<SyncPeer> | null>(null);
const confirmPhrase = ref("");
const formRef = ref();

// P9.1 历史抽屉
const logDrawer = ref(false);
const logPeer = ref<SyncPeer | null>(null);
const logRows = ref<SyncLog[]>([]);
const logLoading = ref(false);

const REQUIRED_PHRASE = "I understand the risks";

/**
 * 把后端复合 status 拆成 (kind, reason) 给 UI 上色用.
 *   "connected"                        → { kind: "ok" }
 *   "disconnected"                     → { kind: "off" }
 *   "warning:minor_version_diff"       → { kind: "warn", reason }
 *   "rejected:schema_mismatch"         → { kind: "rej", reason }
 */
function parseStatus(s: string): { kind: "ok" | "off" | "warn" | "rej"; reason?: string } {
  if (s === "connected") return { kind: "ok" };
  if (s.startsWith("warning:")) return { kind: "warn", reason: s.slice("warning:".length) };
  if (s.startsWith("rejected:")) return { kind: "rej", reason: s.slice("rejected:".length) };
  return { kind: "off" };
}

/**
 * 比对对端版本与本端版本, 返回徽章类型.
 *   未知 → "unknown"
 *   一致 → "match"
 *   仅 patch/minor 不同 → "diff"
 *   major 不同 → "incompat"
 */
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
      const config = {
        unknown: { type: "default" as const, label: "—" },
        match: { type: "success" as const, label: row.peer_version || "" },
        diff: { type: "warning" as const, label: row.peer_version || "" },
        incompat: { type: "error" as const, label: row.peer_version || "" },
      };
      const c = config[b];
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
  // 远程升级 = 触发对端的 watcher. 当前后端只暴露本端升级端点
  // (POST /api/upgrade/request), 远程触发要通过 WS protocol 扩展.
  // 这里先给出"提示用户去对端 UI 触发"的占位提示, 完整跨节点远程升级
  // 在后续 mini-PR 里通过 ws upgrade_request 消息完成.
  message.info(t("upgrade.remoteNotYetImplemented"));
}

async function triggerLocalUpgrade() {
  if (!myVersion.value) return;
  // 找 mesh 里能用的"目标版本": 任意一个 peer 的更高版本号
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

/** 把对端版本对照本端做"高/低/平/未知"判定 (区别 versionBadge 用 match/diff/incompat) */
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

async function loadUpgradeStatus() {
  try {
    upgradeStatus.value = await upgradeApi.status();
  } catch {
    upgradeStatus.value = null;
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
    // 版本接口失败不阻断主流程, 徽章只显示 unknown
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

onMounted(() => {
  loadVersion();
  loadPeers();
  loadUpgradeStatus();
  // 升级 pending 时每 5s 轮询一次, 用户能看到 watcher 接管的进度
  upgradePoller = window.setInterval(() => {
    if (upgradeStatus.value?.pending) {
      loadUpgradeStatus();
    }
  }, 5000);
});

onUnmounted(() => {
  if (upgradePoller !== null) {
    window.clearInterval(upgradePoller);
  }
});

function handleAdd() {
  editingPeer.value = {
    id: Date.now().toString(36) + Math.random().toString(36).substring(2),
    name: "",
    url: "",
    sync_key: "",
    role: "client",
    sync_api_keys: false,
  };
  confirmPhrase.value = "";
  showModal.value = true;
}

function handleEdit(peer: SyncPeer) {
  editingPeer.value = { ...peer };
  confirmPhrase.value = "";
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
    if (editingPeer.value.sync_api_keys && confirmPhrase.value !== REQUIRED_PHRASE) {
      message.error(t("sync.confirmPhraseError"));
      return;
    }
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
  <n-card
    class="v3-card"
    :title="t('sync.peerSync')"
    style="margin-bottom: 24px"
  >
    <template #header-extra>
      <n-space align="center">
        <span v-if="myVersion" style="color:var(--text-color-3);font-size:12px">
          {{ t("sync.myVersion") }}: <strong>{{ myVersion.version }}</strong>
          · schema <code style="font-size:11px">{{ myVersion.schema_hash }}</code>
        </span>
        <n-button @click="loadPeers" tertiary circle>
          <template #icon>
            <n-icon><Refresh /></n-icon>
          </template>
        </n-button>
        <n-button
          v-if="peers.length > 0"
          tertiary
          type="warning"
          @click="triggerLocalUpgrade"
          :title="t('upgrade.localUpgradeTip')"
        >
          <template #icon>
            <n-icon><ArrowUpCircle /></n-icon>
          </template>
          {{ t("upgrade.localUpgradeBtn") }}
        </n-button>
        <n-button type="primary" @click="handleAdd">
          <template #icon>
            <n-icon><Add /></n-icon>
          </template>
          {{ t("common.add") }}
        </n-button>
      </n-space>
    </template>

    <!-- P9.2: 升级 pending 提示 -->
    <n-alert
      v-if="upgradeStatus?.pending"
      :type="(upgradeStatus.waiting_secs ?? 0) > 60 ? 'error' : 'info'"
      style="margin-bottom: 16px"
      :title="t('upgrade.pendingTitle', { v: upgradeStatus.request?.target_version || '?' })"
    >
      <div>{{ t("upgrade.pendingBody", { s: upgradeStatus.waiting_secs ?? 0 }) }}</div>
      <div v-if="(upgradeStatus.waiting_secs ?? 0) > 60" style="margin-top: 8px; color: var(--error-color)">
        ⚠️ {{ t("upgrade.watcherMaybeMissing") }}
      </div>
    </n-alert>

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

    <!-- 编辑 / 新增 -->
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

        <n-form-item :label="t('sync.syncApiKeys')">
          <n-switch v-model:value="editingPeer!.sync_api_keys" />
        </n-form-item>

        <n-alert
          v-if="editingPeer?.sync_api_keys"
          type="error"
          :title="t('sync.apiKeysWarningTitle')"
          style="margin-bottom: 16px"
        >
          {{ t("sync.apiKeysWarningBody") }}
          <div style="margin-top: 8px">
            {{ t("sync.confirmPhrasePrompt") }}
            <code>{{ REQUIRED_PHRASE }}</code>
            <n-input
              v-model:value="confirmPhrase"
              :placeholder="REQUIRED_PHRASE"
              style="margin-top: 8px"
            />
          </div>
        </n-alert>
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
