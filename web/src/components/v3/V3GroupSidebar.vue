<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { Group } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import { AddOutline, LinkOutline, LockClosedOutline, SearchOutline } from "@vicons/ionicons5";
import { NIcon } from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import AggregateGroupModal from "@/components/keys/AggregateGroupModal.vue";
import V3NewGroupFlow from "@/components/v3/V3NewGroupFlow.vue";
import ProviderLogo from "@/components/common/ProviderLogo.vue";
import { hasProviderLogo } from "@/data/providerLogos";

const { t } = useI18n();

interface Props {
  groups: Group[];
  selectedGroup: Group | null;
  loading?: boolean;
}

interface Emits {
  (e: "select", group: Group): void;
  (e: "refresh-and-select", id: number): void;
  (e: "refresh"): void;
}

const props = withDefaults(defineProps<Props>(), { loading: false });
const emit = defineEmits<Emits>();

const search = ref("");
const showCreate = ref(false);
const showAggregate = ref(false);

// Local copy so drag reordering can update optimistically without
// waiting for parent to refresh.
const localOrder = ref<Group[]>([]);
watch(
  () => props.groups,
  groups => {
    localOrder.value = groups.map(g => ({ ...g }));
  },
  { immediate: true, deep: true }
);

const draggingId = ref<number | null>(null);
const dropTarget = ref<{ id: number; pos: "before" | "after" } | null>(null);
const savingOrder = ref(false);
const itemRefs = new Map<number, HTMLElement>();

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase();
  return localOrder.value.filter(
    g => !q || g.name.toLowerCase().includes(q) || (g.display_name || "").toLowerCase().includes(q)
  );
});
const sysGroups = computed(() => {
  const arr = filtered.value.filter(g => g.is_system);
  return arr.sort((a, b) => {
    const aOpen = a.name.toLowerCase().includes("openai") ? 0 : 1;
    const bOpen = b.name.toLowerCase().includes("openai") ? 0 : 1;
    return aOpen - bOpen;
  });
});
const userGroups = computed(() => filtered.value.filter(g => !g.is_system));
const hasSearch = computed(() => search.value.trim().length > 0);
const canDrag = computed(() => !hasSearch.value && !savingOrder.value);

function shortFor(g: Group): string {
  const src = g.display_name || g.name || "?";
  return (
    src
      .replace(/[^A-Za-z0-9]/g, "")
      .slice(0, 2)
      .toUpperCase() || "??"
  );
}

function avatarClass(g: Group): string {
  if (g.channel_type === "anthropic") {
    return "v3-pav-anthropic";
  }
  if (g.channel_type === "gemini") {
    return "v3-pav-google";
  }
  if (g.is_system) {
    return "v3-pav-default";
  }
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
    if (lower.includes(key)) {
      return `v3-pav-${key}`;
    }
  }
  return "v3-pav-default";
}

// Favicon support for sidebar avatars (consistent with Dashboard + Group detail)
const FAVICON_DOMAIN_MAP: Record<string, string> = {
  groq: "groq.com",
  cerebras: "cerebras.ai",
  openrouter: "openrouter.ai",
  together: "together.ai",
  cloudflare: "cloudflare.com",
  mistral: "mistral.ai",
  google: "ai.google.dev",
  cohere: "cohere.com",
  github: "github.com",
  anthropic: "anthropic.com",
  "default-openai": "openai.com",
  "default-anthropic": "anthropic.com",
  "default-gemini": "gemini.google.com",
};

function extractHost(url?: string): string | null {
  if (!url) {
    return null;
  }
  try {
    return new URL(url).hostname;
  } catch {
    return null;
  }
}

