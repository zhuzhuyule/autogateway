<script setup lang="ts">
import { type MenuOption, NIcon } from "naive-ui";
import { computed, h, watch } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import { useMediaQuery } from "@vueuse/core";
import {
  Home,
  Key,
  DocumentText,
  GitBranch,
  Folder,
  Sync,
  Settings,
} from "@vicons/ionicons5";

const { t } = useI18n();

const props = defineProps({
  mode: {
    type: String,
    default: "horizontal",
  },
});

const emit = defineEmits(["close"]);

const isTablet = useMediaQuery("(max-width: 1024px)");

const showLabels = computed(() => !isTablet.value);

const menuOptions = computed<MenuOption[]>(() => {
  const options: MenuOption[] = [
    renderMenuItem("dashboard", t("nav.dashboard"), Home),
    renderMenuItem("keys", t("nav.keys"), Key),
    renderMenuItem("logs", t("nav.logs"), DocumentText),
    renderMenuItem("auto-routing", t("nav.autoRouting"), GitBranch),
    renderMenuItem("model-catalog", t("nav.modelCatalog"), Folder),
    renderMenuItem("model-dedup", t("nav.modelDedup"), Sync),
    renderMenuItem("settings", t("nav.settings"), Settings),
  ];

  return options;
});

const route = useRoute();
const activeMenu = computed(() => route.name);

watch(activeMenu, () => {
  if (props.mode === "vertical") {
    emit("close");
  }
});

function renderMenuItem(key: string, label: string, icon: any): MenuOption {
  return {
    label: () =>
      h(
        RouterLink,
        {
          to: {
            name: key,
          },
          class: "nav-menu-item",
        },
        {
          default: () => [
            h(
              NIcon,
              { size: 18, class: "nav-item-icon" },
              () => h(icon)
            ),
            h("span", { class: "nav-item-text" }, label),
          ],
        }
      ),
    key,
  };
}
</script>

<template>
  <div :class="['nav-container', { 'nav-icon-only': !showLabels }]">
    <n-menu
      :mode="mode"
      :options="menuOptions"
      :value="activeMenu"
      :indent="showLabels ? 16 : 0"
      :collapsed="!showLabels"
      :collapsed-width="44"
      :collapsed-icon-size="18"
      class="adaptive-menu"
    />
  </div>
</template>

<style scoped>
.nav-container {
  display: flex;
  align-items: center;
}

.adaptive-menu :deep(.n-menu-item-content) {
  padding: 0 6px !important;
  height: 32px;
  margin: 2px;
  border-radius: var(--border-radius-sm);
}

.adaptive-menu :deep(.nav-menu-item) {
  display: flex;
  align-items: center;
  gap: 8px;
  text-decoration: none;
  color: var(--text-secondary);
  font-size: 13px;
  font-weight: 500;
  transition: color 0.15s;
}

.adaptive-menu :deep(.n-menu-item:hover) {
  background: var(--hover-bg);
}

.adaptive-menu :deep(.n-menu-item--selected) {
  background: var(--primary-color);
  color: white;
}

.adaptive-menu :deep(.n-menu-item--selected:hover) {
  background: var(--primary-color-hover);
}

.adaptive-menu :deep(.n-menu-item--selected .nav-menu-item) {
  color: white;
}

/* Icon-only mode */
.nav-icon-only.adaptive-menu :deep(.n-menu-item-content) {
  padding: 0 !important;
  justify-content: center;
}

.nav-icon-only.adaptive-menu :deep(.nav-menu-item) {
  justify-content: center;
  padding: 6px;
}

.nav-icon-only.adaptive-menu :deep(.nav-item-text) {
  display: none;
}

/* Vertical mode */
.adaptive-menu :deep(.n-menu--vertical .n-menu-item-content) {
  padding: 0 12px !important;
  justify-content: flex-start;
}
</style>
