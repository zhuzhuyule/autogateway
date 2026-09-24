# 模型别名「管理」页重构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `/aliases?tab=manage` from a three-tier board into an alias-centric list + candidate-pool drawer, and make the numbers on it (24h share, error rate, the auto switch) actually mean what they say.

**Architecture:** Backend gets three surgical changes — a transactional `POST /api/aliases/expose`, an `errors`/`error_rate` column on the existing unbounded `ModelTimings` aggregate, and `PickForAuto` finally honoring `Settings.Enabled`. Frontend: a module-level composable (`web/src/services/aliases.ts`) owns all loading + derivation, and five focused components render it; the 2403-line `AliasManageTab.vue` becomes a ~200-line container. The four parallel view modes collapse into one list + filters.

**Tech Stack:** Go 1.x + gin + gorm + sqlite (`glebarez/sqlite`), Vue 3 `<script setup>` + naive-ui + vue-i18n (three locales).

**Spec:** `docs/superpowers/specs/2026-09-24-alias-manage-page-redesign-design.md`

---

## Amendments to the spec (decided while planning)

These four override the spec; the spec is updated by Task 16.

1. **§3.1/§3.2 "p50" → "24h 平均".** `ModelTimings` aggregates `AVG(duration)`. A real p50 needs a percentile query. Labeling an average "p50" is exactly the "numbers that lie" this redesign exists to kill, so the column reads 平均耗时.
2. **§4.3 "从档位移除" gets no new UI.** Whether an alias *is* a tier is purely its name (`simple|medium|complex`); the tier role is display-only. Removing a candidate from a tier already exists (delete the row); adding one already exists (candidate picker). No rename-to-reserved path (`RenameAlias` correctly refuses reserved targets), so no endpoint is added for it.
3. **§3.1 "按 provider 分组" → "按分组筛选".** An alias spans several groups, so grouping aliases by provider is ill-defined. Instead the list gets a single-select "只看某分组" filter, which answers the real question ("what breaks if I delete group X?").
4. **§6 `ModelAliasModal.vue` is not rewritten.** It is a different entry point (one model → its aliases) and 800 lines of working code. It keeps its UI and only switches to the shared `DEFAULT_WEIGHT` / `DEFAULT_PRIORITY` constants. §6 的"复用抽屉"降级为 follow-up.
5. **`AliasCandidateList.vue` is deleted, not reused.** The list rows are not expandable in-row (the drawer is the detail surface), so its only consumer disappears. `types.ts` keeps `CandidateState`/`STATE_LABEL_KEY`; `StatePill.vue` / `MetricCell.vue` stay.

## File structure

**Backend (modify):** `internal/services/alias_service.go`, `internal/handler/alias_handler.go`, `internal/router/router.go`, `internal/handler/dashboard_handler.go`, `internal/router_engine/selector.go`, `internal/router_engine/middleware.go`
**Backend (test):** `internal/services/alias_service_test.go` (new), `internal/handler/dashboard_usage_test.go` (modify), `internal/router_engine/selector_test.go` (modify)
**Frontend (new):** `web/src/services/aliases.ts`, `web/src/components/aliases/AliasList.vue`, `web/src/components/aliases/AutoRoutingPanel.vue`, `web/src/components/aliases/AliasPickerModal.vue`, `web/src/components/aliases/AliasSuggestDrawer.vue`
**Frontend (rename+modify):** `AliasEditDrawer.vue` → `AliasDetailDrawer.vue`
**Frontend (modify):** `AliasManageTab.vue`, `components/aliases/types.ts`, `api/aliases.ts`, `api/dashboard.ts`, `locales/{zh-CN,en-US,ja-JP}.ts`, `components/keys/ModelAliasModal.vue`
**Frontend (delete):** `AliasTableView.vue`, `AliasSplitView.vue`, `AliasHealthView.vue`, `AliasCandidateList.vue`

Order matters: backend first (Tasks 1–5) so the frontend has endpoints to call; then the data layer (6–8); then extraction of existing code (9–11); then the new surfaces (12–14); then container + deletion (15); then docs/verification (16–17).

---

## Phase P1 — backend

### Task 1: `AliasService.ExposeCandidate`

**Files:**
- Modify: `internal/services/alias_service.go` (append after `RenameAlias`, ~line 459)
- Test: `internal/services/alias_service_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `internal/services/alias_service_test.go`. Follow the sqlite pattern in `internal/router_engine/middleware_integration_test.go:51-69` (in-memory sqlite + `Logger: logger.Default.LogMode(logger.Silent)` + `AutoMigrate`).

```go
package services

import (
	"context"
	"testing"

	"autogateway/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newAliasTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Group{}, &models.ModelAlias{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestExposeCandidate_AddsToSpecifiedGroup(t *testing.T) {
	ctx := context.Background()
	db := newAliasTestDB(t)
	g := models.Group{
		Name:             "nvidia",
		ModelRoutingMode: "specified",
		ExposedModels:    datatypes.JSON(`["llama-3.1-8b"]`),
	}
	if err := db.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	row := models.ModelAlias{Alias: "hermes", GroupID: g.ID, RealModel: "llama-3.3-70b", Weight: 1, Enabled: true}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}

	svc := NewAliasService(db)
	status, err := svc.ExposeCandidate(ctx, "hermes", g.ID, "llama-3.3-70b")
	if err != nil {
		t.Fatalf("ExposeCandidate: %v", err)
	}
	if status != ExposeAdded {
		t.Fatalf("status = %q, want %q", status, ExposeAdded)
	}
	var got models.Group
	if err := db.First(&got, g.ID).Error; err != nil {
		t.Fatalf("reload group: %v", err)
	}
	if !jsonContainsString(string(got.ExposedModels), "llama-3.3-70b") {
		t.Fatalf("exposed_models = %s, want it to contain the model", got.ExposedModels)
	}
	if !jsonContainsString(string(got.ExposedModels), "llama-3.1-8b") {
		t.Fatalf("exposed_models = %s, want the pre-existing entry kept", got.ExposedModels)
	}
}
```

`jsonContainsString` lives in `internal/router_engine/selector.go` (unexported, other package). Inline a local helper in the test file instead of importing it:

```go
func containsModel(raw datatypes.JSON, model string) bool {
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return false
	}
	return slices.Contains(arr, model)
}
```

Add `"encoding/json"` and `"slices"` to the test imports and use `containsModel` in place of `jsonContainsString` in the two assertions above.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/services/ -run TestExposeCandidate -v`
Expected: compile failure — `undefined: ExposeCandidate` / `svc.ExposeCandidate undefined`.

- [ ] **Step 3: Write the remaining cases (still failing)**

Append to the same file:

```go
// 幂等: 已经在暴露列表里 → already_ok, 且不动 JSON(不产生重复项)。
func TestExposeCandidate_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := newAliasTestDB(t)
	g := models.Group{
		Name: "groq", ModelRoutingMode: "specified",
		ExposedModels: datatypes.JSON(`["llama-3.3-70b"]`),
	}
	if err := db.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Create(&models.ModelAlias{
		Alias: "hermes", GroupID: g.ID, RealModel: "llama-3.3-70b", Weight: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	svc := NewAliasService(db)
	status, err := svc.ExposeCandidate(ctx, "hermes", g.ID, "llama-3.3-70b")
	if err != nil {
		t.Fatalf("ExposeCandidate: %v", err)
	}
	if status != ExposeAlreadyOk {
		t.Fatalf("status = %q, want %q", status, ExposeAlreadyOk)
	}
	var got models.Group
	if err := db.First(&got, g.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	var arr []string
	if err := json.Unmarshal(got.ExposedModels, &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 1 {
		t.Fatalf("exposed_models = %v, want no duplicate appended", arr)
	}
}

// passthrough 分组没有白名单概念, 请求该模型本来就是可达的。
func TestExposeCandidate_PassthroughNotNeeded(t *testing.T) {
	db := newAliasTestDB(t)
	g := models.Group{Name: "openai", ModelRoutingMode: "passthrough"}
	if err := db.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Create(&models.ModelAlias{
		Alias: "x", GroupID: g.ID, RealModel: "gpt-4o", Weight: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	status, err := NewAliasService(db).ExposeCandidate(context.Background(), "x", g.ID, "gpt-4o")
	if err != nil {
		t.Fatalf("ExposeCandidate: %v", err)
	}
	if status != ExposeNotNeeded {
		t.Fatalf("status = %q, want %q", status, ExposeNotNeeded)
	}
}

// 黑名单命中时补暴露列表是无效动作(filterByExposed 先拒 blocked),
// 必须回报错误而不是假装成功。
func TestExposeCandidate_BlockedModelRejected(t *testing.T) {
	db := newAliasTestDB(t)
	g := models.Group{
		Name: "zhipu", ModelRoutingMode: "specified",
		BlockedModels: datatypes.JSON(`["glm-4"]`),
	}
	if err := db.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Create(&models.ModelAlias{
		Alias: "y", GroupID: g.ID, RealModel: "glm-4", Weight: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	_, err := NewAliasService(db).ExposeCandidate(context.Background(), "y", g.ID, "glm-4")
	if err == nil {
		t.Fatal("expected an error for a blocked model, got nil")
	}
	var g2 models.Group
	if err := db.First(&g2, g.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(g2.ExposedModels) > 2 {
		t.Fatalf("exposed_models changed to %s, want untouched on error", g2.ExposedModels)
	}
}

// 别名行不存在时不去改分组 —— 过期 UI 上的一次点击不该静默暴露一个模型。
func TestExposeCandidate_RequiresAliasRow(t *testing.T) {
	db := newAliasTestDB(t)
	g := models.Group{Name: "mistral", ModelRoutingMode: "specified"}
	if err := db.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	_, err := NewAliasService(db).ExposeCandidate(context.Background(), "ghost", g.ID, "mistral-7b")
	if err == nil {
		t.Fatal("expected an error when the alias candidate does not exist")
	}
}

// 聚合分组不参与 alias 落点, 直接拒绝。
func TestExposeCandidate_RejectsAggregateGroup(t *testing.T) {
	db := newAliasTestDB(t)
	g := models.Group{Name: "agg", GroupType: "aggregate", ModelRoutingMode: "specified"}
	if err := db.Create(&g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Create(&models.ModelAlias{
		Alias: "a", GroupID: g.ID, RealModel: "m", Weight: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	_, err := NewAliasService(db).ExposeCandidate(context.Background(), "a", g.ID, "m")
	if err == nil {
		t.Fatal("expected an error for an aggregate group")
	}
}
```

- [ ] **Step 4: Implement**

Append to `internal/services/alias_service.go` (before `nonZero` at the end of the file). Add `"encoding/json"` to the import block.

