<script setup lang="ts">
import { type MenuOption, NIcon } from "naive-ui";
import { computed, h, watch } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
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
              { size: 16, class: "nav-item-icon" },
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
  <div>
    <n-menu
      :mode="mode"
      :options="menuOptions"
      :value="activeMenu"
      :indent="18"
      class="compact-menu"
    />
  </div>
</template>

<style scoped>
.compact-menu :deep(.n-menu-item-content) {
  padding: 0 10px !important;
  height: 34px;
  margin: 2px 3px;
}

.compact-menu :deep(.nav-menu-item) {
  display: flex;
  align-items: center;
  gap: 6px;
  text-decoration: none;
  color: inherit;
  font-size: 12px;
  transition: all 0.2s ease;
}

.compact-menu :deep(.n-menu-item) {
  border-radius: 6px;
}

.compact-menu :deep(.n-menu--vertical .n-menu-item-content) {
  justify-content: flex-start;
  padding: 0 16px !important;
}

.compact-menu :deep(.n-menu-item:hover) {
  background: rgba(102, 126, 234, 0.1);
}

.compact-menu :deep(.n-menu-item--selected) {
  background: var(--primary-gradient);
  color: white;
}

.compact-menu :deep(.n-menu-item--selected:hover) {
  background: linear-gradient(135deg, #5a6fd8 0%, #6a4190 100%);
}

.compact-menu :deep(.n-menu-item-content-header) {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--text-color-3);
  padding: 8px 16px 4px;
}
</style>
