package handler

import (
	"net/http"

	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
)

type DedupHandler struct {
	dedupService *services.ModelDedupService
}

func NewDedupHandler(dedupService *services.ModelDedupService) *DedupHandler {
	return &DedupHandler{
		dedupService: dedupService,
	}
}

func (h *DedupHandler) GetSuggestions(c *gin.Context) {
	suggestions := h.dedupService.GetDedupSuggestions()
	c.JSON(http.StatusOK, suggestions)
}

type CreateAggregateRequest struct {
	AggregateName string   `json:"aggregate_name"`
	ModelName     string   `json:"model_name"`
	SourceGroups  []string `json:"source_groups"`
}

func (h *DedupHandler) CreateAggregate(c *gin.Context) {
	var req CreateAggregateRequest
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
			"source_groups":   req.SourceGroups,
		},
	})
}
