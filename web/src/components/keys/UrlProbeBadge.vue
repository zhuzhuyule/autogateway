<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { upstreamApi, type ProbeResult } from "@/api/upstream";
import { normalizeUpstreamUrl } from "@/utils/upstreamUrl";

const props = defineProps<{ url: string; channelType?: string }>();
const emit = defineEmits<{ (e: "detected", res: ProbeResult): void }>();

type State = "idle" | "probing" | "ok" | "fail";
const state = ref<State>("idle");
const detail = ref<ProbeResult | null>(null);
let timer: number | undefined;

// 品牌名规范化: openai → OpenAI, anthropic → Anthropic, gemini → Gemini.
const CHANNEL_LABELS: Record<string, string> = {
  openai: "OpenAI",
  "openai-response": "OpenAI Responses",
  anthropic: "Anthropic",
  gemini: "Gemini",
};
function channelLabel(raw: string | undefined): string {
  if (!raw) {
    return "";
  }
  return CHANNEL_LABELS[raw.toLowerCase()] || raw;
}

// 版本号大写: /v1 → V1, /v1beta → V1beta.
function versionLabel(raw: string | undefined): string {
  if (!raw) {
    return "";
  }
  return raw.replace(/^\/v/, "V");
}

const detectedLabel = computed(() => {
  if (!detail.value) {
    return "";
  }
  return `${channelLabel(detail.value.channel_type)} ${versionLabel(detail.value.version_prefix)}`;
});

watch(
  () => [props.url, props.channelType] as const,
  ([next]) => {
    if (timer) {
      window.clearTimeout(timer);
    }
    if (!next || !/^https?:\/\//.test(next)) {
      state.value = "idle";
      detail.value = null;
      return;
    }
    state.value = "probing";
    timer = window.setTimeout(async () => {
      try {
        // openai-response 在后端 probe 里没有独立分支, 退化成 openai.
        const prefer =
          props.channelType === "openai-response" ? "openai" : props.channelType || undefined;
        // 用户输入可能仍带 # escape 标记 (input 显式保留), probe 前统一去 #
        // 后再 normalize, 避免 backend 收到 …/foo/# → url.Parse 丢 # 后拼出
        // …/foo//v1/models 的双斜杠.
        const stripped = next.replace(/\/+#$/, "").replace(/#$/, "");
        const probeUrl = normalizeUpstreamUrl(stripped);
        const res = await upstreamApi.probe(probeUrl, prefer);
        const payload = (res as unknown as { data: ProbeResult }).data;
        // backend 在所有协议都未命中时返回 200 + 空 channel_type, 当作 fail 展示
        if (!payload?.channel_type) {
          state.value = "fail";
          detail.value = null;
          return;
        }
        state.value = "ok";
        detail.value = payload;
        emit("detected", payload);
      } catch {
        state.value = "fail";
        detail.value = null;
      }
    }, 500);
  },
  { immediate: true }
);
</script>

<template>
  <span class="probe-badge" :data-state="state">
    <template v-if="state === 'idle'">&nbsp;</template>
    <template v-else-if="state === 'probing'">… probing</template>
    <template v-else-if="state === 'ok' && detail">
      ✓ {{ detectedLabel }} · {{ detail.latency_ms }}ms
    </template>
    <template v-else>⚠ unknown</template>
  </span>
</template>

<style scoped>
.probe-badge {
  font: 500 11px/1.6 var(--v3-mono);
  padding: 2px 6px;
  border-radius: 4px;
}
.probe-badge[data-state="ok"] {
  background: var(--v3-ok-soft);
  color: var(--v3-ok);
}
.probe-badge[data-state="fail"] {
  background: oklch(0.96 0.05 80);
  color: oklch(0.5 0.15 80);
}
.probe-badge[data-state="probing"] {
  color: var(--v3-ink-3);
}
</style>
