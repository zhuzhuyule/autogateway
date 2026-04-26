<script setup lang="ts">
import AppFooter from "@/components/AppFooter.vue";
import GlobalTaskProgressBar from "@/components/GlobalTaskProgressBar.vue";
import LanguageSelector from "@/components/LanguageSelector.vue";
import Logout from "@/components/Logout.vue";
import ThemeToggle from "@/components/ThemeToggle.vue";
import { getDashboardStats, getGroupList } from "@/api/dashboard";
import { themeMode } from "@/utils/theme";
import { copy as copyToClipboard } from "@/utils/clipboard";
import {
  GridOutline,
  KeyOutline,
  GitBranchOutline,
  CubeOutline,
  PulseOutline,
  SettingsOutline,
  CopyOutline,
  MenuOutline,
  CloseOutline,
  BookOutline,
} from "@vicons/ionicons5";
import { NDrawer, NDrawerContent, NIcon, useMessage } from "naive-ui";
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const message = useMessage();

const isMenuOpen = ref(false);
const customGroupCount = ref<number | null>(null);

const railItems = computed(() => [
  { name: "dashboard", icon: GridOutline, tip: t("nav.dashboard"), count: null as number | null },
  {
    name: "keys",
    icon: KeyOutline,
    tip: t("nav.keys"),
    count: customGroupCount.value,
  },
  { name: "aliases", icon: GitBranchOutline, tip: t("nav.aliases"), count: null },
  { name: "model-catalog", icon: CubeOutline, tip: t("nav.modelCatalog"), count: null },
  { name: "model-dedup", icon: CopyOutline, tip: t("nav.modelDedup"), count: null },
  { name: "logs", icon: PulseOutline, tip: t("nav.logs"), count: null },
]);

const activeName = computed(() => route.name);

function go(name: string) {
  router.push({ name });
  isMenuOpen.value = false;
}

const stats = ref<{ rpm: number; errRate: number; keys: number } | null>(null);

onMounted(async () => {
  try {
    const r = await getDashboardStats();
    stats.value = {
      rpm: r.data?.rpm?.value ?? 0,
      errRate: r.data?.error_rate?.value ?? 0,
      keys: r.data?.key_count?.value ?? 0,
    };
  } catch {
    // chrome telemetry is decorative; tolerate failure
  }
  try {
    const g = await getGroupList();
    const list =
      (g as unknown as { data: Array<{ is_system?: boolean }> }).data || [];
    customGroupCount.value = list.filter(it => !it.is_system).length;
  } catch {
    /* badge is decorative */
  }
});

const endpoint = computed(() => {
  if (typeof window === "undefined") {return "";}
  return `${window.location.protocol}//${window.location.host}`;
});

async function copyEndpoint() {
  const ok = await copyToClipboard(endpoint.value);
  if (ok) {
    message.success(t("common.copySuccess") || "Copied");
  } else {
    message.error("Copy failed");
  }
}

const versionLabel = "v0.4";
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

        <div class="v3-chrome__telemetry">
          <div class="v3-tlm">
            <span class="v3-tlm__pulse" />
            <span class="v3-tlm__lbl">Live</span>
            <span class="v3-tlm__val">{{ stats ? `${stats.keys} keys` : "—" }}</span>
          </div>
          <div class="v3-tlm">
            <span class="v3-tlm__lbl">RPM</span>
            <span class="v3-tlm__val">{{ stats ? stats.rpm.toFixed(1) : "—" }}</span>
          </div>
          <div class="v3-tlm v3-tlm--opt">
            <span class="v3-tlm__lbl">Theme</span>
            <span class="v3-tlm__val">{{ themeMode }}</span>
          </div>
          <div class="v3-tlm">
            <span class="v3-tlm__lbl">Err</span>
            <span
              class="v3-tlm__val"
              :style="{ color: stats && stats.errRate > 1 ? 'var(--v3-warn)' : undefined }"
            >
              {{ stats ? `${stats.errRate.toFixed(1)}%` : "—" }}
            </span>
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
      <div class="v3-body">
        <nav class="v3-rail">
          <button
            v-for="item in railItems"
            :key="item.name"
            class="v3-rail__btn"
            :class="{ 'v3-rail__btn--active': activeName === item.name }"
            :data-tip="item.tip"
            @click="go(item.name)"
          >
            <n-icon :component="item.icon" :size="17" />
            <span v-if="item.count != null && item.count > 0" class="v3-rail__count">
              {{ item.count > 99 ? "99+" : item.count }}
            </span>
          </button>
          <div class="v3-rail__spacer" />
          <div class="v3-rail__sep" />
          <button
            class="v3-rail__btn"
            :class="{ 'v3-rail__btn--active': activeName === 'settings' }"
            :data-tip="t('nav.settings')"
            @click="go('settings')"
          >
            <n-icon :component="SettingsOutline" :size="17" />
          </button>
        </nav>

        <main class="v3-main scroll">
          <router-view v-slot="{ Component }">
            <transition name="fade" mode="out-in">
              <component :is="Component" />
            </transition>
          </router-view>
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
