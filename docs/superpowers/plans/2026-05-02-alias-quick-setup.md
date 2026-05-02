# Alias Quick Setup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the standalone `ModelDedup` page with a "快速整理" tab on the existing Aliases page that lets users browse all candidate models grouped by family, search them, and bulk-aggregate selections into new or existing aliases.

**Architecture:** Backend exposes one new family-grouped feed (`GET /api/dedup/models`) reusing `deriveFamily` / `scanGroupsByFamily` infrastructure. Frontend adds a new tab to `Aliases.vue` containing a two-pane layout: search-filtered family accordion on the left, existing-alias selector on the right, sticky submit bar at the bottom. Aliases can be created or appended; models can belong to multiple aliases (display only — never disabled).

**Tech Stack:** Go (Gin) backend, Vue 3 + naive-ui + vue-i18n + Vue Router 4 frontend.

**Spec:** `docs/superpowers/specs/2026-05-02-alias-quick-setup-design.md`

**User direction:** Skip test scaffolding for this slice. No new unit tests authored. Verify functionality manually via dev server + browser.

---

## File Structure

### Backend (Go)

| File | Action | Responsibility |
|---|---|---|
| `internal/services/model_dedup_service.go` | modify | Add `GetModelsByFamily()` returning `[]DedupFamily` shape |
| `internal/handler/dedup_handler.go` | modify | Add `GetModels` handler; deprecate `GetSuggestions` via comment |
| `internal/router/router.go:154-159` | modify | Register `GET /api/dedup/models` route |

### Frontend — API client

| File | Action | Responsibility |
|---|---|---|
| `web/src/api/dedup.ts` | create | Typed wrappers for `/api/dedup/models` and `/api/dedup/create` |

### Frontend — components

| File | Action | Responsibility |
|---|---|---|
| `web/src/components/aliases/AliasManageTab.vue` | create | Verbatim extraction of current `Aliases.vue` body |
| `web/src/components/aliases/AliasQuickSetupTab.vue` | create | Top-level container for quick setup tab |
| `web/src/components/aliases/quick/FamilyAccordion.vue` | create | Family-grouped collapsible list of candidates |
| `web/src/components/aliases/quick/ModelEntryRow.vue` | create | Single (group, model) row with checkbox + alias chips |
| `web/src/components/aliases/quick/ExistingAliasesPanel.vue` | create | Right-pane alias card list + append-target selector |
| `web/src/components/aliases/quick/SubmitActionBar.vue` | create | Bottom sticky bar: selection preview + name input + submit |

### Frontend — wiring

| File | Action | Responsibility |
|---|---|---|
| `web/src/views/Aliases.vue` | modify | Becomes thin shell with `<NTabs>` syncing `?tab=` query param |
| `web/src/views/ModelDedup.vue` | delete | Replaced by quick setup tab |
| `web/src/router/index.ts:46-50` | modify | Replace `model-dedup` route with redirect to `/aliases?tab=quick` |
| `web/src/components/Layout.vue:40-51` | modify | Remove `model-dedup` nav entry |

### Frontend — i18n

| File | Action | Responsibility |
|---|---|---|
| `web/src/locales/zh-CN.ts` | modify | Add `aliases.tabManage` / `aliases.tabQuick` / `aliases.quick.*` keys |
| `web/src/locales/en-US.ts` | modify | Same keys, English |
| `web/src/locales/ja-JP.ts` | modify | Same keys, Japanese |

---

## Task 1: Backend — `GetModelsByFamily()` service method

**Files:**
- Modify: `internal/services/model_dedup_service.go`

The new service method walks the same groups as `GetDedupSuggestions` but returns every (group, real_model) pair grouped by `deriveFamily()`. Each entry carries the list of aliases that already include it.

- [ ] **Step 1: Add response types and method in `model_dedup_service.go`**

Append the following at the end of `internal/services/model_dedup_service.go` (after the existing `appendUniqueCandidate` function). The method takes a `*gorm.DB` because alias lookup is one batched DB query.

```go
// DedupFamily groups all candidate (group, real_model) pairs by their
// derived family key. group_count is the number of distinct groups
// offering at least one model in this family — used by the UI to decide
// which families to auto-expand.
type DedupFamily struct {
	Family     string            `json:"family"`
	GroupCount int               `json:"group_count"`
	Models     []DedupModelEntry `json:"models"`
}

// DedupModelEntry is one (group, real_model) candidate. Aliases lists every
// model_aliases row whose (alias, group_id, real_model) matches and is enabled.
type DedupModelEntry struct {
	GroupID   uint     `json:"group_id"`
	GroupName string   `json:"group_name"`
	RealModel string   `json:"real_model"`
	Aliases   []string `json:"aliases"`
}

// GetModelsByFamily returns every candidate model from non-aggregate groups,
// grouped by deriveFamily(real_model). Filters:
//   - skips aggregate groups (alias targets are upstream-side only)
//   - in specified routing mode: candidate set = exposed_models, falling back
//     to available_models if the exposed list is empty (matches picker UX)
//   - in passthrough mode: candidate set = available_models
//   - removes anything in blocked_models
func (s *ModelDedupService) GetModelsByFamily(db *gorm.DB) ([]DedupFamily, error) {
	groups := s.groupManager.GetAllGroups()

	type entry struct {
		groupID   uint
		groupName string
		realModel string
	}

	familyToEntries := map[string][]entry{}
	familyGroups := map[string]map[uint]struct{}{}

	for _, group := range groups {
		if group.GroupType == "aggregate" {
			continue
		}
		candidates := candidateModelsForGroup(group)
		blocked := parseStringSet(group.BlockedModels)
		for m := range candidates {
			if _, isBlocked := blocked[m]; isBlocked {
				continue
			}
			fam := deriveFamily(m)
			familyToEntries[fam] = append(familyToEntries[fam], entry{
				groupID:   group.ID,
				groupName: group.Name,
				realModel: m,
			})
			if familyGroups[fam] == nil {
				familyGroups[fam] = map[uint]struct{}{}
			}
			familyGroups[fam][group.ID] = struct{}{}
		}
	}

	// Batched lookup: every (group_id, real_model) → []alias.
	type aliasRow struct {
		Alias     string
		GroupID   uint
		RealModel string
	}
	var aliasRows []aliasRow
	if err := db.Model(&models.ModelAlias{}).
		Select("alias, group_id, real_model").
		Where("enabled = ?", true).
		Scan(&aliasRows).Error; err != nil {
		return nil, err
	}
	aliasIndex := map[string][]string{}
	for _, r := range aliasRows {
		key := aliasKey(r.GroupID, r.RealModel)
		aliasIndex[key] = append(aliasIndex[key], r.Alias)
	}

	// Assemble + sort.
	out := make([]DedupFamily, 0, len(familyToEntries))
	for fam, entries := range familyToEntries {
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].realModel != entries[j].realModel {
				return entries[i].realModel < entries[j].realModel
			}
			return entries[i].groupName < entries[j].groupName
		})
		modelEntries := make([]DedupModelEntry, 0, len(entries))
		for _, e := range entries {
			aliases := aliasIndex[aliasKey(e.groupID, e.realModel)]
			sort.Strings(aliases)
			modelEntries = append(modelEntries, DedupModelEntry{
				GroupID:   e.groupID,
				GroupName: e.groupName,
				RealModel: e.realModel,
				Aliases:   aliases,
			})
		}
		out = append(out, DedupFamily{
			Family:     fam,
			GroupCount: len(familyGroups[fam]),
			Models:     modelEntries,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GroupCount != out[j].GroupCount {
			return out[i].GroupCount > out[j].GroupCount
		}
		if len(out[i].Models) != len(out[j].Models) {
			return len(out[i].Models) > len(out[j].Models)
		}
		return out[i].Family < out[j].Family
	})
	return out, nil
}

// candidateModelsForGroup returns the set of real_model strings the group
// is willing to serve, applying the routing-mode rules described above.
func candidateModelsForGroup(g *models.Group) map[string]struct{} {
	out := map[string]struct{}{}
	if g.ModelRoutingMode == "specified" {
		for m := range parseStringSet(g.ExposedModels) {
			out[m] = struct{}{}
		}
		if len(out) > 0 {
			return out
		}
		// fall through to available_models — matches picker degrade behaviour
	}
	for m := range parseStringSet(g.AvailableModels) {
		out[m] = struct{}{}
	}
	return out
}

// parseStringSet decodes a datatypes.JSON containing a string array into a set.
// Empty / invalid JSON yields an empty set.
func parseStringSet(raw datatypes.JSON) map[string]struct{} {
	out := map[string]struct{}{}
	if len(raw) == 0 {
		return out
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return out
	}
	for _, s := range arr {
		if s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}

func aliasKey(groupID uint, realModel string) string {
	return fmt.Sprintf("%d::%s", groupID, realModel)
}
```

