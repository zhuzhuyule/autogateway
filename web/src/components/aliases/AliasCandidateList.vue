<script setup lang="ts">
// 别名候选列表 —— 替代原来的"chip 云"(flex-wrap 的 chip 堆)。
//
// 为什么重做:
//   1. 原来的 chip 用 opacity + grayscale 表示"无效", 结果模型名看不清,
//      而且分不出**为什么**无效(unexposed / disabled 长得几乎一样)。
//      这里改成: 行本身始终完整可读, 无效原因用带颜色的文字标签明确写出。
//   2. 原来有效和无效交错在一堆 chip 里, 没有任何分组和汇总。
//      这里改成: 可用在前、不可用置底并加小标题, 一眼看出"几个能用"。
//   3. 原来的 chip 只显示 provider, **不显示分组** —— 同一个模型名可能来自
//      好几个分组, 看不出这条候选落在哪。这里把分组名放在模型名下方。
//   4. 原来"失效 →"按钮嵌在外层 <button> 里 —— 非法 HTML, 浏览器会拆坏
//      DOM。这里外层改成 div[role=button], 内层 button 才合法。
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { ModelAliasRow } from "@/api/aliases";
import type { CandidateRowView } from "@/components/aliases/types";
import StatePill from "@/components/aliases/StatePill.vue";
import ProviderLogo from "@/components/common/ProviderLogo.vue";
import { hasProviderLogo } from "@/data/providerLogos";
import { V3_PROVIDER_DIR, pavClass } from "@/data/v3Catalog";

const props = defineProps<{ rows: CandidateRowView[] }>();

const emit = defineEmits<{
  (e: "edit", row: ModelAliasRow): void;
  (e: "fix", row: ModelAliasRow): void;
}>();

const { t } = useI18n();

const usable = computed(() => props.rows.filter(r => r.state === "usable"));
const unusable = computed(() => props.rows.filter(r => r.state !== "usable"));

function initials(provider: string): string {
  return V3_PROVIDER_DIR[provider]?.short || provider.slice(0, 2).toUpperCase();
}

function fmtMs(ms: number): string {
  if (ms <= 0) return "";
  return ms < 1000 ? `≈ ${ms} ms` : `≈ ${(ms / 1000).toFixed(1)} s`;
}

function tip(r: CandidateRowView): string {
  if (r.state === "unexposed") return t("v3.aliasUnexposedTip", { model: r.row.real_model });
  if (r.state === "blocked") return t("v3.aliasBlockedTip");
  if (r.state === "disabled") return t("v3.aliasDisabledTip");
  return r.row.real_model;
}
</script>

<template>
  <div class="v3-alist">
    <div
      v-for="r in usable"
      :key="r.row.id"
      class="v3-arow"
      role="button"
      tabindex="0"
      :title="r.row.real_model"
      @click="emit('edit', r.row)"
      @keydown.enter.prevent="emit('edit', r.row)"
      @keydown.space.prevent="emit('edit', r.row)"
    >
      <StatePill state="usable" variant="dot" />
      <ProviderLogo
        v-if="hasProviderLogo(r.logoHint)"
        :hint="r.logoHint"
        :size="15"
        class="v3-arow__logo"
      />
      <span v-else :class="pavClass(r.provider)" class="v3-arow__logo v3-arow__logo--txt">
        {{ initials(r.provider) }}
      </span>
      <span class="v3-arow__text">
        <span class="v3-arow__name">{{ r.row.real_model }}</span>
        <span class="v3-arow__sub">
          {{ r.groupName }}
          <template v-if="r.avgMs > 0"> · {{ fmtMs(r.avgMs) }}</template>
          <template v-if="r.cost"> · {{ r.cost }}</template>
        </span>
      </span>
      <span class="v3-arow__share">{{ r.share }}%</span>
    </div>

    <template v-if="unusable.length">
      <div class="v3-alist__h">{{ t("v3.aliasUnusableN", { n: unusable.length }) }}</div>
      <div
        v-for="r in unusable"
        :key="r.row.id"
        class="v3-arow v3-arow--dead"
        role="button"
        tabindex="0"
        :title="tip(r)"
        @click="emit('edit', r.row)"
        @keydown.enter.prevent="emit('edit', r.row)"
        @keydown.space.prevent="emit('edit', r.row)"
      >
        <StatePill :state="r.state" variant="dot" />
        <ProviderLogo
          v-if="hasProviderLogo(r.logoHint)"
          :hint="r.logoHint"
          :size="15"
          class="v3-arow__logo"
        />
        <span v-else :class="pavClass(r.provider)" class="v3-arow__logo v3-arow__logo--txt">
          {{ initials(r.provider) }}
        </span>
        <span class="v3-arow__text">
          <span class="v3-arow__name">{{ r.row.real_model }}</span>
          <span class="v3-arow__sub">{{ r.groupName }}</span>
        </span>
        <StatePill :state="r.state" />
        <button
          v-if="r.state === 'unexposed'"
          type="button"
          class="v3-arow__fix"
          :title="t('v3.aliasUnexposedFixTip')"
          @click.stop="emit('fix', r.row)"
        >
          {{ t("v3.aliasFix") }}
        </button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.v3-alist {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.v3-alist__h {
  font: 600 9.5px var(--v3-mono);
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--v3-ink-4);
  padding: 7px 2px 3px;
}

.v3-arow {
  display: grid;
  grid-template-columns: 7px 15px minmax(0, 1fr) auto;
  align-items: center;
  gap: 7px;
  padding: 5px 7px;
  border: 1px solid transparent;
  border-radius: 5px;
  cursor: pointer;
  transition:
    background 100ms,
    border-color 100ms;
}

.v3-arow__share {
  font: 600 10px/1 var(--v3-mono);
  color: var(--v3-ink-3);
  background: var(--v3-surface-3);
  padding: 2px 5px;
  border-radius: 3px;
  white-space: nowrap;
}
.v3-arow:hover {
  background: var(--v3-surface-2);
  border-color: var(--v3-line);
}
.v3-arow:focus-visible {
  outline: 2px solid var(--v3-accent);
  outline-offset: -1px;
}

/* 无效行只做"弱化", 不用 opacity 压暗 —— 模型名和原因都必须保持可读。 */
.v3-arow--dead {
  grid-template-columns: 7px 15px minmax(0, 1fr) auto auto;
  background: var(--v3-surface-2);
}
.v3-arow--dead:hover {
  background: var(--v3-surface-3);
}

.v3-arow__logo {
  width: 15px;
  height: 15px;
  border-radius: 3px;
  flex-shrink: 0;
}
.v3-arow__logo--txt {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font: 700 7px/1 var(--v3-mono);
  color: white;
}

.v3-arow__text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.v3-arow__name {
  font: 500 11.5px/1.3 var(--v3-mono);
  color: var(--v3-ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.v3-arow__sub {
  font: 400 10px/1.2 var(--v3-mono);
  color: var(--v3-ink-4);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.v3-arow__fix {
  font: 600 9px/1 var(--v3-mono);
  padding: 3px 6px;
  border-radius: 3px;
  border: 1px solid var(--v3-warn);
  background: transparent;
  color: oklch(0.45 0.13 65);
  cursor: pointer;
  white-space: nowrap;
  transition:
    background 100ms,
    color 100ms;
}
.v3-arow__fix:hover {
  background: var(--v3-warn);
  color: white;
}
</style>
