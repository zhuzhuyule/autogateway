<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter, useRoute } from "vue-router";
import { NInput, NModal, NSpin, useMessage } from "naive-ui";
import { useI18n } from "vue-i18n";
import { dedupApi, type DedupFamily, type DedupModelEntry } from "@/api/dedup";
import FamilyAccordion from "./quick/FamilyAccordion.vue";
import ExistingAliasesPanel from "./quick/ExistingAliasesPanel.vue";
import SubmitActionBar from "./quick/SubmitActionBar.vue";

const { t } = useI18n();
const message = useMessage();
const router = useRouter();
const route = useRoute();

const loading = ref(false);
const families = ref<DedupFamily[]>([]);
const searchQuery = ref("");
const submitting = ref(false);
const failureModalOpen = ref(false);
const failures = ref<string[]>([]);

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

async function onSubmit({ alias, entries }: { alias: string; entries: DedupModelEntry[] }) {
  submitting.value = true;
  try {
    const res = await dedupApi.create({
      alias,
      candidates: entries.map(e => ({ group_id: e.group_id, real_model: e.real_model })),
    });
    if (res.created > 0 && res.failures.length === 0) {
      message.success(t("aliases.quick.createdN", { n: res.created }));
      router.replace({
        query: { ...route.query, tab: "manage", highlight: alias },
      });
    } else if (res.created > 0) {
      message.warning(
        t("aliases.quick.partialFailures", { ok: res.created, fail: res.failures.length }),
      );
      failures.value = res.failures;
      failureModalOpen.value = true;
    } else {
      message.error(t("common.requestFailed", { status: "" }));
      failures.value = res.failures;
      failureModalOpen.value = true;
    }
    await load();
    selected.value = new Map();
    targetAlias.value = null;
  } catch {
    message.error(t("common.requestFailed", { status: "" }));
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

    <NModal v-model:show="failureModalOpen" preset="dialog" :title="t('aliases.quick.failureModalTitle')">
      <ul style="padding-left: 20px; margin: 0; max-height: 260px; overflow-y: auto">
        <li v-for="(f, i) in failures" :key="i" style="font: 500 12px var(--v3-mono); margin: 4px 0">
          {{ f }}
        </li>
      </ul>
    </NModal>
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
