# Alias Quick Setup — Design Spec

**Date:** 2026-05-02
**Status:** Approved (inline brainstorm)
**Owner:** Pengfei

## 1. Background

The current "模型去重" (`/model-dedup`) page lists only models that share an identical name across multiple non-aggregate groups, then offers a one-shot Modal to create one alias per duplicate. It sits as a sibling menu item beside "别名" (`/aliases`), even though it is functionally a faster way to enter the same data the alias page manages.

This split causes three problems:

1. **Information architecture** — two same-level menu items for one concept (managing aliases). Users can not tell from the nav which one to use first.
2. **Discovery** — the dedup page only surfaces *exact-name duplicates*. Variants that share a family but differ in size/quantization (e.g. `gpt-oss-120b` vs `gpt-oss-20b`) are invisible there even though they are the same conceptual model the user wants to alias together.
3. **Round-trips** — to merge a known family the user has to leave dedup, open the alias picker, search for the model, click Add, repeat. There is no single workspace where "search models → see who already aliased them → batch-aggregate" is one flow.

The backend already has the building blocks — `deriveFamily()` and `scanGroupsByFamily()` in `internal/services/alias_suggestion_service.go` — but they are only consumed by the unrecognized-model suggestion banner on `/aliases`. They are not exposed as a general "all models grouped by family" feed.

## 2. Goals

- **One menu item for alias management.** The "快速整理" (quick setup) flow becomes a tab inside the existing aliases page.
- **Family-aware browsing.** All models from non-aggregate groups are listed grouped by `deriveFamily()`, regardless of whether duplicates exist.
- **Substring search across family + model name** so a query like `OSS-120` finds `gpt-oss-120b` without the user knowing the family heuristic.
- **Existing-alias awareness** in the same view. Each model entry shows its alias chips; the right pane lists all aliases and acts as the "append target" selector when clicked.
- **Models can belong to multiple aliases.** "Already in alias X" is informational, never a disable condition.

## 3. Non-Goals

- No automated test plan for this slice (per user direction).
- No changes to the alias data model, the `model_aliases` table, or `AliasService.Create` contract.
- No redesign of the "管理" tab interactions (the existing tier board + picker stay as-is). The user mentioned future improvements there, but they are out of scope for this spec.
- No removal of the legacy `GET /api/dedup/suggestions` endpoint in this slice — kept for one release for backwards compat.

## 4. Information Architecture

### 4.1 Menu / Routes

- Left nav: remove the standalone `model-dedup` entry. Final nav: `dashboard / keys / aliases / model-catalog / logs / settings`.
- Aliases route gains a `tab` query param:
  - `/aliases` → defaults to `tab=manage`
  - `/aliases?tab=quick` → quick setup tab
- Legacy `/model-dedup` route: keep one release, redirect to `/aliases?tab=quick`.

### 4.2 Page Structure (Aliases.vue)

```
Aliases.vue
├─ <NTabs v-model="activeTab"> [管理 | 快速整理]
├─ <AliasManageTab>            (existing Aliases.vue body, extracted)
└─ <AliasQuickSetupTab>        (new)
    ├─ <TopSearchBar>          search input + "+ 新建别名"
    ├─ main: <FamilyAccordion>
    │   └─ <ModelEntryRow>     checkbox + group chip + alias chips
    ├─ side: <ExistingAliasesPanel>
    │   └─ <AliasCard>         click to toggle as append target
    └─ footer: <SubmitActionBar>
                               selected preview + mode-aware button
```

The existing `Aliases.vue` body is moved verbatim into `AliasManageTab.vue` to keep the manage tab functionally identical.

## 5. Backend

### 5.1 New Endpoint

`GET /api/dedup/models` — returns all candidate models, grouped by derived family.

```go
type DedupModelsResponse struct {
    Families []DedupFamily `json:"families"`
}

type DedupFamily struct {
    Family     string             `json:"family"`       // deriveFamily() output, e.g. "gpt-oss"
    GroupCount int                `json:"group_count"`  // distinct groups offering any model in this family
    Models     []DedupModelEntry  `json:"models"`
}

type DedupModelEntry struct {
    GroupID    uint     `json:"group_id"`
    GroupName  string   `json:"group_name"`
    RealModel  string   `json:"real_model"`
    Aliases    []string `json:"aliases"`               // aliases that already include (group, real_model)
}
```

