package handler

import (
	"autogateway/internal/encryption"
	app_errors "autogateway/internal/errors"
	"autogateway/internal/i18n"
	"autogateway/internal/models"
	"autogateway/internal/response"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Stats Get dashboard statistics
func (s *Server) Stats(c *gin.Context) {
	var activeKeys, invalidKeys int64
	s.DB.Model(&models.APIKey{}).Where("status = ?", models.KeyStatusActive).Count(&activeKeys)
	s.DB.Model(&models.APIKey{}).Where("status = ?", models.KeyStatusInvalid).Count(&invalidKeys)

	now := time.Now()
	rpmStats, err := s.getRPMStats(now)
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.rpm_stats_failed")
		return
	}
	twentyFourHoursAgo := now.Add(-24 * time.Hour)
	fortyEightHoursAgo := now.Add(-48 * time.Hour)

	currentPeriod, err := s.getHourlyStats(twentyFourHoursAgo, now)
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.current_stats_failed")
		return
	}
	previousPeriod, err := s.getHourlyStats(fortyEightHoursAgo, twentyFourHoursAgo)
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.previous_stats_failed")
		return
	}

	// 计算请求量趋势
	reqTrend := 0.0
	reqTrendIsGrowth := true
	if previousPeriod.TotalRequests > 0 {
		// 有前期数据，计算百分比变化
		reqTrend = (float64(currentPeriod.TotalRequests-previousPeriod.TotalRequests) / float64(previousPeriod.TotalRequests)) * 100
		reqTrendIsGrowth = reqTrend >= 0
	} else if currentPeriod.TotalRequests > 0 {
		// 前期无数据，当前有数据，视为100%增长
		reqTrend = 100.0
		reqTrendIsGrowth = true
	} else {
		// 前期和当前都无数据
		reqTrend = 0.0
		reqTrendIsGrowth = true
	}

	// 计算当前和前期错误率
	currentErrorRate := 0.0
	if currentPeriod.TotalRequests > 0 {
		currentErrorRate = (float64(currentPeriod.TotalFailures) / float64(currentPeriod.TotalRequests)) * 100
	}

	previousErrorRate := 0.0
	if previousPeriod.TotalRequests > 0 {
		previousErrorRate = (float64(previousPeriod.TotalFailures) / float64(previousPeriod.TotalRequests)) * 100
	}

	// 计算错误率趋势
	errorRateTrend := 0.0
	errorRateTrendIsGrowth := false
	if previousPeriod.TotalRequests > 0 {
		// 有前期数据，计算百分点差异
		errorRateTrend = currentErrorRate - previousErrorRate
		errorRateTrendIsGrowth = errorRateTrend < 0 // 错误率下降是好事
	} else if currentPeriod.TotalRequests > 0 {
		// 前期无数据，当前有数据
		errorRateTrend = currentErrorRate // 显示当前错误率
		errorRateTrendIsGrowth = false    // 有错误是坏事（如果错误率>0）
		if currentErrorRate == 0 {
			errorRateTrendIsGrowth = true // 如果当前无错误，标记为正面
		}
	} else {
		// 都无数据
		errorRateTrend = 0.0
		errorRateTrendIsGrowth = true
	}

	// 获取安全警告信息
	securityWarnings := s.getSecurityWarnings(c)

	stats := models.DashboardStatsResponse{
		KeyCount: models.StatCard{
			Value:       float64(activeKeys),
			SubValue:    invalidKeys,
			SubValueTip: i18n.Message(c, "dashboard.invalid_keys"),
		},
		RPM: rpmStats,
		RequestCount: models.StatCard{
			Value:         float64(currentPeriod.TotalRequests),
			Trend:         reqTrend,
			TrendIsGrowth: reqTrendIsGrowth,
		},
		ErrorRate: models.StatCard{
			Value:         currentErrorRate,
			Trend:         errorRateTrend,
			TrendIsGrowth: errorRateTrendIsGrowth,
		},
		SecurityWarnings: securityWarnings,
	}

	response.Success(c, stats)
}

