<script setup lang="ts">
// 别名「管理」页容器 —— 只做数据装配和"开哪个抽屉/弹窗"这一层状态。
//
// 为什么这么薄: 之前这个文件 2400 行, 把加载、派生、四种视图、编辑弹窗、
// picker、建议横幅全塞在一起 —— 改任何一处都要在两千行里找上下文, 而且四份
// 视图各画一遍同一行数据, 口径必然漂移。现在: 加载与派生在 services/aliases.ts,
// 渲染在 AliasList / AutoRoutingPanel / AliasDetailDrawer / AliasPickerModal /
// AliasSuggestDrawer, 这里只剩下"谁打开谁"。
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { NIcon, NInput, NModal, NTooltip, useMessage } from "naive-ui";
import {
  AddOutline,
  AlbumsOutline,
  BulbOutline,
  HelpCircleOutline,
  RefreshOutline,
} from "@vicons/ionicons5";
import AliasAutoRoutingPanel from "./AutoRoutingPanel.vue";
import AliasList from "./AliasList.vue";
import AliasDetailDrawer from "./AliasDetailDrawer.vue";
import AliasPickerModal from "./AliasPickerModal.vue";
import AliasSuggestDrawer from "./AliasSuggestDrawer.vue";
import AliasQuickSetupTab from "./AliasQuickSetupTab.vue";
import { useAliasData } from "@/services/aliases";
import { copy } from "@/utils/clipboard";
import type { PendingPick } from "@/components/aliases/types";

// 兼容老链接 ?tab=quick —— 由视图层传进来, 决定要不要一进来就打开「按家族整理」。
const props = withDefaults(defineProps<{ initialFamilyOpen?: boolean }>(), {
  initialFamilyOpen: false,
});

const { t } = useI18n();
const message = useMessage();
const route = useRoute();
const router = useRouter();

const {
  loading,
  groups,
  aliasViews,
  totalCandidates,
  trafficByKey,
  trafficAvailable,
  suggestionCount,
  groupNameById,
  modelsByGroup,
  refresh,
  reload,
} = useAliasData();

const detailOpen = ref(false);
const detailAlias = ref("");
const pickerOpen = ref(false);
const pickerAlias = ref("");
const pickerSeed = ref<PendingPick[] | null>(null);
const suggestOpen = ref(false);
const familyModalOpen = ref(false);
const newAliasOpen = ref(false);
const newAliasName = ref("");
const highlightAlias = ref<string | null>(null);

function openDetail(alias: string): void {
  detailAlias.value = alias;
  detailOpen.value = true;
}

function openPicker(alias: string, seed?: PendingPick[]): void {
  pickerAlias.value = alias;
  pickerSeed.value = seed || null;
  pickerOpen.value = true;
}

const detailSummary = computed(
  () => aliasViews.value.find(a => a.alias === detailAlias.value) || null
);
const detailRows = computed(() => detailSummary.value?.rows.map(r => r.row) || []);

async function copyAlias(alias: string): Promise<void> {
  await copy(alias);
  message.success(t("v3.aliasCopied", { alias }));
}

function commitNewAlias(): void {
  const name = newAliasName.value.trim();
  if (!name) {
    message.warning(t("v5.alNameRequired"));
    return;
  }
  newAliasOpen.value = false;
  newAliasName.value = "";
  openPicker(name);
}

// 高亮一下"我刚建的那个", 1.5s 后撤掉 —— 列表按调用数排序, 新别名会沉底,
// 没有视觉锚点就找不到。
function flash(alias: string): void {
  highlightAlias.value = alias;
  setTimeout(() => {
    highlightAlias.value = null;
  }, 1500);
}

// picker 里建成 → 提示建了几条, 并把列表刷新到新状态。
function onCreated(ok: number, fail: number): void {
  // ok 与 fail 都是 0 表示选中的候选全都已存在 —— 弹「成功 0 · 失败 0」是噪音。
  if (ok || fail) {
    message.success(t("v5.maCreated", { ok, fail }));
  }
  if (ok) {
    flash(pickerAlias.value);
  }
  refresh();
}

// ?highlight= 有两个来源: 别处跳进来(挂载时), 以及「按家族整理」弹窗里创建完
// 就地 router.push(挂载后)。所以必须 watch, 只在 onMounted 读会漏掉后者。
watch(
  () => route.query.highlight,
  raw => {
    const alias = typeof raw === "string" ? raw : "";
    if (!alias) {
      return;
    }
    // 弹窗里刚建的别名不在已加载的行里, 不重拉一下就没法高亮。
    reload();
    flash(alias);
    setTimeout(() => {
      const { highlight: _drop, ...rest } = route.query;
      router.replace({ query: rest });
    }, 1500);
  },
  { immediate: true }
);

