<script setup lang="ts">
import AppFooter from "@/components/AppFooter.vue";
import GlobalTaskProgressBar from "@/components/GlobalTaskProgressBar.vue";
import LanguageSelector from "@/components/LanguageSelector.vue";
import Logout from "@/components/Logout.vue";
import ThemeToggle from "@/components/ThemeToggle.vue";
import { copy as copyToClipboard } from "@/utils/clipboard";
import {
  GridOutline,
  KeyOutline,
  GitBranchOutline,
  CubeOutline,
  PulseOutline,
  GitNetworkOutline,
  SettingsOutline,
  MenuOutline,
  CloseOutline,
  BookOutline,
  ArrowForwardOutline,
  ArrowBackOutline,
} from "@vicons/ionicons5";
import { NDrawer, NDrawerContent, NIcon, useMessage } from "naive-ui";
import { computed, onMounted, ref, watch } from "vue";
import { loadFreeModelsRegistry } from "@/api/freemodels";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const message = useMessage();

const isMenuOpen = ref(false);
const isRailCollapsed = ref(localStorage.getItem("railCollapsed") === "true");

watch(isRailCollapsed, val => {
  localStorage.setItem("railCollapsed", String(val));
});

const railItems = computed(() => [
  { name: "dashboard", icon: GridOutline, tip: t("nav.dashboard") },
  {
    name: "keys",
    icon: KeyOutline,
    tip: t("nav.keys"),
  },
  { name: "aliases", icon: GitBranchOutline, tip: t("nav.aliases") },
  { name: "model-catalog", icon: CubeOutline, tip: t("nav.modelCatalog") },
  { name: "logs", icon: PulseOutline, tip: t("nav.logs") },
  { name: "sync", icon: GitNetworkOutline, tip: t("nav.sync") },
]);

const activeName = computed(() => route.name);

function go(name: string) {
  router.push({ name });
  isMenuOpen.value = false;
}

onMounted(async () => {
  // Stats and badge counts are no longer displayed in the UI
  // 进入主区域才有 authKey, 此时拉 FreeModels Registry 比 main.ts 早期调用更可靠
  // (尤其 vite HMR 后 main.ts 不重跑, 早期那次失败时 registry 永远空).
  loadFreeModelsRegistry();
});

const endpoint = computed(() => {
  if (typeof window === "undefined") {
    return "";
  }
  return `${window.location.protocol}//${window.location.host}`;
});

async function copyEndpoint() {
  const ok = await copyToClipboard(endpoint.value);
  if (ok) {
    message.success(t("common.copySuccess"));
  } else {
    message.error("Copy failed");
  }
}

// 从 vite VITE_VERSION env (build 时由 git describe 注入) 读;
// dev 模式没设就显示 "dev" 而不是错的 v0.4.
const versionLabel = (import.meta.env.VITE_VERSION as string | undefined) || "dev";
</script>

<template>
  <div class="v3-root">
    <div class="v3-app">
      <!-- ===== Top chrome ===== -->
      <header class="v3-chrome">
        <div class="v3-chrome__brand">
          <div class="v3-chrome__logo">A</div>
          <div>
            <span class="v3-chrome__name">AutoGateway</span>
            <span class="v3-chrome__ver">{{ versionLabel }}</span>
          </div>
        </div>

        <div class="v3-chrome__right">
          <button
            class="v3-chrome__endpoint"
            :title="t('common.copy') || 'Copy'"
            @click="copyEndpoint"
          >
            <span class="v3-dot" />
            {{ endpoint }}
          </button>
          <a
            href="https://github.com/zhuzhuyule/autogateway#readme"
            target="_blank"
            rel="noopener noreferrer"
            class="v3-chrome__btn"
            :title="t('v3.docs')"
          >
            <n-icon :component="BookOutline" :size="13" />
            <span class="v3-chrome__btn-label">{{ t("v3.docs") }}</span>
          </a>
          <div class="v3-chrome__tools">
            <language-selector />
            <theme-toggle />
            <logout class="v3-chrome__logout" />
          </div>
          <button class="v3-chrome__btn v3-chrome__menu" @click="isMenuOpen = true">
            <n-icon :component="MenuOutline" :size="14" />
          </button>
        </div>
      </header>

      <!-- ===== Body: rail + main ===== -->
      <div class="v3-body" :class="{ 'v3-body--collapsed': isRailCollapsed }">
        <nav class="v3-rail" :class="{ 'v3-rail--collapsed': isRailCollapsed }">
          <button
            v-for="item in railItems"
            :key="item.name"
            class="v3-rail__btn"
            :class="{ 'v3-rail__btn--active': activeName === item.name }"
            :data-tip="isRailCollapsed ? item.tip : undefined"
            @click="go(item.name)"
          >
            <n-icon :component="item.icon" :size="17" />
            <span v-if="!isRailCollapsed" class="v3-rail__label">{{ item.tip }}</span>
          </button>
          <div class="v3-rail__spacer" />
          <button
            class="v3-rail__btn v3-rail__toggle"
            :data-tip="isRailCollapsed ? t('common.expand') || 'Expand' : undefined"
            @click="isRailCollapsed = !isRailCollapsed"
          >
            <n-icon
              :component="isRailCollapsed ? ArrowForwardOutline : ArrowBackOutline"
              :size="16"
            />
            <span v-if="!isRailCollapsed" class="v3-rail__label">
              {{
                isRailCollapsed
                  ? t("common.expand") || "Expand"
                  : t("common.collapse") || "Collapse"
              }}
            </span>
          </button>
          <div class="v3-rail__sep" />
          <button
            class="v3-rail__btn"
            :class="{ 'v3-rail__btn--active': activeName === 'settings' }"
            :data-tip="isRailCollapsed ? t('nav.settings') : undefined"
            @click="go('settings')"
          >
            <n-icon :component="SettingsOutline" :size="17" />
            <span v-if="!isRailCollapsed" class="v3-rail__label">{{ t("nav.settings") }}</span>
          </button>
        </nav>

        <main class="v3-main scroll">
          <div class="v3-main-content">
            <router-view v-slot="{ Component }">
              <transition name="fade" mode="out-in">
                <component :is="Component" />
              </transition>
            </router-view>
          </div>
          <app-footer />
        </main>
      </div>
    </div>

    <!-- ===== Mobile menu drawer ===== -->
    <n-drawer v-model:show="isMenuOpen" :width="260" placement="right">
      <n-drawer-content
        body-content-style="padding: 0; display: flex; flex-direction: column; height: 100%;"
      >
        <template #header>
          <div class="v3-mobile__header">
            <span class="v3-chrome__logo">A</span>
            <span style="font-weight: 600">AutoGateway</span>
            <button class="v3-btn v3-btn--ghost v3-btn--icon" @click="isMenuOpen = false">
              <n-icon :component="CloseOutline" :size="14" />
            </button>
          </div>
        </template>
        <div class="v3-mobile__nav">
          <button
            v-for="item in railItems"
            :key="item.name"
            class="v3-mobile__row"
            :class="{ 'v3-mobile__row--active': activeName === item.name }"
            @click="go(item.name)"
          >
            <n-icon :component="item.icon" :size="16" />
            <span>{{ item.tip }}</span>
          </button>
          <button
            class="v3-mobile__row"
            :class="{ 'v3-mobile__row--active': activeName === 'settings' }"
            @click="go('settings')"
          >
            <n-icon :component="SettingsOutline" :size="16" />
            <span>{{ t("nav.settings") }}</span>
          </button>
        </div>
        <div class="v3-mobile__foot">
          <logout />
        </div>
      </n-drawer-content>
    </n-drawer>

    <global-task-progress-bar />
  </div>