- [ ] **Step 2: Add new imports to `model_dedup_service.go`**

Replace the package declaration block at the top of the file:

```go
package services

import (
	"encoding/json"
	"fmt"
	"sort"

	"autogateway/internal/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)
```

- [ ] **Step 3: Verify backend compiles**

Run: `go build ./...`
Expected: no compile errors. If `deriveFamily` is reported undefined, confirm it remains exported (file-private but in the same package) by checking `internal/services/alias_suggestion_service.go:294`.

- [ ] **Step 4: Commit**

```bash
git add internal/services/model_dedup_service.go
git commit -m "✨ feat(model-dedup): GetModelsByFamily 返回家族分组的完整候选集"
```

---

## Task 2: Backend — `GetModels` handler + route registration

**Files:**
- Modify: `internal/handler/dedup_handler.go`
- Modify: `internal/router/router.go`

- [ ] **Step 1: Add `GetModels` handler and deprecate `GetSuggestions`**

Edit `internal/handler/dedup_handler.go`. Add a `*gorm.DB` field to `DedupHandler` (the new endpoint needs a DB handle for the alias lookup) and the handler method. Update `NewDedupHandler` accordingly.

```go
package handler

import (
	"net/http"
	"strings"

	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type DedupHandler struct {
	dedupService *services.ModelDedupService
	aliasService *services.AliasService
	db           *gorm.DB
}

func NewDedupHandler(
	dedupService *services.ModelDedupService,
	aliasService *services.AliasService,
	db *gorm.DB,
) *DedupHandler {
	return &DedupHandler{
		dedupService: dedupService,
		aliasService: aliasService,
		db:           db,
	}
}

// GetModels returns every candidate model from non-aggregate groups,
// grouped by derived family. Used by the Aliases page's "快速整理" tab.
func (h *DedupHandler) GetModels(c *gin.Context) {
	families, err := h.dedupService.GetModelsByFamily(h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"families": families})
}

// Deprecated: legacy duplicate-only feed; superseded by GetModels.
// Kept for one release to avoid breaking any consumer; remove after frontend
// stops calling it (no callers as of this change).
func (h *DedupHandler) GetSuggestions(c *gin.Context) {
	suggestions := h.dedupService.GetDedupSuggestions()
	c.JSON(http.StatusOK, suggestions)
}
```

Leave `CreateAliasUnification` and the request types unchanged below.

- [ ] **Step 2: Update `NewDedupHandler` call site**

The handler is constructed in `cmd/` or `internal/wire/` style code. Find the constructor call:

Run: `grep -rn "NewDedupHandler" /Users/zac/code/github/api-center --include="*.go"`

For each call site, add the DB argument. The DB handle is typically available as `database.DB` or via `db *gorm.DB` already in scope of the wiring code. Pattern:

```go
// before:
dedupHandler := handler.NewDedupHandler(dedupService, aliasService)
// after:
dedupHandler := handler.NewDedupHandler(dedupService, aliasService, db)
```

If the wiring file uses a different DB variable name, use whatever is already in scope at that call site. Do not introduce a new `gorm.DB` instance.

- [ ] **Step 3: Register the new route**

Edit `internal/router/router.go:154-159`. Replace the dedup block with:

```go
	// Model Dedup API — `/suggestions` is deprecated, use `/models` instead.
	dedup := api.Group("/dedup")
	{
		dedup.GET("/models", dedupHandler.GetModels)
		dedup.GET("/suggestions", dedupHandler.GetSuggestions)
		dedup.POST("/create", dedupHandler.CreateAliasUnification)
	}
```

- [ ] **Step 4: Verify backend builds and route registers**

Run: `go build ./...`
Expected: clean build.

Then run: `go run ./cmd/...` (or whatever the standard entry is — e.g. `go run cmd/server/main.go`). With the server up:

Run: `curl -sS -H "Authorization: Bearer <YOUR_AUTH_KEY>" http://localhost:8080/api/dedup/models | head -c 500`
Expected: JSON shaped `{"families":[{"family":"...", "group_count":N, "models":[...]}, ...]}` or `{"families":[]}` if no non-aggregate groups exist.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/dedup_handler.go internal/router/router.go <wire-call-site-file>
git commit -m "✨ feat(api): GET /api/dedup/models 暴露家族分组的候选模型"
```

---

## Task 3: Frontend — `web/src/api/dedup.ts` typed client

**Files:**
- Create: `web/src/api/dedup.ts`

- [ ] **Step 1: Create the file**

```ts
// Typed wrappers for the dedup endpoints used by the Aliases "快速整理" tab.
// Mirrors the shape of internal/services/model_dedup_service.go DedupFamily.

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

