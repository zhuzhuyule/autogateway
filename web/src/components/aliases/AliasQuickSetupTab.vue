<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { NInput, NSpin, useMessage } from "naive-ui";
import { useI18n } from "vue-i18n";
import { dedupApi, type DedupFamily, type DedupModelEntry } from "@/api/dedup";
import FamilyAccordion from "./quick/FamilyAccordion.vue";

const { t } = useI18n();
const message = useMessage();

const loading = ref(false);
const families = ref<DedupFamily[]>([]);
const searchQuery = ref("");

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
          <div style="padding: 16px; color: var(--v3-ink-4); font-size: 12px">
            (右栏 — Task 7)
          </div>
        </div>
      </div>
    </NSpin>
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
