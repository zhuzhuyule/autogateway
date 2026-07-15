<script setup lang="ts">
import { syncApi } from "@/api/sync";
import { NAlert, NButton, NCard, NSpace, useMessage } from "naive-ui";
import { computed, ref } from "vue";
import { useRoute, useRouter } from "vue-router";

const route = useRoute();
const router = useRouter();
const message = useMessage();

const token = computed(() => (typeof route.query.token === "string" ? route.query.token : ""));
const inviter = computed(() =>
  typeof route.query.inviter === "string" ? route.query.inviter : ""
);
const hasParams = computed(() => !!token.value && !!inviter.value);

const joining = ref(false);

async function handleConfirm() {
  if (!hasParams.value || joining.value) return;
  joining.value = true;
  try {
    await syncApi.joinParent(inviter.value, token.value);
    message.success("已加入, 正在跳转到同步页面…");
    router.push({ name: "sync" });
  } catch (err: any) {
    message.error(err.response?.data?.error || "加入失败, 请确认邀请链接是否已过期或已被使用");
  } finally {
    joining.value = false;
  }
}

function handleCancel() {
  router.push({ name: "sync" });
}
</script>

<template>
  <div>
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">加入邀请</div>
    </div>
    <h1 class="v3-viewtitle">加入邀请</h1>

    <n-card class="v3-card" style="max-width: 480px">
      <template v-if="hasParams">
        <p style="margin: 0 0 8px">
          确认加入 <strong>{{ inviter }}</strong> 为父节点?
        </p>
        <p style="margin: 0 0 16px; color: var(--text-color-3); font-size: 12px; line-height: 1.6">
          加入后本机将成为该站点的子节点, 定期镜像其配置。该操作使用一次性邀请令牌, 确认后立即生效,
          请勿重复提交。
        </p>
        <n-space justify="end">
          <n-button @click="handleCancel">取消</n-button>
          <n-button type="primary" :loading="joining" @click="handleConfirm">确认加入</n-button>
        </n-space>
      </template>
      <template v-else>
        <n-alert type="error" title="邀请链接无效">
          缺少必要参数(token / inviter), 请确认链接完整无误, 或联系邀请方重新生成邀请链接。
        </n-alert>
      </template>
    </n-card>
  </div>
</template>
