import http from "@/utils/http";

export interface DedupModelEntry {
  group_id: number;
  group_name: string;
  real_model: string;
  aliases: string[];
}

export interface DedupFamily {
  family: string;
  group_count: number;
  models: DedupModelEntry[];
}

export interface DedupCreateRequest {
  alias: string;
  candidates: { group_id: number; real_model: string }[];
}

export interface DedupCreateResponse {
  success: boolean;
  alias?: string;
  created: number;
  failures: string[];
}

export const dedupApi = {
  async models(): Promise<DedupFamily[]> {
    const data = await http.get<unknown, { families?: DedupFamily[] }>("/dedup/models");
    return data?.families ?? [];
  },

  async create(req: DedupCreateRequest): Promise<DedupCreateResponse> {
    return await http.post<unknown, DedupCreateResponse>("/dedup/create", req);
  },
};
