package services

import (
	"context"
	"testing"
	"time"

	"autogateway/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.RequestLog{}, &models.ModelAlias{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
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

	if len(got) != 2 {
		t.Fatalf("expected 2 suggestions, got %d: %+v", len(got), got)
	}
	if got[0].Model != "gpt-4.1" || got[0].Count != 2 {
		t.Errorf("expected gpt-4.1 count=2 first, got %+v", got[0])
	}
	if got[1].Model != "claude-omega" || got[1].Count != 1 {
		t.Errorf("expected claude-omega count=1 second, got %+v", got[1])
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
