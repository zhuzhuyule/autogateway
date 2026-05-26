import http from "@/utils/http";

export interface SyncPeer {
  id: string;
  name: string;
  url: string;
  sync_key: string;
  role: string;
  status: string;
  sync_api_keys: boolean;
  last_synced_at?: string;
  created_at?: string;
}

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
};