// Chart Get dashboard chart data
func (s *Server) Chart(c *gin.Context) {
	groupID := c.Query("groupId")
	hours := 24
	if rawHours := strings.TrimSpace(c.DefaultQuery("hours", "24")); rawHours != "" {
		parsedHours, err := strconv.Atoi(rawHours)
		if err == nil {
			switch parsedHours {
			case 6, 12, 24, 48:
				hours = parsedHours
			}
		}
	}

	now := time.Now()
	endHour := now.Truncate(time.Hour)
	startHour := endHour.Add(-time.Duration(hours-1) * time.Hour)

	var hourlyStats []models.GroupHourlyStat
	query := s.DB.Table("group_hourly_stats").
		Where("time >= ? AND time < ?", startHour, endHour.Add(time.Hour))
	if groupID != "" {
		query = query.Where("group_id = ?", groupID)
	} else {
		query = query.Where("group_id NOT IN (?)",
			s.DB.Table("groups").Select("id").Where("group_type = ?", "aggregate"))
	}
	if err := query.Order("time asc").Find(&hourlyStats).Error; err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.chart_data_failed")
		return
	}

	statsByHour := make(map[time.Time]map[string]int64)
	for _, stat := range hourlyStats {
		hour := stat.Time.Local().Truncate(time.Hour)
		if _, ok := statsByHour[hour]; !ok {
			statsByHour[hour] = make(map[string]int64)
		}
		statsByHour[hour]["success"] += stat.SuccessCount
		statsByHour[hour]["failure"] += stat.FailureCount
	}

	var labels []string
	var successData, failureData []int64

	for i := range hours {
		hour := startHour.Add(time.Duration(i) * time.Hour)
		labels = append(labels, hour.Format(time.RFC3339))

		if data, ok := statsByHour[hour]; ok {
			successData = append(successData, data["success"])
			failureData = append(failureData, data["failure"])
		} else {
			successData = append(successData, 0)
			failureData = append(failureData, 0)
		}
	}

	chartData := models.ChartData{
		Labels: labels,
		Datasets: []models.ChartDataset{
			{
				Label: i18n.Message(c, "dashboard.success_requests"),
				Data:  successData,
				Color: "rgba(10, 200, 110, 1)",
			},
			{
				Label: i18n.Message(c, "dashboard.failed_requests"),
				Data:  failureData,
				Color: "rgba(255, 70, 70, 1)",
			},
		},
	}

	response.Success(c, chartData)
}

type hourlyStatResult struct {
	TotalRequests int64
	TotalFailures int64
}

func (s *Server) getHourlyStats(startTime, endTime time.Time) (hourlyStatResult, error) {
	var result hourlyStatResult
	err := s.DB.Table("group_hourly_stats").
		Where("time >= ? AND time < ?", startTime, endTime).
		Where("group_id NOT IN (?)",
			s.DB.Table("groups").Select("id").Where("group_type = ?", "aggregate")).
		Select("COALESCE(SUM(success_count), 0) + COALESCE(SUM(failure_count), 0) as total_requests, COALESCE(SUM(failure_count), 0) as total_failures").
		Scan(&result).Error
	return result, err
}

type rpmStatResult struct {
	CurrentRequests  int64
	PreviousRequests int64
}

func (s *Server) getRPMStats(now time.Time) (models.StatCard, error) {
	tenMinutesAgo := now.Add(-10 * time.Minute)
	twentyMinutesAgo := now.Add(-20 * time.Minute)

	var result rpmStatResult
	err := s.DB.Model(&models.RequestLog{}).
		Select("count(case when timestamp >= ? then 1 end) as current_requests, count(case when timestamp >= ? and timestamp < ? then 1 end) as previous_requests", tenMinutesAgo, twentyMinutesAgo, tenMinutesAgo).
		Where("timestamp >= ? AND request_type = ?", twentyMinutesAgo, models.RequestTypeFinal).
		Scan(&result).Error

	if err != nil {
		return models.StatCard{}, err
	}

	currentRPM := float64(result.CurrentRequests) / 10.0
	previousRPM := float64(result.PreviousRequests) / 10.0

	rpmTrend := 0.0
	rpmTrendIsGrowth := true
	if previousRPM > 0 {
		rpmTrend = (currentRPM - previousRPM) / previousRPM * 100
		rpmTrendIsGrowth = rpmTrend >= 0
	} else if currentRPM > 0 {
		rpmTrend = 100.0
		rpmTrendIsGrowth = true
	}

	return models.StatCard{
		Value:         currentRPM,
		Trend:         rpmTrend,
		TrendIsGrowth: rpmTrendIsGrowth,
	}, nil
}