function authHeader(): string {
  const k = localStorage.getItem("authKey");
  return k ? `Bearer ${k}` : "";
}

export const dedupApi = {
  async models(): Promise<DedupFamily[]> {
    const r = await fetch("/api/dedup/models", {
      headers: { Authorization: authHeader() },
    });
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    const data = await r.json();
    return (data?.families ?? []) as DedupFamily[];
  },

  async create(req: DedupCreateRequest): Promise<DedupCreateResponse> {
    const r = await fetch("/api/dedup/create", {
      method: "POST",
      headers: {
        Authorization: authHeader(),
        "Content-Type": "application/json",
      },
      body: JSON.stringify(req),
    });
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    return (await r.json()) as DedupCreateResponse;
  },
};
```

- [ ] **Step 2: Verify TypeScript compiles**

Run: `cd web && pnpm typecheck` (or `npm run typecheck` — check `package.json` `scripts`)
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add web/src/api/dedup.ts
git commit -m "✨ feat(web/api): dedup typed client (models / create)"
```

---

## Task 4: Frontend — Extract `AliasManageTab.vue` from `Aliases.vue`

**Files:**
- Create: `web/src/components/aliases/AliasManageTab.vue`
- Modify: `web/src/views/Aliases.vue`

This is a verbatim extraction. The current `Aliases.vue` body (template + script + scoped styles) is moved into `AliasManageTab.vue`. The view file is temporarily reduced to a single child — the tab shell comes in Task 8.

- [ ] **Step 1: Create `AliasManageTab.vue` with current `Aliases.vue` content**

```bash
mkdir -p web/src/components/aliases
cp web/src/views/Aliases.vue web/src/components/aliases/AliasManageTab.vue
```

No code change inside the new file at this point — the file is the verbatim source of `Aliases.vue` as it stands.

- [ ] **Step 2: Replace `Aliases.vue` body with a one-child wrapper**

Overwrite `web/src/views/Aliases.vue` with:

```vue
<script setup lang="ts">
import AliasManageTab from "@/components/aliases/AliasManageTab.vue";
</script>

<template>
  <AliasManageTab />
</template>
```

- [ ] **Step 3: Verify the page still renders**

Run: `cd web && pnpm dev` (background). Open `http://localhost:5173/aliases` (or whatever the dev port is). The page should look identical to before.

Run: `cd web && pnpm typecheck`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add web/src/components/aliases/AliasManageTab.vue web/src/views/Aliases.vue
git commit -m "♻️ refactor(aliases): 抽出 AliasManageTab 组件，为 tab shell 让路"
```

---

## Task 5: Frontend — Aliases tab shell with `?tab=` query param

**Files:**
- Modify: `web/src/views/Aliases.vue`
- Create: `web/src/components/aliases/AliasQuickSetupTab.vue` (placeholder)

- [ ] **Step 1: Create a placeholder `AliasQuickSetupTab.vue`**

```vue
<script setup lang="ts">
// Body comes in Tasks 6-9. Placeholder lets the tab shell render today.
</script>

<template>
  <div style="padding: 32px; color: var(--v3-ink-3); font-size: 13px">
    快速整理 — 即将上线
  </div>
</template>
```

- [ ] **Step 2: Replace `Aliases.vue` with the tab shell**

```vue
<script setup lang="ts">
import { computed } from "vue";
import { NTabs, NTabPane } from "naive-ui";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import AliasManageTab from "@/components/aliases/AliasManageTab.vue";
import AliasQuickSetupTab from "@/components/aliases/AliasQuickSetupTab.vue";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();

const VALID_TABS = ["manage", "quick"] as const;
type TabKey = (typeof VALID_TABS)[number];

const activeTab = computed<TabKey>({
  get() {
    const raw = (route.query.tab as string) || "manage";
    return (VALID_TABS as readonly string[]).includes(raw) ? (raw as TabKey) : "manage";
  },
  set(val) {
    router.replace({ query: { ...route.query, tab: val } });
  },
});
</script>

<template>
  <NTabs v-model:value="activeTab" type="line" animated>
    <NTabPane name="manage" :tab="t('aliases.tabManage')">
      <AliasManageTab />
    </NTabPane>
    <NTabPane name="quick" :tab="t('aliases.tabQuick')">
      <AliasQuickSetupTab />
    </NTabPane>
  </NTabs>
</template>
```

- [ ] **Step 3: Add the two tab labels to all three locales**

In each of `web/src/locales/zh-CN.ts`, `web/src/locales/en-US.ts`, `web/src/locales/ja-JP.ts`, find the `aliases:` namespace (it already exists; if not, add it next to the `dedup` block). Add these two keys at the top of `aliases:`:

```ts
// zh-CN
aliases: {
  tabManage: "管理",
  tabQuick: "快速整理",
  // ...existing keys
}

// en-US
aliases: {
  tabManage: "Manage",
  tabQuick: "Quick Setup",
  // ...existing keys
}

// ja-JP
aliases: {
  tabManage: "管理",
  tabQuick: "クイック設定",
  // ...existing keys
}
```

If `aliases:` does not exist in a given locale, create it with just these two keys. (Other namespaces such as `v3.aliasesTitle` remain where they are.)

- [ ] **Step 4: Verify both tabs render and `?tab=` updates URL**

Run: `cd web && pnpm dev`. Open `http://localhost:5173/aliases` — should show "管理" tab active. Click "快速整理" — URL should change to `?tab=quick`, body shows the placeholder text. Refresh on `?tab=quick` — should land back on quick tab. Run typecheck.

- [ ] **Step 5: Commit**

```bash
git add web/src/views/Aliases.vue web/src/components/aliases/AliasQuickSetupTab.vue web/src/locales/zh-CN.ts web/src/locales/en-US.ts web/src/locales/ja-JP.ts
git commit -m "✨ feat(aliases): NTabs shell + ?tab= query param 同步"
```

---

## Task 6: Frontend — `FamilyAccordion` + `ModelEntryRow` with data load

**Files:**
- Create: `web/src/components/aliases/quick/ModelEntryRow.vue`
- Create: `web/src/components/aliases/quick/FamilyAccordion.vue`
- Modify: `web/src/components/aliases/AliasQuickSetupTab.vue`

This task wires the data load and renders the family list with checkboxes — but no submit logic yet. After this task the user can browse and search candidates; selection state lives in the parent component for later tasks to consume.

- [ ] **Step 1: Add quick setup i18n keys (zh-CN, en-US, ja-JP)**

In each locale's `aliases:` namespace, add a `quick:` sub-namespace. zh-CN values:

