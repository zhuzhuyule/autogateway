import http from "@/utils/http";

/** 一行 model_aliases 表 */
export interface ModelAliasRow {
  id: number;
  alias: string;
  group_id: number;
  real_model: string;
  weight: number;
  priority: number;
  enabled: boolean;
  is_reserved: boolean;
  created_at: string;
  updated_at: string;
}

export interface AliasCreatePayload {
  alias: string;
  group_id: number;
  real_model: string;
  weight?: number;
  priority?: number;
  enabled?: boolean;
}

export interface AliasUpdatePayload {
  real_model?: string;
  weight?: number;
  priority?: number;
  enabled?: boolean;
}

export interface RoutingSettings {
  Enabled: boolean;
  SimpleThreshold: number;
  ComplexThreshold: number;
}

export interface AliasSuggestion {
  model: string;
  count: number;
  last_seen: string;
}

export const RESERVED_ALIASES = ["auto-simple", "auto-medium", "auto-complex"] as const;
export type ReservedAlias = (typeof RESERVED_ALIASES)[number];

export const aliasesApi = {
  list: () => http.get<ModelAliasRow[]>("/aliases"),
  byAlias: (name: string) => http.get<ModelAliasRow[]>(`/aliases/${encodeURIComponent(name)}`),
  create: (payload: AliasCreatePayload) => http.post<ModelAliasRow>("/aliases", payload),
  update: (id: number, payload: AliasUpdatePayload) =>
    http.put<ModelAliasRow>(`/aliases/${id}`, payload),
  remove: (id: number) => http.delete(`/aliases/${id}`),
  suggestions: () => http.get<AliasSuggestion[]>("/aliases/suggestions"),
};

export const routingSettingsApi = {
  get: () => http.get<RoutingSettings>("/routing/settings"),
  save: (
    payload: Partial<{ enabled: boolean; simple_threshold: number; complex_threshold: number }>
  ) => http.put<RoutingSettings>("/routing/settings", payload),
};
