package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"autogateway/internal/models"

	"gorm.io/datatypes"
)

func TestService_E2E_ExportThenImportToFreshDB(t *testing.T) {
	srcDB := newTestDB(t)
	g := models.Group{Name: "openai-main", ChannelType: "openai", TestModel: "gpt-4o-mini", Upstreams: datatypes.JSON("[]")}
	srcDB.Create(&g)
	srcDB.Create(&models.APIKey{GroupID: g.ID, KeyValue: "enc:sk-secret", KeyHash: "h1", Status: "active"})
	srcDB.Create(&models.ModelAlias{Alias: "fast", GroupID: g.ID, RealModel: "gpt-4o-mini", Weight: 1, Priority: 100, Enabled: true})

	srcSvc := NewService(srcDB, fakeEncSvc{}, "test")
	blob, _, err := srcSvc.Export(context.Background(), "pw")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	dstDB := newTestDB(t)
	dstSvc := NewService(dstDB, fakeEncSvc{}, "test")
	preview, err := dstSvc.Preview(context.Background(), blob, "pw")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	rep, err := dstSvc.Import(context.Background(), blob, "pw", StrategyMerge, preview.ConfirmToken)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if rep.Applied.Groups != 1 || rep.Applied.APIKeys != 1 || rep.Applied.ModelAliases != 1 {
		t.Errorf("applied: %+v", rep.Applied)
	}
	var keys []models.APIKey
	dstDB.Find(&keys)
	if len(keys) != 1 || keys[0].KeyValue != "enc:sk-secret" {
		t.Errorf("re-encrypted key mismatch: %+v", keys)
	}
}

func TestService_Preview_WrongPassword(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db, fakeEncSvc{}, "t")
	blob, _, err := svc.Export(context.Background(), "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Preview(context.Background(), blob, "wrong"); err == nil {
		t.Fatal("expected wrong-password error")
	}
}

func TestService_Import_StaleToken(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db, fakeEncSvc{}, "t")
	blob, _, _ := svc.Export(context.Background(), "p")
	if _, err := svc.Import(context.Background(), blob, "p", StrategyMerge, "bogus-token"); err == nil {
		t.Fatal("expected stale-token error")
	}
}

// TestService_Import_ExpiredToken simulates a token whose TTL has lapsed.
func TestService_Import_ExpiredToken(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db, fakeEncSvc{}, "t")
	blob, _, err := svc.Export(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Preview(context.Background(), blob, "p")
	if err != nil {
		t.Fatal(err)
	}
	// Manually expire the token by rewinding its expires timestamp.
	svc.tokensMu.Lock()
	entry := svc.tokens[rep.ConfirmToken]
	entry.expires = time.Now().Add(-time.Second)
	svc.tokens[rep.ConfirmToken] = entry
	svc.tokensMu.Unlock()

	if _, err := svc.Import(context.Background(), blob, "p", StrategyMerge, rep.ConfirmToken); err == nil {
		t.Fatal("expected expired-token error")
	}
}

// TestService_Import_BlobMismatchToken proves the blob fingerprint binding.
func TestService_Import_BlobMismatchToken(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db, fakeEncSvc{}, "t")
	blobA, _, err := svc.Export(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	// Mint a token bound to blobA.
	rep, err := svc.Preview(context.Background(), blobA, "p")
	if err != nil {
		t.Fatal(err)
	}
	// Produce a different blob (different export run = different salt/nonce).
	blobB, _, err := svc.Export(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Import(context.Background(), blobB, "p", StrategyMerge, rep.ConfirmToken); err == nil {
		t.Fatal("expected blob-mismatch token rejection")
	}
}

// TestService_Import_TokenSingleUse ensures a token cannot be reused after a
// successful import.
func TestService_Import_TokenSingleUse(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db, fakeEncSvc{}, "t")
	blob, _, err := svc.Export(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Preview(context.Background(), blob, "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Import(context.Background(), blob, "p", StrategyMerge, rep.ConfirmToken); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Import(context.Background(), blob, "p", StrategyMerge, rep.ConfirmToken); err == nil {
		t.Fatal("expected reused-token rejection")
	}
}

// TestService_Import_RunsInvalidators asserts that registered post-import
// hooks fire on success but not on early-rejection paths.
func TestService_Import_RunsInvalidators(t *testing.T) {
	db := newTestDB(t)
	called := 0
	svc := NewService(db, fakeEncSvc{}, "t").
		WithInvalidator(func() error { called++; return nil })

	// Success path: export → preview → import → hook fires once.
	blob, _, err := svc.Export(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Preview(context.Background(), blob, "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Import(context.Background(), blob, "p", StrategyMerge, rep.ConfirmToken); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Errorf("invalidator should fire exactly once on success, called=%d", called)
	}

	// Stale-token rejection: hook must NOT fire.
	called = 0
	if _, err := svc.Import(context.Background(), blob, "p", StrategyMerge, "bogus"); err == nil {
		t.Fatal("expected stale-token error")
	}
	if called != 0 {
		t.Errorf("invalidator must NOT fire on stale-token path, called=%d", called)
	}
}

// TestService_Import_InvalidatorErrorBecomesWarning ensures hook failures
// don't fail the import (the DB transaction is already committed).
func TestService_Import_InvalidatorErrorBecomesWarning(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db, fakeEncSvc{}, "t").
		WithInvalidator(func() error { return errors.New("simulated cache fail") })

	blob, _, _ := svc.Export(context.Background(), "p")
	rep, _ := svc.Preview(context.Background(), blob, "p")
	importRep, err := svc.Import(context.Background(), blob, "p", StrategyMerge, rep.ConfirmToken)
	if err != nil {
		t.Fatalf("hook error must not fail import: %v", err)
	}
	found := false
	for _, w := range importRep.Warnings {
		if strings.Contains(w, "simulated cache fail") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected hook error in Warnings, got %+v", importRep.Warnings)
	}
}
