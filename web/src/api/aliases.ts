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
  /** 改候选的目标分组 —— 后端允许了（唯一索引会挡住改成重复组合）。 */
  group_id?: number;
  real_model?: string;
  weight?: number;
  priority?: number;
  enabled?: boolean;
}

/**
 * 一条候选，用于整体替换。
 *
 * `priority` 现在**不由用户直接编辑** —— 它承载"拖拽顺序"(从 1 开始)，
 * 顺序即 SWRR 打平时的先后偏好。
 */
export interface AliasCandidatePayload {
  group_id: number;
  real_model: string;
  weight: number;
  priority: number;
  enabled: boolean;
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
  /**
   * 整体替换某个别名的候选集合 —— 编辑抽屉的"保存"。
   *
   * 别名放在 body 里而不是路径上: `/aliases/{alias}/candidates` 会和已有的
   * `PUT /aliases/:id` 在路由树同一层出现两个不同名的参数段, gin 直接 panic。
   */
  replaceCandidates: (alias: string, candidates: AliasCandidatePayload[]) =>
    http.put<ModelAliasRow[]>("/aliases/candidates", { alias, candidates }),
  /**
   * 整体改别名名。后端会拒绝: 改保留别名 / 改成保留名 / 目标名已存在。
   * 同样走 body 而不是路径(避开 gin 的参数名冲突)。
   */
  rename: (from: string, to: string) =>
    http.put<{ renamed: number }>("/aliases/rename", { from, to }),
  suggestions: () => http.get<AliasSuggestion[]>("/aliases/suggestions"),
  // P4.2 registry-driven 建议: 给定 aggregate group id, 返回该聚合下
  // 跨 sub-group 共享同一 family 但还没建 alias 的候选清单.
  suggestionsFromRegistry: (groupId: number) =>
    http.get<AliasSuggestion[]>(`/aliases/suggestions/registry/${groupId}`),
};

export const routingSettingsApi = {
  get: () => http.get<RoutingSettings>("/routing/settings"),
  save: (
    payload: Partial<{ enabled: boolean; simple_threshold: number; complex_threshold: number }>
  ) => http.put<RoutingSettings>("/routing/settings", payload),
};
