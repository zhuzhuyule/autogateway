<script setup lang="ts">
import type { Group } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import { AddOutline, LinkOutline, SearchOutline } from "@vicons/ionicons5";
import { NIcon } from "naive-ui";
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import AggregateGroupModal from "@/components/keys/AggregateGroupModal.vue";
import GroupFormModal from "@/components/keys/GroupFormModal.vue";

const { t } = useI18n();

interface Props {
  groups: Group[];
  selectedGroup: Group | null;
  loading?: boolean;
}

interface Emits {
  (e: "select", group: Group): void;
  (e: "refresh-and-select", id: number): void;
}

const props = withDefaults(defineProps<Props>(), { loading: false });
const emit = defineEmits<Emits>();

const search = ref("");
const showCreate = ref(false);
const showAggregate = ref(false);

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase();
  return props.groups.filter(
    g =>
      !q ||
      g.name.toLowerCase().includes(q) ||
      (g.display_name || "").toLowerCase().includes(q)
  );
});
const sysGroups = computed(() => filtered.value.filter(g => g.is_system));
const userGroups = computed(() => filtered.value.filter(g => !g.is_system));

function shortFor(g: Group): string {
  const src = g.display_name || g.name || "?";
  return src.replace(/[^A-Za-z0-9]/g, "").slice(0, 2).toUpperCase() || "??";
}

function avatarClass(g: Group): string {
  if (g.channel_type === "anthropic") return "v3-pav-anthropic";
  if (g.channel_type === "gemini") return "v3-pav-google";
  if (g.is_system) return "v3-pav-default";
  const lower = g.name.toLowerCase();
  for (const key of [
    "groq",
    "cerebras",
    "openrouter",
    "together",
    "cloudflare",
    "mistral",
    "google",
    "cohere",
    "github",
    "anthropic",
  ]) {
    if (lower.includes(key)) return `v3-pav-${key}`;
  }
  return "v3-pav-default";
}

function handleCreated(g: Group) {
  showCreate.value = false;
  showAggregate.value = false;
  if (g?.id) emit("refresh-and-select", g.id);
}
</script>

<template>
  <aside class="v3-gl">
    <div class="v3-gl__head">
      <div class="v3-search">
        <n-icon :component="SearchOutline" :size="12" />
        <input
          v-model="search"
          :placeholder="t('keys.searchGroupPlaceholder') || 'Search groups…'"
        />
      </div>
    </div>

    <div class="v3-gl__body scroll">
      <template v-if="sysGroups.length">
        <div class="v3-gl__sect">System aggregates</div>
        <div
          v-for="g in sysGroups"
          :key="g.id"
          class="v3-gl__row"
          :class="{ 'v3-gl__row--active': selectedGroup?.id === g.id }"
          @click="emit('select', g)"
        >
          <span
            class="v3-pav"
            :class="avatarClass(g)"
            style="width: 28px; height: 28px; border-radius: 6px; font-size: 10px"
          >
            {{ shortFor(g) }}
          </span>
          <div style="min-width: 0">
            <div class="v3-gl__row-name">{{ getGroupDisplayName(g) }}</div>
            <div class="v3-gl__row-sub">{{ g.channel_type }} · {{ g.name }}</div>
          </div>
          <span class="v3-chip v3-chip--ok" style="font-size: 9px; padding: 1px 5px">
            sys
          </span>
        </div>
      </template>

      <div class="v3-gl__sect">Custom groups</div>
      <div
        v-for="g in userGroups"
        :key="g.id"
        class="v3-gl__row"
        :class="{ 'v3-gl__row--active': selectedGroup?.id === g.id }"
        @click="emit('select', g)"
      >
        <span
          class="v3-pav"
          :class="avatarClass(g)"
          style="width: 28px; height: 28px; border-radius: 6px; font-size: 10px"
        >
          {{ shortFor(g) }}
        </span>
        <div style="min-width: 0">
          <div class="v3-gl__row-name">{{ getGroupDisplayName(g) }}</div>
          <div class="v3-gl__row-sub">
            {{ g.group_type === "aggregate" ? "aggregate" : g.channel_type }} ·
            {{ g.name }}
          </div>
        </div>
        <span class="v3-gl__row-count tnum">{{ g.key_count ?? "" }}</span>
      </div>
      <div
        v-if="!userGroups.length && !loading"
        style="
          padding: 16px 12px;
          font-size: 11.5px;
          color: var(--v3-ink-3);
          text-align: center;
        "
      >
        {{ t("keys.noGroups") || "No custom groups yet" }}
      </div>
    </div>

    <div class="v3-gl__foot" style="display: flex; flex-direction: column; gap: 8px">
      <button
        class="v3-btn v3-btn--accent"
        style="width: 100%"
        @click="showCreate = true"
      >
        <n-icon :component="AddOutline" :size="12" />
        {{ t("keys.createGroup") || "New group" }}
      </button>
      <button class="v3-btn" style="width: 100%" @click="showAggregate = true">
        <n-icon :component="LinkOutline" :size="12" />
        {{ t("keys.createAggregateGroup") || "New aggregate" }}
      </button>
    </div>

    <group-form-modal v-model:show="showCreate" @success="handleCreated" />
    <aggregate-group-modal
      v-model:show="showAggregate"
      :groups="groups"
      @success="handleCreated"
    />
  </aside>
</template>
