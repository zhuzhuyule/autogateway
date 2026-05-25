package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"autogateway/internal/channel"
	"autogateway/internal/config"
	"autogateway/internal/encryption"
	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"
	"autogateway/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// I18nError represents an error that carries translation metadata.
type I18nError struct {
	APIError  *app_errors.APIError
	MessageID string
	Template  map[string]any
}

// Error implements the error interface.
func (e *I18nError) Error() string {
	if e == nil || e.APIError == nil {
		return ""
	}
	return e.APIError.Error()
}

// NewI18nError is a helper to create an I18n-enabled error.
func NewI18nError(apiErr *app_errors.APIError, msgID string, template map[string]any) *I18nError {
	return &I18nError{
		APIError:  apiErr,
		MessageID: msgID,
		Template:  template,
	}
}

// GroupService handles business logic for group operations.
type GroupService struct {
	db                    *gorm.DB
	settingsManager       *config.SystemSettingsManager
	groupManager          *GroupManager
	keyService            *KeyService
	keyImportSvc          *KeyImportService
	encryptionSvc         encryption.Service
	aggregateGroupService *AggregateGroupService
	freeModelsRegistry    *FreeModelsRegistry
	channelRegistry       []string
}

// NewGroupService constructs a GroupService.
func NewGroupService(
	db *gorm.DB,
	settingsManager *config.SystemSettingsManager,
	groupManager *GroupManager,
	keyService *KeyService,
	keyImportSvc *KeyImportService,
	encryptionSvc encryption.Service,
	aggregateGroupService *AggregateGroupService,
	freeModelsRegistry *FreeModelsRegistry,
) *GroupService {
	return &GroupService{
		db:                    db,
		settingsManager:       settingsManager,
		groupManager:          groupManager,
		keyService:            keyService,
		keyImportSvc:          keyImportSvc,
		encryptionSvc:         encryptionSvc,
		aggregateGroupService: aggregateGroupService,
		freeModelsRegistry:    freeModelsRegistry,
		channelRegistry:       channel.GetChannels(),
	}
}

// GroupCreateParams captures all fields required to create a group.
type GroupCreateParams struct {
	Name                string
	DisplayName         string
	Description         string
	GroupType           string
	Upstreams           json.RawMessage
	ChannelType         string
	Sort                int
	TestModel           string
	ValidationEndpoint  string
	ParamOverrides      map[string]any
	ModelRedirectRules  map[string]string
	ModelRedirectStrict bool
	Config              map[string]any
	HeaderRules         []models.HeaderRule
	ProxyKeys           string
	SubGroups           []SubGroupInput
	ModelRoutingMode    string   // "passthrough" | "specified", 空字符串走默认
	ExposedModels       []string // specified 模式下的白名单
	BlockedModels       []string // 黑名单 (与 mode 正交)
	AvailableModels     []string // 静态模型清单 (LongCat 等无 /models 端点的 provider 用)
}

// GroupUpdateParams captures updatable fields for a group.
type GroupUpdateParams struct {
	Name                *string
	DisplayName         *string
	Description         *string
	GroupType           *string
	Upstreams           json.RawMessage
	HasUpstreams        bool
	ChannelType         *string
	Sort                *int
	TestModel           string
	HasTestModel        bool
	ValidationEndpoint  *string
	ParamOverrides      map[string]any
	ModelRedirectRules  map[string]string
	ModelRedirectStrict *bool
	Config              map[string]any
	HeaderRules         *[]models.HeaderRule
	ProxyKeys           *string
	SubGroups           *[]SubGroupInput
	ModelRoutingMode    *string
	ExposedModels       *[]string
	BlockedModels       *[]string
}

// GroupReorderItem captures a group ID and target sort value.
type GroupReorderItem struct {
	ID   uint
	Sort int
}

// KeyStats captures aggregated API key statistics for a group.
type KeyStats struct {
	TotalKeys   int64 `json:"total_keys"`
	ActiveKeys  int64 `json:"active_keys"`
	InvalidKeys int64 `json:"invalid_keys"`
}

// RequestStats captures request success and failure ratios over a time window.
type RequestStats struct {
	TotalRequests  int64   `json:"total_requests"`
	FailedRequests int64   `json:"failed_requests"`
	FailureRate    float64 `json:"failure_rate"`
}

// GroupStats aggregates all per-group metrics for dashboard usage.
type GroupStats struct {
	KeyStats    KeyStats     `json:"key_stats"`
	Stats24Hour RequestStats `json:"stats_24_hour"`
	Stats7Day   RequestStats `json:"stats_7_day"`
	Stats30Day  RequestStats `json:"stats_30_day"`
}

// ConfigOption describes a configurable override exposed to clients.
type ConfigOption struct {
	Key          string
	Name         string
	Description  string
	DefaultValue any
}

