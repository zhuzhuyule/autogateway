<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from "vue";
import { useI18n } from "vue-i18n";
import { NDrawer, NDrawerContent, NList, NListItem, NTag, NButton, NPopconfirm, NSpin, NEmpty, NIcon } from "naive-ui";
import { RefreshOutline } from "@vicons/ionicons5";
import {
  listVideoTasks,
  cancelVideoTask,
  retryVideoTask,
  deleteVideoTask,
  type VideoTask,
} from "@/api/videoTasks";
import { fmtElapsed, elapsedSeconds } from "@/utils/videoTaskReconcile";

const props = defineProps<{ open: boolean; authKey: string }>();
const emit = defineEmits<{ (e: "update:open", v: boolean): void }>();
const { t } = useI18n();

const tasks = ref<VideoTask[]>([]);
const loading = ref(false);
const now = ref(Date.now()); // 驱动"已等待时长"实时刷新

async function refresh() {
  loading.value = true;
  try {
    const r = await listVideoTasks(props.authKey, { page: 1, pageSize: 50 });
    tasks.value = r.tasks;
  } finally {
    loading.value = false;
  }
}

// 打开期间每 5s 自动刷新列表 + 更新已等待时长, 关闭时停掉
const REFRESH_MS = 5000;
let timer: number | undefined;
function startAutoRefresh() {
  stopAutoRefresh();
  timer = window.setInterval(() => {
    now.value = Date.now();
    void refresh();
  }, REFRESH_MS);
}
function stopAutoRefresh() {
  if (timer) {
    window.clearInterval(timer);
    timer = undefined;
  }
}

watch(
  () => props.open,
  (v) => {
    if (v) {
      now.value = Date.now();
      void refresh();
      startAutoRefresh();
    } else {
      stopAutoRefresh();
    }
  },
);
onBeforeUnmount(stopAutoRefresh);

function statusText(s: string): string {
  const map: Record<string, string> = {
    pending: t("playground.videoQueueStatusPending"),
    running: t("playground.videoQueueStatusRunning"),
    completed: t("playground.videoQueueStatusCompleted"),
    failed: t("playground.videoQueueStatusFailed"),
    canceled: t("playground.videoQueueStatusCanceled"),
  };
  return map[s] ?? s;
}

type TagType = "default" | "success" | "error" | "info" | "warning";
function statusTagType(s: string): TagType {
  const map: Record<string, TagType> = {
    pending: "default",
    running: "info",
    completed: "success",
    failed: "error",
    canceled: "warning",
  };
  return map[s] ?? "default";
}

async function onCancel(id: string) {
  await cancelVideoTask(props.authKey, id);
  await refresh();
}
async function onRetry(id: string) {
  await retryVideoTask(props.authKey, id);
  await refresh();
}
async function onDelete(id: string) {
  await deleteVideoTask(props.authKey, id);
  await refresh();
}
</script>

<template>
  <n-drawer
    :show="open"
    :width="420"
    placement="right"
    @update:show="emit('update:open', $event)"
  >
    <n-drawer-content closable>
      <template #header>
        <div class="vq-head">
          <span>{{ t("playground.videoQueueTitle") }}</span>
          <n-button quaternary circle size="small" :loading="loading" @click="refresh">
            <template #icon>
              <n-icon :component="RefreshOutline" />
            </template>
          </n-button>
        </div>
      </template>

      <n-spin :show="loading">
        <n-empty v-if="!tasks.length" :description="t('playground.videoQueueEmpty')" class="vq-empty" />
        <n-list v-else hoverable>
          <n-list-item v-for="tk in tasks" :key="tk.id">
            <div class="vq-item">
              <div class="vq-main">
                <n-tag :type="statusTagType(tk.status)" size="small" round>
                  {{ statusText(tk.status) }}
                </n-tag>
                <span class="vq-prompt" :title="tk.prompt">{{ tk.prompt }}</span>
                <span v-if="tk.status === 'running' || tk.status === 'pending'" class="vq-progress">{{ fmtElapsed(elapsedSeconds(tk, now)) }}</span>
              </div>
              <div v-if="tk.error" class="vq-error">{{ tk.error }}</div>
              <div class="vq-actions">
                <n-button
                  v-if="tk.status === 'running' || tk.status === 'pending'"
                  size="tiny"
                  @click="onCancel(tk.id)"
                >
                  {{ t("playground.videoQueueCancel") }}
                </n-button>
                <n-button
                  v-if="tk.status === 'failed' || tk.status === 'canceled'"
                  size="tiny"
                  type="primary"
                  @click="onRetry(tk.id)"
                >
                  {{ t("playground.videoQueueRetry") }}
                </n-button>
                <n-popconfirm @positive-click="onDelete(tk.id)">
                  <template #trigger>
                    <n-button size="tiny" type="error" tertiary>
                      {{ t("playground.videoQueueDelete") }}
                    </n-button>
                  </template>
                  {{ t("playground.videoQueueDelete") }}?
                </n-popconfirm>
              </div>
            </div>
          </n-list-item>
        </n-list>
      </n-spin>
    </n-drawer-content>
  </n-drawer>
</template>

<style scoped>
.vq-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
}
.vq-empty {
  margin-top: 48px;
}
.vq-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}
.vq-main {
  display: flex;
  align-items: center;
  gap: 8px;
}
.vq-prompt {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
}
.vq-progress {
  font-size: 12px;
  color: var(--n-text-color-disabled, #999);
}
.vq-error {
  font-size: 12px;
  color: #d03050;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.vq-actions {
  display: flex;
  gap: 6px;
}
</style>
