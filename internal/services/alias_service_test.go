package services

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"autogateway/internal/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// createGroup 建一个分组, 补齐 sqlite 上 NOT NULL 但没有默认值的 JSON 列。
func createGroup(t *testing.T, db *gorm.DB, g *models.Group) *models.Group {
	t.Helper()
	if len(g.Upstreams) == 0 {
		g.Upstreams = datatypes.JSON(`[]`)
	}
	if err := db.Create(g).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	return g
}

func containsModel(raw datatypes.JSON, model string) bool {
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return false
	}
	return slices.Contains(arr, model)
}

// ① specified 分组: 把候选模型追加进白名单, 原有的条目必须留着。
func TestExposeCandidate_AddsToSpecifiedGroup(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	g := models.Group{
		Name:             "nvidia",
		ChannelType:      "openai",
		ModelRoutingMode: "specified",
		ExposedModels:    datatypes.JSON(`["llama-3.1-8b"]`),
	}
	createGroup(t, db, &g)
	row := models.ModelAlias{Alias: "hermes", GroupID: g.ID, RealModel: "llama-3.3-70b", Weight: 1, Enabled: true}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}

	status, err := NewAliasService(db).ExposeCandidate(ctx, "hermes", g.ID, "llama-3.3-70b")
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
	if !containsModel(got.ExposedModels, "llama-3.3-70b") {
		t.Fatalf("exposed_models = %s, want it to contain the model", got.ExposedModels)
	}
	if !containsModel(got.ExposedModels, "llama-3.1-8b") {
		t.Fatalf("exposed_models = %s, want the pre-existing entry kept", got.ExposedModels)
	}
}