// CreateGroup validates and persists a new group.
func (s *GroupService) CreateGroup(ctx context.Context, params GroupCreateParams) (*models.Group, error) {
	name := strings.TrimSpace(params.Name)
	if !isValidGroupName(name) {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_group_name", nil)
	}

	channelType := strings.TrimSpace(params.ChannelType)
	if !s.isValidChannelType(channelType) {
		supported := strings.Join(s.channelRegistry, ", ")
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_channel_type", map[string]any{"types": supported})
	}

	groupType := strings.TrimSpace(params.GroupType)
	if groupType == "" {
		groupType = "standard"
	}
	if groupType != "standard" && groupType != "aggregate" {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_group_type", nil)
	}

	var cleanedUpstreams datatypes.JSON
	var testModel string
	var validationEndpoint string

	switch groupType {
	case "aggregate":
		validationEndpoint = ""
		cleanedUpstreams = datatypes.JSON("[]")
		testModel = "-"
	case "standard":
		testModel = strings.TrimSpace(params.TestModel)
		if testModel == "" {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.test_model_required", nil)
		}
		cleaned, err := s.validateAndCleanUpstreams(params.Upstreams)
		if err != nil {
			return nil, err
		}
		cleanedUpstreams = cleaned

		validationEndpoint = strings.TrimSpace(params.ValidationEndpoint)
		if !isValidValidationEndpoint(validationEndpoint) {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_test_path", nil)
		}
	}

	cleanedConfig, err := s.validateAndCleanConfig(params.Config)
	if err != nil {
		return nil, err
	}

	headerRulesJSON, err := s.normalizeHeaderRules(params.HeaderRules)
	if err != nil {
		return nil, err
	}
	if headerRulesJSON == nil {
		headerRulesJSON = datatypes.JSON("[]")
	}

	// Validate model redirect rules for aggregate groups
	if groupType == "aggregate" && len(params.ModelRedirectRules) > 0 {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.aggregate_no_model_redirect", nil)
	}

	// Validate model redirect rules format
	if err := validateModelRedirectRules(params.ModelRedirectRules); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_model_redirect", map[string]any{"error": err.Error()})
	}

	routingMode := strings.TrimSpace(params.ModelRoutingMode)
	if routingMode == "" {
		routingMode = "passthrough"
	}
	if routingMode != "passthrough" && routingMode != "specified" {
		return nil, NewI18nError(app_errors.ErrValidation, "group.invalid_routing_mode", map[string]any{"mode": routingMode})
	}
	var exposedModelsJSON datatypes.JSON
	if params.ExposedModels != nil {
		buf, err := json.Marshal(params.ExposedModels)
		if err != nil {
			return nil, fmt.Errorf("marshal exposed_models: %w", err)
		}
		exposedModelsJSON = buf
	}

	var availableModelsJSON datatypes.JSON
	if len(params.AvailableModels) > 0 {
		buf, err := json.Marshal(params.AvailableModels)
		if err != nil {
			return nil, fmt.Errorf("marshal available_models: %w", err)
		}
		availableModelsJSON = buf
	}

	var blockedModelsJSON datatypes.JSON
	if params.BlockedModels != nil {
		buf, err := json.Marshal(params.BlockedModels)
		if err != nil {
			return nil, fmt.Errorf("marshal blocked_models: %w", err)
		}
		blockedModelsJSON = buf
	}

	group := models.Group{
		Name:                name,
		DisplayName:         strings.TrimSpace(params.DisplayName),
		Description:         strings.TrimSpace(params.Description),
		GroupType:           groupType,
		Upstreams:           cleanedUpstreams,
		ChannelType:         channelType,
		Sort:                params.Sort,
		TestModel:           testModel,
		ValidationEndpoint:  validationEndpoint,
		ParamOverrides:      params.ParamOverrides,
		ModelRedirectRules:  convertToJSONMap(params.ModelRedirectRules),
		ModelRedirectStrict: params.ModelRedirectStrict,
		Config:              cleanedConfig,
		HeaderRules:         headerRulesJSON,
		ProxyKeys:           strings.TrimSpace(params.ProxyKeys),
		ModelRoutingMode:    routingMode,
		ExposedModels:       exposedModelsJSON,
		BlockedModels:       blockedModelsJSON,
		AvailableModels:     availableModelsJSON,
	}
	if availableModelsJSON != nil {
		now := time.Now()
		group.ModelsRefreshedAt = &now
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, app_errors.ErrDatabase
	}

	if err := tx.Create(&group).Error; err != nil {
		tx.Rollback()
		return nil, app_errors.ParseDBError(err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	// 自动挂入对应 channel_type 的系统默认聚合(仅 standard 分组)
	if group.GroupType == "standard" && s.aggregateGroupService != nil {
		s.aggregateGroupService.AutoJoinSystemAggregate(ctx, &group)
	}

	return &group, nil
}

// ListGroups returns all groups without sub-group relations.
// Each Group is decorated with KeyCount: standard groups carry their own
// api_keys count; aggregate groups sum the counts of every sub-group they
// reference (so the V3 sidebar can show a single trailing number per row).
func (s *GroupService) ListGroups(ctx context.Context) ([]models.Group, error) {
	var groups []models.Group
	if err := s.db.WithContext(ctx).Order("sort asc, id desc").Find(&groups).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if len(groups) == 0 {
		return groups, nil
	}

	// per-group api_key counts in a single GROUP BY query
	type kc struct {
		GroupID uint
		Cnt     int64
	}
	var rows []kc
	if err := s.db.WithContext(ctx).
		Model(&models.APIKey{}).
		Select("group_id, COUNT(*) as cnt").
		Group("group_id").
		Find(&rows).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	standardCounts := make(map[uint]int64, len(rows))
	for _, r := range rows {
		standardCounts[r.GroupID] = r.Cnt
	}

	// for aggregate groups, fan out via group_sub_groups
	var links []models.GroupSubGroup
	if err := s.db.WithContext(ctx).Find(&links).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	aggregateCounts := make(map[uint]int64)
	for _, l := range links {
		aggregateCounts[l.GroupID] += standardCounts[l.SubGroupID]
	}

	for i := range groups {
		if groups[i].GroupType == "aggregate" {
			groups[i].KeyCount = aggregateCounts[groups[i].ID]
		} else {
			groups[i].KeyCount = standardCounts[groups[i].ID]
		}
	}

	return groups, nil
}

// MaybeRefreshAvailableModels 仅在该 standard 分组尚未缓存上游模型列表时触发拉取.
// 用于"添加 key 后自动拉一次"等场景,失败静默不阻断主流程.
func (s *GroupService) MaybeRefreshAvailableModels(ctx context.Context, groupID uint) error {
	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return err
	}
	if group.GroupType != "standard" {
		return nil
	}
	// 已缓存(非空 JSON 数组)就跳过;空字符串 / "[]" / nil 视为未缓存.
	if len(group.AvailableModels) > 2 {
		return nil
	}
	_, err := s.RefreshAvailableModels(ctx, groupID)
	if err != nil {
		logrus.WithError(err).Debugf("MaybeRefreshAvailableModels skipped for group %d", groupID)
	}
	return err
}

