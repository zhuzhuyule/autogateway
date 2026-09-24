<script setup lang="ts">
// 别名建议抽屉 —— 整块从 AliasManageTab 搬出, 职责不变:
// 把「日志里出现但没配别名的模型」和「注册表里的跨子分组家族」摊成一键采纳的 chip。
//
// 建议数据本身住在 services/aliases.ts(表头的计数徽标和这里必须读同一份);
// 这个组件只做呈现, 以及把「打开 picker 审查」和「已改动, 请刷新」两件事告诉容器。
import { useI18n } from "vue-i18n";
import { NDrawer, NDrawerContent, NIcon, NTooltip } from "naive-ui";
import { CloseOutline } from "@vicons/ionicons5";
import type { AliasSuggestion } from "@/api/aliases";
import { createAliasCandidates, useAliasData } from "@/services/aliases";
import type { PendingPick } from "@/components/aliases/types";

defineProps<{ show: boolean }>();

const emit = defineEmits<{
  (e: "update:show", v: boolean): void;
  (e: "openPicker", alias: string, seed: PendingPick[]): void;
  (e: "changed"): void;
}>();

const { t } = useI18n();
const { rows, groups, groupNameById, visibleSuggestions, dismissSuggestion } = useAliasData();

function onClickSuggestion(s: AliasSuggestion) {
  if (s.kind === "family") {
    onClickFamilySuggestion(s);
    return;
  }
  if (s.model) {
    emit("openPicker", s.model, []);
  }
}

/**
 * Family suggestion → open picker pre-populated with every (group, model)
 * pair the backend told us about. We use `existing_alias` as the target
 * name when present (== "append to existing"); otherwise the family name
 * becomes a new alias.
 */
function onClickFamilySuggestion(s: AliasSuggestion) {
  const alias = (s.existing_alias || s.family || "").trim();
  if (!alias || !s.models?.length) {
    return;
  }
  const seeds: PendingPick[] = [];
  const seen = new Set<string>();
  for (const fm of s.models) {
    const ids = fm.in_group_ids || [];
    for (const gid of ids) {
      const groupName =
        groupNameById.value[gid] || groups.value.find(g => g.id === gid)?.name || "";
      // Don't seed picks that would duplicate a row that already exists
      // for this alias+group+model — the API treats those as conflicts.
      const dup = rows.value.some(
        r => r.alias === alias && r.group_id === gid && r.real_model === fm.name
      );
      if (dup) {
        continue;
      }
      const key = `${gid}:${fm.name}`;
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);
      seeds.push({ groupId: gid, groupName, modelId: fm.name });
    }
  }
  emit("openPicker", alias, seeds);
}

// P7: family suggestion 一键采纳 -- 不开 picker, 直接批量建候选。
//
// 具体的"建行 + 补 exposed"逻辑统一在 createAliasCandidates 里 (见那里的注释),
// 这里只负责把 suggestion 的 (group, model) 摊平成 picks —— 与 picker 路径共用
// 同一实现, 保证两条路径的副作用一致。
async function quickAdoptFamilySuggestion(s: AliasSuggestion) {
  const alias = (s.existing_alias || s.family || "").trim();
  if (!alias || !s.models?.length) {
    return;
  }
  const picks: PendingPick[] = [];
  const seen = new Set<string>();
  for (const fm of s.models) {
    for (const gid of fm.in_group_ids || []) {
      const key = `${gid}:${fm.name}`;
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);
      picks.push({
        groupId: gid,
        groupName: groupNameById.value[gid] || groups.value.find(g => g.id === gid)?.name || "",
        modelId: fm.name,
      });
    }
  }
  if (!picks.length) {
    return;
  }
  await createAliasCandidates(alias, picks);
  emit("changed");
}
</script>

