<script setup lang="ts">
// 健康视图 —— 排障用。
//
// 卡片视图按"别名"组织, 适合看某个别名里有什么; 但排障时要回答的是反过来的
// 问题: "**哪些候选现在不生效, 为什么**"。所以这里按**问题类型**分组, 把所有
// 失效候选集中列出, 每条给出对应动作。全部正常的别名只留一行摘要, 不占版面。
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { AliasView, CandidateRowView, CandidateState } from "@/components/aliases/types";
import { STATE_LABEL_KEY, STATE_TRIAGE_ORDER } from "@/components/aliases/types";
import type { ModelAliasRow } from "@/api/aliases";
import StatePill from "@/components/aliases/StatePill.vue";

const props = defineProps<{ aliases: AliasView[]; loading: boolean }>();
const emit = defineEmits<{
  (e: "edit", alias: string): void;
  (e: "fix", row: ModelAliasRow): void;
  (e: "toggle", row: ModelAliasRow): void;
  (e: "copy", alias: string): void;
  (e: "add", alias: string): void;
}>();

const { t } = useI18n();

interface ProblemRow {
  alias: string;
  r: CandidateRowView;
}

const stats = computed(() => {
  let candidates = 0;
  let usable = 0;
  for (const a of props.aliases) {
    candidates += a.total;
    usable += a.usable;
  }
  return {
    aliases: props.aliases.length,
    candidates,
    usable,
    unusable: candidates - usable,
  };
});

// 按问题类型分组的失效候选。顺序取自 STATE_TRIAGE_ORDER(最该先处理的在前)。
const problems = computed<{ state: CandidateState; items: ProblemRow[] }[]>(() => {
  return STATE_TRIAGE_ORDER.map(state => {
    const items: ProblemRow[] = [];
    for (const a of props.aliases) {
      for (const r of a.rows) {
        if (r.state === state) {
          items.push({ alias: a.alias, r });
        }
      }
    }
    return { state, items };
  }).filter(g => g.items.length > 0);
});

/** 一个候选都没有的别名 —— 它被调用时会直接 404, 属于"静默坏了"。 */
const empties = computed(() => props.aliases.filter(a => a.total === 0));

const healthy = computed(() => props.aliases.filter(a => a.total > 0 && a.unusable === 0));

const allGood = computed(
  () => !problems.value.length && !empties.value.length && props.aliases.length > 0
);

/** 每种问题给一句"这是什么、怎么办", 避免用户只看到一个标签不知所以。 */
function hintKey(state: CandidateState): string {
  return `aliases.health.hint_${state}`;
}
</script>

<template>
  <div class="ahv">
    <div class="ahv__stats">
      <div class="ahv__stat">
        <span class="ahv__stat-v">{{ stats.aliases }}</span>
        <span class="ahv__stat-k">{{ t("aliases.health.statAliases") }}</span>
      </div>
      <div class="ahv__stat">
        <span class="ahv__stat-v">{{ stats.candidates }}</span>
        <span class="ahv__stat-k">{{ t("aliases.health.statCandidates") }}</span>
      </div>
      <div class="ahv__stat ahv__stat--ok">
        <span class="ahv__stat-v">{{ stats.usable }}</span>
        <span class="ahv__stat-k">{{ t("aliases.health.statUsable") }}</span>
      </div>
      <div class="ahv__stat" :class="{ 'ahv__stat--bad': stats.unusable > 0 }">
        <span class="ahv__stat-v">{{ stats.unusable }}</span>
        <span class="ahv__stat-k">{{ t("aliases.health.statUnusable") }}</span>
      </div>
    </div>

    <div v-if="allGood" class="ahv__allgood">
      {{ t("aliases.health.allGood", { n: stats.aliases }) }}
    </div>

    <section v-for="g in problems" :key="g.state" class="ahv__sec">
      <header class="ahv__sec-head">
        <StatePill :state="g.state" variant="dot" />
        <span class="ahv__sec-title">{{ t(STATE_LABEL_KEY[g.state]) }}</span>
        <span class="ahv__sec-n">{{ g.items.length }}</span>
        <span class="ahv__sec-hint">{{ t(hintKey(g.state)) }}</span>
      </header>
      <div class="ahv__rows">
        <div v-for="p in g.items" :key="p.r.row.id" class="ahv__row">
          <button class="ahv__alias" @click="emit('edit', p.alias)">{{ p.alias }}</button>
          <span class="ahv__sep">›</span>
          <span class="ahv__group">{{ p.r.groupName }}</span>
          <span class="ahv__sep">›</span>
          <span class="ahv__model">{{ p.r.row.real_model }}</span>
          <span class="ahv__spacer" />
          <button
            v-if="g.state === 'unexposed'"
            class="ahv__act ahv__act--fix"
            :title="t('v3.aliasUnexposedFixTip')"
            @click="emit('fix', p.r.row)"
          >
            {{ t("v3.aliasFix") }}
          </button>
          <!-- disabled 能就地启用; blocked 不行 —— 它是分组的黑名单挡的, 启停
               这个候选完全无效。那种情况给"打开", 让用户去别名编辑里处理
               (改分组 / 移除该候选), 而不是给一个假装能修的按钮。 -->
          <button
            v-else-if="g.state === 'disabled'"
            class="ahv__act"
            @click="emit('toggle', p.r.row)"
          >
            {{ t("aliases.health.enable") }}
          </button>
          <button v-else class="ahv__act" @click="emit('edit', p.alias)">
            {{ t("aliases.health.open") }}
          </button>
        </div>
      </div>
    </section>

    <section v-if="empties.length" class="ahv__sec">
      <header class="ahv__sec-head">
        <span class="ahv__dot ahv__dot--empty" />
        <span class="ahv__sec-title">{{ t("aliases.health.emptyTitle") }}</span>
        <span class="ahv__sec-n">{{ empties.length }}</span>
        <span class="ahv__sec-hint">{{ t("aliases.health.hint_empty") }}</span>
      </header>
      <div class="ahv__rows">
        <div v-for="a in empties" :key="a.alias" class="ahv__row">
          <button class="ahv__alias" @click="emit('edit', a.alias)">{{ a.alias }}</button>
          <span class="ahv__spacer" />
          <button class="ahv__act" @click="emit('add', a.alias)">
            {{ t("aliases.split.addCandidate") }}
          </button>
        </div>
      </div>
    </section>

    <section v-if="healthy.length" class="ahv__sec ahv__sec--calm">
      <header class="ahv__sec-head">
        <span class="ahv__dot ahv__dot--ok" />
        <span class="ahv__sec-title">{{ t("aliases.health.healthyTitle") }}</span>
        <span class="ahv__sec-n">{{ healthy.length }}</span>
      </header>
      <div class="ahv__chips">
        <button
          v-for="a in healthy"
          :key="a.alias"
          class="ahv__chip"
          @click="emit('copy', a.alias)"
        >
          {{ a.alias }}
          <span class="ahv__chip-n">{{ a.usable }}/{{ a.total }}</span>
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ahv {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.ahv__stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
}
.ahv__stat {
  border: 1px solid var(--v3-line);
  border-radius: 8px;
  padding: 12px 14px;
  background: var(--v3-surface);
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.ahv__stat--ok {
  border-color: oklch(from var(--v3-ok) l c h / 0.4);
}
.ahv__stat--bad {
  border-color: oklch(from var(--v3-warn) l c h / 0.5);
  background: oklch(from var(--v3-warn) l c h / 0.05);
}
.ahv__stat-v {
  font: 700 20px var(--v3-mono);
  color: var(--v3-ink);
}
.ahv__stat-k {
  font: 500 10px var(--v3-mono);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--v3-ink-4);
}
.ahv__allgood {
  border: 1px solid oklch(from var(--v3-ok) l c h / 0.4);
  background: var(--v3-ok-soft);
  color: oklch(0.38 0.12 145);
  border-radius: 8px;
  padding: 14px 16px;
  font: 500 12.5px var(--v3-sans);
}