```ts
aliases: {
  tabManage: "管理",
  tabQuick: "快速整理",
  quick: {
    searchPlaceholder: "搜索模型 / 家族,如 OSS-120 / claude",
    familyMeta: "{n} 模型 · {g} 组",
    otherFamily: "其他模型",
    selectionEmpty: "尚未选择模型",
    selectionCount: "已选 {n} 项",
    createButton: "创建别名: {family}",
    appendButton: "追加到 {alias}",
    nameRequired: "请输入别名名称",
    selectAtLeastOne: "至少选择一个模型",
    createdN: "已添加 {n} 条",
    partialFailures: "成功 {ok} / 失败 {fail}",
    failureModalTitle: "未成功的行",
    loadFailed: "加载候选模型失败",
    aliasChipPrefix: "属于",
    targetCardHint: "点击切换为追加目标",
  },
  // ...
}
```

en-US values:

```ts
quick: {
  searchPlaceholder: "Search models or families, e.g. OSS-120 / claude",
  familyMeta: "{n} models · {g} groups",
  otherFamily: "Other models",
  selectionEmpty: "No models selected",
  selectionCount: "{n} selected",
  createButton: "Create alias: {family}",
  appendButton: "Append to {alias}",
  nameRequired: "Enter an alias name",
  selectAtLeastOne: "Select at least one model",
  createdN: "Added {n} entries",
  partialFailures: "Succeeded {ok} / Failed {fail}",
  failureModalTitle: "Failed rows",
  loadFailed: "Failed to load candidate models",
  aliasChipPrefix: "in",
  targetCardHint: "Click to set as append target",
},
```

ja-JP values:

```ts
quick: {
  searchPlaceholder: "モデル / ファミリーを検索 (OSS-120, claude…)",
  familyMeta: "{n} モデル · {g} グループ",
  otherFamily: "その他のモデル",
  selectionEmpty: "未選択",
  selectionCount: "{n} 件選択",
  createButton: "別名を作成: {family}",
  appendButton: "{alias} に追加",
  nameRequired: "別名を入力してください",
  selectAtLeastOne: "1 件以上選択してください",
  createdN: "{n} 件追加しました",
  partialFailures: "成功 {ok} / 失敗 {fail}",
  failureModalTitle: "失敗した行",
  loadFailed: "候補モデルの読み込みに失敗",
  aliasChipPrefix: "所属",
  targetCardHint: "クリックして追加先に設定",
},
```

- [ ] **Step 2: Create `ModelEntryRow.vue`**

```vue
<script setup lang="ts">
import { computed } from "vue";
import { NCheckbox } from "naive-ui";
import { useI18n } from "vue-i18n";
import type { DedupModelEntry } from "@/api/dedup";

const props = defineProps<{
  entry: DedupModelEntry;
  selected: boolean;
  highlightAlias: string | null; // when set, rows containing this alias get accent border
  searchQuery: string;
}>();

const emit = defineEmits<{
  (e: "toggle", entry: DedupModelEntry): void;
  (e: "click-alias", alias: string): void;
}>();

const { t } = useI18n();

const isHighlighted = computed(
  () => !!props.highlightAlias && props.entry.aliases.includes(props.highlightAlias),
);

// Highlight matched substring in the model name. Case-insensitive.
const nameSegments = computed(() => {
  const q = props.searchQuery.trim().toLowerCase();
  const name = props.entry.real_model;
  if (!q) return [{ text: name, hit: false }];
  const lower = name.toLowerCase();
  const segs: { text: string; hit: boolean }[] = [];
  let i = 0;
  while (i < name.length) {
    const idx = lower.indexOf(q, i);
    if (idx === -1) {
      segs.push({ text: name.slice(i), hit: false });
      break;
    }
    if (idx > i) segs.push({ text: name.slice(i, idx), hit: false });
    segs.push({ text: name.slice(idx, idx + q.length), hit: true });
    i = idx + q.length;
  }
  return segs;
});
</script>

<template>
  <label class="quick-row" :class="{ 'quick-row--hl': isHighlighted, 'quick-row--sel': selected }">
    <NCheckbox
      :checked="selected"
      @update:checked="emit('toggle', entry)"
    />
    <span class="quick-row__group">{{ entry.group_name }}</span>
    <span class="quick-row__sep">→</span>
    <code class="quick-row__model">
      <template v-for="(seg, i) in nameSegments" :key="i">
        <mark v-if="seg.hit" class="quick-row__hit">{{ seg.text }}</mark>
        <template v-else>{{ seg.text }}</template>
      </template>
    </code>
    <span class="quick-row__chips">
      <button
        v-for="a in entry.aliases"
        :key="a"
        type="button"
        class="quick-row__alias-chip"
        :title="t('aliases.quick.aliasChipPrefix') + ' ' + a"
        @click.prevent="emit('click-alias', a)"
      >
        {{ a }}
      </button>
    </span>
  </label>
</template>

<style scoped>
.quick-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  border: 1px solid var(--v3-line);
  border-radius: 4px;
  background: var(--v3-surface-2);
  cursor: pointer;
  transition: border-color 120ms, background 120ms;
}
.quick-row:hover {
  border-color: var(--v3-accent);
}
.quick-row--sel {
  border-color: var(--v3-accent);
  background: var(--v3-accent-soft);
}
.quick-row--hl {
  border-color: var(--v3-accent);
  box-shadow: 0 0 0 1px var(--v3-accent-soft);
}
.quick-row__group {
  font: 600 12px var(--v3-sans);
  color: var(--v3-ink);
  min-width: 120px;
}
.quick-row__sep {
  color: var(--v3-ink-4);
  font-size: 11px;
}
.quick-row__model {
  font: 500 12px var(--v3-mono);
  color: var(--v3-ink-2);
  background: var(--v3-surface);
  padding: 2px 6px;
  border-radius: 3px;
  flex: 1;
  word-break: break-all;
}
.quick-row__hit {
  background: var(--v3-warn-soft, oklch(0.95 0.05 80));
  color: var(--v3-warn);
  padding: 0 1px;
  border-radius: 2px;
}
.quick-row__chips {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.quick-row__alias-chip {
  font: 600 10px var(--v3-mono);
  padding: 2px 6px;
  border-radius: 999px;
  border: 1px solid var(--v3-accent);
  color: var(--v3-accent);
  background: transparent;
  cursor: pointer;
}
.quick-row__alias-chip:hover {
  background: var(--v3-accent-soft);
}
</style>
```

- [ ] **Step 3: Create `FamilyAccordion.vue`**

