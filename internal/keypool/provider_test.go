package keypool

import (
	"errors"
	"fmt"
	"testing"

	"autogateway/internal/encryption"
	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"
	"autogateway/internal/ratelimit"
	"autogateway/internal/store"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// newTestDB opens an in-memory SQLite DB and auto-migrates APIKey for tests
// that need real DB transactions (e.g. SetKeyEnabled).
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.APIKey{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// newTestProviderWithDB constructs a KeyProvider with store + encryption + db.
func newTestProviderWithDB(s store.Store, enc encryption.Service, db *gorm.DB) *KeyProvider {
	return &KeyProvider{
		store:         s,
		encryptionSvc: enc,
		db:            db,
	}
}

// noopEncryption wraps encryption.NewService("") for tests.
func newNoopEncryption(t *testing.T) encryption.Service {
	t.Helper()
	svc, err := encryption.NewService("")
	if err != nil {
		t.Fatalf("encryption.NewService: %v", err)
	}
	return svc
}

// seedKey writes key hash + active_keys entry into the store.
func seedKey(t *testing.T, s store.Store, groupID uint, keyID uint, keyValue string) {
	t.Helper()
	keyHashKey := fmt.Sprintf("key:%d", keyID)
	if err := s.HSet(keyHashKey, map[string]any{
		"id":            fmt.Sprint(keyID),
		"key_string":    keyValue, // no encryption in tests
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"group_id":      fmt.Sprint(groupID),
		"created_at":    "0",
	}); err != nil {
		t.Fatalf("seed HSet key %d: %v", keyID, err)
	}
	activeListKey := fmt.Sprintf("group:%d:active_keys", groupID)
	if err := s.LPush(activeListKey, keyID); err != nil {
		t.Fatalf("seed LPush key %d: %v", keyID, err)
	}
}

// newTestProvider constructs a KeyProvider with only store/encryption/ledger set.
// db and settingsManager are nil — SelectKey only touches store + ledger.
func newTestProvider(s store.Store, enc encryption.Service, ledger *ratelimit.Ledger) *KeyProvider {
	return &KeyProvider{
		store:         s,
		encryptionSvc: enc,
		ledger:        ledger,
	}
}

// TestSelectKey_NoLimits verifies baseline behavior: nil ledger, Limits{} → returns key normally.
func TestSelectKey_NoLimits(t *testing.T) {
	s := store.NewMemoryStore()
	enc := newNoopEncryption(t)
	const groupID uint = 1
	seedKey(t, s, groupID, 10, "sk-test-1")

	p := newTestProvider(s, enc, nil)
	apiKey, err := p.SelectKey(groupID, ratelimit.Limits{})
	if err != nil {
		t.Fatalf("SelectKey with nil ledger: %v", err)
	}
	if apiKey.ID != 10 {
		t.Errorf("expected key ID 10, got %d", apiKey.ID)
	}
}

// TestSelectKey_ZeroLimitsWithLedger verifies that Limits{} skips all rate checks
// even when ledger is provided.
func TestSelectKey_ZeroLimitsWithLedger(t *testing.T) {
	s := store.NewMemoryStore()
	enc := newNoopEncryption(t)
	ledger := ratelimit.NewLedger(s)
	const groupID uint = 2
	seedKey(t, s, groupID, 20, "sk-test-2")

	p := newTestProvider(s, enc, ledger)
	apiKey, err := p.SelectKey(groupID, ratelimit.Limits{})
	if err != nil {
		t.Fatalf("SelectKey with Limits{}: %v", err)
	}
	if apiKey.ID != 20 {
		t.Errorf("expected key ID 20, got %d", apiKey.ID)
	}
}

// TestSelectKey_RPMLimitSkipsExhaustedKey verifies that a key exhausted for RPM
// is skipped and the second key is returned.
func TestSelectKey_RPMLimitSkipsExhaustedKey(t *testing.T) {
	s := store.NewMemoryStore()
	enc := newNoopEncryption(t)
	ledger := ratelimit.NewLedger(s)
	const groupID uint = 3
	// Seed two keys. LPush puts 31 at front, then 30 → Rotate gives 31 first.
	seedKey(t, s, groupID, 30, "sk-key-30")
	seedKey(t, s, groupID, 31, "sk-key-31")

	lim := ratelimit.Limits{RPM: 1}
	// Pre-fill key 31's RPM counter to saturate it.
	if err := ledger.Record(groupID, 31, lim); err != nil {
		t.Fatalf("pre-fill Record: %v", err)
	}

	p := newTestProvider(s, enc, ledger)
	// key 31 is exhausted → should be skipped; key 30 should be returned.
	apiKey, err := p.SelectKey(groupID, lim)
	if err != nil {
		t.Fatalf("SelectKey: %v", err)
	}
	if apiKey.ID != 30 {
		t.Errorf("expected key 30 (non-exhausted), got key %d", apiKey.ID)
	}
}

// TestSelectKey_AllKeysExhaustedReturnsNoActiveKeys verifies that when all keys
// in the group are rate-limited, ErrNoActiveKeys is returned.
func TestSelectKey_AllKeysExhaustedReturnsNoActiveKeys(t *testing.T) {
	s := store.NewMemoryStore()
	enc := newNoopEncryption(t)
	ledger := ratelimit.NewLedger(s)
	const groupID uint = 4

	// Seed keys 40 and 41.
	seedKey(t, s, groupID, 40, "sk-key-40")
	seedKey(t, s, groupID, 41, "sk-key-41")

	lim := ratelimit.Limits{RPM: 1}
	// Exhaust both.
	if err := ledger.Record(groupID, 40, lim); err != nil {
		t.Fatalf("pre-fill 40: %v", err)
	}
	if err := ledger.Record(groupID, 41, lim); err != nil {
		t.Fatalf("pre-fill 41: %v", err)
	}

	p := newTestProvider(s, enc, ledger)
	_, err := p.SelectKey(groupID, lim)
	if err == nil {
		t.Fatal("expected error when all keys exhausted, got nil")
	}
	if !errors.Is(err, app_errors.ErrNoActiveKeys) {
		t.Errorf("expected ErrNoActiveKeys, got: %v", err)
	}
}

// TestSetKeyEnabled verifies the manual disable/enable lifecycle for a single key.
func TestSetKeyEnabled(t *testing.T) {
	db := newTestDB(t)
	s := store.NewMemoryStore()
	enc := newNoopEncryption(t)
	const groupID uint = 5
	const keyID uint = 100

	// Insert the key into DB with non-zero failure_count to verify it is
	// properly reset to 0 when the key is re-enabled.
	key := models.APIKey{
		ID:           keyID,
		KeyValue:     "sk-test-100",
		KeyHash:      "hash100",
		GroupID:      groupID,
		Status:       models.KeyStatusActive,
		FailureCount: 5,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create key in DB: %v", err)
	}

	// Seed the key into the store (active) with matching non-zero failure_count.
	keyHashKey := fmt.Sprintf("key:%d", keyID)
	seedKey(t, s, groupID, keyID, "sk-test-100")
	if err := s.HSet(keyHashKey, map[string]any{"failure_count": "5"}); err != nil {
		t.Fatalf("seed failure_count in store: %v", err)
	}

	p := newTestProviderWithDB(s, enc, db)

	// --- Disable the key ---
	if err := p.SetKeyEnabled(keyID, false); err != nil {
		t.Fatalf("SetKeyEnabled(false): %v", err)
	}

	// DB status should be disabled.
	var dbKey models.APIKey
	if err := db.First(&dbKey, keyID).Error; err != nil {
		t.Fatalf("fetch key from DB after disable: %v", err)
	}
	if dbKey.Status != models.KeyStatusDisabled {
		t.Errorf("DB status after disable: want %q, got %q", models.KeyStatusDisabled, dbKey.Status)
	}

	// Store hash status should be disabled.
	hash, err := s.HGetAll(keyHashKey)
	if err != nil {
		t.Fatalf("HGetAll after disable: %v", err)
	}
	if hash["status"] != models.KeyStatusDisabled {
		t.Errorf("store hash status after disable: want %q, got %q", models.KeyStatusDisabled, hash["status"])
	}

	// SelectKey should not return the disabled key (ErrNoActiveKeys expected).
	_, selErr := p.SelectKey(groupID, ratelimit.Limits{})
	if !errors.Is(selErr, app_errors.ErrNoActiveKeys) {
		t.Errorf("after disable: expected ErrNoActiveKeys, got %v", selErr)
	}

	// --- Enable the key ---
	if err := p.SetKeyEnabled(keyID, true); err != nil {
		t.Fatalf("SetKeyEnabled(true): %v", err)
	}

	// DB status should be active, failure_count = 0.
	if err := db.First(&dbKey, keyID).Error; err != nil {
		t.Fatalf("fetch key from DB after enable: %v", err)
	}
	if dbKey.Status != models.KeyStatusActive {
		t.Errorf("DB status after enable: want %q, got %q", models.KeyStatusActive, dbKey.Status)
	}
	if dbKey.FailureCount != 0 {
		t.Errorf("DB failure_count after enable: want 0, got %d", dbKey.FailureCount)
	}

	// Store hash: status should be active, failure_count should be reset to 0.
	hash, err = s.HGetAll(keyHashKey)
	if err != nil {
		t.Fatalf("HGetAll after enable: %v", err)
	}
	if hash["status"] != models.KeyStatusActive {
		t.Errorf("store hash status after enable: want %q, got %q", models.KeyStatusActive, hash["status"])
	}
	if hash["failure_count"] != "0" {
		t.Errorf("store hash failure_count after enable: want %q, got %q", "0", hash["failure_count"])
	}

	// SelectKey should return the re-enabled key.
	got, err := p.SelectKey(groupID, ratelimit.Limits{})
	if err != nil {
		t.Fatalf("SelectKey after enable: %v", err)
	}
	if got.ID != keyID {
		t.Errorf("SelectKey after enable: want key %d, got %d", keyID, got.ID)
	}
}

// TestSyncGroupKeysFromDB_Disabled verifies that SyncGroupKeysFromDB correctly
// handles a key whose DB status is disabled but whose store still reflects active
// state (e.g. the store is stale before a mesh sync arrives).
func TestSyncGroupKeysFromDB_Disabled(t *testing.T) {
	db := newTestDB(t)
	s := store.NewMemoryStore()
	enc := newNoopEncryption(t)
	const groupID uint = 6
	const keyID uint = 200

	// DB: key is disabled.
	key := models.APIKey{
		ID:           keyID,
		KeyValue:     "sk-test-200",
		KeyHash:      "hash200",
		GroupID:      groupID,
		Status:       models.KeyStatusDisabled,
		FailureCount: 0,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create key in DB: %v", err)
	}

	// Store: key is still active (stale — mesh sync hasn't landed yet).
	keyHashKey := fmt.Sprintf("key:%d", keyID)
	if err := s.HSet(keyHashKey, map[string]any{
		"id":            fmt.Sprint(keyID),
		"key_string":    "sk-test-200",
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"group_id":      fmt.Sprint(groupID),
		"created_at":    "0",
	}); err != nil {
		t.Fatalf("seed store hash: %v", err)
	}
	activeListKey := fmt.Sprintf("group:%d:active_keys", groupID)
	if err := s.LPush(activeListKey, keyID); err != nil {
		t.Fatalf("seed active_keys: %v", err)
	}

	p := newTestProviderWithDB(s, enc, db)

	if err := p.SyncGroupKeysFromDB(groupID); err != nil {
		t.Fatalf("SyncGroupKeysFromDB: %v", err)
	}

	// Store hash status must be disabled.
	hash, err := s.HGetAll(keyHashKey)
	if err != nil {
		t.Fatalf("HGetAll after sync: %v", err)
	}
	if hash["status"] != models.KeyStatusDisabled {
		t.Errorf("store hash status after sync: want %q, got %q", models.KeyStatusDisabled, hash["status"])
	}

	// The key must NOT be in active_keys (LRem removed it).
	activeLen, err := s.LLen(activeListKey)
	if err != nil {
		t.Fatalf("LLen active_keys after sync: %v", err)
	}
	if activeLen != 0 {
		t.Errorf("active_keys length after sync: want 0 (disabled key removed), got %d", activeLen)
	}

	// SelectKey must not return the disabled key.
	_, selErr := p.SelectKey(groupID, ratelimit.Limits{})
	if !errors.Is(selErr, app_errors.ErrNoActiveKeys) {
		t.Errorf("after sync: expected ErrNoActiveKeys, got %v", selErr)
	}
}

// ---------------------------------------------------------------------------
// 回归: 残缺 hash 不能进轮转 (空凭据打上游)
//
// 线上现象: Slave 节点不跑 LoadKeysFromDB, store 是空的; 手动 validate-group
// 校验通过后 handleSuccess 只写了 {status, failure_count} 两个字段, 于是
// active_keys 里出现了一把**没有 key_string** 的 key。SelectKey 照常返回它,
// 请求带着 "Authorization: Bearer " (空凭据) 打到上游 —— Gitee 对空 bearer
// 返回的是一个与真实原因无关的 400 Bad Request, 排查成本极高。
// ---------------------------------------------------------------------------

// TestHandleSuccess_MaterializesFullHash 保证 handleSuccess 在 store 里没有
// hash 时会写出**完整**的 hash(含 key_string), 而不是残缺的两字段版本。
func TestHandleSuccess_MaterializesFullHash(t *testing.T) {
	const groupID, keyID = uint(1), uint(101)
	const keyValue = "sk-regression-full-hash"

	db := newTestDB(t)
	if err := db.Create(&models.APIKey{
		ID:       keyID,
		KeyValue: keyValue,
		GroupID:  groupID,
		Status:   models.KeyStatusActive,
	}).Error; err != nil {
		t.Fatalf("create key row: %v", err)
	}

	s := store.NewMemoryStore()
	p := newTestProviderWithDB(s, newNoopEncryption(t), db)

	keyHashKey := fmt.Sprintf("key:%d", keyID)
	activeListKey := fmt.Sprintf("group:%d:active_keys", groupID)

	// store 里完全没有这把 key 的痕迹 —— 模拟 Slave 冷启动 + 首次校验通过。
	if err := p.handleSuccess(keyID, keyHashKey, activeListKey); err != nil {
		t.Fatalf("handleSuccess: %v", err)
	}

	hash, err := s.HGetAll(keyHashKey)
	if err != nil {
		t.Fatalf("HGetAll: %v", err)
	}
	if hash["key_string"] != keyValue {
		t.Fatalf("store hash key_string = %q, want %q (残缺 hash 会让请求带空凭据打上游)",
			hash["key_string"], keyValue)
	}
	if hash["status"] != models.KeyStatusActive {
		t.Errorf("store hash status = %q, want %q", hash["status"], models.KeyStatusActive)
	}

	// 端到端: 选出来的 key 必须带着真凭据。
	sel, err := p.SelectKey(groupID, ratelimit.Limits{})
	if err != nil {
		t.Fatalf("SelectKey: %v", err)
	}
	if sel.KeyValue != keyValue {
		t.Errorf("SelectKey KeyValue = %q, want %q", sel.KeyValue, keyValue)
	}
}

// TestSelectKey_EvictsHashWithoutCredential 保证缺 key_string 的 hash 会被
// 逐出轮转并报 ErrNoActiveKeys —— 宁可失败也不发一个空凭据的上游请求。
func TestSelectKey_EvictsHashWithoutCredential(t *testing.T) {
	const groupID, keyID = uint(2), uint(202)

	s := store.NewMemoryStore()
	p := newTestProvider(s, newNoopEncryption(t), nil)

	keyHashKey := fmt.Sprintf("key:%d", keyID)
	activeListKey := fmt.Sprintf("group:%d:active_keys", groupID)

	// 手工造出线上那个残缺 hash: 只有 status + failure_count, 没有 key_string。
	if err := s.HSet(keyHashKey, map[string]any{
		"status":        models.KeyStatusActive,
		"failure_count": "0",
	}); err != nil {
		t.Fatalf("HSet partial hash: %v", err)
	}
	if err := s.LPush(activeListKey, keyID); err != nil {
		t.Fatalf("LPush active list: %v", err)
	}

	_, err := p.SelectKey(groupID, ratelimit.Limits{})
	if !errors.Is(err, app_errors.ErrNoActiveKeys) {
		t.Fatalf("SelectKey with credential-less hash: got %v, want ErrNoActiveKeys "+
			"(绝不能返回一把空 key 让请求打到上游)", err)
	}

	// 必须从 active_keys 摘出去, 否则每次轮转都白转一圈。
	n, err := s.LLen(activeListKey)
	if err != nil {
		t.Fatalf("LLen: %v", err)
	}
	if n != 0 {
		t.Errorf("active_keys length = %d, want 0 (残缺 key 应被逐出)", n)
	}
}

// TestSyncGroupKeysFromDB_RepairsMissingCredential 保证 mesh sync 路径遇到
// "hash 在但没凭据" 时会用 DB 真值整体重建, 而不是只修 status 就放行。
func TestSyncGroupKeysFromDB_RepairsMissingCredential(t *testing.T) {
	const groupID, keyID = uint(3), uint(303)
	const keyValue = "sk-regression-sync-repair"

	db := newTestDB(t)
	if err := db.Create(&models.APIKey{
		ID:       keyID,
		KeyValue: keyValue,
		GroupID:  groupID,
		Status:   models.KeyStatusActive,
	}).Error; err != nil {
		t.Fatalf("create key row: %v", err)
	}

	s := store.NewMemoryStore()
	p := newTestProviderWithDB(s, newNoopEncryption(t), db)

	keyHashKey := fmt.Sprintf("key:%d", keyID)
	activeListKey := fmt.Sprintf("group:%d:active_keys", groupID)

	// 残缺 hash + 已在 active list (模拟历史脏数据)。
	if err := s.HSet(keyHashKey, map[string]any{
		"status":        models.KeyStatusActive,
		"failure_count": "0",
	}); err != nil {
		t.Fatalf("HSet partial hash: %v", err)
	}
	if err := s.LPush(activeListKey, keyID); err != nil {
		t.Fatalf("LPush: %v", err)
	}

	if err := p.SyncGroupKeysFromDB(groupID); err != nil {
		t.Fatalf("SyncGroupKeysFromDB: %v", err)
	}

	hash, err := s.HGetAll(keyHashKey)
	if err != nil {
		t.Fatalf("HGetAll: %v", err)
	}
	if hash["key_string"] != keyValue {
		t.Errorf("after sync, key_string = %q, want %q", hash["key_string"], keyValue)
	}
}

// TestHydrateStoreFromDB_SeedsMissingKeys 覆盖 Slave 启动时的补齐:
// store 是空的, DB 里有 key → 补齐后必须能选出来。
func TestHydrateStoreFromDB_SeedsMissingKeys(t *testing.T) {
	const groupID = uint(41)

	db := newTestDB(t)
	if err := db.AutoMigrate(&models.Group{}); err != nil {
		t.Fatalf("automigrate groups: %v", err)
	}
	if err := db.Create(&models.Group{
		ID: groupID, Name: "hydrate-a",
		Upstreams: datatypes.JSON([]byte("[]")), ChannelType: "openai",
	}).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Create(&models.APIKey{
		ID: 411, KeyValue: "sk-hydrate-411", GroupID: groupID, Status: models.KeyStatusActive,
	}).Error; err != nil {
		t.Fatalf("create key: %v", err)
	}

	s := store.NewMemoryStore()
	p := newTestProviderWithDB(s, newNoopEncryption(t), db)

	if err := p.HydrateStoreFromDB(); err != nil {
		t.Fatalf("HydrateStoreFromDB: %v", err)
	}

	hash, err := s.HGetAll("key:411")
	if err != nil {
		t.Fatalf("HGetAll: %v", err)
	}
	if hash["key_string"] != "sk-hydrate-411" {
		t.Fatalf("hydrated hash key_string = %q, want sk-hydrate-411", hash["key_string"])
	}

	sel, err := p.SelectKey(groupID, ratelimit.Limits{})
	if err != nil {
		t.Fatalf("SelectKey after hydrate: %v", err)
	}
	if sel.KeyValue != "sk-hydrate-411" {
		t.Errorf("SelectKey KeyValue = %q, want sk-hydrate-411", sel.KeyValue)
	}
}

// TestHydrateStoreFromDB_DoesNotClobberRuntimeState 保证补齐是**非破坏性**的:
// 已存在的 hash 不被 DB 值覆盖, 运行期累计的 failure_count 必须保住。
// 这是它跟 LoadKeysFromDB 的关键区别 —— 否则一个 Slave 重启就会把 Master
// 维护的运行时状态冲掉。
func TestHydrateStoreFromDB_DoesNotClobberRuntimeState(t *testing.T) {
	const groupID, keyID = uint(42), uint(421)

	db := newTestDB(t)
	if err := db.AutoMigrate(&models.Group{}); err != nil {
		t.Fatalf("automigrate groups: %v", err)
	}
	if err := db.Create(&models.Group{
		ID: groupID, Name: "hydrate-b",
		Upstreams: datatypes.JSON([]byte("[]")), ChannelType: "openai",
	}).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	// DB 里 failure_count = 0 (还没落库), 但运行期 store 里已经累计到 7。
	if err := db.Create(&models.APIKey{
		ID: keyID, KeyValue: "sk-hydrate-421", GroupID: groupID, Status: models.KeyStatusActive,
	}).Error; err != nil {
		t.Fatalf("create key: %v", err)
	}

	s := store.NewMemoryStore()
	p := newTestProviderWithDB(s, newNoopEncryption(t), db)

	keyHashKey := fmt.Sprintf("key:%d", keyID)
	if err := s.HSet(keyHashKey, map[string]any{
		"id":            fmt.Sprint(keyID),
		"key_string":    "sk-hydrate-421",
		"status":        models.KeyStatusActive,
		"failure_count": "7",
		"group_id":      fmt.Sprint(groupID),
		"created_at":    "0",
	}); err != nil {
		t.Fatalf("HSet existing hash: %v", err)
	}
	if err := s.LPush(fmt.Sprintf("group:%d:active_keys", groupID), keyID); err != nil {
		t.Fatalf("LPush: %v", err)
	}

	if err := p.HydrateStoreFromDB(); err != nil {
		t.Fatalf("HydrateStoreFromDB: %v", err)
	}

	hash, err := s.HGetAll(keyHashKey)
	if err != nil {
		t.Fatalf("HGetAll: %v", err)
	}
	if hash["failure_count"] != "7" {
		t.Errorf("failure_count = %q, want \"7\" (补齐不能覆盖运行期状态)", hash["failure_count"])
	}
}