Scope rules (matching the existing alias picker semantics):

- Skip groups where `group_type == "aggregate"`.
- For each group:
  - If `model_routing_mode == "specified"`, candidate set = `exposed_models`. If empty, fall back to `available_models` (matches the picker's degrade behavior).
  - Otherwise candidate set = `available_models`.
  - Filter out anything in `blocked_models`.
- For each candidate `(group_id, real_model)`:
  - `family = deriveFamily(real_model)`. If empty, the entry still appears under a synthetic "其他" / empty-family bucket.
  - `aliases` = lookup in `model_aliases` for rows matching `(group_id, real_model)` with `enabled = true`.

Sorting:

- Families sorted by `group_count DESC`, then by `len(models) DESC`, then `family` alphabetical.
- Within a family, models sorted by `(real_model, group_name)` alphabetical.

### 5.2 Reused

- `services.deriveFamily()` — family heuristic.
- `services.scanGroupsByFamily()` — pattern for iterating non-aggregate groups; the new endpoint may inline a variant since it needs the per-`(group, model)` shape rather than `family → set<model>`.
- `POST /api/dedup/create` — handler unchanged. Frontend continues to send `{alias, candidates: [{group_id, real_model}]}` and receive `{success, created, failures}`.
- `services.AliasService.Create` — unchanged; existing `(alias, group_id, real_model)` triple uniqueness conflict surfaces as a failure entry, which the frontend reports.

### 5.3 Kept (deprecation queued)

- `GET /api/dedup/suggestions` — returns the legacy duplicate-only shape. Not consumed after this change but kept for one release. Add a `// Deprecated: use /api/dedup/models` comment in the handler.

## 6. Frontend

### 6.1 `/web/src/api/dedup.ts` (new)

Replace the inline fetch in `ModelDedup.vue` with a typed module mirroring `aliases.ts`:

```ts
export interface DedupModelEntry { groupId: number; groupName: string; realModel: string; aliases: string[]; }
export interface DedupFamily     { family: string; groupCount: number; models: DedupModelEntry[]; }
export const dedupApi = {
  models: (): Promise<DedupFamily[]> => ...,
  create: (alias: string, picks: {groupId: number; realModel: string}[]) => ...,
};
```

### 6.2 `AliasQuickSetupTab.vue` interaction

- **Initial load**: `dedupApi.models()` → families. Auto-expand families where `groupCount > 1`; collapse single-group families.
- **Search**: as user types, the query (lowercased, trimmed) is matched as a substring against `family` ∪ `model.real_model`. Any family with at least one match auto-expands; the matched substring is highlighted in both the family header and the model name. Empty query restores the auto-expand-by-groupCount default.
- **Selection**: each `ModelEntryRow` has a checkbox. Selected entries are tracked in a flat `Map<key, ModelEntryRow>` keyed by `${groupId}:${realModel}`.
- **Existing alias chips**: every row renders one chip per `aliases[]` entry. Chips are clickable — clicking a chip selects the corresponding alias in the right pane (same effect as clicking the alias card directly).
- **Right pane**: list of all aliases known to the page (built from `aliases[]` union across rows; reserved `simple/medium/complex` always shown). Clicking an alias card:
  1. Sets it as the append target.
  2. Highlights all `ModelEntryRow`s whose `aliases[]` contains this alias name (border accent, no disable).
  3. Pre-fills the bottom name input with the alias name in read-only mode.
  Clicking the same card again clears the target.
- **Submit bar**:
  - With no append target: button reads `[创建别名: <familyHint> ⏎]`. `familyHint` resolution: if all selected entries share one family, use it; if mixed, use the most-frequent family among selections (ties broken alphabetically); if all selected entries have empty family, fall back to the first entry's `real_model`. Name input is editable, placeholder = familyHint. User-typed text takes precedence over the hint.
  - With an append target: button reads `[追加到 <alias> ⏎]`. Name input becomes read-only and displays the target alias name. Any user-typed name is preserved in component state and restored when the target is cleared (re-clicking the alias card or selecting a different one).
  - Submit calls `dedupApi.create(name, selected)`. On response:
    - All-success: toast `已添加 N 条`, switch to `manage` tab, scroll the target alias card into view, apply a 1.5 s `--v3-accent-soft` glow.
    - Partial success: toast `成功 X / 失败 Y`, open a small modal listing each failure verbatim. Successful rows still reflected in the manage tab.
    - All-failure: toast error, do not switch tabs.

### 6.3 Component extraction

- New components live under `web/src/components/aliases/`:
  - `AliasManageTab.vue` — extracted body of current `Aliases.vue` (template + script + scoped styles, verbatim).
  - `AliasQuickSetupTab.vue` — top-level container for the new tab.
  - `quick/FamilyAccordion.vue`, `quick/ModelEntryRow.vue`, `quick/ExistingAliasesPanel.vue`, `quick/SubmitActionBar.vue` — internal pieces of the quick setup tab.
- `Aliases.vue` becomes a thin shell:
  ```vue
  <script setup>
  // tab state synced to ?tab= query param
  </script>
  <template>
    <NTabs v-model:value="activeTab">
      <NTabPane name="manage" :tab="t('aliases.tabManage')"><AliasManageTab /></NTabPane>
      <NTabPane name="quick"  :tab="t('aliases.tabQuick')"><AliasQuickSetupTab /></NTabPane>
    </NTabs>
  </template>
  ```
- `ModelDedup.vue` is deleted along with its router entry.

### 6.4 i18n

New keys under `aliases.*`:

- `tabManage`, `tabQuick`
- `quick.searchPlaceholder` ("搜索模型 / 家族…")
- `quick.familyMeta` ("{n} 模型 · {g} 组")
- `quick.createButton` ("创建别名: {family}")
- `quick.appendButton` ("追加到 {alias}")
- `quick.emptySelect` / `quick.emptyName` / `quick.partialFailure`

Keep the old `dedup.*` keys for one release (used by the redirect tomb-stone), but new strings go under `aliases.quick.*`.

## 7. Edge cases

- **Empty result**: `families` is empty (no non-aggregate groups, or all filtered out) → show empty-state card with link to `/keys` (group setup).
- **`(alias, group, model)` triplet conflict**: the row already exists. `AliasService.Create` returns the conflict error; the frontend surfaces it under the partial-failure modal — it is NOT a multi-alias case, it is a literal duplicate row.
- **Reserved alias name** (`simple` / `medium` / `complex`): allowed; `AliasService.Create` already accepts them. The existing manage tab visualizes reserved aliases.
- **Empty family bucket**: if `deriveFamily(model) == ""`, the entry lives under a synthetic family `""` rendered as `其他模型` with no auto-expand.
- **Mixed-family selection**: user selects entries spanning multiple families (e.g. `gpt-oss-120b` + `claude-haiku`). Allowed — the resulting alias holds heterogeneous targets, which is a legitimate use case (cross-provider routing). `familyHint` resolution falls back to most-frequent / first per §6.2.
- **Rapid tab switch during pending submit**: the submit promise is per-tab; if the user switches tabs mid-flight the toast still fires, the manage tab still scrolls/highlights once it loads.

## 8. Implementation order

1. Backend: add `GET /api/dedup/models` handler + service method, wire route.
2. Frontend: add `web/src/api/dedup.ts`.
3. Frontend: extract `AliasManageTab.vue` from existing `Aliases.vue`.
4. Frontend: build `AliasQuickSetupTab.vue` with `FamilyAccordion`, `ExistingAliasesPanel`, `SubmitActionBar`.
5. Frontend: refactor `Aliases.vue` to the tab shell with router-synced `tab` query param.
6. Frontend: add `/model-dedup` redirect; remove `model-dedup` from `Layout.vue` nav.
7. Frontend: delete `ModelDedup.vue`.
8. Backend: comment legacy `/api/dedup/suggestions` as deprecated.
9. i18n: add new keys to `zh-CN`, `en-US`, `ja-JP`.

## 9. Out of scope (followups)

- Improving the existing manage-tab picker UX (the user noted this verbatim — separate ticket).
- Removing legacy `/api/dedup/suggestions` and `dedup.*` i18n keys (next release after this one ships).
- Test scaffolding (per user direction for this slice).