```vue
<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NIcon } from "naive-ui";
import { ChevronDownOutline, ChevronForwardOutline } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";
import type { DedupFamily, DedupModelEntry } from "@/api/dedup";
import ModelEntryRow from "./ModelEntryRow.vue";

const props = defineProps<{
  families: DedupFamily[];
  searchQuery: string;
  selected: Map<string, DedupModelEntry>;
  highlightAlias: string | null;
}>();

const emit = defineEmits<{
  (e: "toggle", entry: DedupModelEntry): void;
  (e: "click-alias", alias: string): void;
}>();

const { t } = useI18n();

function entryKey(e: DedupModelEntry): string {
  return `${e.group_id}::${e.real_model}`;
}

// Per-family expanded state. Default rule: groupCount > 1 expands.
const expanded = ref<Record<string, boolean>>({});
function toggleFamily(fam: string) {
  expanded.value[fam] = !expanded.value[fam];
}
watch(
  () => props.families,
  fams => {
    const next: Record<string, boolean> = {};
    for (const f of fams) next[f.family] = f.group_count > 1;
    expanded.value = next;
  },
  { immediate: true },
);

// Search filter: match family substring OR any model substring.
const visibleFamilies = computed(() => {
  const q = props.searchQuery.trim().toLowerCase();
  if (!q) return props.families;
  const out: DedupFamily[] = [];
  for (const f of props.families) {
    const famHit = f.family.toLowerCase().includes(q);
    const modelHits = f.models.filter(m => m.real_model.toLowerCase().includes(q));
    if (famHit) {
      out.push(f);
      // family-level hit auto-expands
      expanded.value[f.family] = true;
    } else if (modelHits.length > 0) {
      out.push({ ...f, models: modelHits });
      expanded.value[f.family] = true;
    }
  }
  return out;
});

function familyLabel(fam: string): string {
  return fam || t("aliases.quick.otherFamily");
}
</script>

<template>
  <div class="qfam">
    <div v-for="f in visibleFamilies" :key="f.family || '__empty__'" class="qfam__group">
      <button
        type="button"
        class="qfam__head"
        :aria-expanded="!!expanded[f.family]"
        @click="toggleFamily(f.family)"
      >
        <NIcon
          :component="expanded[f.family] ? ChevronDownOutline : ChevronForwardOutline"
          :size="13"
        />
        <span class="qfam__name">{{ familyLabel(f.family) }}</span>
        <span class="qfam__meta">
          {{ t("aliases.quick.familyMeta", { n: f.models.length, g: f.group_count }) }}
        </span>
      </button>
      <div v-if="expanded[f.family]" class="qfam__rows">
        <ModelEntryRow
          v-for="m in f.models"
          :key="entryKey(m)"
          :entry="m"
          :selected="selected.has(entryKey(m))"
          :highlight-alias="highlightAlias"
          :search-query="searchQuery"
          @toggle="(e) => emit('toggle', e)"
          @click-alias="(a) => emit('click-alias', a)"
        />
      </div>
    </div>
    <div v-if="!visibleFamilies.length" class="qfam__empty">
      {{ t("modelcatalog.noData") }}
    </div>
  </div>
</template>

<style scoped>
.qfam {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.qfam__group {
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  overflow: hidden;
  background: var(--v3-surface);
}
.qfam__head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 10px 14px;
  background: var(--v3-surface-2);
  border: 0;
  cursor: pointer;
  font: 600 12px var(--v3-sans);
  color: var(--v3-ink);
}
.qfam__head:hover {
  background: var(--v3-surface-3, var(--v3-surface-2));
}
.qfam__name {
  font: 700 13px var(--v3-mono);
  color: var(--v3-ink);
}
.qfam__meta {
  margin-left: auto;
  font: 500 11px var(--v3-mono);
  color: var(--v3-ink-3);
}
.qfam__rows {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
}
.qfam__empty {
  padding: 40px;
  text-align: center;
  color: var(--v3-ink-4);
  font-size: 12px;
}
</style>
```

- [ ] **Step 4: Replace placeholder `AliasQuickSetupTab.vue` with data-loaded body**

```vue
<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { NInput, NSpin, useMessage } from "naive-ui";
import { useI18n } from "vue-i18n";
import { dedupApi, type DedupFamily, type DedupModelEntry } from "@/api/dedup";
import FamilyAccordion from "./quick/FamilyAccordion.vue";

const { t } = useI18n();
const message = useMessage();

const loading = ref(false);
const families = ref<DedupFamily[]>([]);
const searchQuery = ref("");

// Selection state lives here; child rows are stateless.
const selected = ref(new Map<string, DedupModelEntry>());
function entryKey(e: DedupModelEntry): string {
  return `${e.group_id}::${e.real_model}`;
}
function onToggle(entry: DedupModelEntry) {
  const key = entryKey(entry);
  if (selected.value.has(key)) {
    selected.value.delete(key);
  } else {
    selected.value.set(key, entry);
  }
  // trigger reactivity — Map mutations are not deeply reactive
  selected.value = new Map(selected.value);
}

// Append-target placeholder for Task 7 — defined here so FamilyAccordion can
// already receive the prop.
const targetAlias = ref<string | null>(null);

async function load() {
  loading.value = true;
  try {
    families.value = await dedupApi.models();
  } catch {
    message.error(t("aliases.quick.loadFailed"));
  } finally {
    loading.value = false;
  }
}

function onClickAlias(alias: string) {
  // Wired in Task 7; for now just set the target so the highlight works.
  targetAlias.value = targetAlias.value === alias ? null : alias;
}

onMounted(load);

const selectionCount = computed(() => selected.value.size);
</script>

<template>
  <div class="qsetup">
    <div class="qsetup__top">
      <NInput
        v-model:value="searchQuery"
        :placeholder="t('aliases.quick.searchPlaceholder')"
        clearable
        size="medium"
      />
      <span class="qsetup__count">
        {{ selectionCount === 0
          ? t("aliases.quick.selectionEmpty")
          : t("aliases.quick.selectionCount", { n: selectionCount }) }}
      </span>
    </div>

    <NSpin :show="loading">
      <div class="qsetup__body">
        <div class="qsetup__main">
          <FamilyAccordion
            :families="families"
            :search-query="searchQuery"
            :selected="selected"
            :highlight-alias="targetAlias"
            @toggle="onToggle"
            @click-alias="onClickAlias"
          />
        </div>
        <div class="qsetup__side">
          <!-- ExistingAliasesPanel goes here in Task 7 -->
          <div style="padding: 16px; color: var(--v3-ink-4); font-size: 12px">
            (右栏 — Task 7)
          </div>
        </div>
      </div>
    </NSpin>

    <!-- SubmitActionBar goes here in Task 8 -->
  </div>
</template>

<style scoped>
.qsetup {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px 0;
}
.qsetup__top {
  display: flex;
  align-items: center;
  gap: 12px;
}
.qsetup__count {
  font: 500 11.5px var(--v3-mono);
  color: var(--v3-ink-3);
  white-space: nowrap;
}
.qsetup__body {
  display: grid;
  grid-template-columns: 1fr 280px;
  gap: 16px;
  min-height: 400px;
}
.qsetup__main {
  min-width: 0;
}
.qsetup__side {
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  background: var(--v3-surface);
  align-self: flex-start;
}
</style>
```

