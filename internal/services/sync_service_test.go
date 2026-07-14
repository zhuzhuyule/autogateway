package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"autogateway/internal/models"
	"autogateway/internal/types"

	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
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

// TestSyncService_ProcessPayload_FieldDeletionSurvivesStalePeer 复现并锁定
// "删 Config 字段后被同步还原": 本端记录整体更新(record updated_at 更新, 但
// 没碰 rpm_limit), 对端记录整体更旧、却带着"更新的 rpm_limit 删除版本"。
// 旧的记录级 LWW 会因对端 record 更旧而整块跳过 → rpm_limit 复活(bug)。
// 字段级 LWW 应让删除按字段版本胜出。
func TestSyncService_ProcessPayload_FieldDeletionSurvivesStalePeer(t *testing.T) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "test-node-1"}
	svc := NewSyncService(db, cfg, NewNodeKeypairService(), nil)
	ctx := context.Background()

	justNow := time.Now()
	oneHourAgo := justNow.Add(-1 * time.Hour)

	// 本端: rpm_limit 仍在(旧字段版本 100), 但 record 最近被动过(justNow)
	existing := models.Group{
		ID:             10,
		Name:           "group-fld",
		ChannelType:    "openai",
		TestModel:      "gpt-4o",
		Upstreams:      []byte("[]"),
		Config:         datatypes.JSONMap{"rpm_limit": 500},
		ConfigVersions: datatypes.JSONMap{"config.rpm_limit": int64(100)},
		CreatedAt:      oneHourAgo,
		UpdatedAt:      justNow,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	// 对端: 已删除 rpm_limit(字段版本 200 更新), 但整条 record 更旧(oneHourAgo)
	incoming := &SyncPayload{
		SourcePeerID: "remote-node-2",
		Timestamp:    justNow,
		Groups: []models.Group{{
			ID:             10,
			Name:           "group-fld",
			ChannelType:    "openai",
			TestModel:      "gpt-4o",
			Upstreams:      []byte("[]"),
			Config:         datatypes.JSONMap{}, // rpm_limit 已删
			ConfigVersions: datatypes.JSONMap{"config.rpm_limit": int64(200)},
			CreatedAt:      oneHourAgo,
			UpdatedAt:      oneHourAgo,
		}},
	}
	if err := svc.ProcessPayload(ctx, incoming); err != nil {
		t.Fatalf("ProcessPayload failed: %v", err)
	}

	var got models.Group
	if err := db.Where("name = ?", "group-fld").First(&got).Error; err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if _, ok := got.Config["rpm_limit"]; ok {
		t.Fatalf("rpm_limit 应保持删除(对端删除字段版本更新), 却复活为 %v", got.Config["rpm_limit"])
	}
}

