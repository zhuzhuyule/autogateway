<script setup lang="ts">
// 模型能力图标组 — 把 registry 里的 tags 映射成图标 chip,直观区分模型模态.
// 图标全部来自 @vicons/ionicons5,统一 currentColor 风格.
import {
  ChatbubblesOutline,
  ImageOutline,
  EyeOutline,
  LayersOutline,
  VideocamOutline,
  MicOutline,
  VolumeMediumOutline,
  GitNetworkOutline,
  SwapVerticalOutline,
  BulbOutline,
  ConstructOutline,
  LanguageOutline,
  CodeSlashOutline,
  DocumentTextOutline,
  ShieldCheckmarkOutline,
  SearchOutline,
  CubeOutline,
  MusicalNotesOutline,
} from "@vicons/ionicons5";
import { NIcon, NTooltip } from "naive-ui";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  tags?: string[];
  size?: number;
}
const props = withDefaults(defineProps<Props>(), { tags: () => [], size: 12 });

const { t } = useI18n();

interface CapEntry {
  key: string;
  icon: typeof ChatbubblesOutline;
  /** 标签 i18n key (回退到字面值) */
  labelKey: string;
  /** chip 颜色 hint,对应 v3-chip-- 修饰类后缀 */
  cls: string;
}

// 顺序 = 渲染顺序;一个模型可能命中多个,我们去重展示.
const REGISTRY: Record<string, CapEntry> = {
  "text-generation": {
    key: "text",
    icon: ChatbubblesOutline,
    labelKey: "v3.capText",
    cls: "cap-icon--text",
  },
  chat: { key: "text", icon: ChatbubblesOutline, labelKey: "v3.capText", cls: "cap-icon--text" },
  multimodal: {
    key: "multimodal",
    icon: LayersOutline,
    labelKey: "v3.capMultimodal",
    cls: "cap-icon--multimodal",
  },
  vision: { key: "vision", icon: EyeOutline, labelKey: "v3.capVision", cls: "cap-icon--vision" },
  "image-processing": {
    key: "vision",
    icon: EyeOutline,
    labelKey: "v3.capVision",
    cls: "cap-icon--vision",
  },
  "image-generation": {
    key: "image",
    icon: ImageOutline,
    labelKey: "v3.capImage",
    cls: "cap-icon--image",
  },
  "image-to-image": {
    key: "image",
    icon: ImageOutline,
    labelKey: "v3.capImage",
    cls: "cap-icon--image",
  },
  "video-generation": {
    key: "video",
    icon: VideocamOutline,
    labelKey: "v3.capVideo",
    cls: "cap-icon--video",
  },
  "video-processing": {
    key: "video",
    icon: VideocamOutline,
    labelKey: "v3.capVideo",
    cls: "cap-icon--video",
  },
  embeddings: {
    key: "embedding",
    icon: GitNetworkOutline,
    labelKey: "v3.capEmbedding",
    cls: "cap-icon--embedding",
  },
  rerank: {
    key: "rerank",
    icon: SwapVerticalOutline,
    labelKey: "v3.capRerank",
    cls: "cap-icon--rerank",
  },
  reasoning: {
    key: "reasoning",
    icon: BulbOutline,
    labelKey: "v3.capReasoning",
    cls: "cap-icon--reasoning",
  },
  "tool-use": {
    key: "tool",
    icon: ConstructOutline,
    labelKey: "v3.capTool",
    cls: "cap-icon--tool",
  },
  "function-calling": {
    key: "tool",
    icon: ConstructOutline,
    labelKey: "v3.capTool",
    cls: "cap-icon--tool",
  },
  agentic: { key: "tool", icon: ConstructOutline, labelKey: "v3.capTool", cls: "cap-icon--tool" },
  "speech-recognition": {
    key: "asr",
    icon: MicOutline,
    labelKey: "v3.capASR",
    cls: "cap-icon--asr",
  },
  "speech-synthesis": {
    key: "tts",
    icon: VolumeMediumOutline,
    labelKey: "v3.capTTS",
    cls: "cap-icon--tts",
  },
  "music-generation": {
    key: "music",
    icon: MusicalNotesOutline,
    labelKey: "v3.capMusic",
    cls: "cap-icon--music",
  },
  translation: {
    key: "translate",
    icon: LanguageOutline,
    labelKey: "v3.capTranslate",
    cls: "cap-icon--translate",
  },
  "code-generation": {
    key: "code",
    icon: CodeSlashOutline,
    labelKey: "v3.capCode",
    cls: "cap-icon--code",
  },
  "document-processing": {
    key: "doc",
    icon: DocumentTextOutline,
    labelKey: "v3.capDoc",
    cls: "cap-icon--doc",
  },
  moderation: {
    key: "moderation",
    icon: ShieldCheckmarkOutline,
    labelKey: "v3.capModeration",
    cls: "cap-icon--moderation",
  },
  "web-search": {
    key: "search",
    icon: SearchOutline,
    labelKey: "v3.capSearch",
    cls: "cap-icon--search",
  },
  "3d-generation": {
    key: "3d",
    icon: CubeOutline,
    labelKey: "v3.cap3D",
    cls: "cap-icon--3d",
  },
};

