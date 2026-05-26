package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"autogateway/internal/models"
	"autogateway/internal/types"

	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// mockConfigManager 模拟 ConfigManager 接口
type mockConfigManager struct {
	masterKey string
}

func (m *mockConfigManager) IsMaster() bool { return true }
func (m *mockConfigManager) GetAuthConfig() types.AuthConfig {
	return types.AuthConfig{Key: m.masterKey}
}
func (m *mockConfigManager) GetCORSConfig() types.CORSConfig { return types.CORSConfig{} }
func (m *mockConfigManager) GetPerformanceConfig() types.PerformanceConfig {
	return types.PerformanceConfig{}
}
func (m *mockConfigManager) GetLogConfig() types.LogConfig { return types.LogConfig{} }
func (m *mockConfigManager) GetDatabaseConfig() types.DatabaseConfig {
	return types.DatabaseConfig{}
}
func (m *mockConfigManager) GetEncryptionKey() string { return "" }
func (m *mockConfigManager) GetEffectiveServerConfig() types.ServerConfig {
	return types.ServerConfig{}
}
func (m *mockConfigManager) GetRedisDSN() string  { return "" }
func (m *mockConfigManager) Validate() error      { return nil }
func (m *mockConfigManager) DisplayServerConfig() {}
func (m *mockConfigManager) ReloadConfig() error  { return nil }

// 内存数据库实例化辅助函数 (为了避免与包内其他文件的 newTestDB 重名，使用 newSyncTestDB)
func newSyncTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}

	// 禁用日志输出以保持控制台整洁
	logrus.SetLevel(logrus.PanicLevel)

	// 自动迁移所有相关表（包含我们新增的 SyncPeer 与 SyncLog）
	err = db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.GroupSubGroup{},
		&models.APIKey{},
		&models.ModelAlias{},
		&models.SyncPeer{},
		&models.SyncLog{},
	)
	if err != nil {
		t.Fatalf("failed to auto migrate database: %v", err)
	}

	return db
}

func TestSyncService_EncryptDecrypt(t *testing.T) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "test-node-1"}
	svc := NewSyncService(db, cfg)

	payload := &SyncPayload{
		SourcePeerID: "test-node-1",
		Timestamp:    time.Now(),
		Settings: []models.SystemSetting{
			{
				ID:           1,
				SettingKey:   "test_key",
				SettingValue: "test_val",
				CreatedAt:    time.Now(),
				UpdatedAt:    time.Now(),
			},
		},
	}

	syncKey := "my-shared-secure-sync-key-123456"

	// 1. 测试加密
	ciphertext, err := svc.EncryptPayload(payload, syncKey)
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}
	if len(ciphertext) == 0 {
		t.Fatal("ciphertext is empty")
	}

	// 2. 测试解密
	decrypted, err := svc.DecryptPayload(ciphertext, syncKey)
	if err != nil {
		t.Fatalf("failed to decrypt: %v", err)
	}

	if decrypted.SourcePeerID != payload.SourcePeerID {
		t.Errorf("decrypted SourcePeerID = %s, want %s", decrypted.SourcePeerID, payload.SourcePeerID)
	}
	if len(decrypted.Settings) != 1 || decrypted.Settings[0].SettingValue != "test_val" {
		t.Errorf("decrypted settings mismatch")
	}

	// 3. 测试错误密钥解密失败
	_, err = svc.DecryptPayload(ciphertext, "wrong-key")
	if err == nil {
		t.Fatal("decrypt with wrong key should have failed")
	}
}