// TestSyncService_ProcessPayload_ConcurrentEditDoesNotResurrect 锁定"对端整条
// record 更新(编辑了别的字段、且仍留着被删字段)时, 本端的字段删除不被还原"。
// 这是 record 赢方 = 对端的路径: 非 JSON 字段用对端, 但被删字段按字段级版本仍归本端。
func TestSyncService_ProcessPayload_ConcurrentEditDoesNotResurrect(t *testing.T) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "test-node-1"}
	svc := NewSyncService(db, cfg, NewNodeKeypairService(), nil)
	ctx := context.Background()

	justNow := time.Now()
	oneHourAgo := justNow.Add(-1 * time.Hour)

	// 本端: 已删除 b(字段版本 200), record 整体较旧
	existing := models.Group{
		ID:          10,
		Name:        "group-cc",
		ChannelType: "openai",
		TestModel:   "gpt-4o",
		Upstreams:   []byte("[]"),
		Config:      datatypes.JSONMap{"a": 1},
		ConfigVersions: datatypes.JSONMap{
			"config.a": int64(50), "config.b": int64(200), // b 于 200 被删
		},
		CreatedAt: oneHourAgo,
		UpdatedAt: oneHourAgo,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	// 对端: 编辑了 c(record 整体更新 justNow), 但从没见过 b 的删除, 仍带 b=2(旧版本 100)
	incoming := &SyncPayload{
		SourcePeerID: "remote-node-2",
		Timestamp:    justNow,
		Groups: []models.Group{{
			ID:          10,
			Name:        "group-cc",
			ChannelType: "openai",
			TestModel:   "gpt-4o",
			Upstreams:   []byte("[]"),
			Config:      datatypes.JSONMap{"a": 1, "b": 2, "c": 9},
			ConfigVersions: datatypes.JSONMap{
				"config.a": int64(50), "config.b": int64(100), "config.c": int64(300),
			},
			CreatedAt: oneHourAgo,
			UpdatedAt: justNow, // record 整体更新 → 对端赢记录级
		}},
	}
	if err := svc.ProcessPayload(ctx, incoming); err != nil {
		t.Fatalf("ProcessPayload failed: %v", err)
	}

	var got models.Group
	if err := db.Where("name = ?", "group-cc").First(&got).Error; err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if _, ok := got.Config["b"]; ok {
		t.Fatalf("b 应保持删除(本端删除版本 200 > 对端 100), 却复活为 %v", got.Config["b"])
	}
	if v, ok := got.Config["c"]; !ok || toInt64Version(v) != 9 {
		t.Fatalf("c 应为对端新增的 9(对端字段版本更新), 得到 %v(存在=%v)", got.Config["c"], ok)
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

// 代理按机器: proxy_url 是机器本地配置, 同步 merge 不得跨机器覆盖。
func TestSyncService_ProcessPayload_PreservesLocalProxyURL(t *testing.T) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "test-node-1"}
	svc := NewSyncService(db, cfg, NewNodeKeypairService(), nil)
	ctx := context.Background()
	oneHourAgo := time.Now().Add(-1 * time.Hour)
	justNow := time.Now()

	// 本机已有 group-a, 配了本机代理 + 一个普通 config
	local := models.Group{
		ID: 10, Name: "group-a", DisplayName: "本地", ChannelType: "openai",
		TestModel: "gpt-4o", Upstreams: []byte("[]"),
		Config:    datatypes.JSONMap{"proxy_url": "http://127.0.0.1:7890", "max_retries": float64(3)},
		CreatedAt: oneHourAgo, UpdatedAt: oneHourAgo,
	}
	if err := db.Create(&local).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 远端推来更新的 group-a: 带远端代理 + 改了 max_retries
	incoming := &SyncPayload{
		SourcePeerID: "remote", Timestamp: justNow,
		Groups: []models.Group{{
			ID: 10, Name: "group-a", DisplayName: "远端", ChannelType: "openai",
			TestModel: "gpt-4o", Upstreams: []byte("[]"),
			Config:    datatypes.JSONMap{"proxy_url": "http://vps:8080", "max_retries": float64(9)},
			CreatedAt: oneHourAgo, UpdatedAt: justNow,
		}},
	}
	if err := svc.ProcessPayload(ctx, incoming); err != nil {
		t.Fatalf("ProcessPayload: %v", err)
	}

	var g models.Group
	if err := db.Where("name = ?", "group-a").First(&g).Error; err != nil {
		t.Fatalf("find group-a: %v", err)
	}
	// proxy_url 保留本机(代理按机器)
	if g.Config["proxy_url"] != "http://127.0.0.1:7890" {
		t.Errorf("proxy_url should stay local, got %v", g.Config["proxy_url"])
	}
	// 其它 config 字段仍同步远端
	if fmt.Sprint(g.Config["max_retries"]) != "9" {
		t.Errorf("max_retries should sync from remote, got %v", g.Config["max_retries"])
	}
	// 非 config 字段仍 LWW
	if g.DisplayName != "远端" {
		t.Errorf("other fields should LWW, got %s", g.DisplayName)
	}

	// 新 group-b 从远端来: proxy_url 不继承(本机没配过)
	incoming2 := &SyncPayload{
		SourcePeerID: "remote", Timestamp: justNow,
		Groups: []models.Group{{
			ID: 20, Name: "group-b", ChannelType: "openai", TestModel: "gpt-4o",
			Upstreams: []byte("[]"),
			Config:    datatypes.JSONMap{"proxy_url": "http://vps:8080"},
			CreatedAt: justNow, UpdatedAt: justNow,
		}},
	}
	if err := svc.ProcessPayload(ctx, incoming2); err != nil {
		t.Fatalf("ProcessPayload2: %v", err)
	}
	var gb models.Group
	if err := db.Where("name = ?", "group-b").First(&gb).Error; err != nil {
		t.Fatalf("find group-b: %v", err)
	}
	if v, ok := gb.Config["proxy_url"]; ok {
		t.Errorf("new group from remote must NOT inherit proxy_url, got %v", v)
	}
}

