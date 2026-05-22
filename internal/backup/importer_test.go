package backup

import (
	"bytes"
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

// TestImport_MergeOverwritesZeroValues pins down that merge replaces
// non-zero local values with explicit zero values from the backup
// (Sort=0, ModelRedirectStrict=false, etc.). Catches GORM Updates()
// zero-value-skip pitfall.
func TestImport_MergeOverwritesZeroValues(t *testing.T) {
	db := newTestDB(t)
	db.Create(&models.Group{
		Name:                "openai-main",
		ChannelType:         "openai",
		TestModel:           "gpt-4o-mini",
		DisplayName:         "Local",
		Sort:                5,
		ModelRedirectStrict: true,
		Upstreams:           datatypes.JSON("[]"),
	})

	bk := sampleBackup()
	// backup explicitly carries zero values
	bk.Data.Groups[0].Sort = 0
	bk.Data.Groups[0].ModelRedirectStrict = false
	bk.Data.Groups[0].DisplayName = ""

	imp := NewImporter(db, fakeEncSvc{})
	if _, err := imp.Import(context.Background(), bk, StrategyMerge); err != nil {
		t.Fatal(err)
	}

	var g models.Group
	if err := db.Where("name = ?", "openai-main").First(&g).Error; err != nil {
		t.Fatal(err)
	}
	if g.Sort != 0 {
		t.Errorf("Sort should be overwritten to 0, got %d", g.Sort)
	}
	if g.ModelRedirectStrict != false {
		t.Errorf("ModelRedirectStrict should be overwritten to false, got %v", g.ModelRedirectStrict)
	}
	if g.DisplayName != "" {
		t.Errorf("DisplayName should be overwritten to empty, got %q", g.DisplayName)
	}
}

// TestImport_PreservesNonEmptyUpstreams ensures the JSON column survives
// dto → model encode (json.Marshal of any).
func TestImport_PreservesNonEmptyUpstreams(t *testing.T) {
	db := newTestDB(t)
	bk := &BackupV1{
		SchemaVersion: 1,
		Data: DataV1{
			Groups: []GroupDTO{{
				Name:        "with-upstreams",
				ChannelType: "openai",
				TestModel:   "m",
				Upstreams: []map[string]any{
					{"url": "https://a.example", "weight": 1},
				},
			}},
		},
	}
	imp := NewImporter(db, fakeEncSvc{})
	if _, err := imp.Import(context.Background(), bk, StrategyMerge); err != nil {
		t.Fatal(err)
	}
	var g models.Group
	db.Where("name = ?", "with-upstreams").First(&g)
	if len(g.Upstreams) == 0 || string(g.Upstreams) == "[]" {
		t.Fatalf("Upstreams should contain payload data, got %q", string(g.Upstreams))
	}
	if !bytes.Contains(g.Upstreams, []byte(`"url":"https://a.example"`)) {
		t.Errorf("Upstreams missing url key: %q", string(g.Upstreams))
	}
}

// TestImport_GroupSubGroupRoundTrip exercises the GroupSubGroup branch
// where both parent and child are present in the backup.
func TestImport_GroupSubGroupRoundTrip(t *testing.T) {
	db := newTestDB(t)
	// Pre-existing system aggregate (importer never inserts is_system rows).
	db.Create(&models.Group{
		Name:        "default-openai",
		ChannelType: "openai",
		TestModel:   "m",
		IsSystem:    true,
		GroupType:   "aggregate",
		Upstreams:   datatypes.JSON("[]"),
	})

	bk := &BackupV1{
		SchemaVersion: 1,
		Data: DataV1{
			Groups: []GroupDTO{{
				Name: "openai-main", ChannelType: "openai", TestModel: "m",
			}},
			GroupSubGroups: []GroupSubGroupDTO{
				{ParentName: "default-openai", SubGroupName: "openai-main", Weight: 7},
			},
		},
	}
	imp := NewImporter(db, fakeEncSvc{})
	rep, err := imp.Import(context.Background(), bk, StrategyMerge)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied.GroupSubGroups != 1 {
		t.Errorf("expected 1 sub-group applied, got %+v", rep.Applied)
	}
	var subs []models.GroupSubGroup
	db.Find(&subs)
	if len(subs) != 1 || subs[0].Weight != 7 {
		t.Errorf("group_sub_groups row missing or wrong weight: %+v", subs)
	}
}

// TestImport_RejectsSystemGroupNameCollision pins down that an attacker
// cannot demote a system aggregate by submitting a payload with
// is_system=false but the system name (e.g. "default-openai").
func TestImport_RejectsSystemGroupNameCollision(t *testing.T) {
	db := newTestDB(t)
	// Pre-seed an existing system aggregate.
	db.Create(&models.Group{
		Name:        "default-openai",
		ChannelType: "openai",
		TestModel:   "m",
		IsSystem:    true,
		GroupType:   "aggregate",
		Upstreams:   datatypes.JSON("[]"),
	})

	// Build a malicious payload claiming default-openai is a standard group.
	bk := &BackupV1{
		SchemaVersion: 1,
		Data: DataV1{
			Groups: []GroupDTO{
				{Name: "default-openai", ChannelType: "openai", TestModel: "m", IsSystem: false},
			},
		},
	}
	imp := NewImporter(db, fakeEncSvc{})
	if _, err := imp.Import(context.Background(), bk, StrategyMerge); err == nil {
		t.Fatal("expected error rejecting system-name collision")
	}
	// System group must still be system after the rejection (whole tx rolls back).
	var got models.Group
	if err := db.Where("name = ?", "default-openai").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if !got.IsSystem {
		t.Errorf("system group must not be demoted")
	}
}
