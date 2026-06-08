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
)

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