// TestSyncService_ProcessPayload_PreservesWinnerUpdatedAt 锁定 LWW 时间戳不被
// merge 时刻污染。GORM 对 struct Save 会无条件把 AutoUpdateTime 字段(UpdatedAt)
// 改写成 NowFunc()(callbacks/update.go 的 struct 分支, 除非 SkipHooks)。若合并写入
// 不阻止它, 胜出记录的 updated_at 会被抬到"本机 merge 时刻", 造成:
//   1. 时钟前移: 一条 T=10 的旧编辑合并后 updated_at 变成 now, 之后一条 T=50 的
//      "真正更新"因 effectiveTime(50) < effectiveTime(now) 反被判旧 → 用户的新
//      改动被旧值覆盖(审计点 1 的核心危害)。
//   2. 无限 churn: merge 后 updated_at=now > since, 每轮增量导出都重新命中该记录,
//      两端来回抬时间戳, 永不收敛。
// 修复: 合并 Save 走 SkipHooks, 保留 payload 携带的 UpdatedAt。
func TestSyncService_ProcessPayload_PreservesWinnerUpdatedAt(t *testing.T) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "test-node-1"}
	svc := NewSyncService(db, cfg, NewNodeKeypairService(), nil)
	ctx := context.Background()

	oneHourAgo := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	// 胜出方的编辑时刻: 比本地新(30 分钟前 > 1 小时前), 但明显不是"现在"。
	remoteEdit := time.Now().Add(-30 * time.Minute).Truncate(time.Second)

	// ---- Group ----
	if err := db.Create(&models.Group{
		ID: 10, Name: "group-ts", DisplayName: "本地", ChannelType: "openai",
		TestModel: "gpt-4o", Upstreams: []byte("[]"),
		CreatedAt: oneHourAgo, UpdatedAt: oneHourAgo,
	}).Error; err != nil {
		t.Fatalf("seed group: %v", err)
	}

	// ---- APIKey ----
	if err := db.Create(&models.APIKey{
		ID: 1, GroupID: 10, KeyValue: "sk-x", KeyHash: "h-x", Status: "active",
		CreatedAt: oneHourAgo, UpdatedAt: oneHourAgo,
	}).Error; err != nil {
		t.Fatalf("seed key: %v", err)
	}

	incoming := &SyncPayload{
		SourcePeerID: "remote-node-2",
		Timestamp:    time.Now(),
		Groups: []models.Group{{
			ID: 10, Name: "group-ts", DisplayName: "远端", ChannelType: "openai",
			TestModel: "gpt-4o", Upstreams: []byte("[]"),
			CreatedAt: oneHourAgo, UpdatedAt: remoteEdit,
		}},
		APIKeys: []models.APIKey{{
			ID: 1, GroupID: 10, KeyValue: "sk-x", KeyHash: "h-x", Status: "disabled",
			CreatedAt: oneHourAgo, UpdatedAt: remoteEdit,
		}},
	}
	if err := svc.ProcessPayload(ctx, incoming); err != nil {
		t.Fatalf("ProcessPayload: %v", err)
	}

	var g models.Group
	if err := db.Where("name = ?", "group-ts").First(&g).Error; err != nil {
		t.Fatalf("find group: %v", err)
	}
	if g.DisplayName != "远端" {
		t.Fatalf("group LWW should have applied, got %s", g.DisplayName)
	}
	// 关键断言: updated_at 必须保留远端编辑时刻, 不能被抬到 now。
	if drift := g.UpdatedAt.Sub(remoteEdit); drift > time.Minute || drift < -time.Minute {
		t.Fatalf("group updated_at 被污染: 期望≈%v(远端编辑时刻), 实得 %v (drift=%v) —— GORM 把它改写成了 now",
			remoteEdit, g.UpdatedAt, drift)
	}

	var k models.APIKey
	if err := db.First(&k, uint(1)).Error; err != nil {
		t.Fatalf("find key: %v", err)
	}
	if k.Status != "disabled" {
		t.Fatalf("key LWW should have applied, got status %s", k.Status)
	}
	if drift := k.UpdatedAt.Sub(remoteEdit); drift > time.Minute || drift < -time.Minute {
		t.Fatalf("key updated_at 被污染: 期望≈%v, 实得 %v (drift=%v)", remoteEdit, k.UpdatedAt, drift)
	}
}

