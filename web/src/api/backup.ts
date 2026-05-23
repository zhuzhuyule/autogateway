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
    // http.ts 的响应拦截器统一 `return response.data`。对 responseType:"blob"
    // 来说 response.data 就是 Blob 本身——所以这里 await 直接拿到 Blob，再 .data 会得到 undefined。
    const blob = (await http.post(
      "/admin/backup/export",
      { password },
      { responseType: "blob", hideMessage: true },
    )) as unknown as Blob;
    return blob;
  },

  /** Decrypt and analyze a `.acb` without writing — returns conflicts + confirm_token. */
  async previewBackup(file: File, password: string): Promise<PreviewReport> {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("password", password);
    // 后端直接 c.JSON(rep) 返回裸 PreviewReport（不是 {code,message,data} envelope）；
    // 拦截器再剥一层后，await 拿到的就是 PreviewReport 本身。
    const rep = (await http.post("/admin/backup/preview", fd, {
      headers: { "Content-Type": "multipart/form-data" },
      hideMessage: true,
    })) as unknown as PreviewReport;
    return rep;
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
    const rep = (await http.post("/admin/backup/import", fd, {
      headers: { "Content-Type": "multipart/form-data" },
      hideMessage: true,
    })) as unknown as ImportReport;
    return rep;
  },
};
