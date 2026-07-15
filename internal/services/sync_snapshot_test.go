package services

import (
	"context"
	"testing"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

// newTestSyncService 抽自现有测试的 setup 三段式(newSyncTestDB + mockConfigManager +
// NewSyncService), 返回 (service, db) 供快照测试直接查库断言。keypoolInvalidator=nil,
// ApplySnapshot 里有 nil 检查, 不会 panic。
func newTestSyncService(t *testing.T) (*SyncService, *gorm.DB) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "test-node"}
	return NewSyncService(db, cfg, NewNodeKeypairService(), nil), db
}

func TestApplySnapshot_CreatesMissing(t *testing.T) {
	s, db := newTestSyncService(t)
	// master 快照: group(id=1) + 一个 key(引用 master group_id=1), 本地空
	snap := &SyncPayload{
		Groups:  []models.Group{{ID: 1, Name: "g1", ChannelType: "openai", Upstreams: []byte("[]")}},
		APIKeys: []models.APIKey{{GroupID: 1, KeyValue: "k1", KeyHash: "h1", Status: models.KeyStatusActive}},
	}
	if err := s.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var gc, kc int64
	db.Model(&models.Group{}).Where("deleted_at IS NULL").Count(&gc)
	db.Model(&models.APIKey{}).Where("deleted_at IS NULL").Count(&kc)
	if gc != 1 || kc != 1 {
		t.Fatalf("期望镜像出 1 group / 1 key, got %d/%d", gc, kc)
	}
}

func TestApplySnapshot_DeletesExtra(t *testing.T) {
	s, db := newTestSyncService(t)
	// 本地先有 g1 + 两个 key; master 快照只有 g1(k1) → k2 应被镜像软删
	db.Create(&models.Group{Name: "g1", ChannelType: "openai", Upstreams: []byte("[]")})
	db.Create(&models.APIKey{GroupID: 1, KeyValue: "k1", KeyHash: "h1", Status: models.KeyStatusActive})
	db.Create(&models.APIKey{GroupID: 1, KeyValue: "k2", KeyHash: "h2", Status: models.KeyStatusActive})

	snap := &SyncPayload{
		Groups:  []models.Group{{ID: 1, Name: "g1", ChannelType: "openai", Upstreams: []byte("[]")}},
		APIKeys: []models.APIKey{{GroupID: 1, KeyValue: "k1", KeyHash: "h1", Status: models.KeyStatusActive}},
	}
	if err := s.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var alive int64
	db.Model(&models.APIKey{}).Where("deleted_at IS NULL").Count(&alive)
	if alive != 1 {
		t.Fatalf("多余的 k2 应被镜像软删, 剩 %d 活 key", alive)
	}
}

func TestApplySnapshot_SplitBrainConverges(t *testing.T) {
	s, db := newTestSyncService(t)
	// 分裂态: 本地 h1 是"活"的, master 快照里该 group 无任何 key(等价 master 侧已删)
	db.Create(&models.Group{Name: "agnes", ChannelType: "openai", Upstreams: []byte("[]")})
	db.Create(&models.APIKey{GroupID: 1, KeyValue: "k1", KeyHash: "h1", Status: models.KeyStatusActive})
	snap := &SyncPayload{Groups: []models.Group{{ID: 1, Name: "agnes", ChannelType: "openai", Upstreams: []byte("[]")}}}
	if err := s.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var alive int64
	db.Model(&models.APIKey{}).Where("group_id=1 AND deleted_at IS NULL").Count(&alive)
	if alive != 0 {
		t.Fatal("镜像后应随 master 删除本地多余活 key(分裂态收敛)")
	}
}

func TestApplySnapshot_ExcludedCategorySkipped(t *testing.T) {
	s, db := newTestSyncService(t)
	db.Create(&models.SystemSetting{SettingKey: "app_url", SettingValue: "http://local"})
	snap := &SyncPayload{
		Policy:   &SyncPolicy{ExcludedCategories: []string{"setting"}},
		Settings: []models.SystemSetting{{SettingKey: "app_url", SettingValue: "http://master"}},
	}
	if err := s.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var v models.SystemSetting
	db.Where("setting_key = ?", "app_url").First(&v)
	if v.SettingValue != "http://local" {
		t.Fatal("setting 类别被排除, 不应被 master 覆盖")
	}
}