<template>
  <!-- 建议改成抽屉: 横幅会常驻占版面, 而建议是"有空才看"的辅助信息。 -->
  <NDrawer :show="show" :width="420" placement="right" @update:show="v => emit('update:show', v)">
    <NDrawerContent :title="t('v5.suggestionsTitle')" closable :native-scrollbar="false">
      <div v-if="!visibleSuggestions.length" class="v5-suggest-empty">
        {{ t("v5.suggestionsEmpty") }}
      </div>
      <div class="v5-suggest-banner__list">
        <template
          v-for="(s, idx) in visibleSuggestions"
          :key="s.kind === 'family' ? `f:${s.family}` : `s:${s.model}-${idx}`"
        >
          <!-- Family suggestion: one click pre-fills the picker with every
               (group, model) sibling the backend found, target alias = family
               name (or existing_alias when an alias by that name already
               exists → "append" rather than "create"). -->
          <n-tooltip v-if="s.kind === 'family'" trigger="hover" placement="top">
            <template #trigger>
              <span class="v5-suggest-chip-wrap">
                <!-- 主行: 默认行为 = 一键采纳 (跳过 picker, 直接 batch create alias).
                     admin 信任 backend 推荐时 1 click 完成. 想审查就用旁边"审查" 按钮. -->
                <button
                  class="v5-suggest-chip v5-suggest-chip--family"
                  @click="quickAdoptFamilySuggestion(s)"
                >
                  <span class="v5-suggest-chip__family">{{ s.family }}</span>
                  <span class="v5-suggest-chip__sub">
                    {{ t("v5.suggestFamilyMeta", { n: (s.models || []).length, hits: s.count }) }}
                  </span>
                  <span
                    class="v5-suggest-chip__pill"
                    :class="
                      s.existing_alias
                        ? 'v5-suggest-chip__pill--append'
                        : 'v5-suggest-chip__pill--new'
                    "
                  >
                    {{
                      s.existing_alias ? t("v5.suggestFamilyAppend") : t("v5.suggestFamilyCreate")
                    }}
                  </span>
                </button>
                <!-- 次要操作: 打开 picker 让 admin 检查/调整候选. -->
                <button
                  class="v5-suggest-chip__zap"
                  :title="t('v5.suggestFamilyReview')"
                  @click.stop="onClickSuggestion(s)"
                >
                  {{ t("v5.suggestFamilyReviewBtn") }}
                </button>
                <!-- P8.4: 单条 dismiss ✕, localStorage 持久化, 不影响其他 family. -->
                <button
                  class="v5-suggest-chip__dismiss"
                  :title="t('v5.suggestionsDismissOne')"
                  @click.stop="dismissSuggestion(s.family || '')"
                >
                  <n-icon :component="CloseOutline" :size="10" />
                </button>
              </span>
            </template>
            <div style="font: 500 11px var(--v3-sans); margin-bottom: 4px">
              {{
                s.existing_alias
                  ? t("v5.suggestFamilyTooltipAppend", { alias: s.existing_alias })
                  : t("v5.suggestFamilyTooltipCreate", { alias: s.family })
              }}
            </div>
            <div style="display: flex; flex-direction: column; gap: 2px">
              <div v-for="m in s.models || []" :key="m.name" style="font: 11px var(--v3-mono)">
                <span>{{ m.name }}</span>
                <span v-if="m.count" style="color: var(--v3-ink-4); margin-left: 4px">
                  ×{{ m.count }}
                </span>
                <span
                  v-if="(m.in_group_ids || []).length"
                  style="color: var(--v3-accent); margin-left: 4px"
                >
                  · {{ t("v5.suggestFamilyInGroups", { n: (m.in_group_ids || []).length }) }}
                </span>
                <span v-else-if="!m.from_logs" style="color: var(--v3-ink-4); margin-left: 4px">
                  ·
                </span>
              </div>
            </div>
          </n-tooltip>

          <!-- Single suggestion: legacy chip. -->
          <button
            v-else
            class="v5-suggest-chip"
            :title="s.last_seen ? `Last seen ${s.last_seen}` : ''"
            @click="onClickSuggestion(s)"
          >
            {{ s.model }}
            <span class="v5-suggest-count">×{{ s.count }}</span>
          </button>
        </template>
      </div>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
/* 建议 chip —— 从 AliasManageTab 的样式块原样搬过来。 */
.v5-suggest-banner__list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.v5-suggest-chip {
  padding: 4px 8px;
  border: 1px solid var(--v3-rule);
  border-radius: 4px;
  background: var(--v3-bg);
  cursor: pointer;
  font: 500 11px/1 var(--v3-mono);
  color: var(--v3-ink);
}
.v5-suggest-chip:hover {
  border-color: var(--v3-accent);
}
.v5-suggest-count {
  margin-left: 4px;
  color: var(--v3-ink-3);
}
.v5-suggest-chip--family {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  padding: 5px 10px;
  border-color: oklch(from var(--v3-accent) l c h / 0.4);
  background: oklch(from var(--v3-accent) l c h / 0.04);
}
.v5-suggest-chip--family:hover {
  border-color: var(--v3-accent);
  background: oklch(from var(--v3-accent) l c h / 0.08);
}
.v5-suggest-chip__family {
  font: 600 12px/1 var(--v3-mono);
  color: var(--v3-accent);
}
.v5-suggest-chip__sub {
  font: 500 10.5px/1 var(--v3-mono);
  color: var(--v3-ink-3);
}
.v5-suggest-chip__pill {
  font: 600 9.5px/1 var(--v3-sans);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 2px 5px;
  border-radius: 3px;
}
.v5-suggest-chip__pill--new {
  color: var(--v3-ok);
  background: oklch(from var(--v3-ok) l c h / 0.12);
}
.v5-suggest-chip__pill--append {
  color: var(--v3-warn);
  background: oklch(from var(--v3-warn) l c h / 0.12);
}
/* P7: family chip 容器 + 审查按钮 */
.v5-suggest-chip-wrap {
  display: inline-flex;
  align-items: stretch;
}
.v5-suggest-chip-wrap .v5-suggest-chip {
  border-top-right-radius: 0;
  border-bottom-right-radius: 0;
  border-right-width: 0;
}
.v5-suggest-chip__zap {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0 10px;
  border: 1px solid var(--v3-line);
  border-left: 1px solid var(--v3-line-soft, var(--v3-line));
  border-top-right-radius: 0;
  border-bottom-right-radius: 0;
  background: var(--v3-surface);
  color: var(--v3-ink-4);
  cursor: pointer;
  font: 500 11px var(--v3-sans);
  line-height: 1;
  transition:
    background 0.1s,
    color 0.1s;
}
.v5-suggest-chip__zap:hover {
  background: oklch(from var(--v3-accent) l c h / 0.15);
}
.v5-suggest-chip__zap:active {
  background: oklch(from var(--v3-accent) l c h / 0.28);
}
.v5-suggest-chip__dismiss {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  padding: 0;
  border: 1px solid var(--v3-line);
  border-left: none;
  border-top-right-radius: 6px;
  border-bottom-right-radius: 6px;
  background: var(--v3-surface);
  color: var(--v3-ink-4);
  cursor: pointer;
  transition:
    background 0.1s,
    color 0.1s;
}
.v5-suggest-chip__dismiss:hover {
  background: oklch(from var(--v3-warn, oklch(0.7 0.16 60)) l c h / 0.15);
  color: var(--v3-warn, oklch(0.55 0.18 60));
}
/* 建议抽屉的空态 */
.v5-suggest-empty {
  font: 400 12px var(--v3-sans);
  color: var(--v3-ink-4);
  font-style: italic;
  padding: 8px 2px;
}
</style>
