<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { resolveProviderLogo } from "@/data/providerLogos";

interface Props {
  /** 解析参考字符串, 通常是 system_role / group_name / upstream host —
   *  优先 lobehub 关键字匹配, 命中后渲染品牌 SVG */
  hint: string;
  /** baseUrl / upstream URL — lobehub miss 时, 走 Google s2 favicon 服务拉
   *  这个 host 的图标. 自动剥子域兜底 (apihub.agnes-ai.com → agnes-ai.com).
   *  不直接 fetch 目标 host (绕 CORS), 走 Google s2 反代服务 */
  host?: string;
  /** lobehub & favicon 都失败时, 显示首字母圆色块. 取首字非空字符 + 派生背景色.
   *  通常传 group.display, 没值时整体不渲染 */
  fallbackInitial?: string;
  size?: number | string;
}

const props = withDefaults(defineProps<Props>(), {
  host: "",
  fallbackInitial: "",
  size: "1em",
});

const svgHtml = computed(() => resolveProviderLogo(props.hint) || "");

// favicon 查询: 直接用 apex 域 (剥子域), 因为 Google s2 对子域 (e.g.
// apihub.agnes-ai.com) 经常返 200 + 默认 globe 占位 — 这是 silent fail,
// onError 不触发, apex fallback 链根本进不去. 直接 apex 跳过这个坑,
// 且 provider 的品牌 logo 通常就在 apex 域 favicon.
const faviconUrl = computed<string>(() => {
  const h = (props.host || "").trim();
  if (!h) {
    return "";
  }
  let host = "";
  try {
    const url = new URL(h.startsWith("http") ? h : `https://${h}`);
    host = url.host.toLowerCase();
  } catch {
    return "";
  }
  if (!host) {
    return "";
  }
  const parts = host.split(".");
  const apex = parts.length > 2 ? parts.slice(-2).join(".") : host;
  return googleS2(apex);
});

function googleS2(domain: string): string {
  return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(domain)}&sz=64`;
}

// favicon 加载失败 → 触发 fallback initial. Google s2 对未知域虽然 silent
// 返 globe 占位 (此 err 不触发), 但真正 4xx/5xx / 网络中断仍然能命中.
const faviconFailed = ref(false);

watch(
  () => props.host,
  () => {
    faviconFailed.value = false;
  },
);

function onFaviconError() {
  faviconFailed.value = true;
}

// 首字母 + 哈希派生 HSL 背景 — 同名永远同色, 视觉上稳定
function initialOf(s: string): string {
  for (const ch of s) {
    if (/[\p{L}\p{N}]/u.test(ch)) {
      return ch.toUpperCase();
    }
  }
  return "?";
}
function hashHue(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) - h + s.charCodeAt(i)) | 0;
  }
  return Math.abs(h) % 360;
}
const initialChar = computed(() => initialOf(props.fallbackInitial || props.hint || ""));
const initialBg = computed(() => `hsl(${hashHue(props.fallbackInitial || props.hint || "")}, 45%, 52%)`);

const showFavicon = computed(() => !svgHtml.value && !!faviconUrl.value && !faviconFailed.value);
const showInitial = computed(
  () => !svgHtml.value && (faviconFailed.value || !faviconUrl.value) && !!props.fallbackInitial,
);

const sizeStyle = computed(() => {
  const v = typeof props.size === "number" ? `${props.size}px` : props.size;
  return { width: v, height: v };
});

const initialFontSize = computed(() => {
  // 圆色块里首字母大小 ≈ 半径. size 是 px 数字时直接算; CSS 单位时用相对值
  if (typeof props.size === "number") {
    return `${Math.max(8, Math.round(props.size * 0.55))}px`;
  }
  return "0.55em";
});
</script>

<template>
  <span v-if="svgHtml" class="provider-logo" :style="sizeStyle" v-html="svgHtml" />
  <img
    v-else-if="showFavicon"
    class="provider-logo provider-logo--favicon"
    :style="sizeStyle"
    :src="faviconUrl"
    :alt="hint"
    draggable="false"
    @error="onFaviconError"
  />
  <span
    v-else-if="showInitial"
    class="provider-logo provider-logo--initial"
    :style="{ ...sizeStyle, background: initialBg, fontSize: initialFontSize }"
    :title="fallbackInitial || hint"
  >{{ initialChar }}</span>
</template>

<style scoped>
.provider-logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  line-height: 0;
  border-radius: 4px;
  overflow: hidden;
}
.provider-logo :deep(svg) {
  width: 100%;
  height: 100%;
}
.provider-logo--favicon {
  object-fit: contain;
  background: #fff;
}
.provider-logo--initial {
  line-height: 1;
  color: #fff;
  font-weight: 600;
}
</style>
