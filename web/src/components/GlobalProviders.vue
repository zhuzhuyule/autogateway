<script setup lang="ts">
import { appState } from "@/utils/app-state";
import { actualTheme } from "@/utils/theme";
import { getLocale } from "@/locales";
import {
  darkTheme,
  NConfigProvider,
  NDialogProvider,
  NLoadingBarProvider,
  NMessageProvider,
  useLoadingBar,
  useMessage,
  type GlobalTheme,
  type GlobalThemeOverrides,
  zhCN,
  enUS,
  jaJP,
  dateZhCN,
  dateEnUS,
  dateJaJP,
} from "naive-ui";
import { computed, defineComponent, watch } from "vue";

// 自定义主题配置 - 根据主题动态调整
/**
 * v3 Mission Console palette — concrete sRGB equivalents of the oklch tokens
 * declared in @/assets/design-v3.css. Naive UI's theme engine needs literal
 * color values; keep these in sync if the design tokens move.
 */
const V3_LIGHT = {
  bg: "#f5f7fb",
  surface: "#ffffff",
  surface2: "#f3f5f9",
  surface3: "#e9ecf2",
  line: "#e2e6ee",
  lineStrong: "#c8cfdb",
  ink: "#1f2937",
  ink2: "#4b5563",
  ink3: "#6b7280",
  ink4: "#94a3b8",
  accent: "#4263eb",
  accentHover: "#3b56cf",
  accentPressed: "#314aa8",
  ok: "#16a34a",
  warn: "#d97706",
  danger: "#dc2626",
  info: "#4263eb",
};
const V3_DARK = {
  bg: "#0f141d",
  surface: "#1c2230",
  surface2: "#171c27",
  surface3: "#232a3a",
  line: "#2c3447",
  lineStrong: "#3a445a",
  ink: "#f1f5f9",
  ink2: "#cbd5e1",
  ink3: "#94a3b8",
  ink4: "#64748b",
  accent: "#6c8aff",
  accentHover: "#5b7bff",
  accentPressed: "#4566e6",
  ok: "#22c55e",
  warn: "#f59e0b",
  danger: "#ef4444",
  info: "#6c8aff",
};

const V3_FONT_SANS =
  "'Geist', 'Geist Sans', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif";
const V3_FONT_MONO =
  "'Geist Mono', 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace";

