// Package handler provides HTTP handlers for the application
package handler

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/i18n"
	"autogateway/internal/models"
	"autogateway/internal/response"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
)

func (s *Server) handleGroupError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}

	if svcErr, ok := err.(*services.I18nError); ok {
		if svcErr == nil {
			return false
		}
		if svcErr.Template != nil {
			response.ErrorI18nFromAPIError(c, svcErr.APIError, svcErr.MessageID, svcErr.Template)
		} else {
			response.ErrorI18nFromAPIError(c, svcErr.APIError, svcErr.MessageID)
		}
		return true
	}

	if apiErr, ok := err.(*app_errors.APIError); ok {
		response.Error(c, apiErr)
		return true
	}

	logrus.WithContext(c.Request.Context()).WithError(err).Error("unexpected group service error")
	response.Error(c, app_errors.ErrInternalServer)
	return true
}

// GroupCreateRequest defines the payload for creating a group.
type GroupCreateRequest struct {
	Name                string              `json:"name"`
	DisplayName         string              `json:"display_name"`
	Description         string              `json:"description"`
	GroupType           string              `json:"group_type"` // 'standard' or 'aggregate'
	Upstreams           json.RawMessage     `json:"upstreams"`
	ChannelType         string              `json:"channel_type"`
	Sort                int                 `json:"sort"`
	TestModel           string              `json:"test_model"`
	ValidationEndpoint  string              `json:"validation_endpoint"`
	ParamOverrides      map[string]any      `json:"param_overrides"`
	ModelRedirectRules  map[string]string   `json:"model_redirect_rules"`
	ModelRedirectStrict bool                `json:"model_redirect_strict"`
	Config              map[string]any      `json:"config"`
	HeaderRules         []models.HeaderRule `json:"header_rules"`
	ProxyKeys           string              `json:"proxy_keys"`
	ModelRoutingMode    string              `json:"model_routing_mode"`
	ExposedModels       []string            `json:"exposed_models"`
	BlockedModels       []string            `json:"blocked_models"`
	// AvailableModels 给那些上游没有 /v1/models 端点的 provider 用
	// (e.g. LongCat) — 建组时直接把静态清单灌入,避免后续 refresh 拉空。
	AvailableModels []string `json:"available_models"`
}

// CreateGroup handles the creation of a new group.
func (s *Server) CreateGroup(c *gin.Context) {
	var req GroupCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	params := services.GroupCreateParams{
		Name:                req.Name,
		DisplayName:         req.DisplayName,
		Description:         req.Description,
		GroupType:           req.GroupType,
		Upstreams:           req.Upstreams,
		ChannelType:         req.ChannelType,
		Sort:                req.Sort,
		TestModel:           req.TestModel,
		ValidationEndpoint:  req.ValidationEndpoint,
		ParamOverrides:      req.ParamOverrides,
		ModelRedirectRules:  req.ModelRedirectRules,
		ModelRedirectStrict: req.ModelRedirectStrict,
		Config:              req.Config,
		HeaderRules:         req.HeaderRules,
		ProxyKeys:           req.ProxyKeys,
		ModelRoutingMode:    req.ModelRoutingMode,
		ExposedModels:       req.ExposedModels,
		BlockedModels:       req.BlockedModels,
		AvailableModels:     req.AvailableModels,
	}

	group, err := s.GroupService.CreateGroup(c.Request.Context(), params)
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, s.newGroupResponse(group))
}

// TestGroupModelRequest 是 POST /api/groups/:id/test-model 的载荷.
// model 必须与 group 的 available_models 中暴露的某个具体型号匹配.
type TestGroupModelRequest struct {
	Model string `json:"model" binding:"required"`
}

