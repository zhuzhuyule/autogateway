package backup

import (
	"context"
	"testing"

	"autogateway/internal/models"

	"gorm.io/datatypes"
)

func sampleBackup() *BackupV1 {
	return &BackupV1{
		SchemaVersion: 1,
		ExportedBy:    "test",
		Data: DataV1{
			SystemSettings: []SystemSettingDTO{
				{SettingKey: "app_url", SettingValue: "https://imported"},
			},
			Groups: []GroupDTO{
				{Name: "openai-main", ChannelType: "openai", TestModel: "gpt-4o-mini", GroupType: "standard"},
			},
			APIKeys: []APIKeyDTO{
				{GroupName: "openai-main", KeyValue: "sk-new", Status: "active"},
			},
			ModelAliases: []ModelAliasDTO{
				{Alias: "fast", GroupName: "openai-main", RealModel: "gpt-4o-mini", Weight: 1, Priority: 100, Enabled: true},
			},
		},
	}
}

func TestImport_MergeIntoEmptyDB(t *testing.T) {
	db := newTestDB(t)
	imp := NewImporter(db, fakeEncSvc{})
	rep, err := imp.Import(context.Background(), sampleBackup(), StrategyMerge)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if rep.Applied.Groups != 1 || rep.Applied.APIKeys != 1 {
		t.Errorf("applied counts: %+v", rep.Applied)
	}
	var got []models.APIKey
	db.Find(&got)
	if len(got) != 1 || got[0].KeyValue != "enc:sk-new" {
		t.Errorf("key should be re-encrypted, got %+v", got)
	}
	if got[0].KeyHash == "" {
		t.Errorf("key_hash should be filled")
	}
}

func TestImport_MergeUpdatesExistingGroup(t *testing.T) {
	db := newTestDB(t)
	db.Create(&models.Group{Name: "openai-main", ChannelType: "openai", TestModel: "old", DisplayName: "Old", Upstreams: datatypes.JSON("[]")})

	bk := sampleBackup()
	bk.Data.Groups[0].DisplayName = "New"
	bk.Data.Groups[0].TestModel = "gpt-4o-mini"

	imp := NewImporter(db, fakeEncSvc{})
	if _, err := imp.Import(context.Background(), bk, StrategyMerge); err != nil {
		t.Fatal(err)
	}
	var g models.Group
	db.Where("name = ?", "openai-main").First(&g)
	if g.DisplayName != "New" || g.TestModel != "gpt-4o-mini" {
		t.Errorf("merge should overwrite fields: %+v", g)
	}
}

func TestImport_SkipPreservesExisting(t *testing.T) {
	db := newTestDB(t)
	db.Create(&models.Group{Name: "openai-main", ChannelType: "openai", TestModel: "keep-me", DisplayName: "Keep", Upstreams: datatypes.JSON("[]")})

	imp := NewImporter(db, fakeEncSvc{})
	rep, err := imp.Import(context.Background(), sampleBackup(), StrategySkip)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped.Groups != 1 {
		t.Errorf("expected 1 group skipped, got %+v", rep.Skipped)
	}
	var g models.Group
	db.Where("name = ?", "openai-main").First(&g)
	if g.TestModel != "keep-me" {
		t.Errorf("skip must not overwrite: %+v", g)
	}
	// keys/aliases for this group must also be skipped
	var kc int64
	db.Model(&models.APIKey{}).Count(&kc)
	if kc != 0 {
		t.Errorf("keys must be skipped under same-name group skip, got %d", kc)
	}
}

func TestImport_ReplaceWipesStandardGroups(t *testing.T) {
	db := newTestDB(t)
	// pre-existing standard + its key + alias
	g := models.Group{Name: "old-grp", ChannelType: "openai", TestModel: "m", Upstreams: datatypes.JSON("[]")}
	db.Create(&g)
	db.Create(&models.APIKey{GroupID: g.ID, KeyValue: "enc:old", KeyHash: "ho"})
	db.Create(&models.ModelAlias{Alias: "a", GroupID: g.ID, RealModel: "m", Weight: 1, Priority: 100, Enabled: true})
	// pre-existing system aggregate (must survive)
	sys := models.Group{Name: "default-openai", ChannelType: "openai", TestModel: "m", IsSystem: true, GroupType: "aggregate", Upstreams: datatypes.JSON("[]")}
	db.Create(&sys)

	imp := NewImporter(db, fakeEncSvc{})
	if _, err := imp.Import(context.Background(), sampleBackup(), StrategyReplace); err != nil {
		t.Fatal(err)
	}
	var groups []models.Group
	db.Find(&groups)
	if len(groups) != 2 {
		t.Errorf("expected 1 sys + 1 imported standard, got %d (%+v)", len(groups), groups)
	}
	var oldCount int64
	db.Model(&models.Group{}).Where("name = ?", "old-grp").Count(&oldCount)
	if oldCount != 0 {
		t.Errorf("old standard group must be wiped under replace")
	}
}

func TestImport_SkipsSystemGroupsAndEncryptionKey(t *testing.T) {
	db := newTestDB(t)
	bk := sampleBackup()
	bk.Data.Groups = append(bk.Data.Groups, GroupDTO{Name: "default-openai", IsSystem: true, ChannelType: "openai", TestModel: "m"})
	bk.Data.SystemSettings = append(bk.Data.SystemSettings, SystemSettingDTO{SettingKey: "encryption_key", SettingValue: "hacked"})

	imp := NewImporter(db, fakeEncSvc{})
	rep, err := imp.Import(context.Background(), bk, StrategyMerge)
	if err != nil {
		t.Fatal(err)
	}
	var sysCount int64
	db.Model(&models.Group{}).Where("is_system = ?", true).Count(&sysCount)
	if sysCount != 0 {
		t.Errorf("is_system group must not be inserted from backup")
	}
	var s models.SystemSetting
	if err := db.Where("setting_key = ?", "encryption_key").First(&s).Error; err == nil {
		t.Errorf("encryption_key must not be imported")
	}
	_ = rep
}
