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
import { versionService } from "@/services/version";
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

const { t, te } = useI18n();
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
      // minor 版本不一致只是 cosmetic, 同步仍正常工作 — 用 info 蓝色 + 友好文案,
      // 而不是 warning 黄色让用户以为出问题了.
      const isMinorVerOnly = st.kind === "warn" && st.reason === "minor_version_diff";
      const map = {
        ok: { type: "success" as const, icon: CheckmarkCircle, label: t("sync.statusConnected") },
        off: { type: "default" as const, icon: CloseCircle, label: t("sync.statusDisconnected") },
        warn: { type: "warning" as const, icon: WarningOutline, label: t("sync.statusWarning") },
        rej: { type: "error" as const, icon: CloseCircle, label: t("sync.statusRejected") },
      };
      const item = isMinorVerOnly
        ? { type: "info" as const, icon: CheckmarkCircle, label: t("sync.statusConnectedMinorDiff") }
        : map[st.kind];
      // reason 用 i18n key 翻译, 未知 reason 退回原值
      const reasonText = st.reason
        ? (te(`sync.reason.${st.reason}`) ? t(`sync.reason.${st.reason}`) : st.reason)
        : "";
      return h(
        NTag,
        { type: item.type, size: "small" },
        {
          default: () => (isMinorVerOnly || !reasonText) ? item.label : `${item.label}: ${reasonText}`,
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
      // 注: peer 行不再显示升级按钮 — 跨节点远程升级当前未实现, 显示按钮反而误导.
      // 顶部"升级本端"按钮基于 GitHub release 真实测算, 才是用户唯一可触发的升级路径.
      return h(NSpace, { size: 4 }, {
        default: () => [
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

// GitHub release 真实测算 — 不依赖 mesh 内对端版本
const githubLatest = ref<{ version: string; url: string } | null>(null);
const githubChecked = ref(false);

async function loadGithubLatest() {
  try {
    const info = await versionService.checkForUpdates();
    if (info.latestVersion) {
      githubLatest.value = {
        version: info.latestVersion,
        url: info.releaseUrl || "",
      };
    }
  } catch {
    /* 拉失败不阻断 */
  } finally {
    githubChecked.value = true;
  }
}

/** 本机是否有比当前更新的 release. 基于后端 /api/version + GitHub latest. */
const hasNewerRelease = computed(() => {
  if (!myVersion.value || !githubLatest.value) return false;
  return compareSemverStr(githubLatest.value.version, myVersion.value.version) > 0;
});

/** 简单 semver 字符串比较: -1 / 0 / 1 */
function compareSemverStr(a: string, b: string): number {
  const pa = a.replace(/^v/, "").split(".").map(Number);
  const pb = b.replace(/^v/, "").split(".").map(Number);
  for (let i = 0; i < 3; i++) {
    const x = pa[i] ?? 0, y = pb[i] ?? 0;
    if (x !== y) return x < y ? -1 : 1;
  }
  return 0;
}

/** "升级本端"按钮点击 — 弹复制命令对话框 (不再走 watcher 信号文件路径, 大多数用户没部署) */
function triggerLocalUpgrade() {
  if (!myVersion.value) return;
  const cmd = "bash <(curl -fsSL https://raw.githubusercontent.com/zhuzhuyule/autogateway/main/scripts/update.sh)";
  const latest = githubLatest.value?.version || "latest";
  dialog.info({
    title: hasNewerRelease.value
      ? t("upgrade.copyCmdTitleHasNew", { v: latest })
      : t("upgrade.copyCmdTitleAlreadyLatest"),
    content: () => h("div", [
      h("p", { style: "margin: 0 0 8px 0; color: var(--text-color-2)" },
        hasNewerRelease.value
          ? t("upgrade.copyCmdBody", { from: myVersion.value!.version, to: latest })
          : t("upgrade.copyCmdBodyAlreadyLatest", { v: myVersion.value!.version })),
      h("pre", {
        style: "background: var(--code-color, rgba(0,0,0,0.05)); padding: 10px; border-radius: 6px; " +
          "font-size: 12px; overflow-x: auto; margin: 0; user-select: all"
      }, cmd),
    ]),
    positiveText: t("common.copy"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      await copyText(cmd, "cmd");
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
  if (config.value.sync_enabled && !config.value.sync_key.trim()) {
    message.warning(t("sync.syncSecretRequiredWhenEnabled"));
    return;
  }
  configSaving.value = true;
  try {
    await syncApi.updateConfig(config.value);
    // 立刻 reload 一次 — 防止本地 config 跟后端持久化结果不一致
    await loadConfig();
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

// 本机对外可达 URL — 取浏览器当前 origin (用户用什么访问就显示什么).
// 多机连调时, 用户复制这个 URL 给对端管理员; 对端添加本节点为 peer 时直接粘贴.
const myUrl = computed(() => {
  if (typeof window === "undefined") return "";
  return window.location.origin;
});

async function copyText(text: string, _what: string) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
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
  loadGithubLatest();
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
        <span
          v-if="githubChecked && !hasNewerRelease"
          style="margin-left: 4px; color: var(--success-color, #18a058)"
        >
          ✓
        </span>
        · schema <code style="font-size:11px">{{ myVersion.schema_hash }}</code>
      </span>
    </template>

    <!-- 本机身份卡片 — 对端添加本节点为 peer 时需要的全部信息一处展示 -->
    <div v-if="myVersion" class="v3-sync-identity">
      <div class="v3-sync-identity__title">{{ t("sync.shareWithPeer") }}</div>

      <div class="v3-sync-identity__row">
        <div class="v3-sync-identity__label">{{ t("sync.myUrl") }}</div>
        <div class="v3-sync-identity__value">
          <code>{{ myUrl }}</code>
          <n-button size="tiny" tertiary @click="copyText(myUrl, 'url')">
            {{ t("common.copy") }}
          </n-button>
        </div>
      </div>

      <div class="v3-sync-identity__row">
        <div class="v3-sync-identity__label">{{ t("sync.myIdentity") }}</div>
        <div class="v3-sync-identity__value" :title="myVersion.public_key">
          <code>{{ myVersion.fingerprint }}</code>
          <n-button
            size="tiny"
            tertiary
            @click="copyText(myVersion.fingerprint, 'fp')"
          >
            {{ t("common.copy") }}
          </n-button>
        </div>
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
        <!-- 升级本端按钮 — 仅 GitHub release > 本机 时显示 (真实测算, 不靠 mesh 对比) -->
        <n-button
          v-if="hasNewerRelease"
          tertiary
          type="warning"
          @click="triggerLocalUpgrade"
          :title="t('upgrade.localUpgradeTip')"
        >
          <template #icon><n-icon><ArrowUpCircle /></n-icon></template>
          {{ t("upgrade.localUpgradeBtnWithVersion", { v: githubLatest?.version || "" }) }}
        </n-button>
        <!-- 已是最新 — disabled 状态 -->
        <n-tag v-else-if="githubChecked && myVersion" size="small" type="success" round>
          <template #icon><n-icon><CheckmarkCircle /></n-icon></template>
          {{ t("upgrade.alreadyLatest") }}
        </n-tag>
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
  padding: 14px 16px;
  margin-bottom: 12px;
  border-radius: 8px;
  background: var(--v3-surface-2, rgba(0, 0, 0, 0.02));
  border-left: 3px solid var(--primary-color, #18a058);
}
.v3-sync-identity__title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-color-2);
  margin-bottom: 10px;
}
.v3-sync-identity__row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 6px;
}
.v3-sync-identity__label {
  font-size: 12px;
  color: var(--text-color-3);
  font-weight: 500;
  min-width: 88px;
  flex-shrink: 0;
}
.v3-sync-identity__value {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
}
.v3-sync-identity__value code {
  font: 600 13px var(--v3-mono, ui-monospace);
  letter-spacing: 0.3px;
  background: var(--code-color, rgba(0, 0, 0, 0.04));
  padding: 3px 8px;
  border-radius: 4px;
  word-break: break-all;
}
.v3-sync-identity__hint {
  font-size: 11px;
  color: var(--text-color-3);
  margin-top: 8px;
  line-height: 1.5;
}
</style>
