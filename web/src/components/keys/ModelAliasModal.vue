<script setup lang="ts">
import { aliasesApi, type ModelAliasRow } from "@/api/aliases";
import type { Group } from "@/types/models";
import { CloseOutline, HelpCircleOutline } from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NIcon,
  NModal,
  NSelect,
  NTooltip,
  useMessage,
  type SelectOption,
} from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  group: Group;
  modelId: string;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();

const loading = ref(false);
const allAliases = ref<ModelAliasRow[]>([]);
const allAliasesLoading = ref(false);
const selectedAliases = ref<string[]>([]);

// Defaults applied to every newly created mapping — not user-editable here.
const DEFAULT_WEIGHT = 100;
const DEFAULT_PRIORITY = 0;
const DEFAULT_ENABLED = true;

watch(
  () => props.show,
  show => {
    if (show) {
      reset();
      loadAliases();
    }
  }
);

function reset() {
  selectedAliases.value = [];
}

async function loadAliases() {
  allAliasesLoading.value = true;
  try {
    const res = await aliasesApi.list();
    allAliases.value = (res.data as unknown as ModelAliasRow[]) || [];
  } catch (e) {
    console.error("load aliases failed", e);
    allAliases.value = [];
  } finally {
    allAliasesLoading.value = false;
  }
}

const existingMappedNames = computed(() => {
  return new Set(
    allAliases.value
      .filter(a => a.group_id === props.group?.id && a.real_model === props.modelId)
      .map(a => a.alias)
  );
});

const aliasOptions = computed<SelectOption[]>(() => {
  const seen = new Set<string>();
  const opts: SelectOption[] = [];
  for (const a of allAliases.value) {
    if (seen.has(a.alias)) {
      continue;
    }
    seen.add(a.alias);
    if (existingMappedNames.value.has(a.alias)) {
      continue;
    }
    opts.push({ label: a.alias, value: a.alias });
  }
  return opts.sort((x, y) => String(x.label).localeCompare(String(y.label)));
});

function handleClose() {
  if (loading.value) {
    return;
  }
  emit("update:show", false);
}

async function handleSave() {
  if (loading.value) {
    return;
  }
  if (!selectedAliases.value.length) {
    message.warning(t("v5.maPickFirst"));
    return;
  }
  if (!props.group?.id) {
    message.error("Group missing");
    return;
  }
  loading.value = true;
  let ok = 0;
  let fail = 0;
  for (const alias of selectedAliases.value) {
    try {
      await aliasesApi.create({
        alias,
        group_id: props.group.id,
        real_model: props.modelId,
        weight: DEFAULT_WEIGHT,
        priority: DEFAULT_PRIORITY,
        enabled: DEFAULT_ENABLED,
      });
      ok += 1;
    } catch (e) {
      console.error(`create alias ${alias} failed`, e);
      fail += 1;
    }
  }
  loading.value = false;
  if (ok > 0) {
    message.success(t("v5.maCreated", { ok, fail }));
    emit("success");
    emit("update:show", false);
  } else if (fail > 0) {
    message.error(t("v5.maAllFailed"));
  }
}
</script>

<template>
  <n-modal :show="show" class="v3-modal" @update:show="handleClose">
    <n-card
      class="v3-modal-card v5-ma-card"
      :title="t('v5.maTitle')"
      :bordered="false"
      size="huge"
      role="dialog"
      aria-modal="true"
      style="max-width: 480px"
    >
      <template #header-extra>
        <n-button quaternary circle size="small" @click="handleClose">
          <template #icon><n-icon :component="CloseOutline" /></template>
        </n-button>
      </template>

      <div class="v5-ma-target">
        <div class="v5-ma-target__l">{{ t("v5.maTarget") }}</div>
        <div class="v5-ma-target__v">
          <code>{{ modelId }}</code>
          <span class="v5-ma-target__sep">·</span>
          <span class="v5-ma-target__group">{{ group?.display_name || group?.name }}</span>
        </div>
      </div>

      <div class="v5-ma-field">
        <div class="v5-ma-field__lbl">
          {{ t("v5.maAliases") }}
          <n-tooltip>
            <template #trigger>
              <span class="v5-helpicon"><n-icon :component="HelpCircleOutline" :size="11" /></span>
            </template>
            {{ t("v5.maAliasesTip") }}
          </n-tooltip>
        </div>
        <n-select
          v-model:value="selectedAliases"
          multiple
          filterable
          tag
          clearable
          :options="aliasOptions"
          :loading="allAliasesLoading"
          :placeholder="t('v5.maPlaceholder')"
        />
      </div>

      <template #footer>
        <div style="display: flex; justify-content: flex-end; gap: 8px">
          <n-button @click="handleClose">{{ t("common.cancel") }}</n-button>
          <n-button
            type="primary"
            :loading="loading"
            :disabled="!selectedAliases.length"
            @click="handleSave"
          >
            {{ t("v5.maCreate", { n: selectedAliases.length }) }}
          </n-button>
        </div>
      </template>
    </n-card>
  </n-modal>
</template>

<style scoped>
.v5-ma-card :deep(.n-card__content) {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.v5-ma-target {
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--v3-surface-2);
  border: 1px solid var(--v3-line);
  border-radius: var(--v3-radius);
  padding: 10px 12px;
}
.v5-ma-target__l {
  font: 500 11px/1 var(--v3-sans);
  color: var(--v3-ink-3);
  letter-spacing: 0.04em;
  text-transform: uppercase;
  flex-shrink: 0;
}
.v5-ma-target__v {
  display: flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
}
.v5-ma-target__v code {
  font: 500 12.5px var(--v3-mono);
  color: var(--v3-ink);
}
.v5-ma-target__sep {
  color: var(--v3-ink-4);
}
.v5-ma-target__group {
  font: 400 12px var(--v3-sans);
  color: var(--v3-ink-3);
}
.v5-ma-field__lbl {
  font: 500 12.5px/1.3 var(--v3-sans);
  color: var(--v3-ink);
  margin-bottom: 6px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
</style>
