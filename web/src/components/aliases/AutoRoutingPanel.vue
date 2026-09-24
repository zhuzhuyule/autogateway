<script setup lang="ts">
// auto 智能路由面板: 开关 + 复杂度阈值。
//
// 折叠成一行状态条 —— 阈值是"配一次基本不动"的东西, 之前它常驻占掉半屏,
// 把真正的别名列表挤到下面; 打开页面第一眼看到的应该是别名, 不是滑块。
//
// 开关现在是真的会改行为: 关掉之后 auto 一律走 simple 档(见 router_engine
// PickForAuto), 所以这一行必须把后果写出来, 而不是只写"开/关"。
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { NIcon, NSlider, NSwitch, NTooltip, useMessage } from "naive-ui";
import {
  ChevronDownOutline,
  ChevronUpOutline,
  HelpCircleOutline,
  SettingsOutline,
} from "@vicons/ionicons5";
import { saveRoutingSettings, useAliasData } from "@/services/aliases";

const { t } = useI18n();
const message = useMessage();
const { settings } = useAliasData();

const threshOpen = ref(false);
const SLIDER_MAX = 32000;

const rangeValue = computed({
  get: () => [settings.value.SimpleThreshold, settings.value.ComplexThreshold] as [number, number],
  set: (val: [number, number]) => {
    settings.value.SimpleThreshold = val[0];
    settings.value.ComplexThreshold = val[1];
  },
});

const presetList = [
  { label: "Economy", simple: 500, complex: 2000, color: "var(--v3-ok)" },
  { label: "Balanced", simple: 2000, complex: 8000, color: "var(--v3-warn)" },
  { label: "Performance", simple: 4000, complex: 16000, color: "var(--v3-danger)" },
];

async function applyPreset(p: (typeof presetList)[0]): Promise<void> {
  settings.value.SimpleThreshold = p.simple;
  settings.value.ComplexThreshold = p.complex;
  await saveSettings(true); // 预设是显式动作, 弹 toast
}

async function saveSettings(notify = false): Promise<void> {
  if (settings.value.SimpleThreshold >= settings.value.ComplexThreshold) {
    settings.value.ComplexThreshold = settings.value.SimpleThreshold + 100;
  }
  const next = await saveRoutingSettings({
    enabled: settings.value.Enabled,
    simple_threshold: settings.value.SimpleThreshold,
    complex_threshold: settings.value.ComplexThreshold,
  });
  if (next) {
    if (notify) {
      message.success(t("common.operationSuccess"));
    }
  } else {
    message.error(t("common.requestFailed"));
  }
}

// 节流:连续拖动时只保存最后一次
let saveTimer: ReturnType<typeof setTimeout> | null = null;
function saveSettingsThrottled(): void {
  if (saveTimer) {
    clearTimeout(saveTimer);
  }
  saveTimer = setTimeout(() => {
    saveSettings();
    saveTimer = null;
  }, 400);
}
</script>

<template>
  <div class="v3-thresh-card v3-thresh-card--compact">
    <div class="v3-thresh-bar">
      <NIcon :component="SettingsOutline" :size="14" />
      <span class="v3-thresh-bar__label">{{ t("v3.complexityThresholds") }}</span>
      <NTooltip trigger="hover">
        <template #trigger>
          <NIcon
            :component="HelpCircleOutline"
            :size="13"
            style="cursor: help; color: var(--v3-ink-4)"
          />
        </template>
        {{ t("v3.complexityThresholdsSub") }}
      </NTooltip>

      <span class="v3-thresh-bar__summary">
        {{ t("v3.simple") }} &lt; {{ settings.SimpleThreshold.toLocaleString() }} ·
        {{ t("v3.medium") }} · {{ t("v3.complex") }} ≥
        {{ settings.ComplexThreshold.toLocaleString() }}
      </span>

      <button
        class="v3-btn v3-btn--sm v3-thresh-bar__expand"
        :title="threshOpen ? t('common.collapse') : t('common.expand')"
        @click="threshOpen = !threshOpen"
      >
        <NIcon :component="threshOpen ? ChevronUpOutline : ChevronDownOutline" :size="12" />
      </button>

      <span class="v3-thresh-bar__toggle">
        <span class="v3-thresh-bar__state">
          {{ settings.Enabled ? t("aliases.auto.modeThreshold") : t("aliases.auto.modeSimple") }}
        </span>
        <NSwitch v-model:value="settings.Enabled" size="small" @update:value="saveSettings" />
      </span>
    </div>

    <div v-if="threshOpen" class="v3-thresh-body">
      <div class="v5-qs__quickbtns">
        <button
          v-for="p in presetList"
          :key="p.label"
          class="v3-btn v3-btn--sm"
          style="font-size: 10.5px; padding: 3px 8px; border-radius: 4px"
          :style="{
            borderColor:
              settings.SimpleThreshold === p.simple && settings.ComplexThreshold === p.complex
                ? p.color
                : undefined,
            background:
              settings.SimpleThreshold === p.simple && settings.ComplexThreshold === p.complex
                ? p.color + '10'
                : undefined,
          }"
          @click="applyPreset(p)"
        >
          {{ p.label }} · {{ p.simple }}/{{ p.complex }}
        </button>
      </div>

      <div class="v3-custom-slider">
        <div class="v3-slider-inner">
          <div class="v3-slider-rail">
            <div
              class="v3-slider-seg v3-slider-seg--fast"
              :style="{ width: (settings.SimpleThreshold / SLIDER_MAX) * 100 + '%' }"
            />
            <div
              class="v3-slider-seg v3-slider-seg--balanced"
              :style="{
                left: (settings.SimpleThreshold / SLIDER_MAX) * 100 + '%',
                width:
                  ((settings.ComplexThreshold - settings.SimpleThreshold) / SLIDER_MAX) * 100 + '%',
              }"
            />
            <div
              class="v3-slider-seg v3-slider-seg--flagship"
              :style="{
                left: (settings.ComplexThreshold / SLIDER_MAX) * 100 + '%',
                width: (1 - settings.ComplexThreshold / SLIDER_MAX) * 100 + '%',
              }"
            />
          </div>
          <div
            class="v3-slider-zone"
            :style="{ left: (settings.SimpleThreshold / SLIDER_MAX) * 50 + '%' }"
          >
            FAST
          </div>
          <div
            class="v3-slider-zone"
            :style="{
              left:
                ((settings.SimpleThreshold + settings.ComplexThreshold) / SLIDER_MAX) * 50 + '%',
            }"
          >
            BALANCED
          </div>
          <div
            class="v3-slider-zone"
            :style="{ left: ((settings.ComplexThreshold + SLIDER_MAX) / SLIDER_MAX) * 50 + '%' }"
          >
            COMPLEX
          </div>
          <div
            class="v3-slider-val"
            :style="{ left: (settings.SimpleThreshold / SLIDER_MAX) * 100 + '%' }"
          >
            {{ settings.SimpleThreshold.toLocaleString() }}
          </div>
          <div
            class="v3-slider-val"
            :style="{ left: (settings.ComplexThreshold / SLIDER_MAX) * 100 + '%' }"
          >
            {{ settings.ComplexThreshold.toLocaleString() }}
          </div>
          <NSlider
            v-model:value="rangeValue"
            range
            :min="1"
            :max="SLIDER_MAX"
            :step="100"
            class="v3-slider-overlay"
            @update:value="saveSettingsThrottled"
            @dragend="saveSettings"
          />
          <div class="v3-slider-legend" style="left: 0">0</div>
          <div class="v3-slider-legend" style="right: 0">{{ SLIDER_MAX / 1000 }}K</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* === 折叠后的智能路由状态条 === */