// RefreshAvailableModels 拉取分组对应上游的真实模型列表并缓存到 group.AvailableModels.
// 仅 standard 分组有意义;聚合分组无独立 upstream,直接报错.
func (s *GroupService) RefreshAvailableModels(ctx context.Context, groupID uint) ([]string, error) {
	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, NewI18nError(app_errors.ErrResourceNotFound, "group.not_found", nil)
		}
		return nil, app_errors.ParseDBError(err)
	}
	if group.GroupType != "standard" {
		return nil, NewI18nError(app_errors.ErrBadRequest, "group.refresh_models_not_supported", nil)
	}

	apiKey, err := s.pickActiveDecryptedKey(ctx, group.ID)
	if err != nil {
		return nil, err
	}

	modelIDs, err := FetchUpstreamModels(ctx, &group, apiKey)
	if err != nil {
		// 部分 provider 没有 OpenAI 兼容的 /v1/models 端点 (e.g. iFlytek Spark
		// 只暴露 /v1/chat/completions). 这种情况下用 FreeModels registry 按
		// upstream host 反查 providerId, 拿出该 provider 收录的全部 modelId 兜底.
		// 仅在 404 / "not found" 这类"端点不存在"语义下回退, 401/403/timeout
		// 等鉴权或网络问题继续向上报, 避免错误数据被静默缓存.
		if fallback := s.fallbackModelsFromRegistry(&group, err); fallback != nil {
			logrus.WithField("group_id", group.ID).WithField("count", len(fallback)).
				Info("upstream /v1/models 404, fell back to FreeModels registry")
			modelIDs = fallback
			err = nil
		} else {
			logrus.WithError(err).WithField("group_id", group.ID).Warn("fetch upstream models failed")
			return nil, NewI18nError(app_errors.ErrInternalServer, "group.refresh_models_failed", map[string]any{"error": err.Error()})
		}
	}

	data, err := json.Marshal(modelIDs)
	if err != nil {
		return nil, fmt.Errorf("marshal models list: %w", err)
	}

	// 同步计算 free_models 子集 (Registry × upstream, provider-aware 归一化求交).
	// 跟 available_models 同一刷新时机固化, 让前端零本地算法直接判 Free badge,
	// 也让 backup / proxy routing 用同一个 truth source.
	freeIDs := computeFreeModelIDs(&group, modelIDs, s.freeModelsRegistry)
	freeData, err := json.Marshal(freeIDs)
	if err != nil {
		return nil, fmt.Errorf("marshal free models list: %w", err)
	}

	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("id = ?", group.ID).
		Updates(map[string]any{
			"available_models":    datatypes.JSON(data),
			"free_models":         datatypes.JSON(freeData),
			"models_refreshed_at": &now,
		}).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("invalidate group cache after refresh-models failed")
	}
	logrus.WithFields(logrus.Fields{
		"group_id":      group.ID,
		"available":     len(modelIDs),
		"free":          len(freeIDs),
	}).Info("refresh-models: persisted available + free subset")
	return modelIDs, nil
}

// computeFreeModelIDs 按 "Registry × upstream" 契约算出免费子集:
//   1. 从 Registry 拿该 provider 下 is_free=true 的 entry, 剥前缀得 bare ID 集
//   2. 对每个上游 ID 做 provider-aware 归一化 (OpenRouter 剥 ":free" 后缀;
//      其他 provider 不变), 在 bare 集合里查
//   3. 命中 → 该上游 ID (保留原样, 不剥后缀) 进 freeIDs
// 不靠 ":free" 字面后缀判 free; 不依赖跨 provider fallback.
func computeFreeModelIDs(group *models.Group, upstreamIDs []string, registry *FreeModelsRegistry) []string {
	if registry == nil || len(upstreamIDs) == 0 {
		return []string{}
	}
	// 反查 providerId: 优先 upstream host 反查 (准), fallback group.Name (常重叠)
	providerID := ""
	if baseURL, err := firstUpstreamURL(group); err == nil {
		if host := extractHost(baseURL); host != "" {
			if id, _, ok := registry.LookupProviderByHost(host); ok {
				providerID = id
			}
		}
	}
	if providerID == "" {
		providerID = group.Name
	}

	// Registry 下该 provider 的 free bare ID 集合 (entry.id 已剥 provider 前缀)
	bareSet := make(map[string]struct{}, 32)
	regList := registry.ListModelIDsByProvider(providerID)
	for _, bareID := range regList {
		bareSet[strings.ToLower(bareID)] = struct{}{}
	}
	sampleReg := regList
	if len(sampleReg) > 5 {
		sampleReg = sampleReg[:5]
	}
	sampleUp := upstreamIDs
	if len(sampleUp) > 5 {
		sampleUp = sampleUp[:5]
	}
	logrus.WithFields(logrus.Fields{
		"group":            group.Name,
		"provider_id":      providerID,
		"registry_n":       len(regList),
		"upstream_n":       len(upstreamIDs),
		"registry_sample":  sampleReg,
		"upstream_sample":  sampleUp,
	}).Info("computeFreeModelIDs: starting match")
	if len(bareSet) == 0 {
		return []string{}
	}

	// 直接 string 严格匹配, 不做任何 ID patch (剥前缀/剥后缀都不要).
	// Registry 数据本身已经维护了正确的 model id 形态 (含 ":free" 等真实
	// 后缀, 跟上游 /v1/models 返回完全一致). 任何归一化都会引入数据错位.
	out := make([]string, 0, len(bareSet))
	for _, id := range upstreamIDs {
		if _, ok := bareSet[strings.ToLower(id)]; ok {
			out = append(out, id) // 原样保留
		}
	}
	return out
}