// TestSyncService_ProcessPayload_NoReexportChurnAfterMerge 证明合并不制造增量导出
// churn: 合并一条 updated_at=T 的胜出记录后, 以 since=T 增量导出不应再命中它
// (updated_at 必须仍是 T, 而非被抬到 now)。这是"两端来回推同一条永不收敛"的根因锁。
func TestSyncService_ProcessPayload_NoReexportChurnAfterMerge(t *testing.T) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "test-node-1"}
	svc := NewSyncService(db, cfg, NewNodeKeypairService(), nil)
	ctx := context.Background()

	oneHourAgo := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	remoteEdit := time.Now().Add(-30 * time.Minute).Truncate(time.Second)

	if err := db.Create(&models.Group{
		ID: 10, Name: "group-churn", DisplayName: "本地", ChannelType: "openai",
		TestModel: "gpt-4o", Upstreams: []byte("[]"),
		CreatedAt: oneHourAgo, UpdatedAt: oneHourAgo,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	incoming := &SyncPayload{
		SourcePeerID: "remote", Timestamp: time.Now(),
		Groups: []models.Group{{
			ID: 10, Name: "group-churn", DisplayName: "远端", ChannelType: "openai",
			TestModel: "gpt-4o", Upstreams: []byte("[]"),
			CreatedAt: oneHourAgo, UpdatedAt: remoteEdit,
		}},
	}
	if err := svc.ProcessPayload(ctx, incoming); err != nil {
		t.Fatalf("ProcessPayload: %v", err)
	}

	// 以远端编辑时刻为 since 增量导出: 该记录 updated_at 应恰为 remoteEdit(非 >),
	// 因此不应被重新导出。若被 GORM 抬成 now, 它会重新命中 → churn。
	since := remoteEdit
	out, err := svc.ExportPayload(ctx, &since)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, gr := range out.Groups {
		if gr.Name == "group-churn" {
			t.Fatalf("合并后的记录被重新增量导出 → 说明 updated_at 被抬到 now, 造成 churn")
		}
	}
}

// TestSyncService_ProcessPayload_UpdateVsDelete_EffectiveTime 锁定审计点 1/2:
// 更新与删除跨端竞争时, 按 effectiveTime=max(updated_at, deleted_at) 谁新谁赢。
// GORM 软删只写 deleted_at 不动 updated_at, 所以只看 updated_at 会漏删除信号。
func TestSyncService_ProcessPayload_UpdateVsDelete_EffectiveTime(t *testing.T) {
	newSvc := func(t *testing.T) (*SyncService, *gorm.DB) {
		db := newSyncTestDB(t)
		cfg := &mockConfigManager{masterKey: "test-node-1"}
		return NewSyncService(db, cfg, NewNodeKeypairService(), nil), db
	}
	oneHourAgo := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	tEarly := time.Now().Add(-40 * time.Minute).Truncate(time.Second)
	tLate := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	ctx := context.Background()

	// 场景 1: 本地更新(updated_at=tLate)晚于远端删除(deleted_at=tEarly) → 保持 active。
	t.Run("local update newer than remote delete stays active", func(t *testing.T) {
		svc, db := newSvc(t)
		if err := db.Create(&models.APIKey{
			ID: 1, GroupID: 5, KeyValue: "sk-1", KeyHash: "h1", Status: "active",
			CreatedAt: oneHourAgo, UpdatedAt: tLate, // 本地刚更新过
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		// 远端: 同一 key 在更早时刻被删除(纯软删, updated_at 停在 oneHourAgo)
		incoming := &SyncPayload{
			SourcePeerID: "remote", Timestamp: time.Now(),
			APIKeys: []models.APIKey{{
				ID: 1, GroupID: 5, KeyValue: "sk-1", KeyHash: "h1", Status: "active",
				CreatedAt: oneHourAgo, UpdatedAt: oneHourAgo,
				DeletedAt: gorm.DeletedAt{Time: tEarly, Valid: true},
			}},
		}
		if err := svc.ProcessPayload(ctx, incoming); err != nil {
			t.Fatalf("ProcessPayload: %v", err)
		}
		var k models.APIKey
		if err := db.First(&k, uint(1)).Error; err != nil {
			t.Fatalf("本地更新更新, key 应保持 active 未删, 却查不到(被删): %v", err)
		}
	})

	// 场景 2: 远端删除(deleted_at=tLate)晚于本地更新(updated_at=tEarly) → 软删传播。
	t.Run("remote delete newer than local update propagates", func(t *testing.T) {
		svc, db := newSvc(t)
		if err := db.Create(&models.APIKey{
			ID: 1, GroupID: 5, KeyValue: "sk-1", KeyHash: "h1", Status: "active",
			CreatedAt: oneHourAgo, UpdatedAt: tEarly,
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		incoming := &SyncPayload{
			SourcePeerID: "remote", Timestamp: time.Now(),
			APIKeys: []models.APIKey{{
				ID: 1, GroupID: 5, KeyValue: "sk-1", KeyHash: "h1", Status: "active",
				CreatedAt: oneHourAgo, UpdatedAt: oneHourAgo,
				DeletedAt: gorm.DeletedAt{Time: tLate, Valid: true}, // 更晚的删除
			}},
		}
		if err := svc.ProcessPayload(ctx, incoming); err != nil {
			t.Fatalf("ProcessPayload: %v", err)
		}
		var k models.APIKey
		if err := db.First(&k, uint(1)).Error; err == nil {
			t.Fatalf("远端删除更新, key 应被软删, 却仍能常规查到")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("expected ErrRecordNotFound, got %v", err)
		}
		var ku models.APIKey
		if err := db.Unscoped().First(&ku, uint(1)).Error; err != nil {
			t.Fatalf("unscoped find: %v", err)
		}
		if !ku.DeletedAt.Valid {
			t.Fatalf("expected soft-deleted tombstone")
		}
	})
}

// 删除 key 后, 其墓碑必须能被增量导出(deleted_at > since), 否则对端收不到
// 删除、会把 active 记录同步补回 —— 这是"删除后又被同步回来"的根因修复。
func TestSyncService_ExportPayload_IncludesTombstone(t *testing.T) {
	db := newSyncTestDB(t)
	cfg := &mockConfigManager{masterKey: "test-node-1"}
	svc := NewSyncService(db, cfg, NewNodeKeypairService(), nil)
	ctx := context.Background()

	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	oneHourAgo := time.Now().Add(-1 * time.Hour)

	// key 很久前创建/更新, updated_at = 2h 前
	key := models.APIKey{
		ID: 1, GroupID: 1, KeyValue: "sk-x", Status: "active",
		CreatedAt: twoHoursAgo, UpdatedAt: twoHoursAgo,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 删除前增量导出(since=1h前): active key 的 updated_at=2h前 < since, 不应出现
	before, err := svc.ExportPayload(ctx, &oneHourAgo)
	if err != nil {
		t.Fatalf("export before: %v", err)
	}
	for _, k := range before.APIKeys {
		if k.ID == 1 {
			t.Fatalf("未变化的 active key 不应出现在增量导出")
		}
	}

	// GORM soft delete: 只写 deleted_at, updated_at 仍是 2h 前
	if err := db.Delete(&models.APIKey{}, uint(1)).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}

	// 删除后增量导出: updated_at=2h前 < since, 但 deleted_at=now > since → 墓碑必须导出
	after, err := svc.ExportPayload(ctx, &oneHourAgo)
	if err != nil {
		t.Fatalf("export after: %v", err)
	}
	found := false
	for _, k := range after.APIKeys {
		if k.ID == 1 && k.DeletedAt.Valid {
			found = true
		}
	}
	if !found {
		t.Errorf("删除墓碑应被增量导出(deleted_at > since), 但没找到; 导出 %d 条 key", len(after.APIKeys))
	}
}