```go
// ExposeCandidate 把候选的 real_model 补进目标分组的 exposed_models。
//
// 为什么需要它: 前端「未公开此模型」的处置之前是 router.push 跳到密钥页, 让
// admin 自己找那个模型卡片上的"+加入"。一个状态修复被做成了跨页编排, 而且
// 补 exposed 这一步在前端另一条路径里重复实现过一遍(createAliasCandidates),
// 两处必然漂移。下沉到服务端后, 就地一次调用完成。
//
// 只处理 specified 模式的分组: passthrough 下模型本来可达, 返回 ExposeNotNeeded。
// 黑名单命中的模型明确报错 —— 补白名单不会让它变可达(filterByExposed 先拒
// blocked), 静默返回"成功"就是骗人。
func (s *AliasService) ExposeCandidate(
	ctx context.Context, alias string, groupID uint, realModel string,
) (string, error) {
	alias = strings.TrimSpace(alias)
	realModel = strings.TrimSpace(realModel)
	if alias == "" || realModel == "" || groupID == 0 {
		return "", app_errors.NewAPIError(app_errors.ErrValidation,
			"alias, group_id and real_model are required")
	}

	var row models.ModelAlias
	if err := s.db.WithContext(ctx).
		Where("alias = ? AND group_id = ? AND real_model = ?", alias, groupID, realModel).
		First(&row).Error; err != nil {
		return "", app_errors.NewAPIError(app_errors.ErrNotFound,
			"alias candidate not found")
	}

	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return "", app_errors.ParseDBError(err)
	}
	if group.GroupType == "aggregate" {
		return "", app_errors.NewAPIError(app_errors.ErrValidation,
			"aggregate groups have no exposed model list")
	}
	if group.ModelRoutingMode != "specified" {
		return ExposeNotNeeded, nil
	}
	if jsonArrContains(group.BlockedModels, realModel) {
		return "", app_errors.NewAPIError(app_errors.ErrValidation,
			"model is blocked in this group")
	}
	if jsonArrContains(group.ExposedModels, realModel) {
		return ExposeAlreadyOk, nil
	}

	exposed, err := jsonAppendString(group.ExposedModels, realModel)
	if err != nil {
		return "", err
	}
	if err := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("id = ?", groupID).
		Update("exposed_models", datatypes.JSON(exposed)).Error; err != nil {
		return "", app_errors.ParseDBError(err)
	}
	return ExposeAdded, nil
}

// jsonArrContains reports whether a datatypes.JSON column holding a string
// array contains v. NULL / empty / malformed all read as "no".
func jsonArrContains(raw datatypes.JSON, v string) bool {
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return false
	}
	return slices.Contains(arr, v)
}

// jsonAppendString returns the JSON of raw-with-v-appended, preserving what
// was already there. An unparseable column is reported as an error rather
// than silently replaced — losing an admin's exposed list to a stray byte
// would be far worse than a failed click.
func jsonAppendString(raw datatypes.JSON, v string) ([]byte, error) {
	arr := []string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, app_errors.NewAPIError(app_errors.ErrInternal,
				"group exposed_models is not a JSON string array")
		}
	}
	arr = append(arr, v)
	out, err := json.Marshal(arr)
	if err != nil {
		return nil, app_errors.NewAPIError(app_errors.ErrInternal, err.Error())
	}
	return out, nil
}
```

Add the status constants next to `ReservedAliases` (top of the file) and `"slices"` + `"gorm.io/datatypes"` to the imports:

```go
// ExposeCandidate outcomes. They are part of the API contract: the frontend
// shows a different message per value, and `not_needed` is a success, not an
// error.
const (
	ExposeAdded      = "added"
	ExposeAlreadyOk  = "already_ok"
	ExposeNotNeeded  = "not_needed"
)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/services/ -run TestExposeCandidate -v`
Expected: PASS × 5.

If `app_errors.ErrNotFound` / `ErrInternal` are not declared, check `internal/errors/` and use whatever sentinel the neighbors use (`grep -n "Err[A-Za-z]* = \|Err[A-Za-z]*$" internal/errors/*.go`) — `ParseDBError` already returns a not-found-shaped error for `gorm.ErrRecordNotFound`, so `ErrNotFound` may be spelled differently in this repo. Match the existing spelling; do not invent a new code.

- [ ] **Step 6: Commit**

```bash
git add internal/services/alias_service.go internal/services/alias_service_test.go
git commit -m "✨ feat(aliases): ExposeCandidate — 就地补分组暴露列表的服务端实现"
```

### Task 2: `POST /api/aliases/expose` endpoint

**Files:**
- Modify: `internal/handler/alias_handler.go` (append)
- Modify: `internal/router/router.go:177` area

- [ ] **Step 1: Add the handler**

Append to `internal/handler/alias_handler.go`. Static segment `expose` alongside the existing `candidates` / `rename` PUTs — gin 1.10 lets statics win over `/:id`, and this is a POST so there is no `POST /:id` to collide with anyway.

```go
// Expose adds one alias candidate's model to its group's exposed list.
//
// Body carries the alias rather than the path: same reason as RenameAlias —
// a second parameter segment at the top of the /aliases tree.
func (h *AliasHandler) Expose(c *gin.Context) {
	var req struct {
		Alias     string `json:"alias"`
		GroupID   uint   `json:"group_id"`
		RealModel string `json:"real_model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	status, err := h.svc.ExposeCandidate(c.Request.Context(), req.Alias, req.GroupID, req.RealModel)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"alias": req.Alias, "group_id": req.GroupID, "model": req.RealModel,
		}).Warn("expose alias candidate failed")
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	response.Success(c, gin.H{"status": status})
}
```

- [ ] **Step 2: Register the route**

In `internal/router/router.go`, inside the `aliases` group, next to `aliases.PUT("/candidates", …)`:

```go
		// 就地公开候选所在分组的模型(状态修复, 不再跳页)
		aliases.POST("/expose", aliasHandler.Expose)
```

- [ ] **Step 3: Build**

Run: `go build ./... && go vet ./internal/handler/ ./internal/router/`
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add internal/handler/alias_handler.go internal/router/router.go
git commit -m "✨ feat(api): POST /api/aliases/expose"
```

### Task 3: Error rate on `/api/dashboard/model-timings`

**Files:**
- Modify: `internal/handler/dashboard_handler.go` (`ModelTiming` struct ~line 264, `ModelTimings` query ~line 330)
- Test: `internal/handler/dashboard_usage_test.go`

Why here and not `TopModels`: `TopModels` (`dashboard_handler.go:297`) caps at `Limit(50)` ordered by calls, so a long alias list would silently lose its tail. `ModelTimings` is already the unbounded variant.

- [ ] **Step 1: Write the failing test**

Append to `internal/handler/dashboard_usage_test.go` (reuse its existing request-log seeding helper; the file already creates `models.RequestLog` rows with `IsSuccess` — see line 47):

```go
// ModelTimings 的错误率与 TopModels 同口径 (SUM(CASE WHEN is_success …)),
// 但不带 LIMIT —— 别名列表要用它给每一个别名标错误率。
func TestModelTimings_ReportsErrorRate(t *testing.T) {
	db, router := newUsageTestRouter(t)   // adjust to the helpers this file already defines
	seed(db, "hermes", true, 100)
	seed(db, "hermes", true, 120)
	seed(db, "hermes", false, 900)
	seed(db, "other", true, 50)

	var out struct {
		Data []struct {
			Model     string  `json:"model"`
			Calls     int64   `json:"calls"`
			Errors    int64   `json:"errors"`
			ErrorRate float64 `json:"error_rate"`
			AvgMs     int64   `json:"avg_ms"`
		} `json:"data"`
	}
	rec := getJSON(router, "/api/dashboard/model-timings?window=24h")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	byModel := map[string]int64{}
	var hermesRate float64
	var hermesErrs int64
	for _, r := range out.Data {
		byModel[r.Model] = r.Calls
		if r.Model == "hermes" {
			hermesRate = r.ErrorRate
			hermesErrs = r.Errors
		}
	}
	if byModel["hermes"] != 3 {
		t.Fatalf("hermes calls = %d, want 3", byModel["hermes"])
	}
	if hermesErrs != 1 {
		t.Fatalf("hermes errors = %d, want 1", hermesErrs)
	}
	if hermesRate < 0.32 || hermesRate > 0.34 {
		t.Fatalf("hermes error_rate = %v, want ~0.3333", hermesRate)
	}
}
```

Read the top of `dashboard_usage_test.go` first and reuse the helpers it already has for building a gin router + seeding logs. If it has none, build the router the way `internal/handler/video_task_handler_test.go` does and inline a `seed` closure — do not add a second harness.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/handler/ -run TestModelTimings_ReportsErrorRate -v`
Expected: FAIL — `errors`/`error_rate` come back as 0 because the columns don't exist yet.

- [ ] **Step 3: Implement**

In `internal/handler/dashboard_handler.go`, add two fields to `ModelTiming`:

```go
	// Errors / ErrorRate share TopModels' definition (rows where is_success is
	// false within the window). ErrorRate is 0..1 so the frontend formats it.
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
```

In `ModelTimings`, extend the `Select` and the scan struct and compute the rate in the loop:

```go
	err := s.DB.Model(&models.RequestLog{}).
		Select("model, COUNT(*) as calls, AVG(duration) as avg_ms, "+
			"COALESCE(SUM(total_tokens),0) as tokens, COALESCE(SUM(cost_usd),0) as cost_usd, "+
			"SUM(CASE WHEN is_success THEN 0 ELSE 1 END) as errors").
		Where("timestamp >= ? AND request_type = ? AND model IS NOT NULL AND model != ''", since, models.RequestTypeFinal).
		Group("model").
		Scan(&rows).Error
```

Add `Errors int64` to the local `row` struct, then in the output loop:

```go
		var rate float64
		if r.Calls > 0 {
			rate = float64(r.Errors) / float64(r.Calls)
		}
		out = append(out, ModelTiming{
			…
			Errors:    r.Errors,
			ErrorRate: math.Round(rate*10000) / 10000,
		})
```

Add `"math"` to the imports if it is not already there.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/handler/ -run TestModelTimings -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/dashboard_handler.go internal/handler/dashboard_usage_test.go
git commit -m "✨ feat(api): model-timings 增错误率(无 LIMIT 口径)"
```

### Task 4: `PickForAuto` honors the switch

**Files:**
- Modify: `internal/router_engine/selector.go:403-413`
- Modify: `internal/router_engine/middleware.go:75-83,154-157`
- Test: `internal/router_engine/selector_test.go`

- [ ] **Step 1: Write the failing test**

`selector_test.go` has no sqlite harness; `middleware_integration_test.go:51` does but is a different file in the same package, so the helper is already reachable. Append to `selector_test.go`:

```go
// 关闭智能路由 ≠ 报错, 也 ≠ 透传: model="auto" 固定走 simple 档。
// 之前 Settings.Enabled 存了也读了, 但选路从不查它 —— 关掉之后照样按阈值分档,
// 界面那个开关是假的。
func TestPickForAuto_DisabledFallsBackToSimpleTier(t *testing.T) {
	db := newIntegrationDB(t)
	group := models.Group{Name: "g1", GroupType: "standard", ModelRoutingMode: "passthrough"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Create(&models.APIKey{
		GroupID: group.ID, APIKey: "sk-active-1", Status: "active",
	}).Error; err != nil {
		t.Fatalf("create key: %v", err)
	}
	// 估算 tokens = 100_000 → 阈值本该判成 complex; 关闭后必须落 simple。
	if err := db.Create(&models.ModelAlias{
		Alias: "complex", GroupID: group.ID, RealModel: "big-model", Weight: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed complex: %v", err)
	}
	if err := db.Create(&models.ModelAlias{
		Alias: "simple", GroupID: group.ID, RealModel: "small-model", Weight: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed simple: %v", err)
	}

	s := NewSelector(db, store.NewMemoryStore(), nil)

	s.UpdateSettings(Settings{Enabled: true, SimpleThreshold: 2000, ComplexThreshold: 8000})
	on, err := s.PickForAuto(context.Background(), 100_000)
	if err != nil {
		t.Fatalf("PickForAuto(enabled): %v", err)
	}
	if on.RealModel != "big-model" {
		t.Fatalf("enabled tier pick = %q, want big-model", on.RealModel)
	}

	s.UpdateSettings(Settings{Enabled: false, SimpleThreshold: 2000, ComplexThreshold: 8000})
	off, err := s.PickForAuto(context.Background(), 100_000)
	if err != nil {
		t.Fatalf("PickForAuto(disabled): %v", err)
	}
	if off.RealModel != "small-model" {
		t.Fatalf("disabled pick = %q, want small-model (simple tier)", off.RealModel)
	}
}

// simple 档没有候选时沿用既有「无候选」错误, 不新造错误码。
func TestPickForAuto_DisabledWithEmptySimplePoolErrors(t *testing.T) {
	db := newIntegrationDB(t)
	s := NewSelector(db, store.NewMemoryStore(), nil)
	s.UpdateSettings(Settings{Enabled: false})
	if _, err := s.PickForAuto(context.Background(), 10); err == nil {
		t.Fatal("expected an error when the simple pool is empty")
	}
}
```