// fallbackModelsFromRegistry 仅当 fetchErr 表明上游 /v1/models 端点不存在
// (404) 时, 用 FreeModels registry 按 upstream URL host 反查 providerId,
// 返回该 provider 收录的所有 bare modelId. 任何其他错误 (鉴权/超时/解析失败)
// 或 registry 未知 host / 没有该 provider 的模型, 返回 nil 让调用方继续上报错误.
func (s *GroupService) fallbackModelsFromRegistry(group *models.Group, fetchErr error) []string {
	if s.freeModelsRegistry == nil || group == nil || fetchErr == nil {
		return nil
	}
	msg := fetchErr.Error()
	// upstream_models.go 的错误格式是 "upstream %d: %s",404 才适用 fallback.
	// 也兼容部分 provider 返回 "404 page not found" 但没正确设状态码的边角情况.
	if !strings.Contains(msg, "upstream 404") && !strings.Contains(strings.ToLower(msg), "404 page not found") {
		return nil
	}
	baseURL, err := firstUpstreamURL(group)
	if err != nil {
		return nil
	}
	host := extractHost(baseURL)
	if host == "" {
		return nil
	}
	providerID, _, ok := s.freeModelsRegistry.LookupProviderByHost(host)
	if !ok {
		return nil
	}
	ids := s.freeModelsRegistry.ListModelIDsByProvider(providerID)
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// pickActiveDecryptedKey 取该分组当前活跃 key 中的一个,用于调用上游 /v1/models.
func (s *GroupService) pickActiveDecryptedKey(ctx context.Context, groupID uint) (string, error) {
	var key models.APIKey
	if err := s.db.WithContext(ctx).
		Where("group_id = ? AND status = ?", groupID, models.KeyStatusActive).
		Order("id ASC").
		First(&key).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", NewI18nError(app_errors.ErrBadRequest, "group.refresh_models_no_key", nil)
		}
		return "", app_errors.ParseDBError(err)
	}
	if s.encryptionSvc == nil {
		return key.KeyValue, nil
	}
	plain, err := s.encryptionSvc.Decrypt(key.KeyValue)
	if err != nil {
		return "", fmt.Errorf("decrypt key: %w", err)
	}
	return plain, nil
}

