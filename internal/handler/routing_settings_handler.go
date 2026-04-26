package handler

import (
	app_errors "autogateway/internal/errors"
	"autogateway/internal/response"
	"autogateway/internal/router_engine"

	"github.com/gin-gonic/gin"
)

// RoutingSettingsHandler exposes the smart-routing thresholds + enabled
// flag. Replaces the legacy /api/auto-routing/config endpoint.
type RoutingSettingsHandler struct {
	selector *router_engine.Selector
}

func NewRoutingSettingsHandler(s *router_engine.Selector) *RoutingSettingsHandler {
	return &RoutingSettingsHandler{selector: s}
}

func (h *RoutingSettingsHandler) Get(c *gin.Context) {
	response.Success(c, h.selector.GetSettings())
}

type SettingsPayload struct {
	Enabled          *bool `json:"enabled"`
	SimpleThreshold  *int  `json:"simple_threshold"`
	ComplexThreshold *int  `json:"complex_threshold"`
}

func (h *RoutingSettingsHandler) Save(c *gin.Context) {
	var req SettingsPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	cur := h.selector.GetSettings()
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if req.SimpleThreshold != nil && *req.SimpleThreshold > 0 {
		cur.SimpleThreshold = *req.SimpleThreshold
	}
	if req.ComplexThreshold != nil && *req.ComplexThreshold > 0 {
		cur.ComplexThreshold = *req.ComplexThreshold
	}
	if cur.SimpleThreshold >= cur.ComplexThreshold {
		cur.ComplexThreshold = cur.SimpleThreshold * 2
	}
	h.selector.UpdateSettings(cur)
	response.Success(c, cur)
}
