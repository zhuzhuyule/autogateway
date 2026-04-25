<script setup lang="ts">
import AppFooter from "@/components/AppFooter.vue";
import GlobalTaskProgressBar from "@/components/GlobalTaskProgressBar.vue";
import LanguageSelector from "@/components/LanguageSelector.vue";
import Logout from "@/components/Logout.vue";
import NavBar from "@/components/NavBar.vue";
import ThemeToggle from "@/components/ThemeToggle.vue";
import { useMediaQuery } from "@vueuse/core";
import { ref, watch } from "vue";

const isMenuOpen = ref(false);
const isMobile = useMediaQuery("(max-width: 768px)");

watch(isMobile, value => {
  if (!value) {
    isMenuOpen.value = false;
  }
});

const toggleMenu = () => {
  isMenuOpen.value = !isMenuOpen.value;
};
</script>

<template>
  <n-layout class="main-layout">
    <n-layout-header class="layout-header">
      <div class="header-content">
        <div class="header-brand">
          <div class="brand-icon">
            <img src="@/assets/logo.png" alt="" />
          </div>
          <h1 class="brand-title">GPT Load</h1>
        </div>

        <nav class="header-nav">
          <nav-bar />
        </nav>

        <div class="header-actions">
          <language-selector />
          <theme-toggle />
          <logout v-if="!isMobile" />
          <n-button text @click="toggleMenu" class="menu-toggle">
            <svg viewBox="0 0 24 24" width="20" height="20">
              <path fill="currentColor" d="M3,6H21V8H3V6M3,11H21V13H3V11M3,16H21V18H3V16Z" />
            </svg>
          </n-button>
        </div>
      </div>
    </n-layout-header>

    <n-drawer v-model:show="isMenuOpen" :width="280" placement="right">
      <n-drawer-content
        title="GPT Load"
        body-content-style="padding: 0; display: flex; flex-direction: column; height: 100%;"
      >
        <div style="flex: 1; overflow-y: auto">
          <nav-bar mode="vertical" @close="isMenuOpen = false" />
        </div>
        <div class="mobile-actions">
          <logout />
        </div>
      </n-drawer-content>
    </n-drawer>

    <n-layout-content class="layout-content">
      <div class="content-wrapper">
        <router-view v-slot="{ Component }">
          <transition name="fade" mode="out-in">
            <component :is="Component" />
          </transition>
        </router-view>
      </div>
    </n-layout-content>
    <app-footer />
  </n-layout>

  <!-- 全局任务进度条 -->
  <global-task-progress-bar />
</template>

<style scoped>
.main-layout {
  background: var(--bg-primary);
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}

.layout-header {
  background: var(--header-bg);
  border-bottom: 1px solid var(--border-color);
  position: sticky;
  top: 0;
  z-index: 100;
  padding: 0 24px;
}

.header-content {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 56px;
  max-width: 1400px;
  margin: 0 auto;
  gap: 16px;
}

.header-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}

.brand-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
}

.brand-icon img {
  height: 100%;
  width: 100%;
}

.brand-title {
  font-size: 1.1rem;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
  letter-spacing: -0.3px;
}

.header-nav {
  flex: 1;
  display: flex;
  justify-content: center;
  min-width: 0;
  overflow: hidden;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
}

.menu-toggle {
  padding: 8px;
  border-radius: var(--border-radius-sm);
  transition: background 0.15s;
}

.menu-toggle:hover {
  background: var(--hover-bg);
}

.mobile-actions {
  padding: 16px;
  border-top: 1px solid var(--border-color);
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 12px;
  margin-top: auto;
}

.layout-content {
  flex: 1;
  overflow: auto;
  background: var(--bg-secondary);
}

.content-wrapper {
  padding: 24px;
  max-width: 1400px;
  margin: 0 auto;
  min-height: calc(100vh - 56px);
}

.layout-footer {
  background: transparent;
  padding: 0;
}

/* Tablet */
@media (max-width: 1024px) {
  .header-brand .brand-title {
    display: none;
  }
}

/* Mobile */
@media (max-width: 768px) {
  .header-nav {
    display: none;
  }

  .header-content {
    height: 52px;
    padding: 0 16px;
  }

  .content-wrapper {
    padding: 16px;
  }
}
</style>