// ReorderGroups updates sort values in a single transaction.
func (s *GroupService) ReorderGroups(ctx context.Context, items []GroupReorderItem) error {
	if len(items) == 0 {
		return NewI18nError(app_errors.ErrValidation, "validation.reorder_items_required", nil)
	}

	ids := make([]uint, 0, len(items))
	seenIDs := make(map[uint]struct{}, len(items))

	for _, item := range items {
		if item.ID == 0 {
			return NewI18nError(app_errors.ErrValidation, "validation.reorder_group_id", nil)
		}
		if item.Sort < 0 {
			return NewI18nError(app_errors.ErrValidation, "validation.reorder_sort_negative", nil)
		}
		if _, exists := seenIDs[item.ID]; exists {
			return NewI18nError(app_errors.ErrValidation, "validation.reorder_duplicate_group", map[string]any{"id": item.ID})
		}
		seenIDs[item.ID] = struct{}{}
		ids = append(ids, item.ID)
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return app_errors.ErrDatabase
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	lockedIDs := make([]uint, 0, len(ids))
	if err := tx.Model(&models.Group{}).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).
		Pluck("id", &lockedIDs).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	if len(lockedIDs) != len(ids) {
		return NewI18nError(app_errors.ErrValidation, "validation.reorder_group_not_found", nil)
	}

	for _, item := range items {
		result := tx.Model(&models.Group{}).Where("id = ?", item.ID).Update("sort", item.Sort)
		if result.Error != nil {
			return app_errors.ParseDBError(result.Error)
		}
		if result.RowsAffected != 1 {
			return NewI18nError(app_errors.ErrValidation, "validation.reorder_group_not_found", nil)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return app_errors.ErrDatabase
	}
	tx = nil

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	return nil
}

// UpdateGroup validates and updates an existing group.
func (s *GroupService) UpdateGroup(ctx context.Context, id uint, params GroupUpdateParams) (*models.Group, error) {
	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, app_errors.ErrDatabase
	}
	defer tx.Rollback()

	// 系统默认聚合分组禁止改名/换 channel_type
	if group.IsSystem {
		if params.Name != nil && strings.TrimSpace(*params.Name) != group.Name {
			return nil, NewI18nError(app_errors.ErrBadRequest, "group.system_immutable_field", map[string]any{"field": "name"})
		}
		if params.ChannelType != nil && strings.TrimSpace(*params.ChannelType) != group.ChannelType {
			return nil, NewI18nError(app_errors.ErrBadRequest, "group.system_immutable_field", map[string]any{"field": "channel_type"})
		}
	}

	if params.Name != nil {
		cleanedName := strings.TrimSpace(*params.Name)
		if !isValidGroupName(cleanedName) {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_group_name", nil)
		}
		group.Name = cleanedName
	}

	if params.DisplayName != nil {
		group.DisplayName = strings.TrimSpace(*params.DisplayName)
	}

	if params.Description != nil {
		group.Description = strings.TrimSpace(*params.Description)
	}

	if params.HasUpstreams {
		cleanedUpstreams, err := s.validateAndCleanUpstreams(params.Upstreams)
		if err != nil {
			return nil, err
		}
		group.Upstreams = cleanedUpstreams
	}

	// Check if this group is used as a sub-group in aggregate groups before allowing critical changes
	if group.GroupType != "aggregate" && (params.ChannelType != nil || params.ValidationEndpoint != nil) {
		count, err := s.aggregateGroupService.CountAggregateGroupsUsingSubGroup(ctx, group.ID)
		if err != nil {
			return nil, err
		}

		if count > 0 {
			// Check if ChannelType is being changed
			if params.ChannelType != nil {
				cleanedChannelType := strings.TrimSpace(*params.ChannelType)
				if group.ChannelType != cleanedChannelType {
					return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_referenced_cannot_modify",
						map[string]any{"count": count})
				}
			}

			// Check if ValidationEndpoint is being changed
			if params.ValidationEndpoint != nil {
				cleanedValidationEndpoint := strings.TrimSpace(*params.ValidationEndpoint)
				if group.ValidationEndpoint != cleanedValidationEndpoint {
					return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_referenced_cannot_modify",
						map[string]any{"count": count})
				}
			}
		}
	}

	if params.ChannelType != nil && group.GroupType != "aggregate" {
		cleanedChannelType := strings.TrimSpace(*params.ChannelType)
		if !s.isValidChannelType(cleanedChannelType) {
			supported := strings.Join(s.channelRegistry, ", ")
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_channel_type", map[string]any{"types": supported})
		}
		group.ChannelType = cleanedChannelType
	}

	if params.Sort != nil {
		group.Sort = *params.Sort
	}

	if params.HasTestModel {
		cleanedTestModel := strings.TrimSpace(params.TestModel)
		if cleanedTestModel == "" {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.test_model_empty", nil)
		}
		group.TestModel = cleanedTestModel
	}

	if params.ParamOverrides != nil {
		group.ParamOverrides = params.ParamOverrides
	}

	// Validate model redirect rules for aggregate groups
	if group.GroupType == "aggregate" && params.ModelRedirectRules != nil && len(params.ModelRedirectRules) > 0 {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.aggregate_no_model_redirect", nil)
	}

	// Validate model redirect rules format
	if params.ModelRedirectRules != nil {
		if err := validateModelRedirectRules(params.ModelRedirectRules); err != nil {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_model_redirect", map[string]any{"error": err.Error()})
		}
		group.ModelRedirectRules = convertToJSONMap(params.ModelRedirectRules)
	}

	if params.ModelRedirectStrict != nil {
		group.ModelRedirectStrict = *params.ModelRedirectStrict
	}

	if params.ModelRoutingMode != nil {
		mode := strings.TrimSpace(*params.ModelRoutingMode)
		if mode != "passthrough" && mode != "specified" {
			return nil, NewI18nError(app_errors.ErrValidation, "group.invalid_routing_mode", map[string]any{"mode": mode})
		}
		group.ModelRoutingMode = mode
	}

	if params.ExposedModels != nil {
		buf, err := json.Marshal(*params.ExposedModels)
		if err != nil {
			return nil, fmt.Errorf("marshal exposed_models: %w", err)
		}
		group.ExposedModels = buf
	}

	if params.BlockedModels != nil {
		buf, err := json.Marshal(*params.BlockedModels)
		if err != nil {
			return nil, fmt.Errorf("marshal blocked_models: %w", err)
		}
		group.BlockedModels = buf
	}

	if params.ValidationEndpoint != nil {
		validationEndpoint := strings.TrimSpace(*params.ValidationEndpoint)
		if !isValidValidationEndpoint(validationEndpoint) {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_test_path", nil)
		}
		group.ValidationEndpoint = validationEndpoint
	}

	if params.Config != nil {
		cleanedConfig, err := s.validateAndCleanConfig(params.Config)
		if err != nil {
			return nil, err
		}
		group.Config = cleanedConfig
	}

	if params.ProxyKeys != nil {
		group.ProxyKeys = strings.TrimSpace(*params.ProxyKeys)
	}

	if params.HeaderRules != nil {
		headerRulesJSON, err := s.normalizeHeaderRules(*params.HeaderRules)
		if err != nil {
			return nil, err
		}
		if headerRulesJSON == nil {
			headerRulesJSON = datatypes.JSON("[]")
		}
		group.HeaderRules = headerRulesJSON
	}

	if err := tx.Save(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, app_errors.ErrDatabase
	}

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	return &group, nil
}