// TestGroupModel 用一个活跃 key 对指定 model 发起一次最小载荷的探活, 验证
// 该模型在该分组下是否能 200 通过. aggregate 分组会先按 model 选一个能命中的
// sub-group, 再用 sub-group 自己的 channel/key 跑测试. 测试不影响所选 key 的
// active/invalid 状态 — 模型不存在和 key 失效语义不同.
func (s *Server) TestGroupModel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	var req TestGroupModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		response.ErrorI18nFromAPIError(c, app_errors.ErrValidation, "validation.test_model_required")
		return
	}

	groupDB, ok := s.findGroupByID(c, uint(id))
	if !ok {
		return
	}

	group, err := s.GroupManager.GetGroupByName(groupDB.Name)
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrResourceNotFound, "validation.group_not_found")
		return
	}

	targetGroup := group
	if group.GroupType == "aggregate" {
		subName, selErr := s.SubGroupManager.SelectSubGroupForModel(group, modelName)
		if selErr != nil {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, selErr.Error()))
			return
		}
		if subName == "" {
			response.ErrorI18nFromAPIError(c, app_errors.ErrValidation, "validation.no_sub_group_for_model")
			return
		}
		resolved, resolveErr := s.GroupManager.GetGroupByName(subName)
		if resolveErr != nil {
			response.ErrorI18nFromAPIError(c, app_errors.ErrResourceNotFound, "validation.group_not_found")
			return
		}
		targetGroup = resolved
	}

	result, err := s.KeyService.TestModelConnectivity(targetGroup, modelName)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, err.Error()))
		return
	}

	response.Success(c, gin.H{
		"is_valid":         result.IsValid,
		"status_code":      result.StatusCode,
		"url":              result.URL,
		"error":            result.Error,
		"duration_ms":      result.DurationMs,
		"model":            result.Model,
		"resolved_group":   targetGroup.Name,
		"is_via_aggregate": group.GroupType == "aggregate",
	})
}

// RefreshGroupModels triggers an upstream /v1/models fetch and caches the result.
// GroupHealth P6: 返回 aggregate group 的 sub-group 运行时健康状态
// (latency EWMA, effective weight, 熔断器, cooldown). standard group 返回 null.
func (s *Server) GroupHealth(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}
	name, ok := s.GroupManager.GetGroupNameByID(uint(id))
	if !ok {
		response.Error(c, app_errors.ErrResourceNotFound)
		return
	}
	group, gerr := s.GroupManager.GetGroupByName(name)
	if gerr != nil || group == nil {
		response.Error(c, app_errors.ParseDBError(gerr))
		return
	}
	if group.GroupType != "aggregate" {
		response.Success(c, gin.H{"group_type": group.GroupType, "sub_groups": nil})
		return
	}
	snapshot := s.SubGroupManager.Snapshot(uint(id))
	response.Success(c, gin.H{
		"group_type": "aggregate",
		"sub_groups": snapshot,
	})
}

// ProbeModelsRequest is the payload for POST /api/groups/probe-models.
type ProbeModelsRequest struct {
	BaseURL     string `json:"base_url" binding:"required"`
	ChannelType string `json:"channel_type" binding:"required"`
	Key         string `json:"key"`
}

// ProbeModels 用临时 base_url + key 拉上游真实模型清单, 不依赖已建分组.
// 用于 CreateGroup 流程中给 test_model 下拉提供候选列表.
// 成功返回 {"models": [...]}, 失败返回 400 (参数错误) 或 502 (上游错误).
func (s *Server) ProbeModels(c *gin.Context) {
	var req ProbeModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	models, err := services.ProbeUpstreamModels(c.Request.Context(), req.BaseURL, req.ChannelType, req.Key)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL = "" // 清掉可能含 ?key= 的 URL,避免 key 泄漏到响应 body
		}
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadGateway, "failed to fetch upstream models: "+err.Error()))
		return
	}

	response.Success(c, gin.H{
		"models": models,
		"count":  len(models),
	})
}

func (s *Server) RefreshGroupModels(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}
	models, err := s.GroupService.RefreshAvailableModels(c.Request.Context(), uint(id))
	if s.handleGroupError(c, err) {
		return
	}
	// 解析准确的 FreeModels provider id (上游 host 反查), 避免裸 modelId
	// 跨 provider 误匹配 (e.g. openrouter 上的付费 model 被某个其他 provider
	// 同名 model 借成 free).
	providerHint := ""
	if name, ok := s.GroupManager.GetGroupNameByID(uint(id)); ok {
		if group, gerr := s.GroupManager.GetGroupByName(name); gerr == nil && group != nil {
			providerHint = resolveFreeProviderHint(s.FreeModelsRegistry, group)
		}
	}
	enriched := s.enrichModels(models, providerHint)
	response.Success(c, gin.H{
		"models": enriched,
		"count":  len(enriched),
	})
}

// ListGroups handles listing all groups.
func (s *Server) ListGroups(c *gin.Context) {
	groups, err := s.GroupService.ListGroups(c.Request.Context())
	if s.handleGroupError(c, err) {
		return
	}

	groupResponses := make([]GroupResponse, 0, len(groups))
	for i := range groups {
		groupResponses = append(groupResponses, *s.newGroupResponse(&groups[i]))
	}

	response.Success(c, groupResponses)
}