func TestSyncService_ProcessPayload_LWW_And_Tombstone(t *testing.T) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "test-node-1"}
	svc := NewSyncService(db, cfg)

	ctx := context.Background()

	// 预先向数据库写入一条数据：A 组，更新时间为一小时前
	oneHourAgo := time.Now().Add(-1 * time.Hour)
	existingGroup := models.Group{
		ID:          10,
		Name:        "group-a",
		DisplayName: "本地旧名称",
		ChannelType: "openai",
		TestModel:   "gpt-4o",
		Upstreams:   []byte("[]"),
		CreatedAt:   oneHourAgo,
		UpdatedAt:   oneHourAgo,
	}
	if err := db.Create(&existingGroup).Error; err != nil {
		t.Fatalf("failed to seed: %v", err)
	}

	// 场景 A：传入一条更新的记录（最新写入生效 LWW）
	justNow := time.Now()
	incomingPayload := &SyncPayload{
		SourcePeerID: "remote-node-2",
		Timestamp:    justNow,
		Groups: []models.Group{
			{
				ID:          10,
				Name:        "group-a",
				DisplayName: "对端新名称",
				ChannelType: "openai",
				TestModel:   "gpt-4o",
				Upstreams:   []byte("[]"),
				CreatedAt:   oneHourAgo,
				UpdatedAt:   justNow, // 更加新
			},
			{
				ID:          20, // 新记录
				Name:        "group-b",
				DisplayName: "对端新增组",
				ChannelType: "openai",
				TestModel:   "gpt-4o",
				Upstreams:   []byte("[]"),
				CreatedAt:   justNow,
				UpdatedAt:   justNow,
			},
		},
	}

	if err := svc.ProcessPayload(ctx, incomingPayload); err != nil {
		t.Fatalf("ProcessPayload failed: %v", err)
	}

	// 验证已存在的 ID=10 记录是否被成功更新
	var g10 models.Group
	if err := db.First(&g10, 10).Error; err != nil {
		t.Fatalf("failed to find group 10: %v", err)
	}
	if g10.DisplayName != "对端新名称" {
		t.Errorf("expected LWW update, got DisplayName = %s", g10.DisplayName)
	}

	// 验证新增的 ID=20 记录是否已保存
	var g20 models.Group
	if err := db.First(&g20, 20).Error; err != nil {
		t.Fatalf("failed to find group 20: %v", err)
	}
	if g20.Name != "group-b" {
		t.Errorf("expected newly created group, got Name = %s", g20.Name)
	}

	// 场景 B：传入一条较旧的记录，应该被忽略（LWW）
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	olderPayload := &SyncPayload{
		SourcePeerID: "remote-node-2",
		Timestamp:    justNow,
		Groups: []models.Group{
			{
				ID:          10,
				Name:        "group-a",
				DisplayName: "古老的名称（不应该生效）",
				ChannelType: "openai",
				TestModel:   "gpt-4o",
				Upstreams:   []byte("[]"),
				CreatedAt:   oneHourAgo,
				UpdatedAt:   twoHoursAgo, // 较旧
			},
		},
	}

	if err := svc.ProcessPayload(ctx, olderPayload); err != nil {
		t.Fatalf("ProcessPayload failed: %v", err)
	}

	if err := db.First(&g10, 10).Error; err != nil {
		t.Fatalf("failed to find group 10: %v", err)
	}
	if g10.DisplayName != "对端新名称" {
		t.Errorf("older payload should have been ignored, got DisplayName = %s", g10.DisplayName)
	}

	// 场景 C：软删除墓碑同步 (Tombstones)
	deletedTime := time.Now()
	tombstonePayload := &SyncPayload{
		SourcePeerID: "remote-node-2",
		Timestamp:    deletedTime,
		Groups: []models.Group{
			{
				ID:          10,
				Name:        "group-a",
				DisplayName: "对端新名称",
				ChannelType: "openai",
				TestModel:   "gpt-4o",
				Upstreams:   []byte("[]"),
				CreatedAt:   oneHourAgo,
				UpdatedAt:   deletedTime,
				DeletedAt:   gorm.DeletedAt{Time: deletedTime, Valid: true}, // 墓碑标记
			},
		},
	}

	if err := svc.ProcessPayload(ctx, tombstonePayload); err != nil {
		t.Fatalf("ProcessPayload failed: %v", err)
	}

	// 常规查询应该查询不到已被软删除的记录
	var g10Check models.Group
	err := db.First(&g10Check, 10).Error
	if err == nil {
		t.Error("soft deleted record should not be found by regular query")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected ErrRecordNotFound, got %v", err)
	}

	// Unscoped 查询应该可以找到，且其 DeletedAt 不为空
	var g10Unscoped models.Group
	if err := db.Unscoped().First(&g10Unscoped, 10).Error; err != nil {
		t.Fatalf("unscoped query failed: %v", err)
	}
	if !g10Unscoped.DeletedAt.Valid {
		t.Error("expected valid DeletedAt timestamp on unscoped record")
	}
}