// DeleteGroup removes a group and associated resources.
func (s *GroupService) DeleteGroup(ctx context.Context, id uint) error {
	var apiKeys []models.APIKey
	if err := s.db.WithContext(ctx).Where("group_id = ?", id).Find(&apiKeys).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	keyIDs := make([]uint, 0, len(apiKeys))
	for _, key := range apiKeys {
		keyIDs = append(keyIDs, key.ID)
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return app_errors.ErrDatabase
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	var group models.Group
	if err := tx.First(&group, id).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	if group.IsSystem {
		return NewI18nError(app_errors.ErrBadRequest, "group.system_cannot_delete", nil)
	}

	if err := tx.Where("group_id = ? OR sub_group_id = ?", id, id).Delete(&models.GroupSubGroup{}).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	if err := tx.Where("group_id = ?", id).Delete(&models.APIKey{}).Error; err != nil {
		return app_errors.ErrDatabase
	}

	if err := tx.Delete(&models.Group{}, id).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	if len(keyIDs) > 0 {
		if err := s.keyService.KeyProvider.RemoveKeysFromStore(id, keyIDs); err != nil {
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"groupID":  id,
				"keyCount": len(keyIDs),
			}).WithError(err).Error("failed to remove keys from memory store, rolling back transaction")
			return NewI18nError(app_errors.ErrDatabase, "error.delete_group_cache", nil)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return app_errors.ErrDatabase
	}
	tx = nil

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	return nil
}

// CopyGroup duplicates a group and optionally copies active keys.
func (s *GroupService) CopyGroup(ctx context.Context, sourceGroupID uint, copyKeysOption string) (*models.Group, error) {
	option := strings.TrimSpace(copyKeysOption)
	if option == "" {
		option = "all"
	}
	if option != "none" && option != "valid_only" && option != "all" {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_copy_keys_value", nil)
	}

	var sourceGroup models.Group
	if err := s.db.WithContext(ctx).First(&sourceGroup, sourceGroupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, app_errors.ErrDatabase
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	newGroup := sourceGroup
	newGroup.ID = 0
	newGroup.Name = s.generateUniqueGroupName(ctx, sourceGroup.Name)
	if sourceGroup.DisplayName != "" {
		newGroup.DisplayName = sourceGroup.DisplayName + " Copy"
	}
	newGroup.CreatedAt = time.Time{}
	newGroup.UpdatedAt = time.Time{}
	newGroup.LastValidatedAt = nil

	if err := tx.Create(&newGroup).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	var sourceKeyValues []string
	if option != "none" {
		var sourceKeys []models.APIKey
		query := tx.Where("group_id = ?", sourceGroupID)
		if option == "valid_only" {
			query = query.Where("status = ?", models.KeyStatusActive)
		}
		if err := query.Find(&sourceKeys).Error; err != nil {
			return nil, app_errors.ParseDBError(err)
		}

		for _, sourceKey := range sourceKeys {
			decryptedKey, err := s.encryptionSvc.Decrypt(sourceKey.KeyValue)
			if err != nil {
				logrus.WithContext(ctx).WithError(err).WithField("key_id", sourceKey.ID).Error("failed to decrypt key during group copy, skipping")
				continue
			}
			sourceKeyValues = append(sourceKeyValues, decryptedKey)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return nil, app_errors.ErrDatabase
	}
	tx = nil

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	if len(sourceKeyValues) > 0 {
		keysText := strings.Join(sourceKeyValues, "\n")
		if _, err := s.keyImportSvc.StartImportTask(&newGroup, keysText); err != nil {
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"groupId":  newGroup.ID,
				"keyCount": len(sourceKeyValues),
			}).WithError(err).Error("failed to start async key import task for group copy")
		} else {
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"groupId":  newGroup.ID,
				"keyCount": len(sourceKeyValues),
			}).Info("started async key import task for group copy")
		}
	}

	return &newGroup, nil
}

// GetGroupStats returns aggregated usage statistics for a group.
func (s *GroupService) GetGroupStats(ctx context.Context, groupID uint) (*GroupStats, error) {
	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// 根据分组类型选择不同的统计逻辑
	if group.GroupType == "aggregate" {
		return s.getAggregateGroupStats(ctx, groupID)
	}

	return s.getStandardGroupStats(ctx, groupID)
}

// queryGroupHourlyStats queries aggregated hourly statistics from group_hourly_stats table
func (s *GroupService) queryGroupHourlyStats(ctx context.Context, groupID uint, hours int) (RequestStats, error) {
	var result struct {
		SuccessCount int64
		FailureCount int64
	}

	now := time.Now()
	currentHour := now.Truncate(time.Hour)
	endTime := currentHour.Add(time.Hour) // Include current hour
	startTime := endTime.Add(-time.Duration(hours) * time.Hour)

	if err := s.db.WithContext(ctx).Model(&models.GroupHourlyStat{}).
		Select("SUM(success_count) as success_count, SUM(failure_count) as failure_count").
		Where("group_id = ? AND time >= ? AND time < ?", groupID, startTime, endTime).
		Scan(&result).Error; err != nil {
		return RequestStats{}, err
	}

	return calculateRequestStats(result.SuccessCount+result.FailureCount, result.FailureCount), nil
}

// fetchKeyStats retrieves API key statistics for a group
func (s *GroupService) fetchKeyStats(ctx context.Context, groupID uint) (KeyStats, error) {
	var totalKeys, activeKeys int64

	if err := s.db.WithContext(ctx).Model(&models.APIKey{}).
		Where("group_id = ?", groupID).
		Count(&totalKeys).Error; err != nil {
		return KeyStats{}, fmt.Errorf("failed to get total keys: %w", err)
	}

	if err := s.db.WithContext(ctx).Model(&models.APIKey{}).
		Where("group_id = ? AND status = ?", groupID, models.KeyStatusActive).
		Count(&activeKeys).Error; err != nil {
		return KeyStats{}, fmt.Errorf("failed to get active keys: %w", err)
	}

	return KeyStats{
		TotalKeys:   totalKeys,
		ActiveKeys:  activeKeys,
		InvalidKeys: totalKeys - activeKeys,
	}, nil
}

