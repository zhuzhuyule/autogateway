<script setup lang="ts">
import {
  syncApi,
  upgradeApi,
  type SyncPeer,
  type SyncLog,
  type SyncConfig,
  type SyncPolicy,
  type VersionInfo,
  type UpgradeStatus,
  type PreviewPushResponse,
  type PreviewItem,
} from "@/api/sync";
import { versionService } from "@/services/version";
import {
  Add,
  ArrowDownCircleOutline,
  ArrowUpCircle,
  ArrowUpCircleOutline,
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
  NCollapse,
  NCollapseItem,
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
const config = ref<SyncConfig>({ sync_enabled: false, sync_key: "", is_master: false });
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
      // P11.37: 按钮 disable 闸门 - 未连接的 peer 不让 trigger
      const canTrigger = isPeerConnected(row);
      const otherBusy = triggeringPeerId.value !== null && triggeringPeerId.value !== row.id;
      return h(NSpace, { size: 4 }, {
        default: () => [
          // P11.35 + P11.37: Pull 按钮 wrap NPopconfirm 做二次确认, 同时未连接 disable
          h(
            NPopconfirm,
            { onPositiveClick: () => handleTriggerPull(row), positiveText: t("sync.pullConfirmYes") },
            {
              trigger: () =>
                h(
                  NButton,
                  {
                    size: "small", tertiary: true,
                    loading: triggeringPeerId.value === row.id,
                    disabled: !canTrigger || otherBusy,
                    title: canTrigger ? t("sync.triggerPull") : t("sync.triggerDisabledNotConnected"),
                  },
                  { icon: () => h(NIcon, null, { default: () => h(ArrowDownCircleOutline) }) }
                ),
              default: () => t("sync.pullConfirmHint", { name: row.name }),
            }
          ),
          // P11.35 + P11.36 + P11.37: Push 走 preview modal, 未连接 disable
          h(
            NButton,
            {
              size: "small", tertiary: true,
              loading: triggeringPeerId.value === row.id,
              disabled: !canTrigger || otherBusy,
              onClick: () => handleTriggerPush(row),
              title: canTrigger ? t("sync.triggerPush") : t("sync.triggerDisabledNotConnected"),
            },
            { icon: () => h(NIcon, null, { default: () => h(ArrowUpCircleOutline) }) }
          ),
          // 主从: 设为本站 master(follower 只镜像它); 已是 master 显示✓
          h(
            NButton,
            {
              size: "small",
              tertiary: true,
              type: row.is_master ? "success" : "primary",
              onClick: () => setAsMaster(row),
              title: row.is_master ? "本站当前 master(镜像源)" : "设为本站 master",
            },
            { default: () => (row.is_master ? "主站✓" : "设为主站") }
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
        is_master: !!c.is_master,
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

// 主从: 同步排除清单(仅 master 编辑; 勾选某类别 = 该类别不下发给 follower)
const policy = ref<SyncPolicy>({ excludedCategories: [], excludedFields: {} });
const policySaving = ref(false);
const POLICY_CATEGORIES = ["group", "key", "alias", "subgroup", "setting"] as const;
const CATEGORY_LABELS: Record<string, string> = {
  group: "Provider(分组)",
  key: "密钥",
  alias: "模型别名",
  subgroup: "聚合关系",
  setting: "系统设置",
};
async function loadPolicy() {
  try {
    const p = await syncApi.getPolicy();
    if (p && typeof p === "object") {
      policy.value = {
        excludedCategories: p.excludedCategories || [],
        excludedFields: p.excludedFields || {},
      };
    }
  } catch {
    // 拉失败保留默认(全同步)
  }
}
function toggleCategory(cat: string, excluded: boolean) {
  const set = new Set(policy.value.excludedCategories);
  if (excluded) set.add(cat);
  else set.delete(cat);
  policy.value.excludedCategories = [...set];
}
async function savePolicy() {
  policySaving.value = true;
  try {
    await syncApi.updatePolicy(policy.value);
    message.success("排除清单已保存");
  } catch (err: any) {
    message.error(err.response?.data?.error || t("common.saveFailed"));
  } finally {
    policySaving.value = false;
  }
}

async function setNodeRole(isSlave: boolean) {
  try {
    await syncApi.setRole(isSlave);
    await loadConfig();
    message.success(isSlave ? "已切为子站 follower" : "已切为主站 master");
  } catch (err: any) {
    message.error(err.response?.data?.error || t("common.saveFailed"));
  }
}
async function setAsMaster(peer: SyncPeer) {
  try {
    await syncApi.setPeerMaster(peer.id);
    await loadPeers();
    message.success("已把 " + peer.name + " 设为本站 master");
  } catch (err: any) {
    message.error(err.response?.data?.error || t("common.saveFailed"));
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
  loadPolicy();
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

// P11.35: 手动 trigger pull/push. 用 ref 锁防止双击; 操作结束后 reload peer
// 状态拿最新 last_synced_at + status, 同时反应 sync 历史日志条目.
const triggeringPeerId = ref<string | null>(null);

async function handleTriggerPull(peer: SyncPeer) {
  if (triggeringPeerId.value) return;
  triggeringPeerId.value = peer.id;
  try {
    await syncApi.triggerPull(peer.id);
    message.success(t("sync.pullSuccess", { name: peer.name }));
    loadPeers();
  } catch (err: any) {
    message.error(err.response?.data?.message || err.response?.data?.error || t("sync.pullFailed"));
  } finally {
    triggeringPeerId.value = null;
  }
}

// P11.36: push 二次确认弹窗状态. preview API 拿到的本次将推送的清单.
const pushConfirmShow = ref(false);
const pushConfirmPeer = ref<SyncPeer | null>(null);
const pushConfirmPreview = ref<PreviewPushResponse | null>(null);
const pushConfirmLoading = ref(false);

async function handleTriggerPush(peer: SyncPeer) {
  if (triggeringPeerId.value) return;
  triggeringPeerId.value = peer.id;
  try {
    // 先 preview, 看会发什么
    const preview = await syncApi.previewPush(peer.id);
    pushConfirmPeer.value = peer;
    pushConfirmPreview.value = preview;
    pushConfirmShow.value = true;
  } catch (err: any) {
    message.error(err.response?.data?.message || err.response?.data?.error || t("sync.pushFailed"));
  } finally {
    triggeringPeerId.value = null;
  }
}

// 用户在确认弹窗按 "确认推送" — 真正发起 push
async function confirmTriggerPush() {
  if (!pushConfirmPeer.value || pushConfirmLoading.value) return;
  const peer = pushConfirmPeer.value;
  pushConfirmLoading.value = true;
  try {
    await syncApi.triggerPush(peer.id);
    message.success(t("sync.pushSuccess", { name: peer.name }));
    pushConfirmShow.value = false;
    loadPeers();
  } catch (err: any) {
    message.error(err.response?.data?.message || err.response?.data?.error || t("sync.pushFailed"));
  } finally {
    pushConfirmLoading.value = false;
  }
}

// 按 type 分组 affected, UI 渲染用
const pushConfirmGroupedAffected = computed(() => {
  const buckets: Record<string, PreviewItem[]> = { group: [], subgroup: [], alias: [], key: [], setting: [] };
  for (const item of pushConfirmPreview.value?.affected ?? []) {
    if (buckets[item.type]) buckets[item.type].push(item);
  }
  return buckets;
});

// 每组里 upsert / delete 各几条 — UI 展示在 collapse header 上
function countByAction(items: PreviewItem[]): { upsert: number; deleted: number } {
  let upsert = 0, deleted = 0;
  for (const it of items) {
    if (it.action === "delete") deleted++; else upsert++;
  }
  return { upsert, deleted };
}

// P11.37: NCollapse 展开/全部展开/全部折叠控制. 默认全部 collapsed - 用户先看 summary
const expandedTypes = ref<string[]>([]);
function toggleAllExpand() {
  const allTypes = ["group", "subgroup", "alias", "key", "setting"].filter(
    t => pushConfirmGroupedAffected.value[t]?.length > 0,
  );
  if (expandedTypes.value.length === allTypes.length) {
    expandedTypes.value = [];
  } else {
    expandedTypes.value = allTypes;
  }
}

// 相对时间 — dayjs 没装, 自写一个 minimal helper
function relativeTime(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return t("sync.relJustNow");
  const min = Math.floor(sec / 60);
  if (min < 60) return t("sync.relMinAgo", { n: min });
  const hr = Math.floor(min / 60);
  if (hr < 24) return t("sync.relHourAgo", { n: hr });
  const day = Math.floor(hr / 24);
  return t("sync.relDayAgo", { n: day });
}

// P11.37: peer 是否处于 "可触发同步" 状态. status 字段实际值见 sync_handler.go:
//   active / connected / warning:*  → 可用
//   disconnected / "" / error       → 不可用
function isPeerConnected(peer: SyncPeer): boolean {
  const s = (peer.status || "").trim();
  if (!s || s === "disconnected") return false;
  return s === "active" || s === "connected" || s.startsWith("warning");
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

    <!-- 本站角色切换(master/follower 热切, 立即生效) -->
    <div class="v3-sync-config" style="margin-bottom: 16px">
      <div class="v3-sync-config__row">
        <div class="v3-sync-config__label">
          <div class="v3-sync-config__title">本站角色</div>
          <div class="v3-sync-config__hint">
            主站(master)=权威源, 变更下发所有子站; 子站(follower)=只镜像指定 master。切换立即生效, 无需重启。
          </div>
        </div>
        <n-switch
          :value="config.is_master !== false"
          @update:value="(v: boolean) => setNodeRole(!v)"
        >
          <template #checked>主站 master</template>
          <template #unchecked>子站 follower</template>
        </n-switch>
      </div>
    </div>

    <!-- follower 提示 / master 排除清单 -->
    <n-alert
      v-if="config.is_master === false"
      type="info"
      style="margin-bottom: 16px"
      title="本节点为 follower(子节点)"
    >
      配置以 master(主站)为准, 每分钟自动镜像主站。本地改动不会自动上传, 需手动迁移到主站,
      否则会被下一轮镜像覆盖。请在下方 peer 列表把某个 peer「设为主站」, 本站只镜像它。
    </n-alert>

    <div v-if="config.is_master" class="v3-sync-config" style="margin-bottom: 16px">
      <div class="v3-sync-config__title" style="margin-bottom: 8px">
        同步排除清单 · 本主站为权威源
      </div>
      <div class="v3-sync-config__hint" style="margin-bottom: 12px">
        默认全同步; 勾选表示该类别「不」下发给 follower。本机专属字段(proxy_url
        等)已自动保留, 无需在此设置。
      </div>
      <n-space vertical size="small">
        <n-space>
          <n-checkbox
            v-for="cat in POLICY_CATEGORIES"
            :key="cat"
            :checked="policy.excludedCategories.includes(cat)"
            @update:checked="(v: boolean) => toggleCategory(cat, v)"
          >
            不同步 {{ CATEGORY_LABELS[cat] }}
          </n-checkbox>
        </n-space>
        <div>
          <n-button
            size="small"
            type="primary"
            :loading="policySaving"
            @click="savePolicy"
          >
            保存排除清单
          </n-button>
        </div>
      </n-space>
    </div>

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
        :body-content-style="{ display: 'flex', flexDirection: 'column', height: '100%', padding: '14px' }"
      >
        <!-- flex-height + 父容器 height:100% 让表格撑满抽屉内容区, 不再固定 max-height=600 -->
        <n-data-table
          :columns="logColumns"
          :data="logRows"
          :loading="logLoading"
          :bordered="false"
          size="small"
          flex-height
          style="flex: 1; min-height: 0"
        >
          <template #empty>
            <n-empty :description="t('sync.noHistory')" />
          </template>
        </n-data-table>
      </n-drawer-content>
    </n-drawer>

    <!-- P11.36: 推送二次确认弹窗 - 按表分类展示 affected -->
    <n-modal
      v-model:show="pushConfirmShow"
      preset="card"
      style="width: 640px; max-height: calc(100vh - 80px)"
      :title="t('sync.pushConfirmTitle', { name: pushConfirmPeer?.name || '' })"
      :mask-closable="false"
    >
      <div v-if="pushConfirmPreview" style="display: flex; flex-direction: column; gap: 10px">
        <!-- since 时间 + 总计 -->
        <div style="font: 400 12px var(--v3-sans); color: var(--v3-ink-3); line-height: 1.6">
          <div v-if="pushConfirmPreview.since">
            {{ t("sync.pushConfirmSince", { time: new Date(pushConfirmPreview.since).toLocaleString() }) }}
          </div>
          <div v-else>{{ t("sync.pushConfirmSinceFull") }}</div>
          <div style="margin-top: 4px; color: var(--v3-warn, #d97706)">
            ⚠️ {{ t("sync.pushConfirmLWWNote") }}
          </div>
        </div>

        <!-- 没数据时空状态 -->
        <n-empty
          v-if="pushConfirmPreview.affected.length === 0"
          :description="t('sync.pushConfirmEmpty')"
          style="padding: 20px 0"
        />

        <!-- 按表分类 collapse — 默认全部收起, 头部带 chips 显示 +N / ×N 统计 -->
        <template v-else>
          <div style="display: flex; justify-content: space-between; align-items: center;
                      padding: 2px 0 4px; border-bottom: 1px solid var(--v3-line)">
            <span style="font: 500 12px var(--v3-sans); color: var(--v3-ink-2)">
              {{ t("sync.pushConfirmAffectedTotal", { n: pushConfirmPreview.affected.length }) }}
            </span>
            <n-button text size="tiny" @click="toggleAllExpand">
              {{
                expandedTypes.length > 0
                  ? t("sync.pushConfirmCollapseAll")
                  : t("sync.pushConfirmExpandAll")
              }}
            </n-button>
          </div>

          <n-collapse
            v-model:expanded-names="expandedTypes"
            :default-expanded-names="[]"
            arrow-placement="left"
          >
            <template v-for="(items, type) in pushConfirmGroupedAffected" :key="type">
              <n-collapse-item
                v-if="items.length > 0"
                :name="type"
              >
                <template #header>
                  <span style="font: 600 12px var(--v3-sans); color: var(--v3-ink)">
                    {{ t(`sync.pushConfirmType_${type}`) }}
                  </span>
                </template>
                <template #header-extra>
                  <n-space :size="6" align="center">
                    <n-tag
                      v-if="countByAction(items).upsert > 0"
                      size="small"
                      type="success"
                      :bordered="false"
                    >
                      +{{ countByAction(items).upsert }}
                    </n-tag>
                    <n-tag
                      v-if="countByAction(items).deleted > 0"
                      size="small"
                      type="error"
                      :bordered="false"
                    >
                      ×{{ countByAction(items).deleted }}
                    </n-tag>
                  </n-space>
                </template>
                <div style="max-height: 220px; overflow-y: auto; padding: 2px 0">
                  <div
                    v-for="(item, idx) in items"
                    :key="`${type}-${idx}`"
                    style="display: flex; align-items: center; gap: 8px; padding: 4px 0;
                           font: 400 12px var(--v3-mono); color: var(--v3-ink)"
                  >
                    <span
                      :style="{
                        width: '16px', textAlign: 'center', fontWeight: 700,
                        color: item.action === 'delete' ? 'var(--v3-danger)' : 'var(--v3-ok)',
                      }"
                    >{{ item.action === "delete" ? "×" : "+" }}</span>
                    <span style="flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap"
                      :title="item.name"
                    >{{ item.name }}</span>
                    <span
                      style="font: 400 10.5px var(--v3-mono); color: var(--v3-ink-4)"
                      :title="new Date(item.updated_at).toLocaleString()"
                    >
                      {{ relativeTime(item.updated_at) }}
                    </span>
                  </div>
                </div>
              </n-collapse-item>
            </template>
          </n-collapse>
        </template>
      </div>

      <template #footer>
        <n-space justify="end">
          <n-button @click="pushConfirmShow = false">{{ t("common.cancel") }}</n-button>
          <n-button
            type="primary"
            :loading="pushConfirmLoading"
            :disabled="pushConfirmPreview?.affected.length === 0"
            @click="confirmTriggerPush"
          >
            {{ t("sync.pushConfirmAction") }}
          </n-button>
        </n-space>
      </template>
    </n-modal>
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
