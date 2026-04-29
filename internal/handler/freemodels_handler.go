package handler

import (
	"net/http"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/response"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

// FreeModelsHandler exposes GET /api/freemodels/registry — proxies the
// in-memory snapshot of FreeModelsRegistry so the frontend doesn't have
// to call out to the public CDN itself (avoids CORS, duplicates the cache).
type FreeModelsHandler struct {
	reg *services.FreeModelsRegistry
}

func NewFreeModelsHandler(reg *services.FreeModelsRegistry) *FreeModelsHandler {
	return &FreeModelsHandler{reg: reg}
}

// Registry returns the cached envelope; empty registry → 503 so frontend
// knows to fall back to its bundled static list.
func (h *FreeModelsHandler) Registry(c *gin.Context) {
	snap := h.reg.Snapshot()
	if snap.TotalModels == 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrServiceUnavailable, "freemodels registry not yet populated"))
		return
	}
	// 缓存 5 分钟,前端 reactive store 拉一次后可短期复用
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, snap)
}