// fetchRequestStats retrieves request statistics for multiple time periods
func (s *GroupService) fetchRequestStats(ctx context.Context, groupID uint, stats *GroupStats) []error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	// Define time periods and their corresponding setters
	timePeriods := []struct {
		hours  int
		name   string
		setter func(RequestStats)
	}{
		{24, "24-hour", func(r RequestStats) { stats.Stats24Hour = r }},
		{7 * 24, "7-day", func(r RequestStats) { stats.Stats7Day = r }},
		{30 * 24, "30-day", func(r RequestStats) { stats.Stats30Day = r }},
	}

	// Fetch statistics for each time period concurrently
	for _, period := range timePeriods {
		wg.Add(1)
		go func(hours int, name string, setter func(RequestStats)) {
			defer wg.Done()

			res, err := s.queryGroupHourlyStats(ctx, groupID, hours)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("failed to get %s stats: %w", name, err))
				mu.Unlock()
				return
			}

			mu.Lock()
			setter(res)
			mu.Unlock()
		}(period.hours, period.name, period.setter)
	}

	wg.Wait()
	return errs
}

func (s *GroupService) getStandardGroupStats(ctx context.Context, groupID uint) (*GroupStats, error) {
	stats := &GroupStats{}
	var allErrors []error

	// Fetch key statistics (only for standard groups)
	keyStats, err := s.fetchKeyStats(ctx, groupID)
	if err != nil {
		allErrors = append(allErrors, err)
		// Log error but continue to fetch request stats
		logrus.WithContext(ctx).WithError(err).Warn("failed to fetch key stats, continuing with request stats")
	} else {
		stats.KeyStats = keyStats
	}

	// Fetch request statistics (common for all groups)
	if errs := s.fetchRequestStats(ctx, groupID, stats); len(errs) > 0 {
		allErrors = append(allErrors, errs...)
	}

	// Handle errors
	if len(allErrors) > 0 {
		logrus.WithContext(ctx).WithError(allErrors[0]).Error("errors occurred while fetching group stats")
		// Return partial stats if we have some data
		if stats.Stats24Hour.TotalRequests > 0 || stats.Stats7Day.TotalRequests > 0 || stats.Stats30Day.TotalRequests > 0 {
			return stats, nil
		}
		return nil, NewI18nError(app_errors.ErrDatabase, "database.group_stats_failed", nil)
	}

	return stats, nil
}

func (s *GroupService) getAggregateGroupStats(ctx context.Context, groupID uint) (*GroupStats, error) {
	stats := &GroupStats{}

	// Aggregate groups only need request statistics, not key statistics
	if errs := s.fetchRequestStats(ctx, groupID, stats); len(errs) > 0 {
		logrus.WithContext(ctx).WithError(errs[0]).Error("errors occurred while fetching aggregate group stats")
		// Return partial stats if we have some data
		if stats.Stats24Hour.TotalRequests > 0 || stats.Stats7Day.TotalRequests > 0 || stats.Stats30Day.TotalRequests > 0 {
			return stats, nil
		}
		return nil, NewI18nError(app_errors.ErrDatabase, "database.group_stats_failed", nil)
	}

	return stats, nil
}

// GetGroupConfigOptions returns metadata describing available overrides.
func (s *GroupService) GetGroupConfigOptions() ([]ConfigOption, error) {
	defaultSettings := utils.DefaultSystemSettings()
	settingDefinitions := utils.GenerateSettingsMetadata(&defaultSettings)
	defMap := make(map[string]models.SystemSettingInfo)
	for _, def := range settingDefinitions {
		defMap[def.Key] = def
	}

	currentSettings := s.settingsManager.GetSettings()
	currentSettingsValue := reflect.ValueOf(currentSettings)
	currentSettingsType := currentSettingsValue.Type()
	jsonToFieldMap := make(map[string]string)
	for i := 0; i < currentSettingsType.NumField(); i++ {
		field := currentSettingsType.Field(i)
		jsonTag := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonTag != "" {
			jsonToFieldMap[jsonTag] = field.Name
		}
	}

	groupConfigType := reflect.TypeOf(models.GroupConfig{})
	var options []ConfigOption
	for i := 0; i < groupConfigType.NumField(); i++ {
		field := groupConfigType.Field(i)
		jsonTag := field.Tag.Get("json")
		key := strings.Split(jsonTag, ",")[0]
		if key == "" || key == "-" {
			continue
		}

		definition, ok := defMap[key]
		if !ok {
			continue
		}

		var defaultValue any
		if fieldName, ok := jsonToFieldMap[key]; ok {
			defaultValue = currentSettingsValue.FieldByName(fieldName).Interface()
		}

		options = append(options, ConfigOption{
			Key:          key,
			Name:         definition.Name,
			Description:  definition.Description,
			DefaultValue: defaultValue,
		})
	}

	return options, nil
}

