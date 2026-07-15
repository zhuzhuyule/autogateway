import http from "@/utils/http";

export interface SyncPeer {
  id: string;
  name: string;
  url: string;
  sync_key: string;
  role: string;
  /**
   * P9.1: 复合状态字段, 可能形式:
   *   - "disconnected" / "connected"
   *   - "rejected:major_version_mismatch" / "rejected:schema_mismatch"
   *   - "rejected:fingerprint_mismatch"
   *   - "warning:minor_version_diff"
   */
  status: string;
  last_synced_at?: string;
  /** P9.1 握手回填: 对端版本号, 例如 "v2.4.10" */
  peer_version?: string;
  /** P9.1 握手回填: 对端 schema fingerprint */
  peer_schema_hash?: string;
  /** P9.x 握手回填: 对端 X25519 公钥 (base64), 加密时用 */
  public_key_x25519?: string;
  /** P9.x 用户输入: 期望对端公钥指纹, 用于防 MITM 钉扎 */
  pinned_fingerprint?: string;
  created_at?: string;
}

export interface SyncLog {
  id: number;
  peer_id: string;
  action: "push" | "pull";
  status: "success" | "error";
  error_message?: string;
  details?: string;
  timestamp: string;
}

export interface VersionInfo {
  version: string;
  schema_hash: string;
  /** P9.x: 本节点 X25519 公钥 (base64), 用户复制给对端配 peer 时输入 */
  public_key: string;
  /** P9.x: 公钥短指纹 (用户友好比对) */
  fingerprint: string;
  started_at: string;
}

export interface UpgradeRequest {
  target_version: string;
  requested_by?: string;
  requested_at?: string;
}

export interface UpgradeStatus {
  pending: boolean;
  request?: UpgradeRequest;
  waiting_secs?: number;
}

export const upgradeApi = {
  /**
   * 触发本端升级请求 (写信号文件, 待宿主机 watcher 接管).
   * - 200/202: 成功写入
   * - 400: target_version 非法 / 降级被拒
   * - 409: 已有 pending 升级
   */
  async request(targetVersion: string, requestedBy = "self"): Promise<void> {
    await http.post("/upgrade/request", {
      target_version: targetVersion,
      requested_by: requestedBy,
    });
  },
  async status(): Promise<UpgradeStatus> {
    const response = await http.get("/upgrade/status");
    return response.data;
  },
};

export interface SyncConfig {
  sync_enabled: boolean;
  sync_key: string;
  /** 只读: 本节点是否 master(由 IS_MASTER 环境变量决定)。主从 UI 据此区分展示。 */
  is_master?: boolean;
}

/** master 集中定义的同步排除清单。默认全同步; 列入的类别/字段不下发给 follower。 */
export interface SyncPolicy {
  excludedCategories: string[];
  excludedFields: Record<string, string[]>;
}

// P11.36: push 二次确认 preview 响应
export interface PreviewItem {
  type: "group" | "subgroup" | "alias" | "key" | "setting";
  name: string;
  action: "upsert" | "delete";
  updated_at: string;
}

export interface PreviewPushResponse {
  peer_id: string;
  peer_name: string;
  since: string | null;
  settings_count: number;
  groups_count: number;
  subgroups_count: number;
  aliases_count: number;
  api_keys_count: number;
  affected: PreviewItem[];
}

export const syncApi = {
  /** P9.1: 同步全局配置 (启用开关 + 全局加密密钥) */
  async getConfig(): Promise<SyncConfig> {
    const response = await http.get("/sync/config");
    return response.data;
  },
  async updateConfig(data: SyncConfig): Promise<void> {
    await http.put("/sync/config", data);
  },
  /** 主从: 读取/保存 master 的同步排除清单 sync_policy */
  async getPolicy(): Promise<SyncPolicy> {
    const response = await http.get("/sync/policy");
    return response.data;
  },
  async updatePolicy(data: SyncPolicy): Promise<void> {
    await http.put("/sync/policy", data);
  },
  async getPeers(): Promise<SyncPeer[]> {
    const response = await http.get("/sync/peers");
    return response.data || [];
  },
  async createPeer(data: Partial<SyncPeer>): Promise<SyncPeer> {
    const response = await http.post("/sync/peers", data);
    return response.data;
  },
  async updatePeer(id: string, data: Partial<SyncPeer>): Promise<SyncPeer> {
    const response = await http.put(`/sync/peers/${id}`, data);
    return response.data;
  },
  async deletePeer(id: string): Promise<void> {
    await http.delete(`/sync/peers/${id}`);
  },
  /** P11.35: 手动 trigger 单个 peer 的 pull (HTTP 拉对端最新 delta) */
  async triggerPull(id: string): Promise<void> {
    await http.post(`/sync/peers/${id}/pull`, {});
  },
  /** P11.35: 手动 trigger 单个 peer 的 push (推本端最新 delta 到对端).
   *  要求 WS 已建立; 否则返 500 提示 "peer not connected via WS". */
  async triggerPush(id: string): Promise<void> {
    await http.post(`/sync/peers/${id}/push`, {});
  },
  /** P11.36: push 前先调 preview, 看会发什么. 不打 fingerprint snapshot,
   *  真 push 时数据可能多 (用户中间又改了) — 已知 trade-off. */
  async previewPush(id: string): Promise<PreviewPushResponse> {
    const r = await http.get(`/sync/peers/${id}/preview-push`, { hideMessage: true });
    return r.data as PreviewPushResponse;
  },
  /** P9.1: 拉本端版本信息, 用于 UI 比对对端版本徽章 */
  async getVersion(): Promise<VersionInfo> {
    const response = await http.get("/version");
    return response.data;
  },
  /** P9.1: 拉最近的同步历史日志, 给历史抽屉用 */
  async getLogs(params?: {
    peer_id?: string;
    action?: "push" | "pull";
    limit?: number;
  }): Promise<SyncLog[]> {
    const response = await http.get("/sync/logs", { params });
    return response.data || [];
  },
};