`UpdateSettings` persists to `system_settings`, which `newIntegrationDB` already automigrates (`middleware_integration_test.go:64`). `NewSelector`'s third parameter is `MetaProvider` — check `middleware_integration_test.go` for what it passes (nil, or its mock resolver) and mirror that. `models.APIKey`'s active-status field name must be read from `internal/models/types.go` (the filter is `filterByActiveKeys`); adjust the seed to the real column before running.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/router_engine/ -run TestPickForAuto -v`
Expected: first test FAILs on `disabled pick` (it still returns `big-model`); the empty-pool one passes today and must stay green.

- [ ] **Step 3: Implement in `selector.go`**

```go
// PickForAuto resolves the smart-routing pool by token estimate.
//
// 关闭智能路由时不是"报错"也不是"透传": model="auto" 固定走 simple 档。
// 界面那个开关必须真的影响选路, 否则它只是一个存在 system_settings 里的
// 布尔值。simple 池为空时沿用 PickByAlias 的「无候选」错误。
func (s *Selector) PickForAuto(ctx context.Context, estimatedTokens int) (*Candidate, error) {
	cfg := s.GetSettings()
	tier := TierMedium
	switch {
	case !cfg.Enabled:
		tier = TierSimple
	case estimatedTokens < cfg.SimpleThreshold:
		tier = TierSimple
	case estimatedTokens >= cfg.ComplexThreshold:
		tier = TierComplex
	}
	return s.PickByAlias(ctx, ReservedAlias(tier))
}
```

- [ ] **Step 4: Skip sticky read/write when auto is off**

Sticky exists to keep one conversation on one destination. With the switch off there is no tier decision to freeze, and a 30-minute lock would keep a session pinned to a candidate that the admin can no longer reach through the tier it used to belong to. In `internal/router_engine/middleware.go`, replace the sticky-read block (`if isMultiTurn && sessionKey != "" { … }`) and the sticky-write line (`if stickyKey != "" { s.SetSticky(stickyKey, picked) }`) so both are gated:

```go
		sessionKey, isMultiTurn := parseSession(bodyBytes, c.Request.Header)
		// 关闭智能路由后 model="auto" 固定走 simple 档, 没有"选档"结果需要粘住;
		// 留着 sticky 反而会把会话锁在一个管理员已经从档位里摘掉的候选上。
		stickyRouting := model != "auto" || s.GetSettings().Enabled
		var stickyKey string
		var picked *Candidate
		if isMultiTurn && sessionKey != "" && stickyRouting {
			stickyKey = stickyStoreKey(sessionKey, model)
			if cand := s.GetSticky(stickyKey); cand != nil && s.IsCandidateAlive(c.Request.Context(), cand) {
				picked = cand
			}
		}
```

The write side stays as-is — `stickyKey` is simply empty when routing is off, so `if stickyKey != ""` already skips it.

- [ ] **Step 5: Run the whole package**

Run: `go test ./internal/router_engine/ -v 2>&1 | tail -30`
Expected: PASS, including the three existing sticky contracts in `middleware_integration_test.go`.

- [ ] **Step 6: Commit**

```bash
git add internal/router_engine/selector.go internal/router_engine/middleware.go internal/router_engine/selector_test.go
git commit -m "🐛 fix(router): 智能路由开关真的参与选路, 关闭时 auto 固定走 simple"
```

### Task 5: Backend gate

- [ ] `go build ./... && go vet ./... && go test ./...` — all green. Record the baseline output in the commit-less step; nothing ships until this is clean.

---

## Phase P1 — frontend data layer

### Task 6: API client additions

**Files:**
- Modify: `web/src/api/aliases.ts`
- Modify: `web/src/api/dashboard.ts`

- [ ] **Step 1: `exposeModel`**

In `aliasesApi`, after `rename`:

```ts
  /**
   * 就地公开某个候选所在分组的模型。
   * status: added(已补进暴露列表) / already_ok(本来就公开) / not_needed
   * (分组是 passthrough, 无需操作) —— 三种都是成功。
   */
  exposeModel: (alias: string, groupId: number, realModel: string) =>
    http.post<{ status: "added" | "already_ok" | "not_needed" }>(
      "/aliases/expose",
      { alias, group_id: groupId, real_model: realModel },
      { hideMessage: true }
    ),
```

The route is registered as **POST** (`internal/router/router.go`, next to the `candidates`/`rename` PUTs) — PUT would 404. Confirm after editing: `grep -n "aliases/expose" src/api/aliases.ts && grep -n 'aliases.POST("/expose' ../../internal/router/router.go`.

- [ ] **Step 2: Timing type**

In `web/src/api/dashboard.ts`, add to `ModelTiming`:

```ts
  /** 窗口内失败请求数(与 top-models 同口径)。 */
  errors: number;
  /** 0..1 */
  error_rate: number;
```

- [ ] **Step 3: Type-check**

Run: `cd web && npx vue-tsc --noEmit -p tsconfig.app.json 2>&1 | head -20`
Expected: no new errors mentioning `dashboard.ts` / `aliases.ts`. (Consumers that build `ModelTiming` literals will need the two fields — that is Task 7's job. Record any such errors and fix them there.)

- [ ] **Step 4: Commit**

```bash
git add web/src/api/aliases.ts web/src/api/dashboard.ts
git commit -m "✨ feat(web): 别名 API 客户端 — exposeModel / 错误率字段"
```

### Task 7: `useAliasData` shared store

**Files:**
- Create: `web/src/services/aliases.ts`
- Modify: `web/src/components/aliases/types.ts`

This is the piece that makes the container thin: every load, every derivation, in one place, module-level so the aliases page and the keys modal read the same refs.

- [ ] **Step 1: Extend the shared view types**

Replace `web/src/components/aliases/types.ts` with (comment header kept, new fields added):

```ts
// 别名页各视图共用的类型。
//
// 为什么单独放一个 .ts: `<script setup>` 里不能 `export` 类型, 而列表和详情抽屉
// 需要共享同一份数据结构。放在这里比在组件之间 import 类型更清楚, 也避免每个
// 消费方各定义一套导致字段漂移。
import type { ModelAliasRow } from "@/api/aliases";

/**
 * 一个候选在运行时不参与选路的原因。
 * 判定见 services/aliases.ts 的 candidateState(), 优先级: disabled > blocked > unexposed。
 */
export type CandidateState = "usable" | "disabled" | "unexposed" | "blocked";

/** 候选行 + 渲染所需的全部派生信息。由 store 算好, 组件只负责画。 */
export interface CandidateRowView {
  row: ModelAliasRow;
  /** 分组展示名。 */
  groupName: string;
  /** 分组 channel_type, 用作 provider 标签; 缺失时回退到 groupName。 */
  providerLabel: string;
  /** providerLabel 是否是回退值 —— tooltip 要说清楚, 不能让用户以为是真 provider。 */
  providerIsFallback: boolean;
  /** 24h 平均耗时(ms), 0 表示无样本。 */
  avgMs: number;
  /** 24h 折算成本文案, 空串表示无数据。 */
  cost: string;
  /** 配置占比(按 weight 归一化的百分比), 同别名内相加约 100。 */
  share: number;
  /**
   * 24h 实际调用数, 按 **(分组, 请求名=本别名)** 归因。
   * 日志落的是请求名(改写前), 别名路由下它就是别名本身 —— 所以不能用
   * real_model 去查(那样永远是 0)。
   */
  calls: number;
  /** 同一别名里还有别的候选属于同一分组; 此时 calls 是分组级合计。 */
  callsSharedGroup: boolean;
  state: CandidateState;
}

/**
 * 状态 -> i18n key。列表和抽屉共用一份, 否则各写一套必然漂移。
 */
export const STATE_LABEL_KEY: Record<CandidateState, string> = {
  usable: "v3.aliasStateUsable",
  disabled: "v3.aliasStateDisabled",
  unexposed: "v3.aliasStateUnexposed",
  blocked: "v3.aliasStateBlocked",
};

/** 排障筛选顺序 —— 越靠前越需要处理。 */
export const STATE_TRIAGE_ORDER: CandidateState[] = ["unexposed", "blocked", "disabled"];

export type AutoTier = "" | "simple" | "medium" | "complex";

/** 一个别名 + 它的候选池 + 窗口指标。列表一行 = 一个 AliasView。 */
export interface AliasView {
  alias: string;
  isReserved: boolean;
  /** 该别名是否是 auto 的某个档位(名字等于 simple/medium/complex)。 */
  tier: AutoTier;
  rows: CandidateRowView[];
  usable: number;
  unusable: number;
  total: number;
  /** 窗口内以该别名名请求的总调用数; 无数据为 0。 */
  calls: number;
  errors: number;
  /** 0..1, calls 为 0 时也是 0。 */
  errorRate: number;
  /** 24h 平均耗时(ms)。 */
  avgMs: number;
  /** 24h 折算成本(USD)。 */
  costUsd: number;
  /** 是否有任何候选跨档位复用(同一 (group, model) 也属于别的档位)。 */
  crossTier: boolean;
  /** 首要问题: 用于「有问题」筛选与状态列; 无问题为空串。 */
  problem: "" | "no-candidates" | "unexposed" | "blocked" | "disabled";
}
```

Note the removals relative to the old file: `AliasView.rows[].logoHint` is gone (provider-name guessing is a defect, not a feature), and `STATE_TRIAGE_ORDER` stays only because the filter select uses it.

- [ ] **Step 2: Write the store**

Create `web/src/services/aliases.ts`. Complete file:

```ts
// 别名页的数据层: 加载 + 派生, 不含任何渲染。
//
// 为什么在模块作用域持有 ref 而不是在 setup 里: 别名数据有两个入口(别名页、
// 密钥页的模型卡), 两边看到的必须是同一份状态 —— 一处改完另一处不该等到刷新。
// 仓库没有 pinia, services/ 下 auth.ts / version.ts 已经是同一套 module-level
// ref 的做法, 这里跟着走。
import { computed, ref } from "vue";
import { aliasesApi, RESERVED_ALIASES, routingSettingsApi, type ModelAliasRow, type RoutingSettings } from "@/api/aliases";
import { getModelTimings, getModelTraffic, type ModelTiming } from "@/api/dashboard";
import { keysApi } from "@/api/keys";
import type { Group } from "@/types/models";
import type { AliasView, AutoTier, CandidateRowView, CandidateState } from "@/components/aliases/types";
import { getGroupDisplayName } from "@/utils/display";

/** 新建候选的默认值。两处入口(别名页 / 密钥页弹窗)共用, 不要再各写一份。 */
export const DEFAULT_WEIGHT = 100;
/** 与后端 createOnDB 的 nonZero(req.Priority, 100) 对齐: 这里写 0 会被替换成 100, UI 与库里不一致。 */
export const DEFAULT_PRIORITY = 100;

const loading = ref(false);
const rows = ref<ModelAliasRow[]>([]);
const groups = ref<Group[]>([]);
const settings = ref<RoutingSettings>({ Enabled: true, SimpleThreshold: 2000, ComplexThreshold: 8000 });
/** 按**请求名**索引 —— 别名路由下请求名就是别名, 这才是别名级指标的正确键。 */
const timingsByName = ref<Record<string, ModelTiming>>({});
/** `${group_id}::${请求名}` -> 24h 调用数。 */
const trafficByKey = ref<Record<string, number>>({});
/** 一次 refresh 里 traffic 是否真的取到了(取不到时整列实测占比要隐藏)。 */
const trafficAvailable = ref(false);

export interface GroupInfo {
  mode: string;
  exposed: Set<string>;
  blocked: Set<string>;
  channelType: string;
}

/** Parse a datatypes.JSON-ish column that holds a string array (it arrives as either a real array or a JSON string). */
export function parseStringList(raw: unknown): string[] {
  if (Array.isArray(raw)) {
    return raw.filter((m): m is string => typeof m === "string");
  }
  if (typeof raw === "string" && raw.trim()) {
    try {
      const j = JSON.parse(raw);
      if (Array.isArray(j)) {
        return j.filter((m): m is string => typeof m === "string");
      }
    } catch {
      /* 老数据里存在非法 JSON, 按空处理 */
    }
  }
  return [];
}

const groupInfoById = computed<Record<number, GroupInfo>>(() => {
  const out: Record<number, GroupInfo> = {};
  for (const g of groups.value) {
    if (!g.id) {
      continue;
    }
    const gg = g as unknown as Record<string, unknown>;
    out[g.id] = {
      mode: g.model_routing_mode || "passthrough",
      exposed: new Set(parseStringList(gg.exposed_models)),
      blocked: new Set(parseStringList(gg.blocked_models)),
      channelType: (gg.channel_type as string) || "",
    };
  }
  return out;
});