- [ ] **Step 5: Verify**

Run: `cd web && pnpm dev`. Open `http://localhost:5173/aliases?tab=quick`. The page should:
- Load and render families grouped by `deriveFamily` (multi-group families auto-expanded)
- Show search input filtering both family and model substrings, with hits highlighted
- Toggle checkboxes; selection counter updates
- Show alias chips on rows that already belong to an alias

Run typecheck.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/aliases/quick/ModelEntryRow.vue web/src/components/aliases/quick/FamilyAccordion.vue web/src/components/aliases/AliasQuickSetupTab.vue web/src/locales/zh-CN.ts web/src/locales/en-US.ts web/src/locales/ja-JP.ts
git commit -m "✨ feat(aliases/quick): family accordion + 模型行 + 搜索高亮"
```

---

## Task 7: Frontend — `ExistingAliasesPanel` + append-target highlight

**Files:**
- Create: `web/src/components/aliases/quick/ExistingAliasesPanel.vue`
- Modify: `web/src/components/aliases/AliasQuickSetupTab.vue`

- [ ] **Step 1: Create `ExistingAliasesPanel.vue`**

```vue
<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { DedupFamily } from "@/api/dedup";

const props = defineProps<{
  families: DedupFamily[];
  targetAlias: string | null;
}>();

const emit = defineEmits<{
  (e: "select", alias: string | null): void;
}>();

const { t } = useI18n();

// Reserved aliases always show, even when no row currently maps to them.
const RESERVED = ["simple", "medium", "complex"];

const aliasList = computed(() => {
  const counts = new Map<string, number>();
  for (const f of props.families) {
    for (const m of f.models) {
      for (const a of m.aliases) {
        counts.set(a, (counts.get(a) ?? 0) + 1);
      }
    }
  }
  for (const r of RESERVED) {
    if (!counts.has(r)) counts.set(r, 0);
  }
  return Array.from(counts.entries())
    .map(([alias, count]) => ({ alias, count, reserved: RESERVED.includes(alias) }))
    .sort((a, b) => {
      const ai = RESERVED.indexOf(a.alias);
      const bi = RESERVED.indexOf(b.alias);
      if (ai !== -1 && bi !== -1) return ai - bi;
      if (ai !== -1) return -1;
      if (bi !== -1) return 1;
      return a.alias.localeCompare(b.alias);
    });
});

function select(alias: string) {
  emit("select", props.targetAlias === alias ? null : alias);
}
</script>

<template>
  <div class="qpanel">
    <div class="qpanel__head">已存在别名</div>
    <div class="qpanel__list">
      <button
        v-for="row in aliasList"
        :key="row.alias"
        type="button"
        class="qpanel__card"
        :class="{
          'qpanel__card--active': row.alias === targetAlias,
          'qpanel__card--reserved': row.reserved,
        }"
        :title="t('aliases.quick.targetCardHint')"
        @click="select(row.alias)"
      >
        <span class="qpanel__alias">{{ row.alias }}</span>
        <span class="qpanel__meta">{{ row.count }}</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.qpanel {
  display: flex;
  flex-direction: column;
}
.qpanel__head {
  padding: 12px 14px;
  font: 700 11px var(--v3-mono);
  color: var(--v3-ink-3);
  text-transform: uppercase;
  border-bottom: 1px solid var(--v3-line);
  background: var(--v3-surface-2);
}
.qpanel__list {
  display: flex;
  flex-direction: column;
  padding: 8px;
  gap: 4px;
}
.qpanel__card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border: 1px solid var(--v3-line);
  border-radius: 4px;
  background: var(--v3-surface);
  cursor: pointer;
  transition: border-color 120ms, background 120ms;
  font: inherit;
  color: inherit;
  text-align: left;
}
.qpanel__card:hover {
  border-color: var(--v3-accent);
}
.qpanel__card--active {
  border-color: var(--v3-accent);
  background: var(--v3-accent-soft);
}
.qpanel__card--reserved {
  border-color: oklch(from var(--v3-warn) l c h / 0.3);
}
.qpanel__alias {
  font: 600 12.5px var(--v3-mono);
  color: var(--v3-ink);
}
.qpanel__meta {
  font: 500 11px var(--v3-mono);
  color: var(--v3-ink-3);
}
</style>
```

- [ ] **Step 2: Wire it into `AliasQuickSetupTab.vue`**

Replace the right-pane placeholder block in `AliasQuickSetupTab.vue`:

```vue
<!-- old -->
<div class="qsetup__side">
  <div style="padding: 16px; color: var(--v3-ink-4); font-size: 12px">
    (右栏 — Task 7)
  </div>
</div>
```

with:

```vue
<div class="qsetup__side">
  <ExistingAliasesPanel
    :families="families"
    :target-alias="targetAlias"
    @select="(a) => (targetAlias = a)"
  />
</div>
```

Add the import alongside the existing imports in the same `<script setup>` block:

```ts
import ExistingAliasesPanel from "./quick/ExistingAliasesPanel.vue";
```

Replace the existing `onClickAlias` function body with:

```ts
function onClickAlias(alias: string) {
  targetAlias.value = targetAlias.value === alias ? null : alias;
}
```

(It is already this — confirm no regressions. The behavior is identical, but Task 7 makes it semantically correct now that the right pane drives the same state.)

- [ ] **Step 3: Verify highlight + selection roundtrip**

Run dev server. On `?tab=quick`:
- The right panel lists `simple / medium / complex` plus any other aliases extracted from current data.
- Click `simple` → it highlights in the panel; rows whose `aliases[]` contains `simple` get the accent border in the main list.
- Click `simple` again → de-selects, highlights clear.
- Clicking an alias chip on a row also activates that alias in the right panel (via the `click-alias` event already wired).

Run typecheck.

- [ ] **Step 4: Commit**

```bash
git add web/src/components/aliases/quick/ExistingAliasesPanel.vue web/src/components/aliases/AliasQuickSetupTab.vue
git commit -m "✨ feat(aliases/quick): ExistingAliasesPanel + 联动高亮"
```

---

## Task 8: Frontend — `SubmitActionBar` + create/append flow

**Files:**
- Create: `web/src/components/aliases/quick/SubmitActionBar.vue`
- Modify: `web/src/components/aliases/AliasQuickSetupTab.vue`

This task closes the loop: name input + submit button + partial-failure modal + tab switch with target-alias highlight on the manage tab.

- [ ] **Step 1: Add a tiny `deriveFamily` helper to the frontend (mirrors backend logic, kept local since the helper is small)**

Append to `web/src/api/dedup.ts`:

```ts
const VARIANT_TOKENS = new Set([
  "lite","mini","nano","tiny","small","medium","large","xl","xxl",
  "pro","plus","max","ultra",
  "flash","fast","turbo","thinking","reasoner",
  "vision","image","video","audio",
  "tts","embed","embedding","rerank",
  "chat","instruct","code","coder",
  "preview","experimental","exp","beta","rc","free","trial",
  "haiku","sonnet","opus",
]);
const SIZE_RE = /^[0-9]+(?:\.[0-9]+)?[bm]$/;
const DATE_RE = /^(?:[0-9]{6,8}|[0-9]{4}|[vr][0-9]+|rev[0-9]+)$/;