// GroupUpdateRequest defines the payload for updating a group.
// Using a dedicated struct avoids issues with zero values being ignored by GORM's Update.
type GroupUpdateRequest struct {
	Name                *string             `json:"name,omitempty"`
	DisplayName         *string             `json:"display_name,omitempty"`
	Description         *string             `json:"description,omitempty"`
	GroupType           *string             `json:"group_type,omitempty"`
	Upstreams           json.RawMessage     `json:"upstreams"`
	ChannelType         *string             `json:"channel_type,omitempty"`
	Sort                *int                `json:"sort"`
	TestModel           string              `json:"test_model"`
	ValidationEndpoint  *string             `json:"validation_endpoint,omitempty"`
	ParamOverrides      map[string]any      `json:"param_overrides"`
	ModelRedirectRules  map[string]string   `json:"model_redirect_rules"`
	ModelRedirectStrict *bool               `json:"model_redirect_strict"`
	Config              map[string]any      `json:"config"`
	HeaderRules         []models.HeaderRule `json:"header_rules"`
	ProxyKeys           *string             `json:"proxy_keys,omitempty"`
	ModelRoutingMode    *string             `json:"model_routing_mode,omitempty"`
	ExposedModels       *[]string           `json:"exposed_models,omitempty"`
	BlockedModels       *[]string           `json:"blocked_models,omitempty"`
}

type GroupReorderItemRequest struct {
	ID   uint `json:"id"`
	Sort int  `json:"sort"`
}

type GroupReorderRequest struct {
	Items []GroupReorderItemRequest `json:"items"`
}

func validateGroupReorderItems(items []GroupReorderItemRequest) error {
	if len(items) == 0 {
		return services.NewI18nError(app_errors.ErrValidation, "validation.reorder_items_required", nil)
	}

	seen := make(map[uint]struct{}, len(items))
	for _, item := range items {
		if item.ID == 0 {
			return services.NewI18nError(app_errors.ErrValidation, "validation.reorder_group_id", nil)
		}
		if item.Sort < 0 {
			return services.NewI18nError(app_errors.ErrValidation, "validation.reorder_sort_negative", nil)
		}
		if _, exists := seen[item.ID]; exists {
			return services.NewI18nError(app_errors.ErrValidation, "validation.reorder_duplicate_group", map[string]any{"id": item.ID})
		}
		seen[item.ID] = struct{}{}
	}

	return nil
}

// UpdateGroup handles updating an existing group.
func (s *Server) UpdateGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	var req GroupUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	params := services.GroupUpdateParams{
		Name:                req.Name,
		DisplayName:         req.DisplayName,
		Description:         req.Description,
		GroupType:           req.GroupType,
		ChannelType:         req.ChannelType,
		Sort:                req.Sort,
		ValidationEndpoint:  req.ValidationEndpoint,
		ParamOverrides:      req.ParamOverrides,
		ModelRedirectRules:  req.ModelRedirectRules,
		ModelRedirectStrict: req.ModelRedirectStrict,
		Config:              req.Config,
		ProxyKeys:           req.ProxyKeys,
		ModelRoutingMode:    req.ModelRoutingMode,
		ExposedModels:       req.ExposedModels,
		BlockedModels:       req.BlockedModels,
	}

	if req.Upstreams != nil {
		params.Upstreams = req.Upstreams
		params.HasUpstreams = true
	}

	if req.TestModel != "" {
		params.TestModel = req.TestModel
		params.HasTestModel = true
	}

	if req.HeaderRules != nil {
		rules := req.HeaderRules
		params.HeaderRules = &rules
	}

	group, err := s.GroupService.UpdateGroup(c.Request.Context(), uint(id), params)
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, s.newGroupResponse(group))
}

// ReorderGroups handles batch reorder updates for groups.
func (s *Server) ReorderGroups(c *gin.Context) {
	var req GroupReorderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if err := validateGroupReorderItems(req.Items); s.handleGroupError(c, err) {
		return
	}

	items := make([]services.GroupReorderItem, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, services.GroupReorderItem{
			ID:   item.ID,
			Sort: item.Sort,
		})
	}

	if s.handleGroupError(c, s.GroupService.ReorderGroups(c.Request.Context(), items)) {
		return
	}

	response.SuccessI18n(c, "success.groups_reordered", nil)
}

