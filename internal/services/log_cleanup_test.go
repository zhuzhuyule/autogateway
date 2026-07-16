package services

import (
	"testing"
	"time"

	"autogateway/internal/config"
	"autogateway/internal/models"
)

// TestCleanupExpiredHourlyStats 验证小时统计按 HourlyStatsRetentionDays 保留:
// 超期行删除、近期行保留。未初始化的 settingsManager 返回默认配置
// (HourlyStatsRetentionDays=400)。
func TestCleanupExpiredHourlyStats(t *testing.T) {
	db := newRollupTestDB(t) // migrates GroupHourlyStat
	sm := config.NewSystemSettingsManager()
	svc := NewLogCleanupService(db, sm)

	now := time.Now()
	// 401 天前 → 超过默认 400 天保留 → 应删。
	if err := db.Create(&models.GroupHourlyStat{Time: now.AddDate(0, 0, -401), GroupID: 1, TotalTokens: 100, CostUSD: 1}).Error; err != nil {
		t.Fatalf("seed old: %v", err)
	}
	// 30 天前 → 保留窗口内 → 应留。
	if err := db.Create(&models.GroupHourlyStat{Time: now.AddDate(0, 0, -30), GroupID: 1, TotalTokens: 200, CostUSD: 2}).Error; err != nil {
		t.Fatalf("seed recent: %v", err)
	}

	svc.cleanupExpiredHourlyStats()

	var remaining []models.GroupHourlyStat
	if err := db.Find(&remaining).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("want 1 row kept, got %d", len(remaining))
	}
	if remaining[0].TotalTokens != 200 {
		t.Fatalf("wrong row kept: %+v (expected the 30-day-old one)", remaining[0])
	}
}