export function deriveFamilyClient(modelID: string): string {
  let s = (modelID ?? "").trim().toLowerCase();
  if (!s) return "";
  const par = s.indexOf("(");
  if (par >= 0) s = s.slice(0, par).trim();
  const colon = s.indexOf(":");
  if (colon >= 0) s = s.slice(0, colon);
  const slash = s.lastIndexOf("/");
  if (slash >= 0) s = s.slice(slash + 1);
  s = s.trim();
  if (!s) return "";
  const parts = s.split("-").filter(p => p);
  const out: string[] = [];
  for (let i = 0; i < parts.length; i++) {
    const p = parts[i];
    if (i > 0 && (VARIANT_TOKENS.has(p) || SIZE_RE.test(p) || DATE_RE.test(p))) break;
    out.push(p);
  }
  return out.join("-");
}
```

- [ ] **Step 2: Create `SubmitActionBar.vue`**

```vue
<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NInput, NButton, NIcon } from "naive-ui";
import { CheckmarkCircle } from "@vicons/ionicons5";
import { useI18n } from "vue-i18n";
import type { DedupModelEntry } from "@/api/dedup";
import { deriveFamilyClient } from "@/api/dedup";

const props = defineProps<{
  selected: Map<string, DedupModelEntry>;
  targetAlias: string | null;
  submitting: boolean;
}>();

const emit = defineEmits<{
  (e: "submit", payload: { alias: string; entries: DedupModelEntry[] }): void;
}>();

const { t } = useI18n();

// Cached user-typed name (preserved across target-alias toggles per spec §6.2).
const typedName = ref("");
watch(
  () => props.targetAlias,
  (next, prev) => {
    if (prev && !next) {
      // target cleared; the typed name stays as-is, ready to be reused
    }
  },
);

const familyHint = computed(() => {
  if (!props.selected.size) return "";
  const counts = new Map<string, number>();
  let firstFamily = "";
  let firstModel = "";
  for (const e of props.selected.values()) {
    const fam = deriveFamilyClient(e.real_model);
    if (!firstModel) firstModel = e.real_model;
    if (!firstFamily) firstFamily = fam;
    if (fam) counts.set(fam, (counts.get(fam) ?? 0) + 1);
  }
  if (!counts.size) return firstModel;
  const sorted = Array.from(counts.entries()).sort((a, b) => {
    if (b[1] !== a[1]) return b[1] - a[1];
    return a[0].localeCompare(b[0]);
  });
  return sorted[0][0];
});

const effectiveName = computed(() => {
  if (props.targetAlias) return props.targetAlias;
  return typedName.value.trim() || familyHint.value;
});

const buttonLabel = computed(() => {
  if (props.targetAlias) {
    return t("aliases.quick.appendButton", { alias: props.targetAlias });
  }
  return t("aliases.quick.createButton", { family: familyHint.value || "—" });
});

const canSubmit = computed(
  () => props.selected.size > 0 && effectiveName.value.length > 0 && !props.submitting,
);

function onSubmit() {
  if (!canSubmit.value) return;
  emit("submit", {
    alias: effectiveName.value,
    entries: Array.from(props.selected.values()),
  });
}
</script>

<template>
  <div class="qbar">
    <div class="qbar__count">
      {{ selected.size === 0
        ? t("aliases.quick.selectionEmpty")
        : t("aliases.quick.selectionCount", { n: selected.size }) }}
    </div>
    <div class="qbar__input">
      <NInput
        v-if="!targetAlias"
        v-model:value="typedName"
        :placeholder="familyHint || t('aliases.quick.nameRequired')"
        size="medium"
      />
      <code v-else class="qbar__locked">{{ targetAlias }}</code>
    </div>
    <NButton
      type="primary"
      :disabled="!canSubmit"
      :loading="submitting"
      @click="onSubmit"
    >
      <template #icon><NIcon :component="CheckmarkCircle" /></template>
      {{ buttonLabel }}
    </NButton>
  </div>
</template>

<style scoped>
.qbar {
  position: sticky;
  bottom: 0;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border: 1px solid var(--v3-line);
  border-radius: 6px;
  background: var(--v3-surface-2);
  box-shadow: var(--v3-shadow-md);
}
.qbar__count {
  font: 500 11.5px var(--v3-mono);
  color: var(--v3-ink-2);
  white-space: nowrap;
}
.qbar__input {
  flex: 1;
}
.qbar__locked {
  font: 600 13px var(--v3-mono);
  padding: 6px 10px;
  background: var(--v3-accent-soft);
  color: var(--v3-accent);
  border-radius: 4px;
  display: inline-block;
}
</style>
```

- [ ] **Step 3: Wire submit + tab switch + glow into `AliasQuickSetupTab.vue`**

Add imports:

```ts
import { useRouter, useRoute } from "vue-router";
import { NModal } from "naive-ui";
import { dedupApi } from "@/api/dedup";
import SubmitActionBar from "./quick/SubmitActionBar.vue";
```

Add state and submit handler in `<script setup>`:

```ts
const router = useRouter();
const route = useRoute();

const submitting = ref(false);
const failureModalOpen = ref(false);
const failures = ref<string[]>([]);

async function onSubmit({ alias, entries }: { alias: string; entries: DedupModelEntry[] }) {
  submitting.value = true;
  try {
    const res = await dedupApi.create({
      alias,
      candidates: entries.map(e => ({ group_id: e.group_id, real_model: e.real_model })),
    });
    if (res.created > 0 && res.failures.length === 0) {
      message.success(t("aliases.quick.createdN", { n: res.created }));
      // Hand off to manage tab and ask it to glow this alias.
      router.replace({
        query: { ...route.query, tab: "manage", highlight: alias },
      });
    } else if (res.created > 0) {
      message.warning(
        t("aliases.quick.partialFailures", { ok: res.created, fail: res.failures.length }),
      );
      failures.value = res.failures;
      failureModalOpen.value = true;
    } else {
      message.error(t("common.requestFailed"));
      failures.value = res.failures;
      failureModalOpen.value = true;
    }
    // Refresh data so newly bound entries reflect their new alias chip.
    await load();
    selected.value = new Map();
    targetAlias.value = null;
  } catch {
    message.error(t("common.requestFailed"));
  } finally {
    submitting.value = false;
  }
}
```

Add to the template, below `</NSpin>`:

```vue
<SubmitActionBar
  :selected="selected"
  :target-alias="targetAlias"
  :submitting="submitting"
  @submit="onSubmit"
