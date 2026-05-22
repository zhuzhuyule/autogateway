import http from "@/utils/http";

export interface Counts {
  system_settings: number;
  groups: number;
  api_keys: number;
  model_aliases: number;
  group_sub_groups: number;
}

export interface ConflictReport {
  groups: string[];
  api_keys_by_hash: number;
  aliases: number;
  system_settings: string[];
  group_sub_groups: number;
}

export interface PreviewReport {
  schema_version: number;
  exported_at: string;
  exported_by: string;
  counts: Counts;
  conflicts: ConflictReport;
  will_delete_if_replace: Counts;
  warnings: string[];
  confirm_token: string;
}

export interface ImportReport {
  applied: Counts;
  skipped: Counts;
  warnings: string[];
  elapsed_ms: number;
}

export type Strategy = "merge" | "skip" | "replace";

export const backupApi = {
  /** Download a `.acb` encrypted backup file. */
  async exportBackup(password: string): Promise<Blob> {
    const res = await http.post(
      "/admin/backup/export",
      { password },
      { responseType: "blob" },
    );
    return res.data as Blob;
  },

  /** Decrypt and analyze a `.acb` without writing — returns conflicts + confirm_token. */
  async previewBackup(file: File, password: string): Promise<PreviewReport> {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("password", password);
    const res = await http.post("/admin/backup/preview", fd, {
      headers: { "Content-Type": "multipart/form-data" },
    });
    return res.data as PreviewReport;
  },

  /** Apply the backup to the DB. confirmToken must come from previewBackup(). */
  async importBackup(
    file: File,
    password: string,
    strategy: Strategy,
    confirmToken: string,
  ): Promise<ImportReport> {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("password", password);
    fd.append("strategy", strategy);
    fd.append("confirm_token", confirmToken);
    const res = await http.post("/admin/backup/import", fd, {
      headers: { "Content-Type": "multipart/form-data" },
    });
    return res.data as ImportReport;
  },
};