// 按用户优先级排序: 文本 → 图像 → 向量 → 视频 → 其它
const PRIORITY = [
  "text",
  "multimodal",
  "vision",
  "image",
  "video",
  "embedding",
  "rerank",
  "reasoning",
  "tool",
  "asr",
  "tts",
  "music",
  "code",
  "doc",
  "translate",
  "search",
  "moderation",
  "3d",
];

const visible = computed<CapEntry[]>(() => {
  const seen = new Set<string>();
  const out: CapEntry[] = [];
  for (const tag of props.tags) {
    const entry = REGISTRY[tag];
    if (!entry || seen.has(entry.key)) {
      continue;
    }
    seen.add(entry.key);
    out.push(entry);
  }
  out.sort((a, b) => PRIORITY.indexOf(a.key) - PRIORITY.indexOf(b.key));
  return out;
});

function labelFor(e: CapEntry): string {
  // 回退到 key 作为字面值
  const fallbacks: Record<string, string> = {
    text: "文本",
    multimodal: "多模态",
    vision: "图像理解",
    image: "图像生成",
    video: "视频",
    embedding: "向量",
    rerank: "重排",
    reasoning: "推理",
    tool: "工具调用",
    asr: "语音识别",
    tts: "语音合成",
    music: "音乐",
    code: "代码",
    doc: "文档",
    translate: "翻译",
    search: "联网",
    moderation: "审核",
    "3d": "3D",
  };
  return t(e.labelKey) || fallbacks[e.key] || e.key;
}
</script>

<template>
  <span v-if="visible.length" class="cap-icon-row">
    <n-tooltip v-for="e in visible" :key="e.key" placement="top">
      <template #trigger>
        <span :class="['cap-icon', e.cls]">
          <n-icon :component="e.icon" :size="size" />
        </span>
      </template>
      {{ labelFor(e) }}
    </n-tooltip>
  </span>
</template>

<style scoped>
.cap-icon-row {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
}
.cap-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 2px;
  border-radius: 3px;
  background: var(--v3-surface-2, oklch(0.96 0 0));
  color: var(--v3-ink-3, oklch(0.45 0 0));
  border: 1px solid transparent;
  transition: border-color 120ms;
}
.cap-icon:hover {
  border-color: currentColor;
}
.cap-icon--text {
  background: oklch(0.96 0.02 230);
  color: oklch(0.45 0.15 230);
}
.cap-icon--multimodal {
  background: oklch(0.96 0.04 290);
  color: oklch(0.45 0.18 290);
}
.cap-icon--vision {
  background: oklch(0.96 0.04 250);
  color: oklch(0.45 0.18 250);
}
.cap-icon--image {
  background: oklch(0.96 0.04 320);
  color: oklch(0.5 0.18 320);
}
.cap-icon--video {
  background: oklch(0.96 0.04 20);
  color: oklch(0.5 0.18 20);
}
.cap-icon--embedding {
  background: oklch(0.96 0.04 200);
  color: oklch(0.45 0.16 200);
}
.cap-icon--rerank {
  background: oklch(0.96 0.04 165);
  color: oklch(0.45 0.16 165);
}
.cap-icon--reasoning {
  background: oklch(0.96 0.05 80);
  color: oklch(0.5 0.18 80);
}
.cap-icon--tool {
  background: oklch(0.96 0.03 130);
  color: oklch(0.45 0.15 130);
}
.cap-icon--asr,
.cap-icon--tts,
.cap-icon--music {
  background: oklch(0.96 0.04 350);
  color: oklch(0.5 0.16 350);
}
.cap-icon--code {
  background: oklch(0.96 0.03 150);
  color: oklch(0.45 0.16 150);
}
.cap-icon--doc {
  background: oklch(0.96 0.02 60);
  color: oklch(0.45 0.13 60);
}
.cap-icon--translate {
  background: oklch(0.96 0.04 270);
  color: oklch(0.5 0.16 270);
}
.cap-icon--search {
  background: oklch(0.96 0.04 200);
  color: oklch(0.5 0.16 200);
}
.cap-icon--moderation {
  background: oklch(0.96 0.04 30);
  color: oklch(0.5 0.16 30);
}
.cap-icon--3d {
  background: oklch(0.96 0.03 110);
  color: oklch(0.45 0.16 110);
}
</style>
