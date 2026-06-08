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
	svc := NewSyncService(db, cfg, NewNodeKeypairService(), nil)

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
	svc := NewSyncService(db, cfg, NewNodeKeypairService(), nil)

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

	// 验证已存在的 group-a 记录是否被成功更新 (LWW per name, 不按 id)
	var gA models.Group
	if err := db.Where("name = ?", "group-a").First(&gA).Error; err != nil {
		t.Fatalf("failed to find group-a: %v", err)
	}
	if gA.DisplayName != "对端新名称" {
		t.Errorf("expected LWW update, got DisplayName = %s", gA.DisplayName)
	}

	// 验证新增的 group-b 记录是否已保存 (本端自增分配 id, 不沿用对端 id)
	var gB models.Group
	if err := db.Where("name = ?", "group-b").First(&gB).Error; err != nil {
		t.Fatalf("failed to find group-b: %v", err)
	}
	if gB.Name != "group-b" {
		t.Errorf("expected newly created group, got Name = %s", gB.Name)
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

	if err := db.Where("name = ?", "group-a").First(&gA).Error; err != nil {
		t.Fatalf("failed to find group-a: %v", err)
	}
	if gA.DisplayName != "对端新名称" {
		t.Errorf("older payload should have been ignored, got DisplayName = %s", gA.DisplayName)
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

// TestProcessPayload_MarksSyncMergeContext 验证 ProcessPayload 在事务 context 上
// 设置了 syncMergeKey 标记,GORM hook 可借此判断当前是合并事务,从而短路 NotifyChange,
// 防止 A→B→A 同步回环.
func TestProcessPayload_MarksSyncMergeContext(t *testing.T) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "node-merge"}
	svc := NewSyncService(db, cfg, NewNodeKeypairService(), nil)

	var (
		mergeHookSawFlag    bool
		nonMergeHookSawFlag bool
	)

	// 注册一个 hook,记录 hook 触发时 context 里的 syncMergeKey 状态
	hookFn := func(target *bool) func(tx *gorm.DB) {
		return func(tx *gorm.DB) {
			if tx.Statement == nil {
				return
			}
			if IsSyncMerge(tx.Statement.Context) {
				*target = true
			}
		}
	}

	// 注意: gorm 同名 callback 不能重复注册,我们用两个 hook 名分别测试两路径
	if err := db.Callback().Create().After("gorm:create").Register("test_merge_hook", hookFn(&mergeHookSawFlag)); err != nil {
		t.Fatalf("failed to register merge hook: %v", err)
	}
	defer db.Callback().Create().Remove("test_merge_hook")

	// 路径 1: 通过 ProcessPayload 触发 → hook 应看到 flag=true
	payload := &SyncPayload{
		SourcePeerID: "node-other",
		Timestamp:    time.Now(),
		ModelAliases: []models.ModelAlias{
			{
				ID:        7777,
				Alias:     "loop-defense",
				GroupID:   1,
				RealModel: "test-model",
				Enabled:   true,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}
	if err := svc.ProcessPayload(context.Background(), payload); err != nil {
		t.Fatalf("ProcessPayload failed: %v", err)
	}
	if !mergeHookSawFlag {
		t.Error("expected merge hook to see syncMergeKey=true after ProcessPayload")
	}

	// 路径 2: 直接 DB 操作不带标记 → hook 不应看到 flag=true
	db.Callback().Create().Remove("test_merge_hook")
	if err := db.Callback().Create().After("gorm:create").Register("test_normal_hook", hookFn(&nonMergeHookSawFlag)); err != nil {
		t.Fatalf("failed to register normal hook: %v", err)
	}
	defer db.Callback().Create().Remove("test_normal_hook")

	if err := db.Create(&models.ModelAlias{
		ID:        8888,
		Alias:     "user-write",
		GroupID:   1,
		RealModel: "user-model",
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("direct create failed: %v", err)
	}
	if nonMergeHookSawFlag {
		t.Error("expected normal hook NOT to see syncMergeKey on direct write")
	}
}

// TestPayloadSummary 验证 payloadSummary 输出格式
func TestPayloadSummary(t *testing.T) {
	cases := []struct {
		name string
		p    SyncPayload
		want string
	}{
		{"empty", SyncPayload{}, "(empty)"},
		{
			"groups only",
			SyncPayload{Groups: []models.Group{{}, {}, {}}},
			"groups=3",
		},
		{
			"mixed",
			SyncPayload{
				Settings:     []models.SystemSetting{{}},
				Groups:       []models.Group{{}, {}},
				ModelAliases: []models.ModelAlias{{}, {}, {}, {}, {}},
			},
			"settings=1,groups=2,aliases=5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := payloadSummary(&tc.p)
			if got != tc.want {
				t.Errorf("payloadSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}
