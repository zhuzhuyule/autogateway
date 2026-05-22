package backup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"autogateway/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// newTestDB 启动 sqlite in-memory 并 AutoMigrate 所有备份相关表。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.GroupSubGroup{},
		&models.APIKey{},
		&models.ModelAlias{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// fakeEncSvc 模拟 encryption.Service：明文 ↔ "enc:"+明文。
type fakeEncSvc struct{}

func (fakeEncSvc) Encrypt(s string) (string, error) { return "enc:" + s, nil }
func (fakeEncSvc) Decrypt(s string) (string, error) {
	if len(s) < 4 || s[:4] != "enc:" {
		return "", &fakeDecryptErr{}
	}
	return s[4:], nil
}
func (fakeEncSvc) Hash(s string) string { return "hash:" + s }

type fakeDecryptErr struct{}

func (*fakeDecryptErr) Error() string { return "fake decrypt fail" }

func TestExport_HappyPath(t *testing.T) {
	db := newTestDB(t)
	// seed 1 standard group + 1 system aggregate + key + alias
	g := models.Group{Name: "openai-main", ChannelType: "openai", TestModel: "gpt-4o-mini", Upstreams: datatypes.JSON("[]")}
	if err := db.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	sysG := models.Group{Name: "default-openai", ChannelType: "openai", TestModel: "gpt-4o-mini", IsSystem: true, GroupType: "aggregate", Upstreams: datatypes.JSON("[]")}
	if err := db.Create(&sysG).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.APIKey{GroupID: g.ID, KeyValue: "enc:sk-real-key", KeyHash: "h1", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ModelAlias{Alias: "gpt-fast", GroupID: g.ID, RealModel: "gpt-4o-mini", Weight: 1, Priority: 100, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.GroupSubGroup{GroupID: sysG.ID, SubGroupID: g.ID, Weight: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.SystemSetting{SettingKey: "app_url", SettingValue: "https://example.com"}).Error; err != nil {
		t.Fatal(err)
	}

	exp := NewExporter(db, fakeEncSvc{}, "test-1.0")
	got, warns, err := exp.Export(context.Background())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("schema_version: got %d", got.SchemaVersion)
	}
	if len(got.Data.Groups) != 1 || got.Data.Groups[0].Name != "openai-main" {
		t.Errorf("expected 1 standard group, got %+v", got.Data.Groups)
	}
	if len(got.Data.APIKeys) != 1 || got.Data.APIKeys[0].KeyValue != "sk-real-key" {
		t.Errorf("expected decrypted plaintext key, got %+v", got.Data.APIKeys)
	}
	if len(got.Data.GroupSubGroups) != 1 || got.Data.GroupSubGroups[0].ParentName != "default-openai" {
		t.Errorf("group_sub_groups: got %+v", got.Data.GroupSubGroups)
	}
	if got.ExportedAt.IsZero() || time.Since(got.ExportedAt) > time.Minute {
		t.Errorf("exported_at not stamped")
	}
	_ = warns
}

func TestExport_DecryptFailureProducesWarning(t *testing.T) {
	db := newTestDB(t)
	g := models.Group{Name: "x", ChannelType: "openai", TestModel: "m", Upstreams: datatypes.JSON("[]")}
	db.Create(&g)
	// missing "enc:" prefix → decrypt fails
	db.Create(&models.APIKey{GroupID: g.ID, KeyValue: "raw-no-prefix", KeyHash: "h", Status: "active"})

	exp := NewExporter(db, fakeEncSvc{}, "v")
	got, warns, err := exp.Export(context.Background())
	if err != nil {
		t.Fatalf("Export must not fail on per-key decrypt error: %v", err)
	}
	if len(got.Data.APIKeys) != 0 {
		t.Errorf("undecryptable key must be skipped, got %+v", got.Data.APIKeys)
	}
	if len(warns) == 0 {
		t.Error("expected at least 1 warning")
	}
}

// TestGroupModelToDTO_JSONShape pins down 4 behaviors at once:
// raw shape preservation, omitempty on nullable JSON, IsSystem
// flattening, and empty Upstreams must NOT degrade to null.
func TestGroupModelToDTO_JSONShape(t *testing.T) {
	g := models.Group{
		Name:          "x",
		Upstreams:     datatypes.JSON(`[{"url":"https://a"}]`),
		ExposedModels: datatypes.JSON(`["m1","m2"]`),
		BlockedModels: datatypes.JSON(`[]`),
		// HeaderRules left nil — must be omitted from JSON
		IsSystem: true, // must be flattened to false
	}
	dto := groupModelToDTO(&g)
	out, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"upstreams":[{"url":"https://a"}]`) {
		t.Errorf("upstreams not raw-preserved: %s", s)
	}
	if !strings.Contains(s, `"exposed_models":["m1","m2"]`) {
		t.Errorf("exposed_models not raw-preserved: %s", s)
	}
	if strings.Contains(s, `"header_rules"`) {
		t.Errorf("nil header_rules should be omitted: %s", s)
	}
	if strings.Contains(s, `"is_system":true`) {
		t.Errorf("is_system must always export false: %s", s)
	}
}

// TestGroupModelToDTO_EmptyUpstreamsBecomesArray makes sure the
// NOT-NULL invariant on Upstreams holds even when the source row
// has nil bytes.
func TestGroupModelToDTO_EmptyUpstreamsBecomesArray(t *testing.T) {
	g := models.Group{Name: "x", Upstreams: nil}
	dto := groupModelToDTO(&g)
	out, _ := json.Marshal(dto)
	if !strings.Contains(string(out), `"upstreams":[]`) {
		t.Fatalf("nil Upstreams must marshal as []: %s", out)
	}
}

// TestExport_NoSystemGroupInOutput negatively asserts that no
// system-aggregate group ever appears in Data.Groups.
func TestExport_NoSystemGroupInOutput(t *testing.T) {
	db := newTestDB(t)
	db.Create(&models.Group{Name: "stdA", ChannelType: "openai", TestModel: "m", Upstreams: datatypes.JSON("[]")})
	db.Create(&models.Group{Name: "default-openai", ChannelType: "openai", TestModel: "m", IsSystem: true, GroupType: "aggregate", Upstreams: datatypes.JSON("[]")})
	db.Create(&models.Group{Name: "default-anthropic", ChannelType: "anthropic", TestModel: "m", IsSystem: true, GroupType: "aggregate", Upstreams: datatypes.JSON("[]")})

	exp := NewExporter(db, fakeEncSvc{}, "v")
	got, _, err := exp.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range got.Data.Groups {
		if g.IsSystem {
			t.Errorf("exported group %q has IsSystem=true", g.Name)
		}
		if g.Name == "default-openai" || g.Name == "default-anthropic" {
			t.Errorf("system group leaked into output: %s", g.Name)
		}
	}
	if len(got.Data.Groups) != 1 {
		t.Errorf("expected exactly 1 standard group, got %d", len(got.Data.Groups))
	}
}