const groupNameById = computed<Record<number, string>>(() => {
  const m: Record<number, string> = {};
  for (const g of groups.value) {
    if (g.id) {
      m[g.id] = getGroupDisplayName(g);
    }
  }
  return m;
});

// 候选模型列表的口径必须与"模型"页一致, 否则同一个分组在两处给出不同列表:
// specified → exposed_models(为空时降级用 available), passthrough → available_models。
const modelsByGroup = computed<Record<number, string[]>>(() => {
  const out: Record<number, string[]> = {};
  for (const g of groups.value) {
    if (!g.id) {
      continue;
    }
    const gg = g as unknown as Record<string, unknown>;
    const info = groupInfoById.value[g.id];
    let src = info.mode === "specified" ? [...info.exposed] : parseStringList(gg.available_models);
    if (info.mode === "specified" && src.length === 0) {
      src = parseStringList(gg.available_models);
    }
    out[g.id] = src;
  }
  return out;
});

function candidateState(row: ModelAliasRow): CandidateState {
  // 保留别名的占位行(group_id=0)不是候选。
  if (row.is_reserved && row.group_id === 0) {
    return "usable";
  }
  if (!row.enabled) {
    return "disabled";
  }
  const info = groupInfoById.value[row.group_id];
  // blocked_models 命中时无视 routing mode 直接拒绝(见 router_engine.filterByExposed)。
  if (info?.blocked.has(row.real_model)) {
    return "blocked";
  }
  if (info && info.mode === "specified" && !info.exposed.has(row.real_model)) {
    return "unexposed";
  }
  return "usable";
}

function tierOf(alias: string): AutoTier {
  return (RESERVED_ALIASES as readonly string[]).includes(alias) ? (alias as AutoTier) : "";
}

const aliasViews = computed<AliasView[]>(() => {
  const byName = new Map<string, ModelAliasRow[]>();
  const reserved = new Map<string, boolean>();
  for (const r of rows.value) {
    const list = byName.get(r.alias) || [];
    // 占位行不是候选, 但它的存在让空档位在列表里可见。
    if (!(r.is_reserved && r.group_id === 0)) {
      list.push(r);
    }
    byName.set(r.alias, list);
    reserved.set(r.alias, (reserved.get(r.alias) || false) || r.is_reserved);
  }
  for (const name of RESERVED_ALIASES) {
    if (!byName.has(name)) {
      byName.set(name, []);
      reserved.set(name, true);
    }
  }

  // 同一 (group, model) 出现在多个档位 → 跨档位复用, 要显示出来而不是藏起来。
  const tierKeys = new Map<string, Set<string>>();
  for (const [alias, list] of byName.entries()) {
    if (!tierOf(alias)) {
      continue;
    }
    tierKeys.set(alias, new Set(list.map(r => `${r.group_id}:${r.real_model}`)));
  }

  const out: AliasView[] = [];
  for (const [alias, members] of byName.entries()) {
    const weightTotal = members.reduce((s, m) => s + Math.max(m.weight, 0), 0);
    const groupHits: Record<number, number> = {};
    for (const m of members) {
      groupHits[m.group_id] = (groupHits[m.group_id] || 0) + 1;
    }
    let crossTier = false;
    const derived: CandidateRowView[] = members.map(m => {
      const info = groupInfoById.value[m.group_id];
      const name = groupNameById.value[m.group_id] || String(m.group_id);
      const channel = info?.channelType || "";
      const timing = timingsByName.value[alias];
      for (const [otherTier, keys] of tierKeys) {
        // 只在档位别名之间判定"跨档位复用": 自定义别名与档位共用一条候选是
        // 常态, 不是需要提醒的问题。
        if (tierOf(alias) && otherTier !== alias && keys.has(`${m.group_id}:${m.real_model}`)) {
          crossTier = true;
        }
      }
      return {
        row: m,
        groupName: name,
        providerLabel: channel || name,
        providerIsFallback: !channel,
        avgMs: timing?.avg_ms || 0,
        cost: costChip(timing),
        share:
          weightTotal > 0
            ? Math.round((Math.max(m.weight, 0) / weightTotal) * 100)
            : Math.round(100 / Math.max(1, members.length)),
        calls: trafficByKey.value[`${m.group_id}::${alias}`] || 0,
        callsSharedGroup: (groupHits[m.group_id] || 0) > 1,
        state: candidateState(m),
      };
    });
    const usable = derived.filter(r => r.state === "usable").length;
    const unusable = derived.length - usable;
    let problem: AliasView["problem"] = "";
    if (!derived.length) {
      problem = "no-candidates";
    } else if (derived.some(r => r.state === "unexposed")) {
      problem = "unexposed";
    } else if (derived.some(r => r.state === "blocked")) {
      problem = "blocked";
    } else if (derived.some(r => r.state === "disabled")) {
      problem = "disabled";
    }
    const t = timingsByName.value[alias];
    out.push({
      alias,
      isReserved: reserved.get(alias) || false,
      tier: tierOf(alias),
      rows: derived.sort((a, b) => (a.state === b.state ? a.row.real_model.localeCompare(b.row.real_model) : a.state === "usable" ? -1 : 1)),
      usable,
      unusable,
      total: derived.length,
      calls: t?.calls || 0,
      errors: t?.errors || 0,
      errorRate: t?.error_rate || 0,
      avgMs: t?.avg_ms || 0,
      costUsd: t?.cost_usd || 0,
      crossTier,
      problem,
    });
  }
  return out.sort((a, b) => {
    const ai = RESERVED_ALIASES.indexOf(a.tier as (typeof RESERVED_ALIASES)[number]);
    const bi = RESERVED_ALIASES.indexOf(b.tier as (typeof RESERVED_ALIASES)[number]);
    if (ai !== -1 && bi !== -1) return ai - bi;
    if (ai !== -1) return -1;
    if (bi !== -1) return 1;
    return b.calls - a.calls || a.alias.localeCompare(b.alias);
  });
});

/** 成本 chip 文案: 有成本显示折算价格, 免费但有用量显示 token 数, 否则空。 */
function costChip(t?: ModelTiming): string {
  if (!t) return "";
  if (t.cost_usd > 0) return t.cost_usd < 1 ? `$${t.cost_usd.toFixed(4)}` : `$${t.cost_usd.toFixed(2)}`;
  if (t.tokens > 0) {
    const tok = t.tokens >= 1000 ? `${(t.tokens / 1000).toFixed(1)}K` : `${t.tokens}`;
    return `${tok} tok`;
  }
  return "";
}

const problemCount = computed(() => aliasViews.value.filter(a => a.problem).length);

async function loadCore(): Promise<void> {
  const [r, g, s] = await Promise.all([
    aliasesApi.list(),
    keysApi.getGroups(),
    routingSettingsApi.get(),
  ]);
  rows.value = (r as unknown as { data: ModelAliasRow[] }).data || [];
  groups.value = (g || []).filter(gr => gr.id);
  settings.value = (s as unknown as { data: RoutingSettings }).data || settings.value;
}

async function loadMetrics(): Promise<void> {
  try {
    const res = await getModelTimings("24h");
    const list = (res as unknown as { data: ModelTiming[] }).data || [];
    const map: Record<string, ModelTiming> = {};
    for (const t of list) {
      if (t?.model) {
        map[t.model] = t;
      }
    }
    timingsByName.value = map;
  } catch {
    /* 指标是装饰性的 —— 取不到就不显示, 不影响主流程 */
  }
  try {
    const res = await getModelTraffic("24h");
    const list = (res as unknown as { data: Array<{ group_name: string; model: string; calls: number }> }).data || [];
    // 日志里存的是分组**原始 name**(keypool/validator.go 里 GroupName: group.Name),
    // UI 显示的是 display name, 先映射一次, 否则两个名字体系对不上、实测永远为空。
    const nameToId: Record<string, number> = {};
    for (const g of groups.value) {
      if (g.id && g.name) {
        nameToId[g.name] = g.id;
      }
    }
    const map: Record<string, number> = {};
    for (const t of list) {
      const gid = nameToId[t.group_name];
      if (gid) {
        map[`${gid}::${t.model}`] = t.calls;
      }
    }
    trafficByKey.value = map;
    trafficAvailable.value = list.length > 0;
  } catch {
    trafficAvailable.value = false;
  }
}

let inflight: Promise<void> | null = null;

/** 刷新。并发调用共用同一次请求。 */
function refresh(): Promise<void> {
  if (inflight) {
    return inflight;
  }
  loading.value = true;
  // 顺序不能反: loadMetrics 要用 groups 把日志里的分组 name 换成 id。
  inflight = loadCore()
    .then(loadMetrics)
    .catch(e => {
      console.error(e);
      throw e;
    })
    .finally(() => {
      loading.value = false;
      inflight = null;
    });
  return inflight;
}

/** 候选增删改之后只重拉别名行(比重拉整套轻)。 */
async function reloadRows(): Promise<void> {
  const r = await aliasesApi.list();
  rows.value = (r as unknown as { data: ModelAliasRow[] }).data || [];
}

export function useAliasData() {
  return {
    loading,
    rows,
    groups,
    settings,
    timingsByName,
    trafficByKey,
    trafficAvailable,
    aliasViews,
    problemCount,
    groupInfoById,
    groupNameById,
    modelsByGroup,
    refresh,
    reloadRows,
    candidateState,
  };
}
```

Task 10 adds `suggestions` / `suggestionCount` / `loadSuggestions` to this same module (the header badge and the drawer must read one source), so keep the returned object's shape additive rather than reshaping it later.

- [ ] **Step 3: Type-check the store in isolation**

Run: `cd web && npx vue-tsc --noEmit -p tsconfig.app.json 2>&1 | grep "services/aliases.ts" | head`
Expected: no output. Fix every hit — later tasks import these names, and a wrong type here multiplies.

- [ ] **Step 4: Commit**

```bash
git add web/src/services/aliases.ts web/src/components/aliases/types.ts
git commit -m "✨ feat(web): 别名数据层 composable — 统一口径的派生与刷新"
```

### Task 8: Shared defaults in the keys modal

**Files:**
- Modify: `web/src/components/keys/ModelAliasModal.vue`

- [ ] **Step 1: Find the local defaults**

Run: `grep -n "weight\|priority" web/src/components/keys/ModelAliasModal.vue | head -20`
Expected: literal defaults passed to `aliasesApi.create` (historically `weight: 100, priority: 0`, which the backend rewrites to 100 — the inconsistency that made the UI and the DB disagree).

- [ ] **Step 2: Import the constants**

Add to the import block:

```ts
import { DEFAULT_PRIORITY, DEFAULT_WEIGHT } from "@/services/aliases";
```

Replace those literals with `weight: DEFAULT_WEIGHT` / `priority: DEFAULT_PRIORITY`. No other change to this file — its own UI stays (amendment 4).

- [ ] **Step 3: Verify + commit**

Run: `cd web && npx vue-tsc --noEmit -p tsconfig.app.json 2>&1 | grep ModelAliasModal | head`
Expected: no output.

```bash
git add web/src/components/keys/ModelAliasModal.vue
git commit -m "♻️ refactor(web): 别名候选默认值收敛到单一来源"
```

---

## Phase P1 — extract existing surfaces (pure moves)

Each of these three tasks moves code that already works. The rule: **copy verbatim, change only the component boundary.** No behavior edits, no reformatting of untouched lines. After each task the page must still render identically in `npm run dev`.

### Task 9: `AliasPickerModal.vue`

**Files:**
- Create: `web/src/components/aliases/AliasPickerModal.vue`
- Source: `AliasManageTab.vue` script lines 147-293 + 362-380 (`commitPendingPicks`), template lines 1489-1669, and the `.v3-picker-*` rules in its style block

- [ ] **Step 1: Create the component**

```vue
<script setup lang="ts">
// 批量选模型加入某个别名。整块从 AliasManageTab 原样搬出, 职责不变:
// 选 (分组, 模型) 进暂存区, 确认时一次性建候选 + 补 exposed。
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { NButton, NIcon, NInput, NModal } from "naive-ui";
import {
  AddOutline,
  BanOutline,
  CheckmarkCircle,
  CloseOutline,
  LockClosedOutline,
  PulseOutline,
} from "@vicons/ionicons5";
import { isFree } from "@/data/freeProviders";
import { getGroupDisplayName } from "@/utils/display";
import { DEFAULT_PRIORITY, DEFAULT_WEIGHT, useAliasData } from "@/services/aliases";
import type { Group } from "@/types/models";