onMounted(() => {
  // 一次性读 props: 它来自 ?tab=quick, 挂载后不会再变。
  familyModalOpen.value = props.initialFamilyOpen;
  return refresh();
});
</script>

<template>
  <div class="v3-page-aliases">
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.aliases") }}</div>
      <div class="v3-viewhead__actions">
        <button
          v-if="suggestionCount"
          class="v3-btn"
          :title="t('v5.suggestionsTitle')"
          @click="suggestOpen = true"
        >
          <n-icon :component="BulbOutline" :size="12" />
          {{ t("v5.suggestionsTitle") }}
          <span class="v5-suggest-banner__count">{{ suggestionCount }}</span>
        </button>
        <button class="v3-btn" @click="familyModalOpen = true">
          <n-icon :component="AlbumsOutline" :size="12" />
          {{ t("aliases.browseFamily") }}
        </button>
        <button class="v3-btn" @click="refresh">
          <n-icon :component="RefreshOutline" :size="12" />
          {{ t("v3.refresh") }}
        </button>
        <button class="v3-btn v3-btn--accent" @click="newAliasOpen = true">
          <n-icon :component="AddOutline" :size="12" />
          {{ t("v5.alNewAlias") }}
        </button>
      </div>
    </div>

    <h1 class="v3-viewtitle">
      {{ t("v3.aliasesTitle") }}
      <n-tooltip trigger="hover">
        <template #trigger>
          <n-icon
            :component="HelpCircleOutline"
            :size="15"
            style="margin-left: 6px; cursor: help; color: var(--v3-ink-3)"
          />
        </template>
        {{ t("v3.aliasesDesc") }}
      </n-tooltip>
      <!-- 「N 个别名 / M 条候选」而不是「M 条映射」: 主词是用户概念里的对象。 -->
      <span class="v3-viewtitle__meta">
        {{
          t("aliases.list.titleMeta", { aliases: aliasViews.length, candidates: totalCandidates })
        }}
      </span>
    </h1>

    <AliasAutoRoutingPanel />

    <AliasList
      :aliases="aliasViews"
      :groups="groups"
      :loading="loading"
      :show-measured="trafficAvailable"
      :highlight="highlightAlias"
      @open="openDetail"
      @add="alias => openPicker(alias)"
      @copy="copyAlias"
    />

    <AliasDetailDrawer
      v-model:show="detailOpen"
      :alias="detailAlias"
      :rows="detailRows"
      :groups="groups"
      :group-name-by-id="groupNameById"
      :models-by-group="modelsByGroup"
      :traffic="trafficByKey"
      :show-measured="trafficAvailable"
      :summary="detailSummary"
      :is-reserved="!!detailSummary?.tier"
      @saved="reload"
      @exposed="reload"
    />

    <AliasPickerModal
      v-model:show="pickerOpen"
      :alias="pickerAlias"
      :seed="pickerSeed"
      @created="onCreated"
    />

    <AliasSuggestDrawer
      v-model:show="suggestOpen"
      @open-picker="(alias, seed) => openPicker(alias, seed)"
      @changed="refresh"
    />

    <n-modal v-model:show="newAliasOpen" preset="dialog" :title="t('v5.alNewAlias')">
      <div class="alz-new">
        <n-input
          v-model:value="newAliasName"
          :placeholder="t('v3.aliasNamePlaceholder')"
          @keyup.enter="commitNewAlias"
        />
        <div class="alz-new__actions">
          <button class="v3-btn" @click="newAliasOpen = false">{{ t("common.cancel") }}</button>
          <button class="v3-btn v3-btn--accent" @click="commitNewAlias">
            {{ t("common.save") }}
          </button>
        </div>
      </div>
    </n-modal>

    <n-modal
      v-model:show="familyModalOpen"
      preset="card"
      :title="t('aliases.browseFamily')"
      style="width: 1100px; max-width: 92vw"
    >
      <AliasQuickSetupTab />
    </n-modal>
  </div>
</template>

<style scoped>
/* 建议 badge: 抽屉自带样式, 但入口按钮在本容器里。 */
.v5-suggest-banner__count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 18px;
  height: 16px;
  padding: 0 5px;
  border-radius: 8px;
  background: var(--v3-accent-soft, oklch(0.96 0.05 230));
  color: var(--v3-accent, oklch(0.55 0.15 230));
  font: 600 10px var(--v3-mono);
  margin-left: 2px;
}
.alz-new {
  padding-top: 16px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.alz-new__actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}
</style>
