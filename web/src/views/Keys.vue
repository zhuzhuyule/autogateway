<script setup lang="ts">
import { keysApi } from "@/api/keys";
import EncryptionMismatchAlert from "@/components/EncryptionMismatchAlert.vue";
import V3GroupDetail from "@/components/v3/V3GroupDetail.vue";
import V3GroupSidebar from "@/components/v3/V3GroupSidebar.vue";
import type { Group } from "@/types/models";
import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";

const { t } = useI18n();
const router = useRouter();
const route = useRoute();

const groups = ref<Group[]>([]);
const loading = ref(false);
const selected = ref<Group | null>(null);

onMounted(async () => {
  await loadGroups();
});

async function loadGroups() {
  loading.value = true;
  try {
    groups.value = await keysApi.getGroups();
    if (groups.value.length === 0) {
      selected.value = null;
      return;
    }
    const queryId = route.query.groupId;
    let initial: Group | undefined;
    if (queryId) {
      initial = groups.value.find(g => String(g.id) === String(queryId));
    }
    if (!initial && selected.value?.id) {
      initial = groups.value.find(g => g.id === selected.value!.id);
    }
    if (!initial) {
      initial = groups.value[0];
    }
    selectGroup(initial);
  } catch (e) {
    console.error("load groups failed", e);
    window.$message?.error(t("keys.loadGroupsFailed") || "Failed to load groups");
  } finally {
    loading.value = false;
  }
}

function selectGroup(g: Group | null) {
  selected.value = g;
  if (String(g?.id) !== String(route.query.groupId)) {
    router.replace({ name: "keys", query: { groupId: g?.id || "" } });
  }
}

async function refreshAndSelect(id?: number) {
  await loadGroups();
  if (id != null) {
    const target = groups.value.find(g => g.id === id);
    if (target) {
      selectGroup(target);
    }
  }
}

function handleSubGroupSelect(groupId: number) {
  const target = groups.value.find(g => g.id === groupId);
  if (target) {
    selectGroup(target);
  }
}
</script>

<template>
  <div class="v3-page-keys">
    <encryption-mismatch-alert style="margin-bottom: 16px" />

    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.keys") }}</div>
    </div>
    <h1 class="v3-viewtitle">
      {{ t("v3.groupsHeading") }}
      <span class="v3-viewtitle__meta">{{ t("v3.nGroups", { n: groups.length }) }}</span>
    </h1>

    <div class="v3-layout-keys">
      <v3-group-sidebar
        :groups="groups"
        :selected-group="selected"
        :loading="loading"
        @select="selectGroup"
        @refresh-and-select="refreshAndSelect"
        @refresh="loadGroups"
      />

      <v3-group-detail
        v-if="selected"
        :group="selected"
        :all-groups="groups"
        @refresh="() => refreshAndSelect()"
        @select-group="handleSubGroupSelect"
      />

      <div v-else class="v3-card" style="display: grid; place-items: center">
        <div
          style="padding: 60px 20px; text-align: center; color: var(--v3-ink-3); font-size: 13px"
        >
          {{ t("keys.selectGroupToContinue") || "Select or create a group to start." }}
        </div>
      </div>
    </div>
  </div>
</template>
