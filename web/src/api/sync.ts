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
   *   - "warning:minor_version_diff"
   */
  status: string;
  sync_api_keys: boolean;
  last_synced_at?: string;
  /** P9.1 握手回填: 对端版本号, 例如 "v2.4.10" */
  peer_version?: string;
  /** P9.1 握手回填: 对端 schema fingerprint */
  peer_schema_hash?: string;
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

export const syncApi = {
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
