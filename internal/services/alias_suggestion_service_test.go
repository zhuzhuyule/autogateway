package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"autogateway/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.RequestLog{}, &models.ModelAlias{}, &models.Group{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestDeriveFamily(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"gemini-2.5-flash-lite", "gemini-2.5"},
		{"gemini-2.5-flash", "gemini-2.5"},
		{"gemini-2.5-pro", "gemini-2.5"},
		{"gemini-2.0-flash", "gemini-2.0"},
		{"gpt-oss-120b", "gpt-oss"},
		{"gpt-oss-20b", "gpt-oss"},
		{"gpt-4o-mini", "gpt-4o"},
		{"gpt-4o", "gpt-4o"},
		{"claude-sonnet-4-5-20251022", "claude"},
		{"claude-haiku-4", "claude"},
		{"claude-opus-4", "claude"},
		{"qwen-2.5-72b-instruct", "qwen-2.5"},
		{"qwen-2.5-coder-32b", "qwen-2.5"},
		{"openai/gpt-oss-20b:free", "gpt-oss"},
		{"openrouter/openai/gpt-oss-120b", "gpt-oss"}, // last "/" wins → gpt-oss-120b → gpt-oss
		{"deepseek-r1", "deepseek-r1"},
		{"openrouter/auto", "auto"},
		{"  GEMINI-2.5-Flash  ", "gemini-2.5"},
		{"some-model (free)", "some-model"},
	}
	for _, c := range cases {
		got := deriveFamily(c.in)
		if got != c.want {
			t.Errorf("deriveFamily(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSuggestionsFromUnmatchedLogs(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()

	logs := []models.RequestLog{
		{ID: "1", Timestamp: now.Add(-1 * time.Hour), Model: "gpt-4.1", StatusCode: 405, IsSuccess: false},
		{ID: "2", Timestamp: now.Add(-30 * time.Minute), Model: "gpt-4.1", StatusCode: 404, IsSuccess: false},
		{ID: "3", Timestamp: now.Add(-2 * time.Hour), Model: "claude-omega", StatusCode: 405, IsSuccess: false},
		{ID: "4", Timestamp: now.Add(-3 * time.Hour), Model: "gpt-4o", StatusCode: 200, IsSuccess: true},
		{ID: "5", Timestamp: now.Add(-72 * time.Hour), Model: "old-model", StatusCode: 405, IsSuccess: false},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	svc := NewAliasSuggestionService(db)
	got, err := svc.Suggest(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}

	// gpt-4.1 → family "gpt-4.1" (alone) → single
	// claude-omega → "claude" (alone, no group siblings) → single
	if len(got) != 2 {
		t.Fatalf("expected 2 suggestions, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "single" || got[0].Model != "gpt-4.1" || got[0].Count != 2 {
		t.Errorf("expected gpt-4.1 single count=2 first, got %+v", got[0])
	}
	if got[1].Kind != "single" || got[1].Model != "claude-omega" || got[1].Count != 1 {
		t.Errorf("expected claude-omega single count=1 second, got %+v", got[1])
	}
}

func TestSuggestionsExcludeAlreadyAliased(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	if err := db.Create(&[]models.RequestLog{
		{ID: "1", Timestamp: now, Model: "gpt-4.1", StatusCode: 405, IsSuccess: false},
		{ID: "2", Timestamp: now, Model: "claude-omega", StatusCode: 405, IsSuccess: false},
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Create(&models.ModelAlias{
		Alias: "gpt-4.1", GroupID: 1, RealModel: "gpt-4o", Weight: 1, Priority: 100, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	svc := NewAliasSuggestionService(db)
	got, err := svc.Suggest(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(got) != 1 || got[0].Model != "claude-omega" {
		t.Fatalf("expected only claude-omega, got %+v", got)
	}
}

func TestSuggestionsFamilyPromotion(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()

	// 3 distinct claude-* models all derive to family "claude" via the heuristic
	// (haiku/sonnet/opus are variant tokens). gpt-foo derives to "gpt-foo" alone.
	logs := []models.RequestLog{
		{ID: "1", Timestamp: now, Model: "claude-sonnet-4-5", StatusCode: 405, IsSuccess: false},
		{ID: "2", Timestamp: now, Model: "claude-sonnet-4-5", StatusCode: 405, IsSuccess: false},
		{ID: "3", Timestamp: now, Model: "claude-opus-4", StatusCode: 405, IsSuccess: false},
		{ID: "4", Timestamp: now, Model: "claude-haiku-4", StatusCode: 405, IsSuccess: false},
		{ID: "5", Timestamp: now, Model: "gpt-foo", StatusCode: 405, IsSuccess: false},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	avail, _ := json.Marshal([]string{"claude-haiku-4", "unrelated-model"})
	g := models.Group{
		Name:            "anthropic-prod",
		ChannelType:     "anthropic",
		Upstreams:       datatypes.JSON([]byte(`[]`)),
		TestModel:       "claude-haiku-4",
		AvailableModels: datatypes.JSON(avail),
	}
	if err := db.Create(&g).Error; err != nil {
		t.Fatalf("seed group: %v", err)
	}

	svc := NewAliasSuggestionService(db)
	got, err := svc.Suggest(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 1 family + 1 single, got %d: %+v", len(got), got)
	}

	fam := got[0]
	if fam.Kind != "family" || fam.Family != "claude" {
		t.Fatalf("expected first row family=claude, got %+v", fam)
	}
	if fam.Count != 4 {
		t.Errorf("family total count want 4, got %d", fam.Count)
	}
	if fam.ExistingAlias != "" {
		t.Errorf("no alias yet so ExistingAlias should be empty, got %q", fam.ExistingAlias)
	}
	var haiku *FamilyModel
	for i := range fam.Models {
		if fam.Models[i].Name == "claude-haiku-4" {
			haiku = &fam.Models[i]
		}
	}
	if haiku == nil {
		t.Fatalf("claude-haiku-4 missing from family models: %+v", fam.Models)
	}
	if !haiku.FromLogs {
		t.Errorf("haiku should be FromLogs=true (it was in logs)")
	}
	if len(haiku.InGroupIDs) != 1 || haiku.InGroupIDs[0] != g.ID {
		t.Errorf("haiku InGroupIDs want [%d], got %v", g.ID, haiku.InGroupIDs)
	}

	if got[1].Kind != "single" || got[1].Model != "gpt-foo" {
		t.Errorf("expected gpt-foo single second, got %+v", got[1])
	}
}

func TestSuggestionsSingleLogPromotedByGroupSiblings(t *testing.T) {
	// Single unrecognized model in logs but the user's groups already host
	// 2 sibling models in the same heuristic family → promote to family
	// suggestion. This is the case the heuristic was added for: the CDN
	// `model_family` field gives each gemini variant its own bucket, so
	// we can't rely on it to aggregate.
	db := newTestDB(t)
	now := time.Now()

	logs := []models.RequestLog{
		{ID: "1", Timestamp: now, Model: "gemini-2.5-flash-lite", StatusCode: 405, IsSuccess: false},
		{ID: "2", Timestamp: now, Model: "gemini-2.5-flash-lite", StatusCode: 405, IsSuccess: false},
		{ID: "3", Timestamp: now, Model: "gemini-2.5-flash-lite", StatusCode: 405, IsSuccess: false},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	avail, _ := json.Marshal([]string{"gemini-2.5-flash", "gemini-2.5-pro"})
	g := models.Group{
		Name:            "google",
		ChannelType:     "gemini",
		Upstreams:       datatypes.JSON([]byte(`[]`)),
		TestModel:       "gemini-2.5-flash",
		AvailableModels: datatypes.JSON(avail),
	}
	if err := db.Create(&g).Error; err != nil {
		t.Fatalf("seed group: %v", err)
	}

	svc := NewAliasSuggestionService(db)
	got, err := svc.Suggest(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 family suggestion, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "family" || got[0].Family != "gemini-2.5" {
		t.Fatalf("expected family=gemini-2.5, got %+v", got[0])
	}
	// Models: gemini-2.5-flash-lite (FromLogs=true) + flash + pro (FromLogs=false)
	if len(got[0].Models) != 3 {
		t.Errorf("expected 3 family models, got %+v", got[0].Models)
	}
}

func TestSuggestionsFamilyExistingAlias(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()

	if err := db.Create(&[]models.RequestLog{
		{ID: "1", Timestamp: now, Model: "claude-sonnet-4-5", StatusCode: 405, IsSuccess: false},
		{ID: "2", Timestamp: now, Model: "claude-sonnet-4-5", StatusCode: 405, IsSuccess: false},
		{ID: "3", Timestamp: now, Model: "claude-opus-4", StatusCode: 405, IsSuccess: false},
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Create(&models.ModelAlias{
		Alias: "claude", GroupID: 1, RealModel: "claude-haiku-4",
		Weight: 1, Priority: 100, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	svc := NewAliasSuggestionService(db)
	got, err := svc.Suggest(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "family" || got[0].ExistingAlias != "claude" {
		t.Fatalf("expected family with ExistingAlias=claude, got %+v", got)
	}
}
