<script setup lang="ts">
import { ref } from "vue";
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

// ---------- Export ----------
const exportPwd = ref("");
const exporting = ref(false);

async function doExport() {
  if (!exportPwd.value || exportPwd.value.length < 8) {
    msg.warning("Password should be at least 8 characters.");
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
    msg.success("Backup downloaded. Keep the password safe.");
  } catch (e: unknown) {
    msg.error(`Export failed: ${formatErr(e)}`);
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
    msg.warning("Pick a .acb file and enter the password.");
    return;
  }
  previewing.value = true;
  try {
    preview.value = await backupApi.previewBackup(
      importFile.value,
      importPwd.value,
    );
  } catch (e: unknown) {
    msg.error(`Preview failed: ${formatErr(e)}`);
  } finally {
    previewing.value = false;
  }
}

async function doImport() {
  if (!preview.value || !importFile.value) return;
  if (strategy.value === "replace" && deleteConfirm.value !== "DELETE") {
    msg.warning("Type DELETE (uppercase) to confirm replace.");
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
      `Imported. applied groups=${rep.applied.groups}, keys=${rep.applied.api_keys}, aliases=${rep.applied.model_aliases}.`,
    );
    preview.value = null;
    deleteConfirm.value = "";
  } catch (e: unknown) {
    msg.error(`Import failed: ${formatErr(e)}`);
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
    <NCard title="Export Configuration">
      <NSpace vertical :size="12">
        <NAlert type="warning">
          The backup file is encrypted with the password you set below.
          <strong>Lost password = lost data.</strong> Keep it together with the file.
        </NAlert>
        <NSpace>
          <NInput
            v-model:value="exportPwd"
            type="password"
            placeholder="Backup password (≥ 8 chars)"
            style="width: 320px"
            show-password-on="click"
          />
          <NButton @click="generateRandomPassword">Random</NButton>
          <NButton type="primary" :loading="exporting" @click="doExport">
            Download Backup
          </NButton>
        </NSpace>
      </NSpace>
    </NCard>

    <NCard title="Restore from Backup">
      <NSpace vertical :size="12">
        <NAlert type="info">
          We strongly recommend exporting a current backup before restoring.
        </NAlert>
        <NUpload :max="1" :default-upload="false" accept=".acb" @change="onFileChange">
          <NButton>Choose .acb file</NButton>
        </NUpload>
        <NInput
          v-model:value="importPwd"
          type="password"
          placeholder="Backup password"
          style="width: 320px"
          show-password-on="click"
        />
        <NButton :loading="previewing" :disabled="!importFile || !importPwd" @click="doPreview">
          Preview
        </NButton>

        <template v-if="preview">
          <NDescriptions :column="2" bordered size="small" label-placement="left">
            <NDescriptionsItem label="Schema">{{ preview.schema_version }}</NDescriptionsItem>
            <NDescriptionsItem label="Exported at">{{ preview.exported_at }}</NDescriptionsItem>
            <NDescriptionsItem label="Groups">
              {{ preview.counts.groups }}
              ({{ preview.conflicts.groups.length }} conflict)
            </NDescriptionsItem>
            <NDescriptionsItem label="API Keys">
              {{ preview.counts.api_keys }}
              ({{ preview.conflicts.api_keys_by_hash }} conflict)
            </NDescriptionsItem>
            <NDescriptionsItem label="Aliases">
              {{ preview.counts.model_aliases }}
              ({{ preview.conflicts.aliases }} conflict)
            </NDescriptionsItem>
            <NDescriptionsItem label="Settings">
              {{ preview.counts.system_settings }}
              ({{ preview.conflicts.system_settings.length }} conflict)
            </NDescriptionsItem>
          </NDescriptions>

          <div>Conflict strategy:</div>
          <NRadioGroup v-model:value="strategy">
            <NRadio value="merge">Merge (upsert; existing fields overwritten)</NRadio>
            <NRadio value="skip">Skip (keep all local; only add new)</NRadio>
            <NRadio value="replace">
              <strong style="color: var(--n-color-danger, #d03050)">Replace</strong>
              (delete all non-system groups + their keys/aliases, then import)
            </NRadio>
          </NRadioGroup>

          <NAlert v-if="strategy === 'replace'" type="error">
            Replace will delete
            <strong>{{ preview.will_delete_if_replace.groups }}</strong> groups,
            <strong>{{ preview.will_delete_if_replace.api_keys }}</strong> keys,
            <strong>{{ preview.will_delete_if_replace.model_aliases }}</strong> aliases.
            Type <code>DELETE</code> below to confirm.
            <NInput
              v-model:value="deleteConfirm"
              placeholder="Type DELETE to confirm"
              style="margin-top: 8px"
            />
          </NAlert>

          <NButton type="primary" :loading="importing" @click="doImport">
            Apply Import
          </NButton>
        </template>
      </NSpace>
    </NCard>
  </NSpace>
</template>
