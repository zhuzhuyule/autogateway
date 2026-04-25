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
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    suggestions,
	})
}

type SubGroupConfig struct {
	Name     string            `json:"name"`
	Weight   int               `json:"weight"`
	Redirect map[string]string `json:"redirect"`
}

type CreateAggregateRequest struct {
	AggregateName string           `json:"aggregate_name"`
	ModelName     string           `json:"model_name"`
	SubGroups     []SubGroupConfig `json:"sub_groups"`
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
			"config": map[string]interface{}{
				"name":       req.AggregateName,
				"type":       "aggregate",
				"sub_groups": req.SubGroups,
				"model_list": []string{req.ModelName},
			},
		},
	})
}
