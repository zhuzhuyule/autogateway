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

/**
 * One suggestion row. `kind` discriminates the payload:
 * - "single": a single unrecognized model — fields {model, count, last_seen}
 * - "family": several models share a registry model_family —
 *   the UI offers a one-click "create one alias for the whole family" action.
 *
 * Server-side promotion thresholds (>=2 distinct models AND >=3 hits) live
 * in `internal/services/alias_suggestion_service.go`; below threshold, each
 * model is emitted as its own `kind=single` row.
 */
export interface AliasSuggestion {
  kind: "single" | "family";
  // single-mode (also populated in family-mode for the aggregate hit count)
  model?: string;
  count: number;
  last_seen?: string;

  // family-mode only
  family?: string;
  models?: AliasSuggestionFamilyModel[];
  /** alias name if a same-named alias already exists — UI offers "append to existing" instead of "create" */
  existing_alias?: string;
}

export interface AliasSuggestionFamilyModel {
  name: string;
  count?: number;
  last_seen?: string;
  from_logs: boolean;
  /** groups currently exposing this model — candidates for becoming alias targets */
  in_group_ids?: number[];
}

export const RESERVED_ALIASES = ["simple", "medium", "complex"] as const;
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
