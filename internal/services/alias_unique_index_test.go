package services

import (
	"context"
	"testing"

	migrations "autogateway/internal/db/migrations"
	"autogateway/internal/models"

	"gorm.io/gorm"
)

// newTestDBWithAliasUniqueIndex 复刻生产启动顺序: AutoMigrate 建表(不再带唯一索引
// tag) → V2_8_3 建部分唯一索引。少了第二步, 这里测的东西根本不存在。
func newTestDBWithAliasUniqueIndex(t *testing.T) *gorm.DB {
	t.Helper()
	gormDB := newTestDB(t)
	if err := migrations.V2_8_3_PartialUniqueAliasCandidate(gormDB); err != nil {
		t.Fatalf("apply V2_8_3: %v", err)
	}
	return gormDB
}

func aliasRowsOf(t *testing.T, gormDB *gorm.DB, alias string) []models.ModelAlias {
	t.Helper()
	var rows []models.ModelAlias
	if err := gormDB.Where("alias = ? AND group_id <> 0", alias).Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("list rows: %v", err)
	}
	return rows
}

// 「删除别名 → 撤销」是编辑抽屉的一条正常路径: 先整体替换成空集(软删), 再把同一批
// 三元组写回来。唯一索引若把墓碑算在内, 第二步必然 UNIQUE constraint failed → 400。
func TestReplaceCandidates_ReAddAfterSoftDelete(t *testing.T) {
	ctx := context.Background()
	gormDB := newTestDBWithAliasUniqueIndex(t)
	svc := NewAliasService(gormDB)
	g := createGroup(t, gormDB, &models.Group{Name: "openai-main"})
	triple := []AliasCandidateInput{{GroupID: g.ID, RealModel: "gpt-4o", Weight: 10, Priority: 1}}

	if _, err := svc.ReplaceCandidates(ctx, "undo-me", triple); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if _, err := svc.ReplaceCandidates(ctx, "undo-me", nil); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	if _, err := svc.ReplaceCandidates(ctx, "undo-me", triple); err != nil {
		t.Fatalf("re-add after soft delete (undo): %v", err)
	}

	rows := aliasRowsOf(t, gormDB, "undo-me")
	if len(rows) != 1 {
		t.Fatalf("active rows after undo = %d, want 1", len(rows))
	}
	if rows[0].RealModel != "gpt-4o" || rows[0].Weight != 10 {
		t.Fatalf("restored candidate = %+v, want gpt-4o weight 10", rows[0])
	}
}

// 部分索引只放过墓碑, 不放过两条活行 —— 重复三元组仍然必须被挡(SWRR 权重翻倍)。
func TestReplaceCandidates_StillRejectsActiveDuplicate(t *testing.T) {
	ctx := context.Background()
	gormDB := newTestDBWithAliasUniqueIndex(t)
	svc := NewAliasService(gormDB)
	g := createGroup(t, gormDB, &models.Group{Name: "openai-main"})

	// 绕过 ReplaceCandidates 自己建一行, 再手动插一条同三元组的活行。
	if _, err := svc.ReplaceCandidates(ctx, "dup", []AliasCandidateInput{
		{GroupID: g.ID, RealModel: "gpt-4o", Weight: 5, Priority: 1},
	}); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	err := gormDB.Create(&models.ModelAlias{
		Alias: "dup", GroupID: g.ID, RealModel: "gpt-4o", Weight: 5, Priority: 1, Enabled: true,
	}).Error
	if err == nil {
		t.Fatal("active duplicate insert succeeded: partial unique index is not enforcing")
	}

	// 同一批输入里出现重复三元组时, ReplaceCandidates 自己会收敛成一条, 不该报错。
	if _, err := svc.ReplaceCandidates(ctx, "dup", []AliasCandidateInput{
		{GroupID: g.ID, RealModel: "gpt-4o", Weight: 5, Priority: 1},
		{GroupID: g.ID, RealModel: "gpt-4o", Weight: 7, Priority: 2},
	}); err != nil {
		t.Fatalf("replace with in-list duplicate: %v", err)
	}
	rows := aliasRowsOf(t, gormDB, "dup")
	if len(rows) != 1 {
		t.Fatalf("active rows = %d, want 1", len(rows))
	}
	if rows[0].Weight != 7 {
		t.Fatalf("weight = %d, want 7 (last entry wins)", rows[0].Weight)
	}
}

// 迁移必须能反复跑: 每次启动都会执行一遍。
func TestV2_8_3_PartialUniqueAliasCandidateIsIdempotent(t *testing.T) {
	gormDB := newTestDB(t)
	for i := 0; i < 3; i++ {
		if err := migrations.V2_8_3_PartialUniqueAliasCandidate(gormDB); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	var idxName string
	err := gormDB.Raw(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_alias_group_model_active'`,
	).Scan(&idxName).Error
	if err != nil {
		t.Fatalf("introspect index: %v", err)
	}
	if idxName != "idx_alias_group_model_active" {
		t.Fatal("partial unique index missing after migration")
	}
}
