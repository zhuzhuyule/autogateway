package handler

import (
	"encoding/json"
	"net/http"

	"gpt-load/internal/autoroute"
	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
)

type AutoRouteHandler struct {
	autoRouteManager *autoroute.ConfigManager
	groupManager     *services.GroupManager
}

func NewAutoRouteHandler(autoRouteManager *autoroute.ConfigManager, groupManager *services.GroupManager) *AutoRouteHandler {
	return &AutoRouteHandler{
		autoRouteManager: autoRouteManager,
		groupManager:     groupManager,
	}
}

func (h *AutoRouteHandler) GetConfig(c *gin.Context) {
	cfg := h.autoRouteManager.GetConfig()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"config":  cfg,
	})
}

type SaveConfigRequest struct {
	Enabled          bool                              `json:"enabled"`
	SimpleThreshold  int                               `json:"simple_threshold"`
	ComplexThreshold int                               `json:"complex_threshold"`
	GroupMapping     map[string]autoroute.GroupMapping `json:"group_mapping"`
}

func (h *AutoRouteHandler) SaveConfig(c *gin.Context) {
	var req SaveConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	cfg := &autoroute.RouteConfig{
		Enabled:          req.Enabled,
		SimpleThreshold:  req.SimpleThreshold,
		ComplexThreshold: req.ComplexThreshold,
		GroupMapping:     req.GroupMapping,
	}

	if err := h.autoRouteManager.Save(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"config":  cfg,
	})
}

type TestRouteRequest struct {
	GroupName   string                 `json:"group_name"`
	RequestBody map[string]interface{} `json:"request_body"`
}

func (h *AutoRouteHandler) TestRoute(c *gin.Context) {
	var req TestRouteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	cfg := h.autoRouteManager.GetConfig()
	mapping, found := cfg.GroupMapping[req.GroupName]
	if !found {
		c.JSON(http.StatusOK, gin.H{
			"success":      true,
			"target_group": req.GroupName,
			"analysis":     nil,
			"message":     "no mapping found, use original group",
		})
		return
	}

	classifier := autoroute.NewClassifier(nil)
	bodyBytes, err := json.Marshal(req.RequestBody)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	analysis, err := classifier.Analyze(bodyBytes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	var targetGroup string
	switch analysis.Level {
	case autoroute.Simple:
		targetGroup = mapping.SimpleGroup
	case autoroute.Complex:
		targetGroup = mapping.ComplexGroup
	default:
		targetGroup = mapping.MediumGroup
	}

	c.JSON(http.StatusOK, gin.H{
		"success":      true,
		"target_group": targetGroup,
		"analysis":     analysis,
	})
}