function buildOverrides(p: typeof V3_LIGHT): GlobalThemeOverrides {
  return {
    common: {
      primaryColor: p.accent,
      primaryColorHover: p.accentHover,
      primaryColorPressed: p.accentPressed,
      primaryColorSuppl: p.accentHover,
      successColor: p.ok,
      successColorHover: p.ok,
      successColorPressed: p.ok,
      warningColor: p.warn,
      warningColorHover: p.warn,
      warningColorPressed: p.warn,
      errorColor: p.danger,
      errorColorHover: p.danger,
      errorColorPressed: p.danger,
      infoColor: p.info,
      infoColorHover: p.info,
      infoColorPressed: p.info,
      bodyColor: p.bg,
      cardColor: p.surface,
      modalColor: p.surface,
      popoverColor: p.surface,
      tableColor: p.surface,
      tableHeaderColor: p.surface2,
      inputColor: p.surface,
      inputColorDisabled: p.surface2,
      actionColor: p.surface2,
      hoverColor: p.surface2,
      textColorBase: p.ink,
      textColor1: p.ink,
      textColor2: p.ink2,
      textColor3: p.ink3,
      placeholderColor: p.ink4,
      iconColor: p.ink3,
      borderColor: p.line,
      dividerColor: p.line,
      borderRadius: "6px",
      borderRadiusSmall: "5px",
      fontFamily: V3_FONT_SANS,
      fontFamilyMono: V3_FONT_MONO,
      fontSize: "13px",
    },
    Card: {
      color: p.surface,
      colorModal: p.surface,
      textColor: p.ink,
      titleTextColor: p.ink,
      borderColor: p.line,
      paddingMedium: "16px",
      paddingLarge: "20px",
      paddingSmall: "12px",
    },
    Modal: {
      color: p.surface,
      textColor: p.ink,
    },
    Dialog: {
      color: p.surface,
      textColor: p.ink,
      titleTextColor: p.ink,
      iconColorInfo: p.info,
      iconColorSuccess: p.ok,
      iconColorWarning: p.warn,
      iconColorError: p.danger,
    },
    Button: {
      fontWeight: "500",
      heightMedium: "30px",
      heightSmall: "26px",
      heightLarge: "36px",
      heightTiny: "22px",
      borderRadiusMedium: "5px",
      borderRadiusSmall: "5px",
      borderRadiusLarge: "6px",
    },
    Input: {
      color: p.surface,
      colorDisabled: p.surface2,
      colorFocus: p.surface,
      textColor: p.ink,
      placeholderColor: p.ink4,
      border: `1px solid ${p.line}`,
      borderHover: `1px solid ${p.accent}`,
      borderFocus: `1px solid ${p.accent}`,
      heightMedium: "30px",
      heightSmall: "26px",
      heightLarge: "36px",
      borderRadius: "5px",
    },
    Select: {
      peers: {
        InternalSelection: {
          color: p.surface,
          colorActive: p.surface,
          textColor: p.ink,
          placeholderColor: p.ink4,
          border: `1px solid ${p.line}`,
          borderHover: `1px solid ${p.accent}`,
          borderActive: `1px solid ${p.accent}`,
          borderFocus: `1px solid ${p.accent}`,
          borderRadius: "5px",
          heightMedium: "30px",
          heightSmall: "26px",
        },
        InternalSelectMenu: {
          color: p.surface,
          optionTextColor: p.ink,
          optionTextColorActive: p.accent,
          optionColorPending: p.surface2,
          optionColorActive: "transparent",
          borderRadius: "6px",
        },
      },
    },
    InputNumber: {
      iconColor: p.ink3,
    },
    Menu: {
      itemHeight: "32px",
      borderRadius: "5px",
      itemColorActive: p.surface3,
      itemColorActiveHover: p.surface3,
      itemTextColorActive: p.accent,
      itemTextColorActiveHover: p.accent,
      itemIconColorActive: p.accent,
      itemIconColorActiveHover: p.accent,
    },
    DataTable: {
      tdColor: p.surface,
      tdColorHover: p.surface2,
      tdColorStriped: p.surface2,
      thColor: p.surface2,
      thTextColor: p.ink3,
      tdTextColor: p.ink,
      borderColor: p.line,
      borderRadius: "6px",
      thFontWeight: "500",
      thPaddingMedium: "10px 14px",
      tdPaddingMedium: "10px 14px",
    },
    Pagination: {
      itemTextColor: p.ink2,
      itemTextColorActive: "#ffffff",
      itemColor: p.surface,
      itemColorHover: p.surface2,
      itemColorActive: p.accent,
      itemColorActiveHover: p.accentHover,
      itemBorder: `1px solid ${p.line}`,
      itemBorderActive: `1px solid ${p.accent}`,
      itemBorderHover: `1px solid ${p.lineStrong}`,
      itemBorderRadius: "5px",
    },
    Tag: {
      borderRadius: "4px",
      color: p.surface2,
      textColor: p.ink2,
      border: `1px solid ${p.line}`,
    },
    Switch: {
      railColorActive: p.accent,
    },
    Drawer: {
      color: p.surface,
      textColor: p.ink,
      titleTextColor: p.ink,
    },
    Form: {
      labelTextColor: p.ink2,
      labelFontWeightTop: "500",
    },
    Divider: {
      color: p.line,
    },
    Tabs: {
      tabTextColorActiveBar: p.accent,
      tabTextColorHoverBar: p.accent,
      tabTextColorBar: p.ink2,
      tabTextColorBar_underline: p.ink2,
      barColor: p.accent,
    },
    Notification: {
      color: p.surface,
      textColor: p.ink,
      titleTextColor: p.ink,
      borderRadius: "6px",
    },
    Message: {
      color: p.surface,
      textColor: p.ink,
      colorInfo: p.surface,
      colorSuccess: p.surface,
      colorWarning: p.surface,
      colorError: p.surface,
      borderRadius: "6px",
    },
    LoadingBar: {
      colorLoading: p.accent,
      colorError: p.danger,
      height: "2px",
    },
    Tooltip: {
      color: p.ink,
      textColor: p.surface,
      borderRadius: "5px",
    },
    Popover: {
      color: p.surface,
      textColor: p.ink,
      borderRadius: "6px",
    },
    Alert: {
      borderRadius: "6px",
    },
  };
}

const themeOverrides = computed<GlobalThemeOverrides>(() =>
  buildOverrides(actualTheme.value === "dark" ? V3_DARK : V3_LIGHT)
);

// 根据当前主题动态返回主题对象
const theme = computed<GlobalTheme | undefined>(() => {
  return actualTheme.value === "dark" ? darkTheme : undefined;
});

// 根据当前语言返回对应的 locale 配置
const locale = computed(() => {
  const currentLocale = getLocale();
  switch (currentLocale) {
    case "zh-CN":
      return zhCN;
    case "en-US":
      return enUS;
    case "ja-JP":
      return jaJP;
    default:
      return zhCN;
  }
});

// 根据当前语言返回对应的日期 locale 配置
const dateLocale = computed(() => {
  const currentLocale = getLocale();
  switch (currentLocale) {
    case "zh-CN":
      return dateZhCN;
    case "en-US":
      return dateEnUS;
    case "ja-JP":
      return dateJaJP;
    default:
      return dateZhCN;
  }
});

function useGlobalMessage() {
  window.$message = useMessage();
}

const LoadingBar = defineComponent({
  setup() {
    const loadingBar = useLoadingBar();
    watch(
      () => appState.loading,
      loading => {
        if (loading) {
          loadingBar.start();
        } else {
          loadingBar.finish();
        }
      }
    );
    return () => null;
  },
});

const Message = defineComponent({
  setup() {
    useGlobalMessage();
    return () => null;
  },
});
</script>

<template>
  <n-config-provider
    :theme="theme"
    :theme-overrides="themeOverrides"
    :locale="locale"
    :date-locale="dateLocale"
  >
    <n-loading-bar-provider>
      <n-message-provider placement="top-right">
        <n-dialog-provider>
          <slot />
          <loading-bar />
          <message />
        </n-dialog-provider>
      </n-message-provider>
    </n-loading-bar-provider>
  </n-config-provider>
</template>