.ahv__sec {
  border: 1px solid var(--v3-line);
  border-radius: 8px;
  overflow: hidden;
  background: var(--v3-surface);
}
.ahv__sec--calm {
  opacity: 0.9;
}
.ahv__sec-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 12px;
  background: var(--v3-surface-2);
  border-bottom: 1px solid var(--v3-line);
}
.ahv__sec-title {
  font: 600 12px var(--v3-sans);
  color: var(--v3-ink);
}
.ahv__sec-n {
  font: 600 10px var(--v3-mono);
  padding: 1px 5px;
  border-radius: 3px;
  background: var(--v3-surface-3);
  color: var(--v3-ink-3);
}
.ahv__sec-hint {
  font: 400 10.5px var(--v3-sans);
  color: var(--v3-ink-4);
}
.ahv__dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--v3-ink-4);
  flex-shrink: 0;
}
.ahv__dot--empty {
  background: var(--v3-danger);
}
.ahv__dot--ok {
  background: var(--v3-ok);
}

.ahv__rows {
  display: flex;
  flex-direction: column;
  padding: 5px;
  gap: 2px;
}
.ahv__row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 9px;
  border-radius: 5px;
}
.ahv__row:hover {
  background: var(--v3-surface-2);
}
.ahv__alias {
  font: 600 11px var(--v3-mono);
  color: var(--v3-accent);
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
}
.ahv__alias:hover {
  text-decoration: underline;
}
.ahv__sep {
  color: var(--v3-ink-4);
  font-size: 11px;
}
.ahv__group {
  font: 400 11px var(--v3-mono);
  color: var(--v3-ink-3);
}
.ahv__model {
  font: 500 11px var(--v3-mono);
  color: var(--v3-ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ahv__spacer {
  flex: 1;
}
.ahv__act {
  font: 600 10px var(--v3-mono);
  border: 1px solid var(--v3-line);
  background: transparent;
  color: var(--v3-ink-3);
  border-radius: 3px;
  padding: 2px 7px;
  cursor: pointer;
  white-space: nowrap;
}
.ahv__act:hover {
  border-color: var(--v3-accent);
  color: var(--v3-accent);
}
.ahv__act--fix {
  border-color: var(--v3-warn);
  color: oklch(0.45 0.13 65);
}
.ahv__act--fix:hover {
  background: var(--v3-warn);
  color: white;
  border-color: var(--v3-warn);
}

.ahv__chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 10px;
}
.ahv__chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font: 600 11px var(--v3-mono);
  color: var(--v3-ink);
  border: 1px solid var(--v3-line);
  background: var(--v3-surface-2);
  border-radius: 4px;
  padding: 4px 8px;
  cursor: pointer;
}
.ahv__chip:hover {
  border-color: var(--v3-accent);
  color: var(--v3-accent);
}
.ahv__chip-n {
  color: var(--v3-ink-4);
  font-weight: 500;
}
</style>
