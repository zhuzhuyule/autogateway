// Package handler provides HTTP handlers for the application
package handler

import (
	"crypto/subtle"
	"net/http"
	"time"

	"autogateway/internal/config"
	"autogateway/internal/encryption"
	"autogateway/internal/i18n"
	"autogateway/internal/services"
	"autogateway/internal/types"

	"github.com/gin-gonic/gin"
	"go.uber.org/dig"
	"gorm.io/gorm"
)

// Server contains dependencies for HTTP handlers
type Server struct {
	DB                         *gorm.DB
	config                     types.ConfigManager
	SettingsManager            *config.SystemSettingsManager
	GroupManager               *services.GroupManager
	GroupService               *services.GroupService
	AggregateGroupService      *services.AggregateGroupService
	KeyManualValidationService *services.KeyManualValidationService
	TaskService                *services.TaskService
	KeyService                 *services.KeyService
	KeyImportService           *services.KeyImportService
	KeyDeleteService           *services.KeyDeleteService
	LogService                 *services.LogService
	FreeModelsRegistry         *services.FreeModelsRegistry
	CommonHandler              *CommonHandler
	EncryptionSvc              encryption.Service
}

// NewServerParams defines the dependencies for the NewServer constructor.
type NewServerParams struct {
	dig.In
	DB                         *gorm.DB
	Config                     types.ConfigManager
	SettingsManager            *config.SystemSettingsManager
	GroupManager               *services.GroupManager
	GroupService               *services.GroupService
	AggregateGroupService      *services.AggregateGroupService
	KeyManualValidationService *services.KeyManualValidationService
	TaskService                *services.TaskService
	KeyService                 *services.KeyService
	KeyImportService           *services.KeyImportService
	KeyDeleteService           *services.KeyDeleteService
	LogService                 *services.LogService
	FreeModelsRegistry         *services.FreeModelsRegistry
	CommonHandler              *CommonHandler
	EncryptionSvc              encryption.Service
}

// NewServer creates a new handler instance with dependencies injected by dig.
func NewServer(params NewServerParams) *Server {
	return &Server{
		DB:                         params.DB,
		config:                     params.Config,
		SettingsManager:            params.SettingsManager,
		GroupManager:               params.GroupManager,
		GroupService:               params.GroupService,
		AggregateGroupService:      params.AggregateGroupService,
		KeyManualValidationService: params.KeyManualValidationService,
		TaskService:                params.TaskService,
		KeyService:                 params.KeyService,
		KeyImportService:           params.KeyImportService,
		KeyDeleteService:           params.KeyDeleteService,
		LogService:                 params.LogService,
		FreeModelsRegistry:         params.FreeModelsRegistry,
		CommonHandler:              params.CommonHandler,
		EncryptionSvc:              params.EncryptionSvc,
	}
}

// EnrichedModel is the per-model object returned by RefreshGroupModels and
// /api/models. Backend looks each id up in FreeModels Registry so the
// frontend doesn't have to. All metadata is "best effort": fields default
// to zero values when registry has no record.
type EnrichedModel struct {
	ID           string `json:"id"`
	IsFree       bool   `json:"is_free"`
	FreeTier     string `json:"free_tier,omitempty"` // "full" | "trial" | ""
	Tier         string `json:"tier,omitempty"`      // "small" | "medium" | "large"
	Speed        string `json:"speed,omitempty"`     // "fast" | "balanced" | "slow"
	IsReasoning  bool   `json:"is_reasoning,omitempty"`
	IsMultimodal bool   `json:"is_multimodal,omitempty"`
	HasToolUse   bool   `json:"has_tool_use,omitempty"`
	ContextLabel string `json:"context_label,omitempty"`
}

// enrichModels runs each id through the FreeModels Registry. providerHint
// (e.g. group's freeProvider id) is optional; on miss we fall back to the
// bare-modelId index.
func (s *Server) enrichModels(ids []string, providerHint string) []EnrichedModel {
	out := make([]EnrichedModel, 0, len(ids))
	for _, id := range ids {
		em := EnrichedModel{ID: id}
		if s.FreeModelsRegistry != nil {
			if meta := s.FreeModelsRegistry.Lookup(providerHint, id); meta != nil {
				em.IsFree = meta.IsFree
				em.FreeTier = meta.FreeTier
				em.Tier = meta.Tier
				em.Speed = meta.Speed
				em.IsReasoning = meta.IsReasoning
				em.IsMultimodal = meta.IsMultimodal
				em.HasToolUse = meta.HasToolUse
				em.ContextLabel = meta.ContextLabel
			}
		}
		out = append(out, em)
	}
	return out
}

// LoginRequest represents the login request payload
type LoginRequest struct {
	AuthKey string `json:"auth_key" binding:"required"`
}

// LoginResponse represents the login response
type LoginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// Login handles authentication verification
func (s *Server) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": i18n.Message(c, "auth.invalid_request"),
		})
		return
	}

	expected := s.SettingsManager.GetEffectiveAuthKey()

	isValid := expected != "" &&
		subtle.ConstantTimeCompare([]byte(req.AuthKey), []byte(expected)) == 1

	if isValid {
		c.JSON(http.StatusOK, LoginResponse{
			Success: true,
			Message: i18n.Message(c, "auth.authentication_successful"),
		})
	} else {
		c.JSON(http.StatusUnauthorized, LoginResponse{
			Success: false,
			Message: i18n.Message(c, "auth.authentication_failed"),
		})
	}
}

// Health handles health check requests
func (s *Server) Health(c *gin.Context) {
	uptime := "unknown"
	if startTime, exists := c.Get("serverStartTime"); exists {
		if st, ok := startTime.(time.Time); ok {
			uptime = time.Since(st).String()
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"uptime":    uptime,
	})
}
