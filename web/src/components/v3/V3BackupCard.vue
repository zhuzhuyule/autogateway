<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import {
  NAlert,
  NButton,
  NCard,
  NDescriptions,
  NDescriptionsItem,
  NInput,
  NRadio,
  NRadioGroup,
  NSpace,
  NUpload,
  useMessage,
  type UploadFileInfo,
} from "naive-ui";
import { backupApi, type PreviewReport, type Strategy } from "@/api/backup";

const msg = useMessage();
const { t } = useI18n();

// ---------- Export ----------
const exportPwd = ref("");
const exporting = ref(false);

async function doExport() {
  if (!exportPwd.value || exportPwd.value.length < 8) {
    msg.warning(t("settings.backup.msgPasswordShort"));
    return;
  }
  exporting.value = true;
  try {
    const blob = await backupApi.exportBackup(exportPwd.value);
    const ts = new Date().toISOString().replace(/[:.]/g, "-");
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `api-center-backup-${ts}.acb`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(a.href);
    msg.success(t("settings.backup.msgExportSuccess"));
  } catch (e: unknown) {
    msg.error(t("settings.backup.msgExportFailed", { detail: formatErr(e) }));
  } finally {
    exporting.value = false;
  }
}

function generateRandomPassword() {
  const buf = new Uint8Array(24);
  crypto.getRandomValues(buf);
  // base64url-ish, strip non-alphanumeric, cap at 32
  exportPwd.value = btoa(String.fromCharCode(...buf))
    .replace(/[+/=]/g, "")
    .slice(0, 32);
}

// ---------- Import ----------
const importFile = ref<File | null>(null);
const importPwd = ref("");
const preview = ref<PreviewReport | null>(null);
const previewing = ref(false);
const strategy = ref<Strategy>("merge");
const deleteConfirm = ref(""); // for replace
const importing = ref(false);

function onFileChange(opts: { fileList: UploadFileInfo[] }) {
  importFile.value = (opts.fileList[0]?.file as File) ?? null;
  preview.value = null;
  deleteConfirm.value = "";
}

async function doPreview() {
  if (!importFile.value || !importPwd.value) {
    msg.warning(t("settings.backup.msgPickFile"));
    return;
  }
  previewing.value = true;
  try {
    preview.value = await backupApi.previewBackup(
      importFile.value,
      importPwd.value,
    );
  } catch (e: unknown) {
    msg.error(t("settings.backup.msgPreviewFailed", { detail: formatErr(e) }));
  } finally {
    previewing.value = false;
  }
}

async function doImport() {
  if (!preview.value || !importFile.value) return;
  if (strategy.value === "replace" && deleteConfirm.value !== "DELETE") {
    msg.warning(t("settings.backup.msgTypeDelete"));
    return;
  }
  importing.value = true;
  try {
    const rep = await backupApi.importBackup(
      importFile.value,
      importPwd.value,
      strategy.value,
      preview.value.confirm_token,
    );
    msg.success(
      t("settings.backup.msgImportSuccess", {
        groups: rep.applied.groups,
        keys: rep.applied.api_keys,
        aliases: rep.applied.model_aliases,
      }),
    );
    preview.value = null;
    deleteConfirm.value = "";
  } catch (e: unknown) {
    msg.error(t("settings.backup.msgImportFailed", { detail: formatErr(e) }));
  } finally {
    importing.value = false;
  }
}

function formatErr(e: unknown): string {
  if (e && typeof e === "object") {
    const anyE = e as { response?: { data?: { error?: string; detail?: string } }; message?: string };
    const code = anyE.response?.data?.error;
    const detail = anyE.response?.data?.detail;
    if (code) return detail ? `${code} — ${detail}` : code;
    if (anyE.message) return anyE.message;
  }
  return String(e);
}
</script>

