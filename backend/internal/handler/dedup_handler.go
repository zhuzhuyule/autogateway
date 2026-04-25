package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/zhuzhuyule/b4a/internal/services"
)

type DedupHandler struct {
	dedupService      *services.ModelDedupService
	suggestionService *services.AggregateSuggestionService
}

func NewDedupHandler(
	dedupService *services.ModelDedupService,
	suggestionService *services.AggregateSuggestionService,
) *DedupHandler {
	return &DedupHandler{
		dedupService:      dedupService,
		suggestionService: suggestionService,
	}
}

func (h *DedupHandler) GetSuggestions(c *gin.Context) {
	suggestions := h.dedupService.GetDedupSuggestions()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    suggestions,
	})
}

func (h *DedupHandler) CreateAggregate(c *gin.Context) {
	var req services.AggregateGroupSuggestion

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": map[string]interface{}{
			"aggregate_name": req.AggregateName,
			"model_name":     req.ModelName,
			"config":         h.suggestionService.CreateAggregateConfig(&req),
		},
	})
}
