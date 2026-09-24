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