export interface PendingPick {
  groupId: number;
  groupName: string;
  modelId: string;
}

const props = defineProps<{ show: boolean; alias: string; seed?: PendingPick[] | null }>();
const emit = defineEmits<{
  (e: "update:show", v: boolean): void;
  (e: "created", ok: number, fail: number): void;
}>();

const { t } = useI18n();
const { groups, rows, groupNameById, modelsByGroup, groupInfoById } = useAliasData();

const search = ref("");
const activeGroupId = ref<number | null>(null);
const pending = ref<PendingPick[]>([]);
const submitting = ref(false);

watch(
  () => props.show,
  open => {
    if (!open) {
      return;
    }
    // Stage seeds BEFORE the open watcher flips pending: 家庭建议要预填暂存区。
    pending.value = props.seed ? [...props.seed] : [];
    search.value = "";
    if (!activeGroupId.value && groups.value.length) {
      activeGroupId.value = groups.value.find(g => g.group_type !== "aggregate")?.id || null;
    }
  },
  { immediate: true }
);

const keySet = computed(() => new Set(pending.value.map(p => `${p.groupId}:${p.modelId}`)));

function isPending(modelId: string): boolean {
  return activeGroupId.value ? keySet.value.has(`${activeGroupId.value}:${modelId}`) : false;
}

const filteredModels = computed(() => {
  if (!activeGroupId.value) {
    return [];
  }
  const g = groups.value.find(gr => gr.id === activeGroupId.value);
  if (!g) {
    return [];
  }
  const source = modelsByGroup.value[g.id as number] || [];
  const blocked = groupInfoById.value[g.id as number]?.blocked || new Set<string>();
  const bound = new Set(
    rows.value.filter(r => r.alias === props.alias && r.group_id === g.id).map(r => r.real_model)
  );
  const q = search.value.toLowerCase().trim();
  return (q ? source.filter(m => m.toLowerCase().includes(q)) : source)
    .map(id => ({ id, alreadyBound: bound.has(id), blocked: blocked.has(id) }))
    .sort((a, b) => Number(b.blocked === false && a.alreadyBound) - Number(a.blocked === false && b.alreadyBound) || a.id.localeCompare(b.id));
});
```

**Do not re-derive the sort.** Copy the original `filteredPickerModels` body's free-model detection (`findProviderByUpstreams(g.upstreams || [])` → `isFree(providerId, id)`) and its comparator verbatim from `AliasManageTab.vue:195-240`; the only change is that `pickerTargetAlias.value` becomes `props.alias` and `rows.value`/`groups.value` come from the store. Dropping the free-priority sort is a functional regression.

Continue the script with the originals of `togglePendingPick`, `removePending`, and a commit function:

```ts
async function commit(): Promise<void> {
  if (!props.alias || !pending.value.length) {
    return;
  }
  submitting.value = true;
  const { ok, fail } = await createCandidates(props.alias, pending.value, rows, groupNameById.value);
  submitting.value = false;
  emit("created", ok, fail);
  if (ok > 0 || (!ok && !fail)) {
    emit("update:show", false);
  }
}
```

`createCandidates` is the **verbatim** body of `createAliasCandidates` (`AliasManageTab.vue:308-360`), hoisted into the new component as a local async function taking `(alias, picks)` and reading the store directly — including the exposed-models patch loop. That patch stays in the frontend for now; Task 10's `AliasDetailDrawer` uses the new backend endpoint, and the picker keeps its existing behavior rather than half-migrating. (Follow-up: switch the picker's patch to `/aliases/expose` once the endpoint has shipped.)

Template: move lines 1489-1669 as-is, with `pickerOpen` → `props.show` via `v-model:show` on `n-modal`, `pickerTargetAlias` → `props.alias`, `filteredPickerModels` → `filteredModels`, `pickerSearch` → `search`, `pickerActiveGroupId` → `activeGroupId`, `pendingPicks` → `pending`, `pickerSubmitting` → `submitting`, `commitPendingPicks` → `commit`, `touchedGroupIds` deleted (it does not exist in the original — do not invent it). Style block: move every `.v3-picker-*` rule, and keep the two inline-styled wrapper divs exactly as they were.

- [ ] **Step 2: Wire it into the container**

In `AliasManageTab.vue`, replace the whole `n-modal` picker block and all picker-only script (lines 147-293, 362-380, plus `PendingPick`, `pendingPicksSeed`, `openPicker`) with:

```ts
const pickerOpen = ref(false);
const pickerAlias = ref("");
const pickerSeed = ref<PendingPick[] | null>(null);
function openPicker(alias: string, seed?: PendingPick[]): void {
  pickerAlias.value = alias;
  pickerSeed.value = seed || null;
  pickerOpen.value = true;
}
async function onPickerCreated(ok: number, fail: number): Promise<void> {
  if (ok > 0) {
    message.success(t("v5.maCreated", { ok, fail }));
  } else if (fail > 0) {
    message.error(t("v5.maAllFailed"));
  }
  await refresh();
}
```

```html
    <AliasPickerModal
      v-model:show="pickerOpen"
      :alias="pickerAlias"
      :seed="pickerSeed"
      @created="onPickerCreated"
    />
```

- [ ] **Step 3: Verify by eye + type-check**

Run: `cd web && npm run dev` (background), open `/aliases?tab=manage`, click a `+` on any alias, pick two models across two groups, confirm; then re-open and confirm the previously added rows show as 已绑定.
Run: `cd web && npx vue-tsc --noEmit -p tsconfig.app.json 2>&1 | head`
Expected: no new errors.

- [ ] **Step 4: Commit**

```bash
git add web/src/components/aliases/AliasPickerModal.vue web/src/components/aliases/AliasManageTab.vue
git commit -m "♻️ refactor(aliases): 模型选择弹窗抽成独立组件"
```

### Task 10: `AliasSuggestDrawer.vue`

**Files:**
- Create: `web/src/components/aliases/AliasSuggestDrawer.vue`
- Source: `AliasManageTab.vue` script lines 759-920 + template lines 1098-1173

- [ ] **Step 1: Move it**

Copy `suggestions`, `dismissedFamilies`, `loadDismissedFamilies`, `saveDismissedFamilies`, `dismissOneFamily`, `visibleSuggestions`, `loadSuggestions`, `onClickSuggestion`, `onClickFamilySuggestion`, `quickAdoptFamilySuggestion` and the `NDrawer` template into the new component. It reads the store (`rows`, `groups`, `groupNameById`) via `useAliasData()` instead of props, and exposes the two actions the container still needs:

```ts
const emit = defineEmits<{
  (e: "openPicker", alias: string, seed: PendingPick[]): void;
  (e: "changed"): void;
}>();
```

`onClickSuggestion` / `onClickFamilySuggestion` end by emitting `openPicker` (the picker lives in the container); `quickAdoptFamilySuggestion` emits `changed` after `createAliasCandidates` so the container refreshes. `createAliasCandidates` is shared by the picker and here — put it in `web/src/services/aliases.ts` (appended in this task, with its original comment block intact) so both call the same implementation, and export the candidate-creation signature:

```ts
/** 建候选的唯一入口: 统一默认值 + specified 分组补 exposed。两处调用共用。 */
export async function createAliasCandidates(
  alias: string,
  picks: Array<{ groupId: number; modelId: string }>
): Promise<{ ok: number; fail: number }>
```

It reads `rows`/`groupInfoById` from the module-level refs, so it needs no parameters beyond `(alias, picks)`. Update Task 9's picker to call this exported function instead of its local copy.

- [ ] **Step 2: Container**

Replace the moved script/template with `<AliasSuggestDrawer v-model:show="suggestDrawerOpen" @open-picker="…" @changed="refresh" />` plus a `suggestionCount` computed exported from the component via `defineExpose` — or, simpler and preferred: keep `loadSuggestions()`'s result in the store (move `suggestions` into `services/aliases.ts`) so the header button's count badge and the drawer read one source. Do the latter.

- [ ] **Step 3: Verify + commit**

Confirm the suggestion drawer opens, a family chip still pre-fills the picker, and 采纳 writes rows.

```bash
git add web/src/components/aliases/AliasSuggestDrawer.vue web/src/services/aliases.ts web/src/components/aliases/AliasPickerModal.vue web/src/components/aliases/AliasManageTab.vue
git commit -m "♻️ refactor(aliases): 建议抽屉抽独立组件, 建候选逻辑收敛到 services"
```

### Task 11: `AutoRoutingPanel.vue`

**Files:**
- Create: `web/src/components/aliases/AutoRoutingPanel.vue`
- Source: `AliasManageTab.vue` script lines 986-1036 + template lines 1175-1309

- [ ] **Step 1: Move the whole threshold card**

Copy `SLIDER_MAX`, `rangeValue`, `presetList`, `applyPreset`, `saveSettings`, `saveSettingsThrottled`, `threshOpen` and the `.v3-thresh-card` block verbatim. `settings` comes from `useAliasData()`. The header row must state the consequence of the switch, because that is now real behavior:

```html
        <span class="v3-thresh-bar__state">
          {{
            settings.Enabled
              ? t("aliases.auto.modeThreshold")
              : t("aliases.auto.modeSimple")
          }}
        </span>
```

i18n keys added here (all three locales, Task 15 collects them; add them now so `t()` never returns a raw key):

- `aliases.auto.modeThreshold` — zh `按请求长度选档`, en `Pick tier by request length`, ja `リクエスト長で段階選択`
- `aliases.auto.modeSimple` — zh `已关闭：一律走「简单」档`, en `Off: always the "simple" tier`, ja `オフ：常に「簡単」段階`

- [ ] **Step 2: Container + verify + commit**

`<AutoRoutingPanel />` replaces the inline card; delete the moved script lines. Confirm the slider still persists (reload the page, value kept) and that toggling the switch shows the new mode text.

```bash
git add web/src/components/aliases/AutoRoutingPanel.vue web/src/components/aliases/AliasManageTab.vue web/src/locales/
git commit -m "♻️ refactor(aliases): auto 路由面板抽独立组件"
```

---

## Phase P1 — new surfaces

### Task 12: `AliasList.vue`

**Files:**
- Create: `web/src/components/aliases/AliasList.vue`

- [ ] **Step 1: Script**

```vue
<script setup lang="ts">
// 别名列表 —— 页面主轴。一行 = 一个别名(不是一条候选), 这是这一版重构的出发点:
// 之前页面按 auto 的三个档位分栏, 自定义别名被当成候选塞进档位里, 于是
// "61 条映射" 之类的标题按数据库行说话, 而用户心里想的是"我有几个别名"。
//
// 三个筛选器取代了原来四个视图模式(卡片/表格/分栏/健康):
//   搜索   — 别名 / 候选模型 / 分组名
//   状态   — 全部 | 有问题 | auto 档位
//   分组   — 只看用到某个分组的别名(回答"删了 g1 会波及谁")
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { NIcon, NInput, NSelect, NSpin } from "naive-ui";
import { LockClosedOutline, SearchOutline } from "@vicons/ionicons5";
import StatePill from "@/components/aliases/StatePill.vue";
import { getGroupDisplayName } from "@/utils/display";
import type { AliasView } from "@/components/aliases/types";
import type { Group } from "@/types/models";

const props = defineProps<{
  aliases: AliasView[];
  groups: Group[];
  loading: boolean;
  /** traffic 接口是否可用; false 时不渲染实测相关文案。 */
  showMeasured: boolean;
}>();

const emit = defineEmits<{
  (e: "open", alias: string): void;
  (e: "add", alias: string): void;
  (e: "copy", alias: string): void;
}>();

const { t } = useI18n();
const query = ref("");
const status = ref<"all" | "problem" | "auto">("all");
const groupId = ref<number | "all">("all");
type SortKey = "calls" | "errorRate" | "avgMs" | "alias";
const sortKey = ref<SortKey>("calls");
const sortDesc = ref(true);