// dashboardLookback 把 window 查询参数 (1h|6h|24h|7d) 映射成回看时长,
// 默认 24h。dashboard 的窗口型端点共用。
func dashboardLookback(window string) time.Duration {
	switch strings.TrimSpace(window) {
	case "1h":
		return time.Hour
	case "6h":
		return 6 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// TopModelStat is one row of /api/dashboard/top-models.
type TopModelStat struct {
	Model     string   `json:"model"`
	Calls     int64    `json:"calls"`
	AvgMs     int64    `json:"avg_ms"`
	Errors    int64    `json:"errors"`
	ErrorRate float64  `json:"error_rate"`
	Groups    []string `json:"groups"`
}

// TopModels returns the highest-volume models within the requested window.
// Used by the v3 Dashboard "Top models · 24h" card so it stops inferring
// counts from per-group stats.
//
// GET /api/dashboard/top-models?window=24h&limit=10
//   - window: 1h | 6h | 24h | 7d (default 24h)
//   - limit:  1..50 (default 10)
func (s *Server) TopModels(c *gin.Context) {
	since := time.Now().Add(-dashboardLookback(c.DefaultQuery("window", "24h")))

	type row struct {
		Model  string
		Calls  int64
		AvgMs  float64
		Errors int64
	}
	var rows []row
	err := s.DB.Model(&models.RequestLog{}).
		Select("model, COUNT(*) as calls, AVG(duration) as avg_ms, SUM(CASE WHEN is_success THEN 0 ELSE 1 END) as errors").
		Where("timestamp >= ? AND request_type = ? AND model IS NOT NULL AND model != ''", since, models.RequestTypeFinal).
		Group("model").
		Order("calls DESC").
		Limit(50).
		Scan(&rows).Error
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_top_models")
		return
	}

	// gather provider/group attribution per model from the same window
	type ga struct {
		Model     string
		GroupName string
	}
	var attrs []ga
	if err := s.DB.Model(&models.RequestLog{}).
		Select("DISTINCT model, group_name").
		Where("timestamp >= ? AND request_type = ? AND model IS NOT NULL AND model != ''", since, models.RequestTypeFinal).
		Scan(&attrs).Error; err != nil {
		logrus.WithError(err).Warn("top-models attribution query failed")
	}
	groupsByModel := make(map[string][]string, len(rows))
	for _, a := range attrs {
		if a.GroupName == "" {
			continue
		}
		groupsByModel[a.Model] = append(groupsByModel[a.Model], a.GroupName)
	}

	out := make([]TopModelStat, 0, len(rows))
	for _, r := range rows {
		errRate := 0.0
		if r.Calls > 0 {
			errRate = float64(r.Errors) / float64(r.Calls) * 100
		}
		out = append(out, TopModelStat{
			Model:     r.Model,
			Calls:     r.Calls,
			AvgMs:     int64(r.AvgMs),
			Errors:    r.Errors,
			ErrorRate: errRate,
			Groups:    groupsByModel[r.Model],
		})
	}

	response.Success(c, out)
}

// ModelTiming is one row of /api/dashboard/model-timings.
type ModelTiming struct {
	Model string `json:"model"`
	AvgMs int64  `json:"avg_ms"`
	Calls int64  `json:"calls"`
	// ①成本可观测性: 窗口内该模型累计 token 与按挂牌价折算成本 (免费源/未知模型 → 0)。
	// 前端在模型卡上挂成本/用量 chip (ModelCatalog / Aliases)。
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// ModelTimings returns avg request duration (ms) per model for the last 24h.
// Lightweight variant of TopModels: no LIMIT, no group attribution. The
// frontend uses the result map to decorate model cards with a "≈ X ms" chip.
//
// GET /api/dashboard/model-timings?window=24h
//   - window: 1h | 6h | 24h | 7d (default 24h)
func (s *Server) ModelTimings(c *gin.Context) {
	since := time.Now().Add(-dashboardLookback(c.DefaultQuery("window", "24h")))

	type row struct {
		Model   string
		Calls   int64
		AvgMs   float64
		Tokens  int64
		CostUSD float64
	}
	var rows []row
	err := s.DB.Model(&models.RequestLog{}).
		Select("model, COUNT(*) as calls, AVG(duration) as avg_ms, "+
			"COALESCE(SUM(total_tokens),0) as tokens, COALESCE(SUM(cost_usd),0) as cost_usd").
		Where("timestamp >= ? AND request_type = ? AND model IS NOT NULL AND model != ''", since, models.RequestTypeFinal).
		Group("model").
		Scan(&rows).Error
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_top_models")
		return
	}

	out := make([]ModelTiming, 0, len(rows))
	for _, r := range rows {
		out = append(out, ModelTiming{
			Model:   r.Model,
			AvgMs:   int64(r.AvgMs),
			Calls:   r.Calls,
			Tokens:  r.Tokens,
			CostUSD: r.CostUSD,
		})
	}
	response.Success(c, out)
}

// UsageSummaryResponse 是 /api/dashboard/usage-summary 的响应。
type UsageSummaryResponse struct {
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`         // 按挂牌价折算的等价价值 (免费源实际支出为 0)
	MeteredRequests  int64   `json:"metered_requests"` // 窗口内成功解析出用量的请求数
}

// UsageSummary 汇总窗口内的 token 用量与按挂牌价折算的成本 (①成本可观测性)。
// 数据来自 RequestLog, 受日志保留策略限制 —— 因此只做窗口 (1h|6h|24h|7d) 视图,
// 月度长周期需另把 token 卷进 group_hourly_stats (后续)。
//
// GET /api/dashboard/usage-summary?window=24h
func (s *Server) UsageSummary(c *gin.Context) {
	since := time.Now().Add(-dashboardLookback(c.DefaultQuery("window", "24h")))

	var out UsageSummaryResponse
	err := s.DB.Model(&models.RequestLog{}).
		Select("COALESCE(SUM(prompt_tokens),0) as prompt_tokens, "+
			"COALESCE(SUM(completion_tokens),0) as completion_tokens, "+
			"COALESCE(SUM(total_tokens),0) as total_tokens, "+
			"COALESCE(SUM(cost_usd),0) as cost_usd, "+
			"COUNT(CASE WHEN total_tokens > 0 THEN 1 END) as metered_requests").
		Where("timestamp >= ? AND request_type = ?", since, models.RequestTypeFinal).
		Scan(&out).Error
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_top_models")
		return
	}

	response.Success(c, out)
}

// UsageRollupResponse 是 /api/dashboard/usage-rollup 的响应。
type UsageRollupResponse struct {
	Days             int     `json:"days"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// UsageRollup 汇总长周期(默认 30 天)的 token 用量与折算成本。数据来自
// group_hourly_stats 小时卷积表, 独立于 RequestLog 保留期, 故可跨"本月"等长窗口。
// 排除聚合父分组行(其统计是子分组的镜像累加), 避免父+子双重计数 —— 与
// getHourlyStats 的口径一致。
//
// GET /api/dashboard/usage-rollup?days=30  (days: 1..365)
func (s *Server) UsageRollup(c *gin.Context) {
	days := 30
	if raw := strings.TrimSpace(c.DefaultQuery("days", "30")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	var out UsageRollupResponse
	err := s.DB.Table("group_hourly_stats").
		Where("time >= ?", since).
		Where("group_id NOT IN (?)",
			s.DB.Table("groups").Select("id").Where("group_type = ?", "aggregate")).
		Select("COALESCE(SUM(prompt_tokens),0) as prompt_tokens, " +
			"COALESCE(SUM(completion_tokens),0) as completion_tokens, " +
			"COALESCE(SUM(total_tokens),0) as total_tokens, " +
			"COALESCE(SUM(cost_usd),0) as cost_usd").
		Scan(&out).Error
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_top_models")
		return
	}
	out.Days = days

	response.Success(c, out)
}

// getSecurityWarnings 检查安全配置并返回警告信息
func (s *Server) getSecurityWarnings(c *gin.Context) []models.SecurityWarning {
	var warnings []models.SecurityWarning

	// 获取生效的 AUTH_KEY (DB > env bootstrap) 和 ENCRYPTION_KEY
	authKey := s.SettingsManager.GetEffectiveAuthKey()
	encryptionKey := s.config.GetEncryptionKey()

	// 检查AUTH_KEY
	if authKey == "" {
		warnings = append(warnings, models.SecurityWarning{
			Type:       "AUTH_KEY",
			Message:    i18n.Message(c, "dashboard.auth_key_missing"),
			Severity:   "high",
			Suggestion: i18n.Message(c, "dashboard.auth_key_required"),
		})
	} else {
		authWarnings := checkPasswordSecurity(c, authKey, "AUTH_KEY")
		warnings = append(warnings, authWarnings...)
	}

	// 检查ENCRYPTION_KEY
	if encryptionKey == "" {
		warnings = append(warnings, models.SecurityWarning{
			Type:       "ENCRYPTION_KEY",
			Message:    i18n.Message(c, "dashboard.encryption_key_missing"),
			Severity:   "high",
			Suggestion: i18n.Message(c, "dashboard.encryption_key_recommended"),
		})
	} else {
		encryptionWarnings := checkPasswordSecurity(c, encryptionKey, "ENCRYPTION_KEY")
		warnings = append(warnings, encryptionWarnings...)
	}

	// 检查系统级代理密钥
	systemSettings := s.SettingsManager.GetSettings()
	if systemSettings.ProxyKeys != "" {
		proxyKeys := strings.Split(systemSettings.ProxyKeys, ",")
		for i, key := range proxyKeys {
			key = strings.TrimSpace(key)
			if key != "" {
				keyName := fmt.Sprintf("%s #%d", i18n.Message(c, "dashboard.global_proxy_key"), i+1)
				proxyWarnings := checkPasswordSecurity(c, key, keyName)
				warnings = append(warnings, proxyWarnings...)
			}
		}
	}

	// 检查分组级代理密钥
	var groups []models.Group
	if err := s.DB.Where("proxy_keys IS NOT NULL AND proxy_keys != ''").Find(&groups).Error; err == nil {
		for _, group := range groups {
			if group.ProxyKeys != "" {
				proxyKeys := strings.Split(group.ProxyKeys, ",")
				for i, key := range proxyKeys {
					key = strings.TrimSpace(key)
					if key != "" {
						keyName := fmt.Sprintf("%s [%s] #%d", i18n.Message(c, "dashboard.group_proxy_key"), group.Name, i+1)
						proxyWarnings := checkPasswordSecurity(c, key, keyName)
						warnings = append(warnings, proxyWarnings...)
					}
				}
			}
		}
	}

	return warnings
}

// checkPasswordSecurity 综合检查密码安全性
func checkPasswordSecurity(c *gin.Context, password, keyType string) []models.SecurityWarning {
	var warnings []models.SecurityWarning

	// 1. 长度检查
	if len(password) < 16 {
		warnings = append(warnings, models.SecurityWarning{
			Type:       keyType,
			Message:    i18n.Message(c, "security.password_too_short", map[string]any{"keyType": keyType, "length": len(password)}),
			Severity:   "high", // 长度不足是高风险
			Suggestion: i18n.Message(c, "security.password_recommendation_16"),
		})
	} else if len(password) < 32 {
		warnings = append(warnings, models.SecurityWarning{
			Type:       keyType,
			Message:    i18n.Message(c, "security.password_short", map[string]any{"keyType": keyType, "length": len(password)}),
			Severity:   "medium",
			Suggestion: i18n.Message(c, "security.password_recommendation_32"),
		})
	}

	// 2. 常见弱密码检查
	lower := strings.ToLower(password)
	weakPatterns := []string{
		"password", "123456", "admin", "secret", "test", "demo",
		"sk-123456", "key", "token", "pass", "pwd", "qwerty",
		"abc", "default", "user", "login", "auth", "temp",
	}

	for _, pattern := range weakPatterns {
		if strings.Contains(lower, pattern) {
			warnings = append(warnings, models.SecurityWarning{
				Type:       keyType,
				Message:    i18n.Message(c, "security.password_weak_pattern", map[string]any{"keyType": keyType, "pattern": pattern}),
				Severity:   "high",
				Suggestion: i18n.Message(c, "security.password_avoid_common"),
			})
			break
		}
	}

	// 3. 复杂度检查（仅在长度足够时检查）
	if len(password) >= 16 && !hasGoodComplexity(password) {
		warnings = append(warnings, models.SecurityWarning{
			Type:       keyType,
			Message:    i18n.Message(c, "security.password_low_complexity", map[string]any{"keyType": keyType}),
			Severity:   "medium",
			Suggestion: i18n.Message(c, "security.password_complexity"),
		})
	}

	return warnings
}

// hasGoodComplexity 检查密码复杂度
func hasGoodComplexity(password string) bool {
	var hasUpper, hasLower, hasDigit, hasSpecial bool

	for _, char := range password {
		switch {
		case char >= 'A' && char <= 'Z':
			hasUpper = true
		case char >= 'a' && char <= 'z':
			hasLower = true
		case char >= '0' && char <= '9':
			hasDigit = true
		case !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')):
			hasSpecial = true
		}
	}

	// 至少包含3种类型的字符
	count := 0
	if hasUpper {
		count++
	}
	if hasLower {
		count++
	}
	if hasDigit {
		count++
	}
	if hasSpecial {
		count++
	}

	return count >= 3
}

// Encryption scenario types
const (
	ScenarioNone             = ""
	ScenarioDataNotEncrypted = "data_not_encrypted"
	ScenarioKeyNotConfigured = "key_not_configured"
	ScenarioKeyMismatch      = "key_mismatch"
)

// EncryptionStatus checks if ENCRYPTION_KEY is configured but keys are not encrypted
func (s *Server) EncryptionStatus(c *gin.Context) {
	hasMismatch, scenarioType, message, suggestion := s.checkEncryptionMismatch(c)

	response.Success(c, gin.H{
		"has_mismatch":  hasMismatch,
		"scenario_type": scenarioType,
		"message":       message,
		"suggestion":    suggestion,
	})
}

// checkEncryptionMismatch detects encryption configuration mismatches
func (s *Server) checkEncryptionMismatch(c *gin.Context) (bool, string, string, string) {
	encryptionKey := s.config.GetEncryptionKey()

	// Sample check API keys
	var sampleKeys []models.APIKey
	if err := s.DB.Limit(20).Where("key_hash IS NOT NULL AND key_hash != ''").Find(&sampleKeys).Error; err != nil {
		logrus.WithError(err).Error("Failed to fetch sample keys for encryption check")
		return false, ScenarioNone, "", ""
	}

	if len(sampleKeys) == 0 {
		// No keys in database, no mismatch
		return false, ScenarioNone, "", ""
	}

	// Check hash consistency with unencrypted data
	noopService, err := encryption.NewService("")
	if err != nil {
		logrus.WithError(err).Error("Failed to create noop encryption service")
		return false, ScenarioNone, "", ""
	}

	unencryptedHashMatchCount := 0
	for _, key := range sampleKeys {
		// For unencrypted data: key_hash should match SHA256(key_value)
		expectedHash := noopService.Hash(key.KeyValue)
		if expectedHash == key.KeyHash {
			unencryptedHashMatchCount++
		}
	}

	unencryptedConsistencyRate := float64(unencryptedHashMatchCount) / float64(len(sampleKeys))

	// If ENCRYPTION_KEY is configured, also check if current key can decrypt the data
	var currentKeyHashMatchCount int
	if encryptionKey != "" {
		currentService, err := encryption.NewService(encryptionKey)
		if err == nil {
			for _, key := range sampleKeys {
				// Try to decrypt and re-hash to check if current key matches
				decrypted, err := currentService.Decrypt(key.KeyValue)
				if err == nil {
					// Successfully decrypted, check if hash matches
					expectedHash := currentService.Hash(decrypted)
					if expectedHash == key.KeyHash {
						currentKeyHashMatchCount++
					}
				}
			}
		}
	}
	currentKeyConsistencyRate := float64(currentKeyHashMatchCount) / float64(len(sampleKeys))

	// Scenario A: ENCRYPTION_KEY configured but data not encrypted
	if encryptionKey != "" && unencryptedConsistencyRate > 0.8 {
		return true,
			ScenarioDataNotEncrypted,
			i18n.Message(c, "dashboard.encryption_key_configured_but_data_not_encrypted"),
			i18n.Message(c, "dashboard.encryption_key_migration_required")
	}

	// Scenario B: ENCRYPTION_KEY not configured but data is encrypted
	if encryptionKey == "" && unencryptedConsistencyRate < 0.2 {
		return true,
			ScenarioKeyNotConfigured,
			i18n.Message(c, "dashboard.data_encrypted_but_key_not_configured"),
			i18n.Message(c, "dashboard.configure_same_encryption_key")
	}

	// Scenario C: ENCRYPTION_KEY configured but doesn't match encrypted data
	if encryptionKey != "" && unencryptedConsistencyRate < 0.2 && currentKeyConsistencyRate < 0.2 {
		return true,
			ScenarioKeyMismatch,
			i18n.Message(c, "dashboard.encryption_key_mismatch"),
			i18n.Message(c, "dashboard.use_correct_encryption_key")
	}

	return false, ScenarioNone, "", ""
}
