<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter, useRoute } from "vue-router";
import { NInput, NSpin, useMessage } from "naive-ui";
import { useI18n } from "vue-i18n";
import { dedupApi, type DedupFamily, type DedupModelEntry, type DedupCreateError } from "@/api/dedup";
import FamilyAccordion from "./quick/FamilyAccordion.vue";
import ExistingAliasesPanel from "./quick/ExistingAliasesPanel.vue";
import SubmitActionBar from "./quick/SubmitActionBar.vue";

const { t, te } = useI18n();
const message = useMessage();
const router = useRouter();
const route = useRoute();

const loading = ref(false);
const families = ref<DedupFamily[]>([]);
const searchQuery = ref("");
const submitting = ref(false);

const selected = ref(new Map<string, DedupModelEntry>());
function entryKey(e: DedupModelEntry): string {
  return `${e.group_id}::${e.real_model}`;
}
function onToggle(entry: DedupModelEntry) {
  const key = entryKey(entry);
  if (selected.value.has(key)) {
    selected.value.delete(key);
  } else {
    selected.value.set(key, entry);
  }
  selected.value = new Map(selected.value);
}

// Family-level bulk-select: add or remove a batch of entries in one shot.
// Idempotent — adding an already-selected entry is a no-op, removing an
// unselected one too.
function onBulkToggle({ entries, mode }: { entries: DedupModelEntry[]; mode: "add" | "remove" }) {
  const next = new Map(selected.value);
  for (const e of entries) {
    const key = entryKey(e);
    if (mode === "add") next.set(key, e);
    else next.delete(key);
  }
  selected.value = next;
}

const targetAlias = ref<string | null>(null);

async function load() {
  loading.value = true;
  try {
    families.value = await dedupApi.models();
  } catch {
    message.error(t("aliases.quick.loadFailed"));
  } finally {
    loading.value = false;
  }
}

function onClickAlias(alias: string) {
  targetAlias.value = targetAlias.value === alias ? null : alias;
}

// Translate stable backend error codes to i18n strings. Falls back to the
// server-supplied message when the code is unknown — avoids leaking raw
// "pq: duplicate key violates constraint ..." text.
function translateError(err: unknown): string {
  const data = (err as { response?: { data?: DedupCreateError } } | undefined)?.response?.data;
  if (data?.code) {
    const key = `aliases.quick.errors.${data.code}`;
    if (te(key)) return t(key);
    if (data.message) return data.message;
  }
  return t("aliases.quick.errors.UNKNOWN");
}

async function onSubmit({ alias, entries }: { alias: string; entries: DedupModelEntry[] }) {
  submitting.value = true;
  try {
    const res = await dedupApi.create({
      alias,
      candidates: entries.map(e => ({ group_id: e.group_id, real_model: e.real_model })),
    });
    message.success(t("aliases.quick.createdN", { n: res.created }));
    selected.value = new Map();
    targetAlias.value = null;
    router.replace({
      query: { ...route.query, tab: "manage", highlight: alias },
    });
  } catch (err) {
    message.error(translateError(err), { duration: 5000, closable: true });
    await load();
  } finally {
    submitting.value = false;
  }
}

onMounted(load);

const selectionCount = computed(() => selected.value.size);
</script>

<template>
  <div class="qsetup">
    <div class="qsetup__top">
      <NInput
        v-model:value="searchQuery"
        :placeholder="t('aliases.quick.searchPlaceholder')"
        clearable
        size="medium"
      />
      <span class="qsetup__count">
        {{ selectionCount === 0
          ? t("aliases.quick.selectionEmpty")
          : t("aliases.quick.selectionCount", { n: selectionCount }) }}
      </span>
    </div>

    <NSpin :show="loading">
      <div class="qsetup__body">
        <div class="qsetup__main">
          <FamilyAccordion
            :families="families"
            :search-query="searchQuery"
            :selected="selected"
            :highlight-alias="targetAlias"
            @toggle="onToggle"
            @bulk-toggle="onBulkToggle"
            @click-alias="onClickAlias"
          />
        </div>
        <div class="qsetup__side">
          <ExistingAliasesPanel
            :families="families"
            :target-alias="targetAlias"
            @select="(a) => (targetAlias = a)"
          />
        </div>
      </div>
    </NSpin>

    <SubmitActionBar
      :selected="selected"
      :target-alias="targetAlias"
      :submitting="submitting"
      @submit="onSubmit"
    />
  </div>
</template>

<style scoped>
.qsetup {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px 0;
}
.qsetup__top {
  display: flex;
  align-items: center;
  gap: 12px;
}
.qsetup__count {
  font: 500 11.5px var(--v3-mono);
  color: var(--v3-ink-3);
  white-space: nowrap;
}
.qsetup__body {
  display: grid;
  grid-template-columns: 1fr 280px;
  gap: 16px;
  min-height: 400px;
}
.qsetup__main {
  min-width: 0;
}
.qsetup__side {
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  background: var(--v3-surface);
  align-self: flex-start;
}
</style>
