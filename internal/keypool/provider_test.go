package keypool

import (
	"errors"
	"fmt"
	"testing"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/encryption"
	"autogateway/internal/models"
	"autogateway/internal/ratelimit"
	"autogateway/internal/store"

	"github.com/glebarez/sqlite"
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