const statusOptions = computed(() => [
  { value: "all", label: t("aliases.list.filterAll") },
  { value: "problem", label: t("aliases.list.filterProblem") },
  { value: "auto", label: t("aliases.list.filterAuto") },
]);

const groupOptions = computed(() => [
  { value: "all" as const, label: t("aliases.list.filterAnyGroup") },
  ...props.groups
    .filter(g => g.id && g.group_type !== "aggregate")
    .map(g => ({ value: g.id as number, label: getGroupDisplayName(g) })),
]);

const visible = computed(() => {
  const q = query.value.trim().toLowerCase();
  let list = props.aliases.filter(a => {
    if (status.value === "problem" && !a.problem) return false;
    if (status.value === "auto" && !a.tier) return false;
    if (groupId.value !== "all" && !a.rows.some(r => r.row.group_id === groupId.value)) return false;
    if (!q) return true;
    return (
      a.alias.toLowerCase().includes(q) ||
      a.rows.some(r => r.row.real_model.toLowerCase().includes(q) || r.groupName.toLowerCase().includes(q))
    );
  });
  const dir = sortDesc.value ? -1 : 1;
  list = list.slice().sort((x, y) => {
    // 档位永远排在最前 —— 它们是 auto 的实现细节, 不是和用户一样的普通别名。
    const tx = (x.tier ? 0 : 1) - (y.tier ? 0 : 1);
    if (tx !== 0) return tx;
    switch (sortKey.value) {
      case "calls":
        return (x.calls - y.calls) * dir;
      case "errorRate":
        return (x.errorRate - y.errorRate) * dir;
      case "avgMs":
        return (x.avgMs - y.avgMs) * dir;
      default:
        return x.alias.localeCompare(y.alias) * dir;
    }
  });
  return list;
});

function ariaSort(k: SortKey): "ascending" | "descending" | "none" {
  return sortKey.value === k ? (sortDesc.value ? "descending" : "ascending") : "none";
}
function toggleSort(k: SortKey): void {
  if (sortKey.value === k) {
    sortDesc.value = !sortDesc.value;
  } else {
    sortKey.value = k;
    sortDesc.value = true;
  }
}

function problemLabel(a: AliasView): string {
  switch (a.problem) {
    case "no-candidates":
      return t("aliases.list.problemNoCandidates");
    case "unexposed":
      return t("aliases.list.problemUnexposed", { n: a.rows.filter(r => r.state === "unexposed").length });
    case "blocked":
      return t("aliases.list.problemBlocked", { n: a.rows.filter(r => r.state === "blocked").length });
    case "disabled":
      return t("aliases.list.problemDisabled", { n: a.rows.filter(r => r.state === "disabled").length });
    default:
      return "";
  }
}

