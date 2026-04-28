package handler

import (
	"time"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/response"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

// AliasSuggestionHandler exposes GET /api/aliases/suggestions.
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