function faviconFor(g: Group): string {
  const role = (g.system_role || "").trim();
  if (role && FAVICON_DOMAIN_MAP[role]) {
    return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(FAVICON_DOMAIN_MAP[role])}&sz=64`;
  }
  const lower = g.name.toLowerCase();
  for (const k of Object.keys(FAVICON_DOMAIN_MAP)) {
    if (lower.includes(k)) {
      return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(FAVICON_DOMAIN_MAP[k])}&sz=64`;
    }
  }
  const host = extractHost(g.upstreams?.[0]?.url);
  if (host) {
    return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(host)}&sz=64`;
  }
  return "";
}

// providerHint 把 group 解析成 ProviderLogo 能识别的字符串(system_role 优先,
// 否则回退到 name + 第一个 upstream host)。
function providerHint(g: Group): string {
  return [
    g.system_role || "",
    g.name || "",
    extractHost(g.upstreams?.[0]?.url) || "",
  ]
    .filter(Boolean)
    .join(" ");
}

const faviconErr = reactive<Record<string, boolean>>({});
function onFaviconErr(g: Group) {
  if (g.id != null) {
    faviconErr[String(g.id)] = true;
  }
}
function isFaviconBroken(g: Group): boolean {
  return g.id != null && faviconErr[String(g.id)] === true;
}

function subTextFor(g: Group): string {
  if (g.group_type === "aggregate") {
    return t("v3.aggregate") || "aggregate";
  }
  return g.channel_type;
}

function handleCreated(g: Group) {
  showCreate.value = false;
  showAggregate.value = false;
  if (g?.id) {
    emit("refresh-and-select", g.id);
  }
}

function setItemRef(el: Element | null, id?: number) {
  if (id == null) {
    return;
  }
  if (el instanceof HTMLElement) {
    itemRefs.set(id, el);
  } else {
    itemRefs.delete(id);
  }
}

function onDragStart(ev: DragEvent, g: Group) {
  if (!canDrag.value || g.is_system || g.id == null) {
    ev.preventDefault();
    return;
  }
  draggingId.value = g.id;
  dropTarget.value = null;
  if (ev.dataTransfer) {
    ev.dataTransfer.effectAllowed = "move";
    ev.dataTransfer.setData("text/plain", String(g.id));
  }
}

function resolvePos(ev: DragEvent, id: number): "before" | "after" {
  const el = itemRefs.get(id);
  if (!el) {
    return "after";
  }
  const rect = el.getBoundingClientRect();
  return ev.clientY < rect.top + rect.height / 2 ? "before" : "after";
}

function onDragOver(ev: DragEvent, g: Group) {
  if (!canDrag.value || draggingId.value == null || g.is_system || g.id == null) {
    return;
  }
  ev.preventDefault();
  if (ev.dataTransfer) {
    ev.dataTransfer.dropEffect = "move";
  }
  const pos = resolvePos(ev, g.id);
  if (!dropTarget.value || dropTarget.value.id !== g.id || dropTarget.value.pos !== pos) {
    dropTarget.value = { id: g.id, pos };
  }
}

async function onDrop(ev: DragEvent, target: Group) {
  ev.preventDefault();
  const sourceId = draggingId.value;
  const dt = dropTarget.value;
  draggingId.value = null;
  dropTarget.value = null;
  if (
    !canDrag.value ||
    sourceId == null ||
    target.id == null ||
    target.is_system ||
    !dt ||
    sourceId === target.id
  ) {
    return;
  }

  const previous = localOrder.value.map(g => ({ ...g }));
  const srcIdx = localOrder.value.findIndex(g => g.id === sourceId);
  const tgtIdx = localOrder.value.findIndex(g => g.id === target.id);
  if (srcIdx < 0 || tgtIdx < 0) {
    return;
  }
  const arr = [...localOrder.value];
  const [moved] = arr.splice(srcIdx, 1);
  let insert = tgtIdx;
  if (srcIdx < tgtIdx) {
    insert -= 1;
  }
  if (dt.pos === "after") {
    insert += 1;
  }
  if (insert < 0) {
    insert = 0;
  }
  if (insert > arr.length) {
    insert = arr.length;
  }
  if (insert === srcIdx) {
    return;
  }
  arr.splice(insert, 0, moved);
  localOrder.value = arr;

  // persist
  const items = arr
    .filter(g => g.id != null)
    .map((g, i) => ({ id: g.id as number, sort: (i + 1) * 10 }));
  savingOrder.value = true;
  try {
    await keysApi.reorderGroups(items);
    window.$message?.success(t("keys.dragSortSaved") || "Order saved");
    emit("refresh");
  } catch {
    localOrder.value = previous;
    window.$message?.error(t("keys.dragSortSaveFailed") || "Save order failed");
  } finally {
    savingOrder.value = false;
  }
}

function onDragEnd() {
  draggingId.value = null;
  dropTarget.value = null;
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
        <div class="v3-gl__sect">{{ t("v5.sidebarSysSect") }}</div>
        <div
          v-for="g in sysGroups"
          :key="g.id"
          class="v3-gl__row"
          :class="{ 'v3-gl__row--active': selectedGroup?.id === g.id }"
          @click="emit('select', g)"
        >
          <span class="v5-picon" style="width: 38px; height: 38px">
            <ProviderLogo
              v-if="hasProviderLogo(providerHint(g))"
              :hint="providerHint(g)"
              :size="28"
            />
            <img
              v-else-if="faviconFor(g) && !isFaviconBroken(g)"
              :src="faviconFor(g)"
              alt=""
              draggable="false"
              @error="onFaviconErr(g)"
            />
            <span
              v-else
              :class="['v3-pav', avatarClass(g)]"
              style="width: 100%; height: 100%; border-radius: 0; font-size: 12px"
            >
              {{ shortFor(g) }}
            </span>
          </span>
          <div style="min-width: 0">
            <div class="v3-gl__row-name">
              {{ getGroupDisplayName(g) }}
              <span v-if="g.group_type === 'aggregate'" class="v3-gl__row-tag">
                {{ t("v5.aggregateChip") }}
              </span>
            </div>
            <div class="v3-gl__row-sub">{{ subTextFor(g) }}</div>
          </div>
          <n-icon :component="LockClosedOutline" :size="12" style="color: var(--v3-ink-3)" />
        </div>
      </template>

      <div class="v3-gl__sect">{{ t("v5.sidebarCustomSect") }}</div>
      <div
        v-for="g in userGroups"
        :key="g.id"
        :ref="el => setItemRef(el as Element | null, g.id)"
        class="v3-gl__row"
        :class="{
          'v3-gl__row--active': selectedGroup?.id === g.id,
          'v3-gl__row--dragging': draggingId === g.id,
          'v3-gl__row--drop-before':
            dropTarget?.id === g.id && dropTarget?.pos === 'before' && draggingId !== g.id,
          'v3-gl__row--drop-after':
            dropTarget?.id === g.id && dropTarget?.pos === 'after' && draggingId !== g.id,
        }"
        :draggable="canDrag"
        @click="emit('select', g)"
        @dragstart="onDragStart($event, g)"
        @dragover="onDragOver($event, g)"
        @drop="onDrop($event, g)"
        @dragend="onDragEnd"
      >
        <span class="v5-picon" style="width: 38px; height: 38px">
          <ProviderLogo
            v-if="hasProviderLogo(providerHint(g))"
            :hint="providerHint(g)"
            :size="28"
          />
          <img
            v-else-if="faviconFor(g) && !isFaviconBroken(g)"
            :src="faviconFor(g)"
            alt=""
            draggable="false"
            @error="onFaviconErr(g)"
          />
          <span
            v-else
            :class="['v3-pav', avatarClass(g)]"
            style="width: 100%; height: 100%; border-radius: 0; font-size: 10px"
          >
            {{ shortFor(g) }}
          </span>
        </span>
        <div style="min-width: 0">
          <div class="v3-gl__row-name">
            {{ getGroupDisplayName(g) }}
            <span v-if="g.group_type === 'aggregate'" class="v3-gl__row-tag">
              {{ t("v5.aggregateChip") }}
            </span>
          </div>
          <div class="v3-gl__row-sub">{{ subTextFor(g) }}</div>
        </div>
        <span v-if="g.key_count != null" class="v3-gl__row-count tnum">
          {{ g.key_count }}
        </span>
      </div>
      <div
        v-if="!userGroups.length && !loading"
        style="padding: 16px 12px; font-size: 11.5px; color: var(--v3-ink-3); text-align: center"
      >
        {{ t("keys.noGroups") || "No custom groups yet" }}
      </div>
    </div>

    <div class="v3-gl__foot" style="display: flex; flex-direction: column; gap: 8px">
      <button class="v3-btn v3-btn--accent" style="width: 100%" @click="showCreate = true">
        <n-icon :component="AddOutline" :size="12" />
        {{ t("keys.createGroup") || "New group" }}
      </button>
      <button class="v3-btn" style="width: 100%" @click="showAggregate = true">
        <n-icon :component="LinkOutline" :size="12" />
        {{ t("keys.createAggregateGroup") || "New aggregate" }}
      </button>
    </div>

    <v3-new-group-flow
      v-model:show="showCreate"
      :existing-group-names="groups.map(g => g.name)"
      @success="handleCreated"
    />
    <aggregate-group-modal v-model:show="showAggregate" :groups="groups" @success="handleCreated" />
  </aside>
</template>

<style scoped>
.v3-gl__row {
  position: relative;
  cursor: pointer;
}
.v3-gl__row-name {
  display: flex;
  align-items: center;
  gap: 6px;
}
.v3-gl__row-tag {
  font: 700 9px var(--v3-mono);
  background: var(--v3-accent-soft);
  color: var(--v3-accent);
  padding: 1px 4px;
  border-radius: 3px;
  text-transform: uppercase;
  letter-spacing: 0.02em;
  line-height: 1;
}
.v3-gl__row--dragging {
  opacity: 0.45;
}
.v3-gl__row--drop-before::before,
.v3-gl__row--drop-after::after {
  content: "";
  position: absolute;
  left: 8px;
  right: 8px;
  height: 2px;
  border-radius: 2px;
  background: var(--v3-accent);
  pointer-events: none;
}
.v3-gl__row--drop-before::before {
  top: -1px;
}
.v3-gl__row--drop-after::after {
  bottom: -1px;
}
</style>