</template>

<style scoped>
/* Ensure chrome buttons inherit dark text colors via tooling override */
.v3-chrome__tools {
  display: flex;
  align-items: center;
  gap: 4px;
}

.v3-chrome__tools :deep(.n-button) {
  color: var(--v3-chrome-ink-2);
}
.v3-chrome__tools :deep(.n-button:hover) {
  color: var(--v3-chrome-ink);
  background: oklch(1 0 0 / 0.08);
}

.v3-chrome__tools :deep(.language-selector-btn) {
  min-width: auto;
  padding: 0 8px;
  height: 28px;
  font-size: 11.5px;
  color: var(--v3-chrome-ink-2);
}
.v3-chrome__tools :deep(.logout-button) {
  background: transparent;
  border: 1px solid oklch(1 0 0 / 0.08);
  color: var(--v3-chrome-ink-2);
  height: 28px;
  padding: 0 10px;
  font-size: 11.5px;
}
.v3-chrome__tools :deep(.logout-button:hover) {
  color: oklch(0.78 0.18 25);
  border-color: oklch(0.78 0.18 25 / 0.4);
  background: oklch(0.78 0.18 25 / 0.1);
  transform: none;
  box-shadow: none;
}

.v3-chrome__endpoint {
  cursor: pointer;
  border: 1px solid oklch(0 0 0 / 0.35);
  font-family: var(--v3-mono);
  display: flex;
  align-items: center;
  gap: 8px;
  background: oklch(0 0 0 / 0.25);
  padding: 5px 10px;
  border-radius: 5px;
  font-size: 11.5px;
  color: oklch(0.85 0.012 250);
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.v3-chrome__endpoint:hover {
  background: oklch(0 0 0 / 0.35);
  border-color: oklch(0.62 0.16 240 / 0.6);
}

.v3-chrome__menu {
  display: none;
}

.v3-chrome__btn {
  text-decoration: none;
}

@media (max-width: 1100px) {
  .v3-chrome__btn-label {
    display: none;
  }
}

@media (max-width: 768px) {
  .v3-chrome__telemetry {
    gap: 10px;
  }
  .v3-chrome__tools {
    display: none;
  }
  .v3-chrome__endpoint {
    max-width: 140px;
  }
  .v3-chrome__menu {
    display: inline-flex;
  }
}

@media (max-width: 480px) {
  .v3-chrome__endpoint {
    display: none;
  }
}

/* Mobile drawer styling */
.v3-mobile__header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 4px 0;
}
.v3-mobile__header button {
  margin-left: auto;
}
.v3-mobile__nav {
  flex: 1;
  overflow: auto;
  padding: 8px;
}
.v3-mobile__row {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 10px 12px;
  border: 0;
  background: transparent;
  cursor: pointer;
  border-radius: 6px;
  color: var(--v3-ink-2);
  font: 500 13px var(--v3-sans);
  text-align: left;
}
.v3-mobile__row:hover {
  background: var(--v3-surface-2);
}
.v3-mobile__row--active {
  background: var(--v3-info-soft);
  color: var(--v3-accent);
}
.v3-mobile__foot {
  border-top: 1px solid var(--v3-line);
  padding: 12px 16px;
}

/* Page transitions */
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.18s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