// ② 幂等: 已经在暴露列表里 -> already_ok, 且不产生重复项。
func TestExposeCandidate_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	g := models.Group{
		Name: "groq", ChannelType: "openai", ModelRoutingMode: "specified",
		ExposedModels: datatypes.JSON(`["llama-3.3-70b"]`),
	}
	createGroup(t, db, &g)
	if err := db.Create(&models.ModelAlias{
		Alias: "hermes", GroupID: g.ID, RealModel: "llama-3.3-70b", Weight: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	status, err := NewAliasService(db).ExposeCandidate(ctx, "hermes", g.ID, "llama-3.3-70b")
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

// ③ passthrough 分组没有白名单概念, 该模型本来可达。
func TestExposeCandidate_PassthroughNotNeeded(t *testing.T) {
	db := newTestDB(t)
	g := models.Group{Name: "openai", ChannelType: "openai", ModelRoutingMode: "passthrough"}
	createGroup(t, db, &g)
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

// ④ 黑名单命中时补白名单是无效动作(filterByExposed 先拒 blocked),
// 必须报错而不是假装成功。
func TestExposeCandidate_BlockedModelRejected(t *testing.T) {
	db := newTestDB(t)
	g := models.Group{
		Name: "zhipu", ChannelType: "openai", ModelRoutingMode: "specified",
		BlockedModels: datatypes.JSON(`["glm-4"]`),
	}
	createGroup(t, db, &g)
	if err := db.Create(&models.ModelAlias{
		Alias: "y", GroupID: g.ID, RealModel: "glm-4", Weight: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	if _, err := NewAliasService(db).ExposeCandidate(context.Background(), "y", g.ID, "glm-4"); err == nil {
		t.Fatal("expected an error for a blocked model, got nil")
	}
	var got models.Group
	if err := db.First(&got, g.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if containsModel(got.ExposedModels, "glm-4") {
		t.Fatalf("exposed_models = %s, want untouched on error", got.ExposedModels)
	}
}

// ⑤ 候选行不存在时不去改分组 —— 过期 UI 上的一次点击不该静默暴露一个模型。
func TestExposeCandidate_RequiresAliasRow(t *testing.T) {
	db := newTestDB(t)
	g := models.Group{Name: "mistral", ChannelType: "openai", ModelRoutingMode: "specified"}
	createGroup(t, db, &g)
	if _, err := NewAliasService(db).ExposeCandidate(context.Background(), "ghost", g.ID, "mistral-7b"); err == nil {
		t.Fatal("expected an error when the alias candidate does not exist")
	}
}

// ⑥ 聚合分组不参与 alias 落点, 直接拒绝。
func TestExposeCandidate_RejectsAggregateGroup(t *testing.T) {
	db := newTestDB(t)
	g := models.Group{
		Name: "agg", ChannelType: "openai", GroupType: "aggregate", ModelRoutingMode: "specified",
	}
	createGroup(t, db, &g)
	if err := db.Create(&models.ModelAlias{
		Alias: "a", GroupID: g.ID, RealModel: "m", Weight: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	if _, err := NewAliasService(db).ExposeCandidate(context.Background(), "a", g.ID, "m"); err == nil {
		t.Fatal("expected an error for an aggregate group")
	}
}

// ReplaceCandidates 的插入分支必须尊重 enabled=false。
// gorm 对带 `default:true` tag 的字段会在零值时跳过该列(让 DB 默认值生效),
// 所以"删掉候选 → 撤销写回"或"新建一条停用候选"都会把停用状态悄悄变成启用,
// 流量立刻打到用户明确关掉的模型上。
func TestReplaceCandidates_PreservesDisabledFlag(t *testing.T) {
	ctx := context.Background()
	gormDB := newTestDB(t)
	svc := NewAliasService(gormDB)
	g := createGroup(t, gormDB, &models.Group{Name: "openai-main"})
	off := false

	candidates := []AliasCandidateInput{
		{GroupID: g.ID, RealModel: "gpt-4o", Weight: 10, Priority: 1, Enabled: &off},
		{GroupID: g.ID, RealModel: "gpt-4o-mini", Weight: 5, Priority: 2},
	}
	rows, err := svc.ReplaceCandidates(ctx, "keep-off", candidates)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	byModel := map[string]models.ModelAlias{}
	for _, r := range rows {
		byModel[r.RealModel] = r
	}
	if got := byModel["gpt-4o"].Enabled; got {
		t.Fatalf("disabled candidate came back enabled=true")
	}
	if got := byModel["gpt-4o-mini"].Enabled; !got {
		t.Fatalf("candidate without explicit enabled should default to true, got false")
	}

	// Create (POST /aliases) 走同一个插入路径, 一起守住。
	created, err := svc.Create(ctx, AliasCreateRequest{
		Alias: "keep-off", GroupID: g.ID, RealModel: "claude-sonnet-4", Enabled: &off,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Enabled {
		t.Fatalf("Create with enabled=false produced an enabled row")
	}
}

// === RenameAlias ===
//
// 补这组测试的直接原因: 上周的实机测试里我以为验过了"目标重名"这条守卫,
// 其实 to=medium 先被"不能改成保留名"拦下了 —— 那条守卫当时**根本没被走到**。
// 三条守卫都在这里固定住。

// newAliasWithRows 建一个分组 + 若干候选行, 返回 service。
func newAliasWithRows(t *testing.T, alias string, modelsToBind ...string) (*AliasService, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	g := createGroup(t, db, &models.Group{
		Name: "grp-" + alias, GroupType: "standard", ChannelType: "openai",
	})
	svc := NewAliasService(db)
	for _, m := range modelsToBind {
		if _, err := svc.Create(context.Background(), AliasCreateRequest{
			Alias: alias, GroupID: g.ID, RealModel: m,
		}); err != nil {
			t.Fatalf("seed %s/%s: %v", alias, m, err)
		}
	}
	return svc, db
}

func countAlias(t *testing.T, db *gorm.DB, alias string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.ModelAlias{}).Where("alias = ?", alias).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", alias, err)
	}
	return n
}

// 正常改名: 所有候选行一起搬过去, 旧名字清空。
func TestRenameAlias_MovesAllRows(t *testing.T) {
	svc, db := newAliasWithRows(t, "old-name", "m-a", "m-b", "m-c")

	n, err := svc.RenameAlias(context.Background(), "old-name", "new-name")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if n != 3 {
		t.Fatalf("renamed = %d, want 3", n)
	}
	if got := countAlias(t, db, "old-name"); got != 0 {
		t.Fatalf("old-name 还剩 %d 行, want 0", got)
	}
	if got := countAlias(t, db, "new-name"); got != 3 {
		t.Fatalf("new-name = %d 行, want 3", got)
	}
}

// 改成同名: 空操作, 不报错也不动数据。
func TestRenameAlias_SameNameIsNoop(t *testing.T) {
	svc, db := newAliasWithRows(t, "same-name", "m-a")

	n, err := svc.RenameAlias(context.Background(), "same-name", "same-name")
	if err != nil {
		t.Fatalf("rename to same: %v", err)
	}
	if n != 0 {
		t.Fatalf("renamed = %d, want 0", n)
	}
	if got := countAlias(t, db, "same-name"); got != 1 {
		t.Fatalf("行数 = %d, want 1", got)
	}
}

// 守卫①: 保留别名(simple/medium/complex)不能改名 —— 它们是 auto 路由按名字寻址的档位池。
func TestRenameAlias_RejectsReservedSource(t *testing.T) {
	svc, db := newAliasWithRows(t, "medium", "m-a")

	if _, err := svc.RenameAlias(context.Background(), "medium", "not-reserved"); err == nil {
		t.Fatal("改保留别名应被拒, 实际通过了")
	}
	if got := countAlias(t, db, "medium"); got != 1 {
		t.Fatalf("被拒后 medium 行数 = %d, want 1 (不该被动过)", got)
	}
}

// 守卫②: 不能改成保留名 —— 那会撞进自动路由的命名空间。
func TestRenameAlias_RejectsReservedTarget(t *testing.T) {
	svc, db := newAliasWithRows(t, "custom", "m-a")

	if _, err := svc.RenameAlias(context.Background(), "custom", "complex"); err == nil {
		t.Fatal("改成保留名应被拒, 实际通过了")
	}
	if got := countAlias(t, db, "custom"); got != 1 {
		t.Fatalf("被拒后 custom 行数 = %d, want 1", got)
	}
}

// 守卫③(上周实机没走到的那条): 目标名已有候选则拒绝 —— 否则两边候选会静默合并,
// 权重分配莫名变化, 而且唯一索引 (alias, group_id, real_model) 会撞车。
func TestRenameAlias_RejectsExistingTarget(t *testing.T) {
	db := newTestDB(t)
	g := createGroup(t, db, &models.Group{
		Name: "grp-two", GroupType: "standard", ChannelType: "openai",
	})
	svc := NewAliasService(db)
	for _, a := range []struct{ alias, model string }{
		{"from-alias", "m-a"},
		{"to-alias", "m-b"},
	} {
		if _, err := svc.Create(context.Background(), AliasCreateRequest{
			Alias: a.alias, GroupID: g.ID, RealModel: a.model,
		}); err != nil {
			t.Fatalf("seed %s: %v", a.alias, err)
		}
	}

	if _, err := svc.RenameAlias(context.Background(), "from-alias", "to-alias"); err == nil {
		t.Fatal("改成已存在的别名应被拒, 实际通过了")
	}
	// 两边都必须原封不动 —— 被拒的改名不能留下半截状态。
	if got := countAlias(t, db, "from-alias"); got != 1 {
		t.Fatalf("from-alias = %d, want 1", got)
	}
	if got := countAlias(t, db, "to-alias"); got != 1 {
		t.Fatalf("to-alias = %d, want 1", got)
	}
}

// 按组收敛的别名列表: 只回指定 group 集合内启用的别名 —
// proxy model list 用它, 不把别的组的别名广播成"本组可用模型".
func TestListEnabledAliasNamesForGroups(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ga := createGroup(t, db, &models.Group{Name: "ga", ChannelType: "openai", TestModel: "m"})
	gb := createGroup(t, db, &models.Group{Name: "gb", ChannelType: "openai", TestModel: "m"})
	svc := NewAliasService(db)

	rows := []models.ModelAlias{
		{Alias: "alias-a", GroupID: ga.ID, RealModel: "m-a", Weight: 1, Enabled: true},
		{Alias: "alias-b", GroupID: gb.ID, RealModel: "m-b", Weight: 1, Enabled: true},
		{Alias: "alias-off", GroupID: ga.ID, RealModel: "m-a2", Weight: 1, Enabled: false},
		{Alias: "placeholder", GroupID: 0, RealModel: "", Weight: 1, Enabled: true},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", rows[i].Alias, err)
		}
	}

	names, err := svc.ListEnabledAliasNamesForGroups(ctx, []uint{ga.ID})
	if err != nil {
		t.Fatalf("list for ga: %v", err)
	}
	if !slices.Equal(names, []string{"alias-a"}) {
		t.Fatalf("names = %v, want [alias-a]", names)
	}

	both, err := svc.ListEnabledAliasNamesForGroups(ctx, []uint{ga.ID, gb.ID})
	if err != nil {
		t.Fatalf("list for ga+gb: %v", err)
	}
	if !slices.Equal(both, []string{"alias-a", "alias-b"}) {
		t.Fatalf("both = %v, want [alias-a alias-b]", both)
	}

	empty, err := svc.ListEnabledAliasNamesForGroups(ctx, nil)
	if err != nil || empty != nil {
		t.Fatalf("empty ids = (%v, %v), want (nil, nil)", empty, err)
	}
}
