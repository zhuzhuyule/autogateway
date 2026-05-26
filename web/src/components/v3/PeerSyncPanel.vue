<script setup lang="ts">
import { syncApi, type SyncPeer } from "@/api/sync";
import { Add, CheckmarkCircle, CloseCircle, Create, Refresh, Trash } from "@vicons/ionicons5";
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
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
  useMessage,
} from "naive-ui";
import { h, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

const peers = ref<SyncPeer[]>([]);
const loading = ref(false);

const showModal = ref(false);
const editingPeer = ref<Partial<SyncPeer> | null>(null);
const confirmPhrase = ref("");
const formRef = ref();

const columns = [
  {
    title: t("sync.peerName") || "Peer Name",
    key: "name",
  },
  {
    title: "URL",
    key: "url",
  },
  {
    title: t("sync.status") || "Status",
    key: "status",
    render(row: SyncPeer) {
      const isConnected = row.status === "connected";
      return h(
        NTag,
        { type: isConnected ? "success" : "error", size: "small" },
        {
          default: () => (isConnected ? "Connected" : "Disconnected"),
          icon: () =>
            h(NIcon, {
              component: isConnected ? CheckmarkCircle : CloseCircle,
            }),
        }
      );
    },
  },
  {
    title: t("sync.lastSync") || "Last Sync",
    key: "last_synced_at",
    render(row: SyncPeer) {
      if (!row.last_synced_at) return "Never";
      return h(NTime, { time: new Date(row.last_synced_at) });
    },
  },
  {
    title: t("common.actions"),
    key: "actions",
    render(row: SyncPeer) {
      return h(NSpace, null, {
        default: () => [
          h(
            NButton,
            {
              size: "small",
              tertiary: true,
              onClick: () => handleEdit(row),
            },
            { icon: () => h(NIcon, null, { default: () => h(Create) }) }
          ),
          h(
            NPopconfirm,
            {
              onPositiveClick: () => handleDelete(row.id),
            },
            {
              trigger: () =>
                h(
                  NButton,
                  {
                    size: "small",
                    type: "error",
                    tertiary: true,
                  },
                  { icon: () => h(NIcon, null, { default: () => h(Trash) }) }
                ),
              default: () => t("common.confirmDelete"),
            }
          ),
        ],
      });
    },
  },
];

async function loadPeers() {
  loading.value = true;
  try {
    peers.value = await syncApi.getPeers();
  } catch (err: any) {
    message.error(err.response?.data?.error || t("sync.loadFailed") || "Failed to load peers");
  } finally {
    loading.value = false;
  }
}

onMounted(() => {
  loadPeers();
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

    if (editingPeer.value.sync_api_keys && confirmPhrase.value !== "I understand the risks") {
      message.error(t("sync.confirmPhraseError") || "Please type the confirmation phrase exactly.");
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
  <n-card class="v3-card" :title="t('sync.peerSync') || 'P2P Mesh Sync'" style="margin-bottom: 24px;">
    <template #header-extra>
      <n-space>
        <n-button @click="loadPeers" tertiary circle>
          <template #icon>
            <n-icon><Refresh /></n-icon>
          </template>
        </n-button>
        <n-button type="primary" @click="handleAdd">
          <template #icon>
            <n-icon><Add /></n-icon>
          </template>
          {{ t("common.add") }}
        </n-button>
      </n-space>
    </template>

    <n-data-table
      :columns="columns"
      :data="peers"
      :loading="loading"
      :bordered="false"
      size="small"
    />

    <n-modal v-model:show="showModal" preset="card" style="width: 500px" title="Sync Peer">
      <n-form ref="formRef" :model="editingPeer || {}">
        <n-form-item :label="t('sync.peerName') || 'Peer Name'" path="name" :rule="{ required: true, message: 'Required' }">
          <n-input v-model:value="editingPeer!.name" placeholder="e.g. Cloud Node 1" />
        </n-form-item>
        
        <n-form-item label="Peer URL" path="url" :rule="{ required: true, message: 'Required' }">
          <n-input v-model:value="editingPeer!.url" placeholder="http://peer-ip:port" />
        </n-form-item>
        
        <n-form-item :label="t('sync.syncKey') || 'Sync Key'" path="sync_key" :rule="{ required: true, message: 'Required' }">
          <n-input v-model:value="editingPeer!.sync_key" type="password" show-password-on="click" placeholder="Must match the peer's SyncKey" />
        </n-form-item>

        <n-form-item :label="t('sync.syncApiKeys') || 'Sync API Keys'">
          <n-switch v-model:value="editingPeer!.sync_api_keys" />
        </n-form-item>

        <n-alert v-if="editingPeer?.sync_api_keys" type="error" title="CRITICAL WARNING" style="margin-bottom: 16px;">
          Syncing API keys exposes highly sensitive credentials to the peer node. Ensure the peer is trusted and the connection is secure.
          <div style="margin-top: 8px;">
            To confirm, type <strong>I understand the risks</strong> below:
            <n-input v-model:value="confirmPhrase" placeholder="I understand the risks" style="margin-top: 8px;" />
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
  </n-card>
</template>
