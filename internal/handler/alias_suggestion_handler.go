package handler

import (
	"strconv"
	"time"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/response"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

// AliasSuggestionHandler exposes GET /api/aliases/suggestions and
// GET /api/groups/:id/alias-suggestions (P4.2 registry-driven).
type AliasSuggestionHandler struct {
	svc *services.AliasSuggestionService
}

func NewAliasSuggestionHandler(svc *services.AliasSuggestionService) *AliasSuggestionHandler {
	return &AliasSuggestionHandler{svc: svc}
}

func (h *AliasSuggestionHandler) Suggest(c *gin.Context) {
	rows, err := h.svc.Suggest(c.Request.Context(), 24*time.Hour)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}
	response.Success(c, rows)
}

// SuggestFromRegistry P4.2 registry-driven 建议: 给定 aggregate group id,
// 返回 "跨 sub-group 共享同一 family 但还没建 alias" 的候选清单, 让 admin
// 一键采纳.
func (h *AliasSuggestionHandler) SuggestFromRegistry(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid group id"))
		return
	}
	rows, err := h.svc.SuggestFromRegistry(c.Request.Context(), uint(id))
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}
	response.Success(c, rows)
}