/>

<NModal v-model:show="failureModalOpen" preset="dialog" :title="t('aliases.quick.failureModalTitle')">
  <ul style="padding-left: 20px; margin: 0; max-height: 260px; overflow-y: auto">
    <li v-for="(f, i) in failures" :key="i" style="font: 500 12px var(--v3-mono); margin: 4px 0">
      {{ f }}
    </li>
  </ul>
</NModal>
```

- [ ] **Step 4: Add the manage-tab glow effect**

Edit `web/src/components/aliases/AliasManageTab.vue` (the verbatim copy from Task 4). Add a watcher that consumes `?highlight=` and applies a transient class to the matched alias card. Inside the existing `<script setup>`:

```ts
import { useRoute, useRouter } from "vue-router";

const route = useRoute();
const router = useRouter();
const highlightAlias = ref<string | null>(null);

watch(
  () => route.query.highlight,
  raw => {
    const a = typeof raw === "string" ? raw : null;
    if (!a) return;
    highlightAlias.value = a;
    setTimeout(() => {
      highlightAlias.value = null;
      // Strip the query param after the glow ends.
      const { highlight: _drop, ...rest } = route.query;
      router.replace({ query: rest });
    }, 1500);
  },
  { immediate: true },
);
```

Add `'v5-alias-card--glow': grp.alias === highlightAlias` to the `:class` binding of the custom-alias card in the template (the loop over `customAliases`). Add the same binding to the tier-board cards by the same `tier.id === highlightAlias` check.

Add the keyframe + class at the end of the scoped `<style>`:

```css
@keyframes v5-alias-glow {
  0% { box-shadow: 0 0 0 0 var(--v3-accent); }
  50% { box-shadow: 0 0 0 6px var(--v3-accent-soft); }
  100% { box-shadow: 0 0 0 0 transparent; }
}
.v5-alias-card--glow {
  animation: v5-alias-glow 1.5s ease-out 1;
  border-color: var(--v3-accent) !important;
}
```

- [ ] **Step 5: Verify end-to-end**

Run dev server. On `?tab=quick`:
- Select 2-3 models from one family. The bottom bar shows the count and the family hint inside the input placeholder. Submit. Should: toast "已添加 N 条" → switch to `?tab=manage&highlight=<family>` → the matching alias card glows for ~1.5 s.
- Select 1 model that already exists in some `(alias, group, model)` triplet (e.g. retry the same submit). Should: failure modal opens listing the conflict; partial successes still apply.
- Click an alias in the right panel (e.g. `simple`) → submit appends to it. Manage tab glows the `simple` card.

Run typecheck.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/aliases/quick/SubmitActionBar.vue web/src/components/aliases/AliasQuickSetupTab.vue web/src/components/aliases/AliasManageTab.vue web/src/api/dedup.ts
git commit -m "✨ feat(aliases/quick): SubmitActionBar + 提交流程 + 管理 Tab 高亮联动"
```

---

## Task 9: Cleanup — delete `ModelDedup.vue`, redirect, remove nav entry

**Files:**
- Delete: `web/src/views/ModelDedup.vue`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/components/Layout.vue`

- [ ] **Step 1: Replace the `model-dedup` route with a redirect**

Edit `web/src/router/index.ts`. Replace the existing `model-dedup` block (lines 46-50):

```ts
// before:
{
  path: "model-dedup",
  name: "model-dedup",
  component: () => import("@/views/ModelDedup.vue"),
},
// after:
{
  path: "model-dedup",
  redirect: { path: "/aliases", query: { tab: "quick" } },
},
```

- [ ] **Step 2: Delete `ModelDedup.vue`**

```bash
rm web/src/views/ModelDedup.vue
```

- [ ] **Step 3: Remove `model-dedup` from the nav rail**

Edit `web/src/components/Layout.vue:40-51`. Drop the line:

```ts
{ name: "model-dedup", icon: CopyOutline, tip: t("nav.modelDedup") },
```

If `CopyOutline` is unused after this removal, also drop it from the `@vicons/ionicons5` import in the same file.

- [ ] **Step 4: Verify**

Run: `cd web && pnpm dev`.
- Sidebar no longer shows "模型去重".
- `http://localhost:5173/model-dedup` → URL replaced with `/aliases?tab=quick`, quick setup tab loads.
- `pnpm typecheck` clean.

- [ ] **Step 5: Commit**

```bash
git add web/src/router/index.ts web/src/components/Layout.vue
git rm web/src/views/ModelDedup.vue
git commit -m "🔥 refactor(nav): 删除 ModelDedup 视图，model-dedup 路由 redirect 至 /aliases?tab=quick"
```

---

## Self-Review Notes

(Run after Task 9 completes; not a separate task — the engineer reads this and confirms the spec is fully covered.)

- ✅ Spec §4 (IA): Tasks 5 and 9 cover the tab shell, `?tab=` query param, redirect, and nav removal.
- ✅ Spec §5 (Backend): Tasks 1 and 2 cover `GET /api/dedup/models`, the family-grouped response shape, scoping rules (aggregate / blocked / specified mode fallback), and the deprecation comment on `GetSuggestions`. Sorting (group_count DESC, then model count DESC, then family alphabetical; within-family by real_model then group_name) is implemented in Task 1.
- ✅ Spec §6 (Frontend): Tasks 3-8 cover the API client, component extraction, family accordion, search highlighting, alias chip per row, right pane, append-target highlight, family hint, name input lock-in-append-mode preserving typed text, and submit-then-switch-and-glow flow.
- ✅ Spec §7 (Edge cases):
  - Empty result set → `FamilyAccordion` shows the `modelcatalog.noData` empty hint (Task 6 step 3).
  - Triplet conflict → surfaces in the partial-failure modal with the verbatim error from `AliasService.Create` (Task 8 step 3).
  - Reserved alias → `ExistingAliasesPanel` always lists `simple/medium/complex` even if zero rows currently map (Task 7 step 1).
  - Empty family → bucket key `""` rendered as `aliases.quick.otherFamily` translation (Task 6 step 1 + 3).
  - Mixed-family selection → `familyHint` falls back to most-frequent then first model name (Task 8 step 2).
- ✅ Tests: explicitly out of scope per user direction (acknowledged in plan header).

---

**Plan complete and saved to `docs/superpowers/plans/2026-05-02-alias-quick-setup.md`.**