.v3-thresh-card--compact {
  padding: 8px 12px;
  margin-bottom: 16px;
}
.v3-thresh-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.v3-thresh-bar__label {
  font: 600 12.5px var(--v3-sans);
  color: var(--v3-ink);
  white-space: nowrap;
}
.v3-thresh-bar__summary {
  font: 500 10.5px var(--v3-mono);
  color: var(--v3-ink-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}
.v3-thresh-bar__expand {
  margin-left: auto;
  flex-shrink: 0;
}
.v3-thresh-bar__toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.v3-thresh-bar__state {
  font: 500 10.5px var(--v3-mono);
  color: var(--v3-ink-3);
  white-space: nowrap;
}
.v3-thresh-body {
  margin-top: 14px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

/* Custom Slider Styles */
.v3-custom-slider {
  position: relative;
  height: 85px;
  margin-top: 35px;
  padding: 0 10px;
}
.v3-slider-inner {
  position: relative;
  width: 100%;
  height: 100%;
}
.v3-slider-rail {
  position: absolute;
  top: 40px;
  left: 0;
  right: 0;
  height: 8px;
  background: var(--v3-surface-3);
  border-radius: 4px;
  overflow: hidden;
}
.v3-slider-seg {
  position: absolute;
  top: 0;
  height: 100%;
}
.v3-slider-seg--fast {
  background: var(--v3-ok);
}
.v3-slider-seg--balanced {
  background: var(--v3-warn);
}
.v3-slider-seg--flagship {
  background: var(--v3-danger);
}
.v3-slider-zone {
  position: absolute;
  top: 58px;
  transform: translateX(-50%);
  font: 700 9.5px var(--v3-mono);
  color: var(--v3-ink-3);
  letter-spacing: 0.08em;
  pointer-events: none;
  white-space: nowrap;
}
.v3-slider-val {
  position: absolute;
  top: 2px;
  transform: translateX(-50%);
  background: var(--v3-chrome);
  color: var(--v3-chrome-ink);
  padding: 2px 6px;
  border-radius: 4px;
  font: 700 11px var(--v3-mono);
  pointer-events: none;
  z-index: 15;
}
.v3-slider-val::after {
  content: "";
  position: absolute;
  bottom: -4px;
  left: 50%;
  transform: translateX(-50%);
  border-left: 4px solid transparent;
  border-right: 4px solid transparent;
  border-top: 4px solid var(--v3-chrome);
}
.v3-slider-overlay {
  position: absolute;
  top: 33px;
  left: 0;
  right: 0;
  z-index: 20;
}
:deep(.n-slider-rail),
:deep(.n-slider-rail__fill),
:deep(.n-slider-handle),
.v3-slider-seg,
.v3-slider-zone,
.v3-slider-val {
  transition: none !important;
}
:deep(.n-slider-rail) {
  background-color: transparent !important;
}
:deep(.n-slider-rail__fill) {
  background-color: transparent !important;
}
:deep(.n-slider-handle) {
  border: 2px solid var(--v3-chrome) !important;
  background-color: #fff !important;
  box-shadow: var(--v3-shadow-md) !important;
}
:deep(.v3-slider-overlay .n-slider-handle:nth-child(1)) {
  border-color: var(--v3-ok) !important;
}
:deep(.v3-slider-overlay .n-slider-handle:nth-child(2)) {
  border-color: var(--v3-warn) !important;
}
.v3-slider-legend {
  position: absolute;
  top: 38px;
  font: 500 9px var(--v3-mono);
  color: var(--v3-ink-4);
  pointer-events: none;
}
.v3-slider-legend:first-of-type {
  left: -6px;
}
.v3-slider-legend:last-of-type {
  right: -6px;
}
</style>
