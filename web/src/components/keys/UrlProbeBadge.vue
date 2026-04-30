<script setup lang="ts">
import { ref, watch } from "vue";
import { upstreamApi, type ProbeResult } from "@/api/upstream";

const props = defineProps<{ url: string; channelType?: string }>();
const emit = defineEmits<{ (e: "detected", res: ProbeResult): void }>();

type State = "idle" | "probing" | "ok" | "fail";
const state = ref<State>("idle");
const detail = ref<ProbeResult | null>(null);
let timer: number | undefined;

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
        const res = await upstreamApi.probe(next, prefer);
        const payload = (res as unknown as { data: ProbeResult }).data;
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
      ✓ {{ detail.channel_type }} ({{ detail.version_prefix }}) · {{ detail.latency_ms }}ms
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