// GroupResponse defines the structure for a group response, excluding sensitive or large fields.
type GroupResponse struct {
	ID                  uint                `json:"id"`
	Name                string              `json:"name"`
	Endpoint            string              `json:"endpoint"`
	DisplayName         string              `json:"display_name"`
	Description         string              `json:"description"`
	GroupType           string              `json:"group_type"`
	Upstreams           datatypes.JSON      `json:"upstreams"`
	ChannelType         string              `json:"channel_type"`
	Sort                int                 `json:"sort"`
	TestModel           string              `json:"test_model"`
	ValidationEndpoint  string              `json:"validation_endpoint"`
	ParamOverrides      datatypes.JSONMap   `json:"param_overrides"`
	ModelRedirectRules  datatypes.JSONMap   `json:"model_redirect_rules"`
	ModelRedirectStrict bool                `json:"model_redirect_strict"`
	Config              datatypes.JSONMap   `json:"config"`
	HeaderRules         []models.HeaderRule `json:"header_rules"`
	ProxyKeys           string              `json:"proxy_keys"`
	LastValidatedAt     *time.Time          `json:"last_validated_at"`
	IsSystem            bool                `json:"is_system"`
	SystemRole          string              `json:"system_role"`
	AvailableModels     datatypes.JSON      `json:"available_models,omitempty"`
	ModelsRefreshedAt   *time.Time          `json:"models_refreshed_at,omitempty"`
	ModelRoutingMode    string              `json:"model_routing_mode"`
	ExposedModels       datatypes.JSON      `json:"exposed_models,omitempty"`
	BlockedModels       datatypes.JSON      `json:"blocked_models,omitempty"`
	KeyCount            int64               `json:"key_count"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

// newGroupResponse creates a new GroupResponse from a models.Group.
func (s *Server) newGroupResponse(group *models.Group) *GroupResponse {
	appURL := s.SettingsManager.GetAppUrl()
	endpoint := ""
	if appURL != "" {
		u, err := url.Parse(appURL)
		if err == nil {
			// 系统默认聚合分组使用无 /proxy 前缀的快捷路径,与 router 中注册的捷径一致.
			if group.IsSystem {
				switch group.SystemRole {
				case models.SystemRoleDefaultOpenAI:
					u.Path = strings.TrimRight(u.Path, "/") + "/openai"
				case models.SystemRoleDefaultGemini:
					u.Path = strings.TrimRight(u.Path, "/") + "/gemini"
				case models.SystemRoleDefaultAnthropic:
					u.Path = strings.TrimRight(u.Path, "/") + "/anthropic"
				default:
					u.Path = strings.TrimRight(u.Path, "/") + "/proxy/" + group.Name
				}
			} else {
				u.Path = strings.TrimRight(u.Path, "/") + "/proxy/" + group.Name
			}
			endpoint = u.String()
		}
	}

	// Parse header rules from JSON
	var headerRules []models.HeaderRule
	if len(group.HeaderRules) > 0 {
		if err := json.Unmarshal(group.HeaderRules, &headerRules); err != nil {
			logrus.WithError(err).Error("Failed to unmarshal header rules")
			headerRules = make([]models.HeaderRule, 0)
		}
	}

	return &GroupResponse{
		ID:                  group.ID,
		Name:                group.Name,
		Endpoint:            endpoint,
		DisplayName:         group.DisplayName,
		Description:         group.Description,
		GroupType:           group.GroupType,
		Upstreams:           group.Upstreams,
		ChannelType:         group.ChannelType,
		Sort:                group.Sort,
		TestModel:           group.TestModel,
		ValidationEndpoint:  group.ValidationEndpoint,
		ParamOverrides:      group.ParamOverrides,
		ModelRedirectRules:  group.ModelRedirectRules,
		ModelRedirectStrict: group.ModelRedirectStrict,
		Config:              group.Config,
		HeaderRules:         headerRules,
		ProxyKeys:           group.ProxyKeys,
		LastValidatedAt:     group.LastValidatedAt,
		IsSystem:            group.IsSystem,
		SystemRole:          group.SystemRole,
		AvailableModels:     group.AvailableModels,
		ModelsRefreshedAt:   group.ModelsRefreshedAt,
		ModelRoutingMode:    group.ModelRoutingMode,
		ExposedModels:       group.ExposedModels,
		BlockedModels:       group.BlockedModels,
		KeyCount:            group.KeyCount,
		CreatedAt:           group.CreatedAt,
		UpdatedAt:           group.UpdatedAt,
	}
}

// DeleteGroup handles deleting a group.
func (s *Server) DeleteGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	if s.handleGroupError(c, s.GroupService.DeleteGroup(c.Request.Context(), uint(id))) {
		return
	}
	response.SuccessI18n(c, "success.group_deleted", nil)
}

// ConfigOption represents a single configurable option for a group.
type ConfigOption struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	DefaultValue any    `json:"default_value"`
}

// GetGroupConfigOptions returns a list of available configuration options for groups.
func (s *Server) GetGroupConfigOptions(c *gin.Context) {
	options, err := s.GroupService.GetGroupConfigOptions()
	if s.handleGroupError(c, err) {
		return
	}

	translated := make([]ConfigOption, 0, len(options))
	for _, option := range options {
		name := option.Name
		if strings.HasPrefix(name, "config.") {
			name = i18n.Message(c, name)
		}
		description := option.Description
		if strings.HasPrefix(description, "config.") {
			description = i18n.Message(c, description)
		}

		translated = append(translated, ConfigOption{
			Key:          option.Key,
			Name:         name,
			Description:  description,
			DefaultValue: option.DefaultValue,
		})
	}

	response.Success(c, translated)
}

// calculateRequestStats is a helper to compute request statistics.
func (s *Server) GetGroupStats(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	stats, err := s.GroupService.GetGroupStats(c.Request.Context(), uint(id))
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, stats)
}

// GroupCopyRequest defines the payload for copying a group.
type GroupCopyRequest struct {
	CopyKeys string `json:"copy_keys"` // "none"|"valid_only"|"all"
}

// GroupCopyResponse defines the response for group copy operation.
type GroupCopyResponse struct {
	Group *GroupResponse `json:"group"`
}

// CopyGroup handles copying a group with optional content.

func (s *Server) CopyGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	var req GroupCopyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	newGroup, err := s.GroupService.CopyGroup(c.Request.Context(), uint(id), req.CopyKeys)
	if s.handleGroupError(c, err) {
		return
	}

	groupResponse := s.newGroupResponse(newGroup)
	copyResponse := &GroupCopyResponse{
		Group: groupResponse,
	}

	response.Success(c, copyResponse)
}

// List godoc
// Returns the lightweight group catalog used by dropdowns and the rail
// badge: id / name / display_name / channel_type / group_type / is_system
// + available_models so the frontend can drive filters without paying for
// preloading api_keys.
func (s *Server) List(c *gin.Context) {
	var groups []models.Group
	if err := s.DB.
		Select("id, name, display_name, channel_type, group_type, is_system, available_models").
		Order("sort asc, id desc").
		Find(&groups).Error; err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_groups")
		return
	}
	response.Success(c, groups)
}

// AddSubGroupsRequest defines the payload for adding sub groups to an aggregate group
type AddSubGroupsRequest struct {
	SubGroups []services.SubGroupInput `json:"sub_groups"`
}

// UpdateSubGroupWeightRequest defines the payload for updating a sub group weight
type UpdateSubGroupWeightRequest struct {
	Weight int `json:"weight"`
}

// GetSubGroups handles getting sub groups of an aggregate group
func (s *Server) GetSubGroups(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	subGroups, err := s.AggregateGroupService.GetSubGroups(c.Request.Context(), uint(id))
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, subGroups)
}

// AddSubGroups handles adding sub groups to an aggregate group
func (s *Server) AddSubGroups(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	var req AddSubGroupsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if err := s.AggregateGroupService.AddSubGroups(c.Request.Context(), uint(id), req.SubGroups); s.handleGroupError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.sub_groups_added", nil)
}

// UpdateSubGroupWeight handles updating the weight of a sub group
func (s *Server) UpdateSubGroupWeight(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	subGroupID, err := strconv.Atoi(c.Param("subGroupId"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_sub_group_id")
		return
	}

	var req UpdateSubGroupWeightRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if err := s.AggregateGroupService.UpdateSubGroupWeight(c.Request.Context(), uint(id), uint(subGroupID), req.Weight); s.handleGroupError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.sub_group_weight_updated", nil)
}

// DeleteSubGroup handles deleting a sub group from an aggregate group
func (s *Server) DeleteSubGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	subGroupID, err := strconv.Atoi(c.Param("subGroupId"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_sub_group_id")
		return
	}

	if err := s.AggregateGroupService.DeleteSubGroup(c.Request.Context(), uint(id), uint(subGroupID)); s.handleGroupError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.sub_group_deleted", nil)
}

// GetParentAggregateGroups handles getting parent aggregate groups that reference a group
func (s *Server) GetParentAggregateGroups(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	parentGroups, err := s.AggregateGroupService.GetParentAggregateGroups(c.Request.Context(), uint(id))
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, parentGroups)
}
