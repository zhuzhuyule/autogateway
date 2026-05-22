package backup

import (
	"context"
	"testing"

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
	tok, _, err := dstSvc.Preview(context.Background(), blob, "pw")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	rep, err := dstSvc.Import(context.Background(), blob, "pw", StrategyMerge, tok)
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
	if _, _, err := svc.Preview(context.Background(), blob, "wrong"); err == nil {
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
