package services

import (
	"testing"
	"time"

	"autogateway/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newRollupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.RequestLog{}, &models.GroupHourlyStat{}, &models.APIKey{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func mkLog(id string, groupID, parentID uint, ts time.Time, reqType string, total int, cost float64, success bool) *models.RequestLog {
	return &models.RequestLog{
		ID:               id,
		Timestamp:        ts,
		GroupID:          groupID,
		ParentGroupID:    parentID,
		IsSuccess:        success,
		StatusCode:       200,
		Duration:         10,
		RequestType:      reqType,
		PromptTokens:     total / 2,
		CompletionTokens: total - total/2,
		TotalTokens:      total,
		CostUSD:          cost,
	}
}

func hourlyRow(t *testing.T, db *gorm.DB, groupID uint) models.GroupHourlyStat {
	t.Helper()
	var st models.GroupHourlyStat
	if err := db.Where("group_id = ?", groupID).First(&st).Error; err != nil {
		t.Fatalf("no hourly row for group %d: %v", groupID, err)
	}
	return st
}

func TestWriteLogsToDB_RollsTokensIntoHourly(t *testing.T) {
	db := newRollupTestDB(t)
	svc := &RequestLogService{db: db}
	hour := time.Date(2026, 7, 16, 10, 15, 0, 0, time.UTC) // 落在 10:00 桶

	logs := []*models.RequestLog{
		mkLog("a", 1, 0, hour, models.RequestTypeFinal, 100, 0.01, true),
		mkLog("b", 1, 0, hour, models.RequestTypeFinal, 200, 0.02, true),
		mkLog("c", 1, 0, hour, models.RequestTypeRetry, 999, 9.9, false), // retry 不计入
	}
	if err := svc.writeLogsToDB(logs); err != nil {
		t.Fatalf("write: %v", err)
	}

	st := hourlyRow(t, db, 1)
	if st.TotalTokens != 300 {
		t.Fatalf("total_tokens = %d, want 300", st.TotalTokens)
	}
	if st.PromptTokens != 150 || st.CompletionTokens != 150 {
		t.Fatalf("prompt/completion = %d/%d, want 150/150", st.PromptTokens, st.CompletionTokens)
	}
	if st.CostUSD < 0.0299 || st.CostUSD > 0.0301 {
		t.Fatalf("cost = %v, want ~0.03", st.CostUSD)
	}
	if st.SuccessCount != 2 {
		t.Fatalf("success = %d, want 2", st.SuccessCount)
	}
}

func TestWriteLogsToDB_AccumulatesAcrossBatches(t *testing.T) {
	db := newRollupTestDB(t)
	svc := &RequestLogService{db: db}
	hour := time.Date(2026, 7, 16, 10, 15, 0, 0, time.UTC)

	if err := svc.writeLogsToDB([]*models.RequestLog{
		mkLog("a", 1, 0, hour, models.RequestTypeFinal, 100, 0.01, true),
	}); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	// 第二批同一小时桶 → OnConflict 应做增量累加, 而非覆盖。
	if err := svc.writeLogsToDB([]*models.RequestLog{
		mkLog("b", 1, 0, hour.Add(20*time.Minute), models.RequestTypeFinal, 250, 0.05, true),
	}); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	st := hourlyRow(t, db, 1)
	if st.TotalTokens != 350 {
		t.Fatalf("total_tokens = %d, want 350 (accumulated)", st.TotalTokens)
	}
	if st.CostUSD < 0.0599 || st.CostUSD > 0.0601 {
		t.Fatalf("cost = %v, want ~0.06 (accumulated)", st.CostUSD)
	}
}

func TestWriteLogsToDB_ParentGroupAlsoRolled(t *testing.T) {
	db := newRollupTestDB(t)
	svc := &RequestLogService{db: db}
	hour := time.Date(2026, 7, 16, 10, 15, 0, 0, time.UTC)

	// 子分组 1, 父聚合分组 5 —— 两条 hourly 行都应记到 token/cost。
	if err := svc.writeLogsToDB([]*models.RequestLog{
		mkLog("a", 1, 5, hour, models.RequestTypeFinal, 120, 0.03, true),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	child := hourlyRow(t, db, 1)
	parent := hourlyRow(t, db, 5)
	if child.TotalTokens != 120 || parent.TotalTokens != 120 {
		t.Fatalf("child/parent total_tokens = %d/%d, want 120/120", child.TotalTokens, parent.TotalTokens)
	}
}
