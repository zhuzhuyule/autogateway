package services

import (
	"context"
	"testing"
	"time"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

func activeKeyCount(t *testing.T, db *gorm.DB, groupID uint, keyHash string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.APIKey{}).
		Where("group_id = ? AND key_hash = ? AND deleted_at IS NULL", groupID, keyHash).
		Count(&n).Error; err != nil {
		t.Fatalf("count active: %v", err)
	}
	return n
}

func newMergeSvc(t *testing.T) (*SyncService, *gorm.DB) {
	t.Helper()
	db := newSyncTestDB(t)
	svc := NewSyncService(db, &mockConfigManager{masterKey: "node-B"}, NewNodeKeypairService(), nil)
	return svc, db
}

// delete 后重加相同 key: 合并对端更新版的活 key 时, 绝不能复活旧墓碑而产生第二条活记录.
// 这是 dup_hash(cc336aeb "2 条都活") churn 的直接重现.
func TestProcessPayload_RepaddedKey_NoDuplicateActive(t *testing.T) {
	svc, db := newMergeSvc(t)
	ctx := context.Background()

	t1 := time.Now().Add(-3 * time.Hour)
	t2 := time.Now().Add(-2 * time.Hour)
	t3 := time.Now().Add(-1 * time.Hour)

	// 本端: 旧墓碑(ID=1, deleted_at=t1) + 重加的活(ID=2, updated_at=t2), 同 key_hash
	db.Create(&models.APIKey{ID: 1, GroupID: 1, KeyValue: "sk-X", KeyHash: "hashX", Status: models.KeyStatusActive, CreatedAt: t1, UpdatedAt: t1})
	db.Model(&models.APIKey{}).Where("id = ?", 1).Update("deleted_at", t1) // 变墓碑, deleted_at 设成旧时间
	db.Create(&models.APIKey{ID: 2, GroupID: 1, KeyValue: "sk-X", KeyHash: "hashX", Status: models.KeyStatusActive, CreatedAt: t2, UpdatedAt: t2})

	// incoming: 对端更新版的活 key(updated_at 更晚)
	payload := &SyncPayload{APIKeys: []models.APIKey{
		{GroupID: 1, KeyValue: "sk-X", KeyHash: "hashX", Status: models.KeyStatusActive, UpdatedAt: t3},
	}}
	if err := svc.ProcessPayload(ctx, payload); err != nil {
		t.Fatalf("process: %v", err)
	}

	// 断言1: incoming 的更新应落到"活"记录(ID=2), 而非墓碑
	var active models.APIKey
	if err := db.Where("group_id = ? AND key_hash = ? AND deleted_at IS NULL", 1, "hashX").First(&active).Error; err != nil {
		t.Fatalf("no active key found: %v", err)
	}
	if active.ID != 2 || !active.UpdatedAt.Equal(t3) {
		t.Fatalf("incoming 应更新活记录 ID=2 至 updated_at=t3, got ID=%d updated=%v (bug: 更新落到墓碑上, 活 key 没同步)", active.ID, active.UpdatedAt)
	}
	// 断言2: 墓碑(ID=1)的 updated_at 不能被抬高, 否则每轮 export 重命中 = churn
	var tomb models.APIKey
	db.Unscoped().Where("id = ?", 1).First(&tomb)
	if tomb.UpdatedAt.Equal(t3) {
		t.Fatalf("墓碑 ID=1 的 updated_at 被污染成 t3 → 每轮增量导出重命中 = churn 根因")
	}
	// 断言3: 活记录唯一
	if got := activeKeyCount(t, db, 1, "hashX"); got != 1 {
		t.Fatalf("expected exactly 1 active key, got %d", got)
	}
}

// 同一 payload 合并两次必须幂等: 不重复新建, 不 churn.
func TestProcessPayload_Idempotent(t *testing.T) {
	svc, db := newMergeSvc(t)
	ctx := context.Background()
	tk := time.Now().Add(-1 * time.Hour)

	payload := &SyncPayload{APIKeys: []models.APIKey{
		{GroupID: 1, KeyValue: "sk-I", KeyHash: "hashI", Status: models.KeyStatusActive, UpdatedAt: tk},
	}}
	for i := 0; i < 3; i++ {
		if err := svc.ProcessPayload(ctx, payload); err != nil {
			t.Fatalf("process #%d: %v", i, err)
		}
	}
	if got := activeKeyCount(t, db, 1, "hashI"); got != 1 {
		t.Fatalf("expected 1 active after 3x merge (idempotent), got %d", got)
	}
}

// incoming 旧墓碑不应误删本端更晚重加的活 key.
func TestProcessPayload_OldTombstone_DoesNotKillNewerActive(t *testing.T) {
	svc, db := newMergeSvc(t)
	ctx := context.Background()
	tOld := time.Now().Add(-2 * time.Hour)
	tNew := time.Now().Add(-1 * time.Hour)

	db.Create(&models.APIKey{ID: 1, GroupID: 1, KeyValue: "sk-Y", KeyHash: "hashY", Status: models.KeyStatusActive, CreatedAt: tNew, UpdatedAt: tNew})

	tomb := models.APIKey{GroupID: 1, KeyValue: "sk-Y", KeyHash: "hashY", UpdatedAt: tOld}
	tomb.DeletedAt = gorm.DeletedAt{Time: tOld, Valid: true}
	if err := svc.ProcessPayload(ctx, &SyncPayload{APIKeys: []models.APIKey{tomb}}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := activeKeyCount(t, db, 1, "hashY"); got != 1 {
		t.Fatalf("expected active key to survive old tombstone, got %d", got)
	}
}

// incoming 新墓碑应软删本端更旧的活 key(删除信号 LWW 生效).
func TestProcessPayload_NewTombstone_DeletesOlderActive(t *testing.T) {
	svc, db := newMergeSvc(t)
	ctx := context.Background()
	tOld := time.Now().Add(-2 * time.Hour)
	tNew := time.Now().Add(-1 * time.Hour)

	db.Create(&models.APIKey{ID: 1, GroupID: 1, KeyValue: "sk-Z", KeyHash: "hashZ", Status: models.KeyStatusActive, CreatedAt: tOld, UpdatedAt: tOld})

	tomb := models.APIKey{GroupID: 1, KeyValue: "sk-Z", KeyHash: "hashZ", UpdatedAt: tOld}
	tomb.DeletedAt = gorm.DeletedAt{Time: tNew, Valid: true}
	if err := svc.ProcessPayload(ctx, &SyncPayload{APIKeys: []models.APIKey{tomb}}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := activeKeyCount(t, db, 1, "hashZ"); got != 0 {
		t.Fatalf("expected active key soft-deleted by newer tombstone, got %d", got)
	}
}