// validateAndCleanConfig verifies GroupConfig overrides.
func (s *GroupService) validateAndCleanConfig(configMap map[string]any) (map[string]any, error) {
	if configMap == nil {
		return nil, nil
	}

	var tempGroupConfig models.GroupConfig
	groupConfigType := reflect.TypeOf(tempGroupConfig)
	validFields := make(map[string]bool)
	for i := 0; i < groupConfigType.NumField(); i++ {
		jsonTag := groupConfigType.Field(i).Tag.Get("json")
		fieldName := strings.Split(jsonTag, ",")[0]
		if fieldName != "" && fieldName != "-" {
			validFields[fieldName] = true
		}
	}

	for key := range configMap {
		if !validFields[key] {
			message := fmt.Sprintf("unknown config field: '%s'", key)
			return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": message})
		}
	}

	if err := s.settingsManager.ValidateGroupConfigOverrides(configMap); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	configBytes, err := json.Marshal(configMap)
	if err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	var validatedConfig models.GroupConfig
	if err := json.Unmarshal(configBytes, &validatedConfig); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	validatedBytes, err := json.Marshal(validatedConfig)
	if err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	var finalMap map[string]any
	if err := json.Unmarshal(validatedBytes, &finalMap); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	return finalMap, nil
}

// normalizeHeaderRules deduplicates and normalises header rules.
func (s *GroupService) normalizeHeaderRules(rules []models.HeaderRule) (datatypes.JSON, error) {
	if len(rules) == 0 {
		return nil, nil
	}

	normalized := make([]models.HeaderRule, 0, len(rules))
	seenKeys := make(map[string]bool)

	for _, rule := range rules {
		key := strings.TrimSpace(rule.Key)
		if key == "" {
			continue
		}
		canonicalKey := http.CanonicalHeaderKey(key)
		if seenKeys[canonicalKey] {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.duplicate_header", map[string]any{"key": canonicalKey})
		}
		seenKeys[canonicalKey] = true
		normalized = append(normalized, models.HeaderRule{Key: canonicalKey, Value: rule.Value, Action: rule.Action})
	}

	if len(normalized) == 0 {
		return nil, nil
	}

	headerRulesBytes, err := json.Marshal(normalized)
	if err != nil {
		return nil, NewI18nError(app_errors.ErrInternalServer, "error.process_header_rules", map[string]any{"error": err.Error()})
	}

	return datatypes.JSON(headerRulesBytes), nil
}

// validateAndCleanUpstreams validates upstream definitions.
func (s *GroupService) validateAndCleanUpstreams(upstreams json.RawMessage) (datatypes.JSON, error) {
	if len(upstreams) == 0 {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "upstreams field is required"})
	}

	var defs []struct {
		URL    string `json:"url"`
		Weight int    `json:"weight"`
	}
	if err := json.Unmarshal(upstreams, &defs); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": err.Error()})
	}

	if len(defs) == 0 {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "at least one upstream is required"})
	}

	hasActiveUpstream := false
	for i := range defs {
		defs[i].URL = strings.TrimSpace(defs[i].URL)
		if defs[i].URL == "" {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "upstream URL cannot be empty"})
		}
		if !strings.HasPrefix(defs[i].URL, "http://") && !strings.HasPrefix(defs[i].URL, "https://") {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": fmt.Sprintf("invalid URL format for upstream: %s", defs[i].URL)})
		}
		if defs[i].Weight < 0 {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "upstream weight must be a non-negative integer"})
		}
		if defs[i].Weight > 0 {
			hasActiveUpstream = true
		}
	}

	if !hasActiveUpstream {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "at least one upstream must have a weight greater than 0"})
	}

	cleanedUpstreams, err := json.Marshal(defs)
	if err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": err.Error()})
	}

	return datatypes.JSON(cleanedUpstreams), nil
}

func calculateRequestStats(total, failed int64) RequestStats {
	stats := RequestStats{
		TotalRequests:  total,
		FailedRequests: failed,
	}
	if total > 0 {
		rate := float64(failed) / float64(total)
		stats.FailureRate = math.Round(rate*10000) / 10000
	}
	return stats
}

func (s *GroupService) generateUniqueGroupName(ctx context.Context, baseName string) string {
	var groups []models.Group
	if err := s.db.WithContext(ctx).Select("name").Find(&groups).Error; err != nil {
		return baseName + "_copy"
	}

	existingNames := make(map[string]bool, len(groups))
	for _, group := range groups {
		existingNames[group.Name] = true
	}

	copyName := baseName + "_copy"
	if !existingNames[copyName] {
		return copyName
	}

	for i := 2; i <= 1000; i++ {
		candidate := fmt.Sprintf("%s_copy_%d", baseName, i)
		if !existingNames[candidate] {
			return candidate
		}
	}

	return copyName
}

// isValidGroupName validates the group name.
func isValidGroupName(name string) bool {
	if name == "" {
		return false
	}
	match, _ := regexp.MatchString("^[a-z0-9_-]{1,100}$", name)
	return match
}

// isValidValidationEndpoint validates custom validation endpoint path.
func isValidValidationEndpoint(endpoint string) bool {
	if endpoint == "" {
		return true
	}
	if !strings.HasPrefix(endpoint, "/") {
		return false
	}
	if strings.Contains(endpoint, "://") {
		return false
	}
	return true
}

// isValidChannelType checks channel type against registered channels.
func (s *GroupService) isValidChannelType(channelType string) bool {
	for _, t := range s.channelRegistry {
		if t == channelType {
			return true
		}
	}
	return false
}

// convertToJSONMap converts a map[string]string to datatypes.JSONMap
func convertToJSONMap(input map[string]string) datatypes.JSONMap {
	if len(input) == 0 {
		return datatypes.JSONMap{}
	}

	result := make(datatypes.JSONMap)
	for k, v := range input {
		result[k] = v
	}
	return result
}

// validateModelRedirectRules validates the format and content of model redirect rules
func validateModelRedirectRules(rules map[string]string) error {
	if len(rules) == 0 {
		return nil
	}

	for key, value := range rules {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("model name cannot be empty")
		}
	}

	return nil
}