<template>
  <NSpace vertical :size="16">
    <NCard :title='t("settings.backup.exportTitle")'>
      <NSpace vertical :size="12">
        <NAlert type="warning">
          {{ t("settings.backup.exportWarnHead") }}
          <strong>{{ t("settings.backup.exportWarnBold") }}</strong>
          {{ t("settings.backup.exportWarnTail") }}
        </NAlert>
        <NSpace>
          <NInput
            v-model:value="exportPwd"
            type="password"
            :placeholder='t("settings.backup.exportPasswordPlaceholder")'
            :aria-label='t("settings.backup.exportPasswordAria")'
            style="width: 320px"
            show-password-on="click"
          />
          <NButton @click="generateRandomPassword">{{ t("settings.backup.randomBtn") }}</NButton>
          <NButton type="primary" :loading="exporting" @click="doExport">
            {{ t("settings.backup.downloadBtn") }}
          </NButton>
        </NSpace>
      </NSpace>
    </NCard>

    <NCard :title='t("settings.backup.restoreTitle")'>
      <NSpace vertical :size="12">
        <NAlert type="info">
          {{ t("settings.backup.restoreWarn") }}
        </NAlert>
        <NUpload :max="1" :default-upload="false" accept=".acb" @change="onFileChange">
          <NButton>{{ t("settings.backup.chooseFile") }}</NButton>
        </NUpload>
        <NInput
          v-model:value="importPwd"
          type="password"
          :placeholder='t("settings.backup.restorePasswordPlaceholder")'
          :aria-label='t("settings.backup.restorePasswordAria")'
          style="width: 320px"
          show-password-on="click"
        />
        <NButton :loading="previewing" :disabled="!importFile || !importPwd" @click="doPreview">
          {{ t("settings.backup.previewBtn") }}
        </NButton>

        <template v-if="preview">
          <NDescriptions :column="2" bordered size="small" label-placement="left">
            <NDescriptionsItem :label='t("settings.backup.fieldSchema")'>{{ preview.schema_version }}</NDescriptionsItem>
            <NDescriptionsItem :label='t("settings.backup.fieldExportedAt")'>{{ preview.exported_at }}</NDescriptionsItem>
            <NDescriptionsItem :label='t("settings.backup.fieldGroups")'>
              {{ preview.counts.groups }}
              {{ t("settings.backup.conflictSuffix", { n: preview.conflicts.groups.length }) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label='t("settings.backup.fieldApiKeys")'>
              {{ preview.counts.api_keys }}
              {{ t("settings.backup.conflictSuffix", { n: preview.conflicts.api_keys_by_hash }) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label='t("settings.backup.fieldAliases")'>
              {{ preview.counts.model_aliases }}
              {{ t("settings.backup.conflictSuffix", { n: preview.conflicts.aliases }) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label='t("settings.backup.fieldSettings")'>
              {{ preview.counts.system_settings }}
              {{ t("settings.backup.conflictSuffix", { n: preview.conflicts.system_settings.length }) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label='t("settings.backup.fieldSubGroups")'>
              {{ preview.counts.group_sub_groups }}
              {{ t("settings.backup.conflictSuffix", { n: preview.conflicts.group_sub_groups }) }}
            </NDescriptionsItem>
          </NDescriptions>

          <div>{{ t("settings.backup.strategyLabel") }}</div>
          <NRadioGroup v-model:value="strategy">
            <NRadio value="merge">{{ t("settings.backup.strategyMerge") }}</NRadio>
            <NRadio value="skip">{{ t("settings.backup.strategySkip") }}</NRadio>
            <NRadio value="replace">
              <strong style="color: var(--n-color-danger, #d03050)">
                {{ t("settings.backup.strategyReplaceTitle") }}
              </strong>
              {{ t("settings.backup.strategyReplaceDesc") }}
            </NRadio>
          </NRadioGroup>

          <NAlert v-if="strategy === 'replace'" type="error">
            {{ t("settings.backup.replaceWarn", {
                groups: preview.will_delete_if_replace.groups,
                keys: preview.will_delete_if_replace.api_keys,
                aliases: preview.will_delete_if_replace.model_aliases,
            }) }}
            <NInput
              v-model:value="deleteConfirm"
              :placeholder='t("settings.backup.deletePlaceholder")'
              :aria-label='t("settings.backup.deleteAria")'
              style="margin-top: 8px"
            />
          </NAlert>

          <NButton type="primary" :loading="importing" @click="doImport">
            {{ t("settings.backup.applyBtn") }}
          </NButton>
        </template>
      </NSpace>
    </NCard>
  </NSpace>
</template>