function fmtCalls(n: number): string {
  return n.toLocaleString();
}
function fmtRate(r: number): string {
  return r === 0 ? "0%" : `${(r * 100).toFixed(1)}%`;
}
function fmtMs(ms: number): string {
  if (!ms) return "—";
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms`;
}
</script>
```

- [ ] **Step 2: Template**

```html
<template>
  <div class="alz">
    <div class="alz__bar">
      <NInput v-model:value="query" size="small" clearable :placeholder="t('aliases.list.search')" class="alz__q">
        <template #prefix><NIcon :component="SearchOutline" /></template>
      </NInput>
      <NSelect v-model:value="status" size="small" :options="statusOptions" class="alz__f" />
      <NSelect v-model:value="groupId" size="small" :options="groupOptions" class="alz__f alz__f--grp" />
      <span class="alz__count">{{ t("aliases.list.showing", { shown: visible.length, total: aliases.length }) }}</span>
    </div>

    <NSpin :show="loading">
      <table class="alz__table">
        <thead>
          <tr>
            <th scope="col" class="alz__th--sort" :aria-sort="ariaSort('alias')" @click="toggleSort('alias')">
              {{ t("aliases.list.colAlias") }}
            </th>
            <th scope="col">{{ t("aliases.list.colRole") }}</th>
            <th scope="col" class="alz__num">{{ t("aliases.list.colCandidates") }}</th>
            <th scope="col" class="alz__th--sort alz__num" :aria-sort="ariaSort('calls')" @click="toggleSort('calls')">
              {{ t("aliases.list.colCalls") }}
            </th>
            <th scope="col" class="alz__th--sort alz__num" :aria-sort="ariaSort('errorRate')" @click="toggleSort('errorRate')">
              {{ t("aliases.list.colErrorRate") }}
            </th>
            <th scope="col" class="alz__th--sort alz__num" :aria-sort="ariaSort('avgMs')" @click="toggleSort('avgMs')">
              {{ t("aliases.list.colAvg") }}
            </th>
            <th scope="col">{{ t("aliases.list.colState") }}</th>
            <th scope="col" class="alz__num">{{ t("aliases.list.colActions") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="a in visible"
            :key="a.alias"
            class="alz__row"
            :class="{ 'alz__row--problem': !!a.problem }"
            @click="emit('open', a.alias)"
          >
            <td>
              <code class="alz__name">{{ a.alias }}</code>
              <span v-if="a.isReserved && !a.tier" class="alz__lock"><NIcon :component="LockClosedOutline" :size="10" /></span>
            </td>
            <td>
              <span v-if="a.tier" class="alz__tier" :class="`alz__tier--${a.tier}`">
                {{ t("aliases.list.roleTier", { tier: t(`v3.${a.tier}`) }) }}
              </span>
              <span v-else class="alz__dim">—</span>
            </td>
            <td class="alz__num alz__mono">
              {{ a.usable }}<span v-if="a.unusable" class="alz__bad">/{{ a.total }}</span><span v-else class="alz__dim">/{{ a.total }}</span>
            </td>
            <td class="alz__num alz__mono">{{ a.calls ? fmtCalls(a.calls) : "—" }}</td>
            <td class="alz__num alz__mono" :class="{ 'alz__bad': a.errorRate >= 0.1 }">
              {{ a.calls ? fmtRate(a.errorRate) : "—" }}
            </td>
            <td class="alz__num alz__mono">{{ fmtMs(a.avgMs) }}</td>
            <td>
              <StatePill v-if="a.problem" :state="a.problem === 'no-candidates' ? 'disabled' : a.problem" variant="pill" />
              <span v-else class="alz__ok">●</span>
              <div v-if="problemLabel(a)" class="alz__problem">{{ problemLabel(a) }}</div>
              <div v-if="a.crossTier" class="alz__dim">{{ t("aliases.list.crossTier") }}</div>
            </td>
            <td class="alz__num" @click.stop>
              <div class="alz__acts">
                <button class="alz__act" @click="emit('copy', a.alias)">{{ t("aliases.list.copyName") }}</button>
                <button class="alz__act alz__act--primary" @click="emit('add', a.alias)">{{ t("aliases.list.addCandidate") }}</button>
                <button class="alz__act" @click="emit('open', a.alias)">{{ t("aliases.list.edit") }}</button>
              </div>
            </td>
          </tr>
          <tr v-if="!visible.length">
            <td colspan="8" class="alz__empty">{{ t("aliases.list.empty") }}</td>
          </tr>
        </tbody>
      </table>
    </NSpin>
  </div>
</template>
```

Two problems with that draft, fix them as you transcribe:

1. `StatePill` takes a `CandidateState`; `a.problem` is `"no-candidates" | "unexposed" | "blocked" | "disabled"`. Add a `STATE_PILL_FOR: Record<AliasView["problem"], CandidateState>` in `types.ts` mapping `no-candidates → disabled` (there is no "empty" pill state) and use it, so the pill vocabulary stays one set. Do not pass a raw `problem` string.
2. `showMeasured` is unused in the template. Either bind it — the 24h calls / error-rate / avg columns render `—` for every row when it is false, so hide the three `<th>`/`<td>` groups behind `v-if="showMeasured"` — or drop the prop. **Bind it**: showing an all-`—` metrics block reads as "nothing is being used", which is exactly the lie this redesign removes.

- [ ] **Step 3: Styles**

```css
<style scoped>
.alz { display: flex; flex-direction: column; gap: 10px; }
.alz__bar { display: flex; align-items: center; gap: 10px; }
.alz__q { max-width: 280px; }
.alz__f { width: 150px; }
.alz__f--grp { width: 190px; }
.alz__count { margin-left: auto; font: 500 10.5px var(--v3-mono); color: var(--v3-ink-4); }
.alz__table { width: 100%; border-collapse: collapse; font: 400 12px var(--v3-sans); background: var(--v3-surface); border: 1px solid var(--v3-line); border-radius: 8px; overflow: hidden; }
.alz__table thead th { position: sticky; top: 0; background: var(--v3-surface-2); border-bottom: 1px solid var(--v3-line); padding: 7px 10px; text-align: left; font: 600 10px var(--v3-mono); letter-spacing: .06em; text-transform: uppercase; color: var(--v3-ink-3); white-space: nowrap; }
.alz__th--sort { cursor: pointer; user-select: none; }
.alz__th--sort:hover { color: var(--v3-ink); }
.alz__table td { padding: 7px 10px; border-bottom: 1px solid var(--v3-line); vertical-align: top; }
.alz__row { cursor: pointer; }
.alz__row:hover { background: var(--v3-surface-2); }
.alz__row--problem { background: oklch(from var(--v3-warn) l c h / .04); }
.alz__num { text-align: right; }
.alz__mono { font-family: var(--v3-mono); white-space: nowrap; }
.alz__name { font: 600 11.5px var(--v3-mono); color: var(--v3-ink); }
.alz__dim { color: var(--v3-ink-4); }
.alz__bad { color: var(--v3-danger); }
.alz__ok { color: var(--v3-ok); font-size: 9px; }
.alz__tier { font: 600 9.5px var(--v3-mono); padding: 1px 6px; border-radius: 999px; white-space: nowrap; }
.alz__tier--simple { background: oklch(from var(--v3-ok) l c h / .12); color: oklch(.45 .1 150); }
.alz__tier--medium { background: oklch(from var(--v3-warn) l c h / .12); color: oklch(.5 .12 65); }
.alz__tier--complex { background: oklch(from var(--v3-danger) l c h / .12); color: oklch(.48 .16 25); }
.alz__lock { color: var(--v3-warn); margin-left: 3px; }
.alz__problem { font: 400 9.5px var(--v3-sans); color: oklch(.5 .12 65); margin-top: 2px; white-space: nowrap; }
.alz__acts { display: flex; justify-content: flex-end; gap: 5px; }
.alz__act { font: 600 10px var(--v3-mono); border: 1px solid var(--v3-line); background: transparent; color: var(--v3-ink-3); border-radius: 3px; padding: 2px 6px; cursor: pointer; }
.alz__act:hover { border-color: var(--v3-accent); color: var(--v3-accent); }
.alz__act--primary { border-color: var(--v3-accent); color: var(--v3-accent); }
.alz__empty { text-align: center; color: var(--v3-ink-4); font-style: italic; padding: 40px 10px !important; }
</style>
```

- [ ] **Step 4: i18n (all three locales, one commit-able unit)**

Read `web/src/locales/zh-CN.ts`'s `aliases:` block first, then add an `aliases.list.*` sub-block after `aliases.edit`. Keys (values per locale below — en/ja are the same keys translated):

| key | zh-CN |
|---|---|
| `search` | 搜索别名 / 模型 / 分组 |
| `filterAll` | 全部 |
| `filterProblem` | 有问题 ({n}) → render as `t(…, { n })` in the label; simplest correct: build the label in `statusOptions` with the count |
| `filterAuto` | auto 档位 |
| `filterAnyGroup` | 任意分组 |
| `showing` | 显示 {shown} / {total} 个别名 |
| `colAlias` | 别名 |
| `colRole` | 角色 |
| `colCandidates` | 候选 |
| `colCalls` | 24h 请求 |
| `colErrorRate` | 错误率 |
| `colAvg` | 平均耗时 |
| `colState` | 状态 |
| `colActions` | 操作 |
| `roleTier` | auto · {tier} |
| `crossTier` | 候选跨档位复用 |
| `copyName` | 复制 |
| `addCandidate` | + 候选 |
| `edit` | 编辑 |
| `empty` | 没有匹配的别名。 |
| `problemNoCandidates` | 一个候选都没有, 调用时会失败 |
| `problemUnexposed` | {n} 个候选所在分组未公开此模型 |
| `problemBlocked` | {n} 个候选在分组黑名单里 |
| `problemDisabled` | {n} 个候选已停用 |

`aliases.view_cards/view_table/view_split/view_health` and the `aliases.table/split/health` blocks become dead — delete them in Task 15, not here (deleting early breaks the still-mounted views mid-refactor).

- [ ] **Step 5: Type-check + commit**

```bash
git add web/src/components/aliases/AliasList.vue web/src/components/aliases/types.ts web/src/locales/
git commit -m "✨ feat(web): 别名列表 — 一行一个别名 + 三筛选器取代四视图"
```

### Task 13: `AliasDetailDrawer.vue` (rename + extend)

**Files:**
- Create by moving: `web/src/components/aliases/AliasEditDrawer.vue` → `AliasDetailDrawer.vue`

- [ ] **Step 1: Move the file unchanged**

```bash
git mv web/src/components/aliases/AliasEditDrawer.vue web/src/components/aliases/AliasDetailDrawer.vue
```

Update the import in `AliasManageTab.vue`. Run `cd web && npx vue-tsc --noEmit -p tsconfig.app.json` — must be clean before continuing.

- [ ] **Step 2: Fix the traffic key (this is the bug that made 实测 always 0)**

In `AliasDetailDrawer.vue`, `callsOf` currently looks up `${c.groupId}::${c.realModel}`, but the log's `model` column holds the **requested** name — for alias-routed traffic that is the alias, never the `real_model`, so the lookup can never hit. Also add two props. Change the props block:

```ts
  /** `${group_id}::${请求名}` -> 24h 实际调用数。别名路由下请求名 = 别名本身。 */
  traffic: Record<string, number>;
  /** traffic 是否可用; false 时隐藏实测相关列。 */
  showMeasured: boolean;
  /** 本别名在窗口内的整体指标, 用于页脚摘要。 */
  summary?: AliasView | null;
```

and the lookup:

```ts
function callsOf(c: DraftCandidate): number {
  // 按 (分组, 别名) 归因: 日志的 model 列存的是请求名, 别名请求下就是别名本身。
  // 同一分组在本别名下有多条候选时, 日志分不开, 只能给分组级合计 —— 用
  // sharedGroupNote 如实标注, 不假装是模型级数字。
  return props.traffic[`${c.groupId}::${props.alias}`] || 0;
}
const multiCandidatePerGroup = computed(() => {
  const hits: Record<number, number> = {};
  for (const c of draft.value) {
    hits[c.groupId] = (hits[c.groupId] || 0) + 1;
  }
  return Object.values(hits).some(n => n > 1);
});
```

`actualTotal` must then sum over **distinct groups** (otherwise a group with two candidates counts twice):

```ts
const actualTotal = computed(() => {
  const perGroup = new Map<number, number>();
  for (const c of draft.value) {
    perGroup.set(c.groupId, callsOf(c));
  }
  return Array.from(perGroup.values()).reduce((s, n) => s + n, 0);
});
```

Wrap the `.aed__share-actual` element in `v-if="showMeasured && actualTotal > 0"` and the `.aed__meta-sep` 24h span the same way.

- [ ] **Step 3: In-place expose**

A candidate row whose group is in `specified` mode without this model is silently skipped at routing time. Today the only affordance is a `router.push` to the keys page. Add the button to the drawer row, before the enable switch:

```ts
import { aliasesApi } from "@/api/aliases";

const exposing = ref<Record<string, boolean>>({});

async function exposeCandidate(c: DraftCandidate): Promise<void> {
  const key = `${c.groupId}:${c.realModel}`;
  exposing.value = { ...exposing.value, [key]: true };
  try {
    const res = await aliasesApi.exposeModel(props.alias, c.groupId, c.realModel);
    const status = (res as unknown as { data: { status: string } }).data.status;
    message.success(
      status === "not_needed"
        ? t("aliases.drawer.exposeNotNeeded")
        : status === "already_ok"
          ? t("aliases.drawer.exposeAlreadyOk")
          : t("aliases.drawer.exposeDone")
    );
    // 暴露状态来自 group.exposed_models, 只有重拉分组才能刷新徽标。
    emit("exposed");
  } catch (e) {
    message.error(
      e instanceof Error && e.message.includes("blocked")
        ? t("aliases.drawer.exposeBlocked")
        : t("common.requestFailed")
    );
  } finally {
    const next = { ...exposing.value };
    delete next[key];
    exposing.value = next;
  }
}
```

The drawer needs to know a row is unexposed. It gets `rows: ModelAliasRow[]` and `groups: Group[]` already, so derive it locally rather than widening the prop surface — add next to `callsOf`:

```ts
function stateOf(c: DraftCandidate): CandidateState {
  // 与 services/aliases.ts 的 candidateState 同一套判定, 但作用在草稿行上
  // (草稿可能还没入库, 没有 row.id 可用)。
  if (!c.enabled) return "disabled";
  const g = props.groups.find(x => x.id === c.groupId) as unknown as Record<string, unknown>;
  if (!g) return "usable";
  const arr = (raw: unknown) => parseStringList(raw);
  if (arr(g.blocked_models).includes(c.realModel)) return "blocked";
  const mode = (g.model_routing_mode as string) || "passthrough";
  if (mode === "specified" && !arr(g.exposed_models).includes(c.realModel)) return "unexposed";
  return "usable";
}
```

`parseStringList` is exported from `services/aliases.ts` (defined in Task 7), so JSON-array columns are parsed in exactly one place across the app. Template, inside `.aed__row` after the group line:

```html
            <StatePill v-if="stateOf(c) !== 'usable'" :state="stateOf(c)" />
            <button
              v-if="stateOf(c) === 'unexposed'"
              class="aed__expose"
              :disabled="exposing[`${c.groupId}:${c.realModel}`]"
              :title="t('aliases.drawer.exposeTip')"
              @click.stop="exposeCandidate(c)"
            >
              {{ t("aliases.drawer.expose") }}
            </button>
```

Add `(e: "exposed"): void;` to `defineEmits`. Import `StatePill` and `CandidateState`.

- [ ] **Step 4: Footer summary**

```html
        <div v-if="summary" class="aed__summary">
          <span>{{ t("aliases.drawer.window") }}: {{ summary.calls.toLocaleString() }} · </span>
          <span>{{ t("aliases.drawer.errRate") }} {{ (summary.errorRate * 100).toFixed(1) }}% · </span>
          <span>{{ t("aliases.drawer.avgMs") }} {{ summary.avgMs }}ms</span>
          <span v-if="summary.costUsd > 0"> · {{ t("aliases.drawer.cost") }} ${{ summary.costUsd.toFixed(4) }}</span>
          <NTooltip trigger="hover">
            <template #trigger><span class="aed__why">ⓘ</span></template>
            {{ t("aliases.drawer.shareDivergedTip") }}
          </NTooltip>
        </div>
```

Prop: `summary?: AliasView | null` — import the type from `@/components/aliases/types`. `shareDivergedTip` is the honest explanation of why 配置占比 ≠ 实测占比: `配置占比是你写的权重比例; 实际选路还会乘上游先验、成功率采样与延迟, 所以两者会偏离。` — that is the tooltip §3.2 promises.

- [ ] **Step 5: i18n**

`aliases.drawer.*` (three locales): `expose`(公开), `exposeTip`(把该模型加入分组的公开列表, 立即生效), `exposeDone`(已公开), `exposeAlreadyOk`(本来就已公开), `exposeNotNeeded`(该分组是直通模式, 无需公开), `exposeBlocked`(该模型在分组黑名单里, 请先解除黑名单), `window`(24h), `errRate`(错误率), `avgMs`(平均), `cost`(折算成本), `shareDivergedTip` (as above).

- [ ] **Step 6: Verify + commit**

In the browser: open an alias with an unexposed candidate → click 公开 → toast + pill flips to 可用 and the keys page shows the model exposed. Confirm 24h 实测 now shows non-zero for any alias with traffic.

```bash
git add web/src/components/aliases/ web/src/services/aliases.ts web/src/locales/
git commit -m "🐛 fix(aliases): 实测分流按 (分组,别名) 归因; ✨ 抽屉内就地公开模型"
```

### Task 14: Container rewrite

**Files:**
- Modify: `web/src/components/aliases/AliasManageTab.vue` (script+template replaced; ~200 lines total)

- [ ] **Step 1: New script**

```vue
<script setup lang="ts">
// 别名「管理」页的容器: 只做数据装配和"开哪个抽屉/弹窗"这一层状态。
// 加载与派生在 services/aliases.ts, 渲染在 AliasList / AutoRoutingPanel /
// AliasDetailDrawer / AliasPickerModal / AliasSuggestDrawer。
import { computed, onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { NIcon, useMessage } from "naive-ui";
import {
  AddOutline,
  AlbumsOutline,
  BulbOutline,
  RefreshOutline,
} from "@vicons/ionicons5";
import AliasAutoRoutingPanel from "./AutoRoutingPanel.vue";
import AliasList from "./AliasList.vue";
import AliasDetailDrawer from "./AliasDetailDrawer.vue";
import AliasPickerModal, { type PendingPick } from "./AliasPickerModal.vue";
import AliasSuggestDrawer from "./AliasSuggestDrawer.vue";
import { useAliasData } from "@/services/aliases";
import { copy } from "@/utils/clipboard";
import { NModal, NInput } from "naive-ui";
import AliasQuickSetupTab from "./AliasQuickSetupTab.vue";

const props = withDefaults(defineProps<{ initialFamilyOpen?: boolean }>(), { initialFamilyOpen: false });

const { t } = useI18n();
const message = useMessage();
const route = useRoute();
const router = useRouter();

const {
  loading, groups, aliasViews, problemCount, trafficByKey, trafficAvailable,
  groupNameById, modelsByGroup, refresh, reloadRows,
} = useAliasData();

const detailOpen = ref(false);
const detailAlias = ref("");
const pickerOpen = ref(false);
const pickerAlias = ref("");
const pickerSeed = ref<PendingPick[] | null>(null);
const suggestOpen = ref(false);
const familyModalOpen = ref(props.initialFamilyOpen);
const newAliasOpen = ref(false);
const newAliasName = ref("");
const highlightAlias = ref<string | null>(null);

const suggestionsOpen = computed(() => suggestOpen);

function openDetail(alias: string): void {
  detailAlias.value = alias;
  detailOpen.value = true;
}
function openPicker(alias: string, seed?: PendingPick[]): void {
  pickerAlias.value = alias;
  pickerSeed.value = seed || null;
  pickerOpen.value = true;
}

const detailSummary = computed(() => aliasViews.value.find(a => a.alias === detailAlias.value) || null);
const detailRows = computed(() => detailSummary.value?.rows.map(r => r.row) || []);

async function copyAlias(alias: string): Promise<void> {
  await copy(alias);
  message.success(t("v3.aliasCopied", { alias }));
}

function commitNewAlias(): void {
  const name = newAliasName.value.trim();
  if (!name) {
    message.warning(t("v5.alNameRequired"));
    return;
  }
  newAliasOpen.value = false;
  newAliasName.value = "";
  openPicker(name);
}

// 快速整理/建议里创建完, 高亮一下, 让"我刚加的那个"在列表里找得到。
function flashAlias(alias: string): void {
  highlightAlias.value = alias;
  setTimeout(() => {
    highlightAlias.value = null;
    const { highlight: _drop, ...rest } = route.query;
    router.replace({ query: rest });
  }, 1500);
}

onMounted(() => {
  const fromQuery = typeof route.query.highlight === "string" ? route.query.highlight : "";
  if (fromQuery) {
    flashAlias(fromQuery);
  }
  return refresh();
});
</script>
```

Keep `?highlight=` working (the quick-setup tab hands off to the manage tab that way) — but note the current implementation watches the query with `immediate: true` before data loads; the `onMounted` version above is equivalent and simpler.

- [ ] **Step 2: New template**

```html
<template>
  <div class="v3-page-aliases">
    <div class="v3-viewhead">
      <div class="v3-viewhead__crumb">{{ t("v3.crumb.aliases") }}</div>
      <div class="v3-viewhead__actions">
        <button v-if="suggestionCount" class="v3-btn" :title="t('v5.suggestionsTitle')" @click="suggestOpen = true">
          <n-icon :component="BulbOutline" :size="12" />
          {{ t("v5.suggestionsTitle") }}
          <span class="v5-suggest-banner__count">{{ suggestionCount }}</span>
        </button>
        <button class="v3-btn" @click="familyModalOpen = true">
          <n-icon :component="AlbumsOutline" :size="12" />
          {{ t("aliases.browseFamily") }}
        </button>
        <button class="v3-btn" @click="refresh">
          <n-icon :component="RefreshOutline" :size="12" />
          {{ t("v3.refresh") }}
        </button>
        <button class="v3-btn v3-btn--accent" @click="newAliasOpen = true">
          <n-icon :component="AddOutline" :size="12" />
          {{ t("v5.alNewAlias") }}
        </button>
      </div>
    </div>

    <h1 class="v3-viewtitle">
      {{ t("v3.aliasesTitle") }}
      <n-tooltip trigger="hover">
        <template #trigger>
          <n-icon :component="HelpCircleOutline" :size="15" style="margin-left: 6px; cursor: help; color: var(--v3-ink-3)" />
        </template>
        {{ t("v3.aliasesDesc") }}
      </n-tooltip>
      <!-- 「N 个别名 / M 条候选」而不是「M 条映射」: 主词是用户概念里的对象。 -->
      <span class="v3-viewtitle__meta">
        {{ t("aliases.list.titleMeta", { aliases: aliasViews.length, candidates: totalCandidates }) }}
      </span>
    </h1>

    <AutoRoutingPanel />

    <AliasList
      :aliases="aliasViews"
      :groups="groups"
      :loading="loading"
      :show-measured="trafficAvailable"
      @open="openDetail"
      @add="openPicker"
      @copy="copyAlias"
    />

    <AliasDetailDrawer
      v-model:show="detailOpen"
      :alias="detailAlias"
      :rows="detailRows"
      :groups="groups"
      :group-name-by-id="groupNameById"
      :models-by-group="modelsByGroup"
      :traffic="trafficByKey"
      :show-measured="trafficAvailable"
      :summary="detailSummary"
      @saved="refresh"
      @exposed="refresh"
    />

    <AliasPickerModal v-model:show="pickerOpen" :alias="pickerAlias" :seed="pickerSeed" @created="onCreated" />
    <AliasSuggestDrawer v-model:show="suggestOpen" @open-picker="openPicker" @changed="refresh" />

    <n-modal v-model:show="newAliasOpen" preset="dialog" :title="t('v5.alNewAlias')" style="width: 400px">
      <div style="padding-top: 16px; display: flex; flex-direction: column; gap: 16px">
        <n-input v-model:value="newAliasName" :placeholder="t('v3.aliasNamePlaceholder')" @keyup.enter="commitNewAlias" />
        <div style="display: flex; justify-content: flex-end; gap: 12px">
          <button class="v3-btn" @click="newAliasOpen = false">{{ t("common.cancel") }}</button>
          <button class="v3-btn v3-btn--accent" @click="commitNewAlias">{{ t("common.save") }}</button>
        </div>
      </div>
    </n-modal>

    <n-modal v-model:show="familyModalOpen" preset="card" :title="t('aliases.browseFamily')" style="width: 1100px">
      <AliasQuickSetupTab />
    </n-modal>
  </div>
</template>
```

Resolve these before typing it in (the template references them, the script must define them — this is the container's whole remaining job):

- `suggestionCount` → from `useAliasData()` (Task 10 moved `suggestions` into the store): add `suggestionCount` to the destructuring.
- `totalCandidates` → `computed(() => aliasViews.value.reduce((s, a) => s + a.total, 0))`.
- `onCreated(ok, fail)` → the Task 9 handler (`v5.maCreated` / `v5.maAllFailed` toasts + `refresh()`).
- `HelpCircleOutline` → add to the `@vicons/ionicons5` import.
- Merge the two `naive-ui` import lines into one.
- Delete the unused `suggestionsOpen` computed and `reloadRows` from the destructuring.
- `aliases.list.titleMeta` zh: `{aliases} 个别名 / {candidates} 条候选` (en: `{aliases} aliases / {candidates} candidates`, ja: `{aliases} 個のエイリアス / {candidates} 件の候補`).

- [ ] **Step 3: Styles**

Keep from the old 685-line style block only what the remaining markup uses: `.v3-page-aliases`, `.v3-viewhead*`, `.v3-viewtitle*`, `.v3-btn*`, `.v5-suggest-banner__count`, `.v5-suggest-empty`. Delete the tier-card, `.v3-arow*`, slider, and picker rules — the slider and picker CSS travels with `AutoRoutingPanel.vue` and `AliasPickerModal.vue` respectively. Run `grep -o 'class="[^"]*"'` over the new file and cross-check every remaining selector has a consumer.

- [ ] **Step 4: Verify**

`cd web && npm run type-check` (must be clean; note `npm run build` skips types in this repo, so type-check is the gate). Then in the browser: list renders one row per alias; row click opens the drawer; `+ 候选` opens the picker; 建议 badge count matches the drawer; family modal still creates aliases; the auto switch persists.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/aliases/AliasManageTab.vue
git commit -m "♻️ refactor(aliases): 管理页容器化 (2403 → ~200 行)"
```

---

## Phase P3 — deletion and cleanup

### Task 15: Remove the four view modes

**Files:**
- Delete: `AliasTableView.vue`, `AliasSplitView.vue`, `AliasHealthView.vue`, `AliasCandidateList.vue`
- Modify: `web/src/locales/{zh-CN,en-US,ja-JP}.ts`

- [ ] **Step 1: Confirm nothing imports them**

```bash
cd web && grep -rn "AliasTableView\|AliasSplitView\|AliasHealthView\|AliasCandidateList" src --include=*.vue --include=*.ts | grep -v "^src/components/aliases/Alias\(Table\|Split\|Health\|Candidate\)"
```
Expected: no hits outside the files themselves (comments inside `types.ts` are fine — clean them up if reworded).

- [ ] **Step 2: Delete**

```bash
git rm web/src/components/aliases/AliasTableView.vue web/src/components/aliases/AliasSplitView.vue web/src/components/aliases/AliasHealthView.vue web/src/components/aliases/AliasCandidateList.vue
```

- [ ] **Step 3: Drop the view-mode storage key**

`alias-view-mode` has no reader anymore. There is no migration to write — the next read just misses. Grep to be sure nothing else reads it: `grep -rn "alias-view-mode" web/src` → expected: no hits after this task. Keep `alias-suggest-dismissed-families`.

- [ ] **Step 4: Prune dead i18n in all three locales**

Delete `aliases.view_cards/view_table/view_split/view_health`, the `aliases.table`, `aliases.split`, `aliases.health` blocks. For every other key you touched on this page, verify usage:

```bash
cd web && for k in $(grep -oE 't\("aliases\.[a-zA-Z.]+"' -r src --include=*.vue --include=*.ts | sed -E 's/t\("//; s/"//' | sort -u); do echo "$k"; done > /tmp/used-alias-keys.txt
```
Then diff against the three locale files' `aliases.*` keys and delete any key present in a locale but absent from `/tmp/used-alias-keys.txt` — **except** keys reached through computed names (`t(\`v3.${a.tier}\`)`, `STATE_LABEL_KEY`). Those need a manual pass; check each against `v3.*` / `aliases.*` before removing.

- [ ] **Step 5: Parity check + commit**

```bash
cd web && node -e "
const zh=require('./src/locales/zh-CN.ts'); " 2>/dev/null || true
npm run i18n:check 2>/dev/null || npx tsx -e "
import zh from './src/locales/zh-CN.ts';
import en from './src/locales/en-US.ts';
import ja from './src/locales/ja-JP.ts';
const flat=(o,p='')=>Object.entries(o).flatMap(([k,v])=>typeof v==='object'&&v?flat(v,p+k+'.'):[p+k]);
const a=new Set(flat(zh.default??zh)), b=new Set(flat(en.default??en)), c=new Set(flat(ja.default??ja));
const only=(x,y,l)=>[...x].filter(k=>!y.has(k)).map(k=>l+':'+k);
console.log([...only(a,b,'zh'),...only(a,c,'zh'),...only(b,a,'en'),...only(c,a,'ja')].join('\n')||'parity ok');
"
```
Expected: `parity ok` (or an empty diff list you then fix). If the repo already ships an i18n parity script, use it instead of the inline snippet — check `package.json`.

```bash
git add -A web/src/locales web/src/components/aliases
git commit -m "🔥 refactor(aliases): 删除四种视图模式与其死文案"
```

---

## Phase P3 — verification

### Task 16: Amend the spec

**Files:**
- Modify: `docs/superpowers/specs/2026-09-24-alias-manage-page-redesign-design.md`

- [ ] Step 1: Fold the five amendments at the top of this plan into the spec sections they touch (§3.1 p50→平均, §3.1 provider 分组→分组筛选, §4.3 从档位移除, §6 `ModelAliasModal` / `AliasCandidateList`). Mark each with `> 实施时修订:` so the history stays legible. Do not rewrite the sections wholesale.
- [ ] Step 2: `git add docs/superpowers/specs && git commit -m "📝 docs(aliases): 补实施期修订"`.

### Task 17: Full gate + real-data smoke

- [ ] **Step 1: Backend**

`go build ./... && go vet ./... && go test ./...` — all green, output captured.

- [ ] **Step 2: Frontend**

```bash
cd web && npm run type-check && npm run lint:check
```
`type-check` must be clean. `lint:check`/`format:check` are **non-gating in CI by design** (`ci.yml`), but record the result: the error count must not exceed the pre-change baseline. Get the baseline with `git stash` … no — do not touch the shared tree; instead run `npx eslint web/src/components/aliases` and fix everything in the files this branch owns.

- [ ] **Step 3: Real-data browser smoke (1280×900, light theme)**

Build a throwaway stack — **never** the dev server against the user's data dir, and never `npm run build` (it would overwrite their dirty `web/dist/`):

```bash
mkdir -p /tmp/ag-alias && cd /tmp/ag-alias && ../../api-center/autogateway serve  # or the repo's run target, with AG_DB_PATH=/tmp/ag-alias/test.db
cd api-center/web && npx vite --port 5299   # dev server; verify it proxies to the test backend, not :3001
```

Seed via HTTP: two groups (one `specified` with a model deliberately missing from `exposed_models`), one custom alias with 3 candidates across both groups, one request log per candidate name so traffic is non-zero.

Drive **headless Chrome over CDP** (the in-app browser has a 0×0 viewport and the detail panes never mount — use `Emulation.setDeviceMetricsOverride` at 1280×900):

- [ ] List row count === distinct alias count; reserved tiers are first.
- [ ] Every metric column shows real numbers where traffic exists; `—` only where it genuinely doesn't.
- [ ] Open the drawer: 配置占比 column sums to ~100; 24h 实测占比 is **non-zero** for a seeded alias (this is the §5.2 regression test — if it is 0, the key fix failed).
- [ ] Click 公开 on the unexposed candidate → toast, pill flips, `GET /api/groups` shows the model in `exposed_models`.
- [ ] Search / status / group filters each change the row set; 有问题 badge count equals rows with a problem.
- [ ] Toggle the auto switch off → `POST /proxy/<group>/chat/completions {"model":"auto", …}` with a huge prompt lands on a `simple` candidate (check `request_logs.model` and the upstream addr). This is the P2 acceptance and the one that proves the switch isn't decorative.
- [ ] Screenshots of list + drawer at 1280×900 saved under `/tmp/ag-alias/` for the final report.
- [ ] Console: zero application errors (`Runtime.consoleAPICalled` filter).

- [ ] **Step 4: Report**

Summarize what shipped, the five spec amendments, and any decision that needs the user — with the screenshots. Clean up `/tmp/ag-alias` processes afterwards.
