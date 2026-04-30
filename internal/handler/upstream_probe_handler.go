package handler

import (
	"strings"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/response"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

// UpstreamProbeHandler exposes GET /api/upstream/probe.
type UpstreamProbeHandler struct{}

func NewUpstreamProbeHandler() *UpstreamProbeHandler { return &UpstreamProbeHandler{} }

// Probe accepts ?url=<base_url> and returns ProbeResult or 400/502.
func (h *UpstreamProbeHandler) Probe(c *gin.Context) {
	raw := strings.TrimSpace(c.Query("url"))
	if raw == "" {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_payload")
		return
	}
	prefer := strings.TrimSpace(c.Query("prefer"))
	res, err := services.ProbeUpstream(c.Request.Context(), raw, prefer)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadGateway, err.Error()))
		return
	}
	response.Success(c, res)
}
