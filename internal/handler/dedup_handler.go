package handler

import (
	"net/http"
	"strings"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

type DedupHandler struct {
	dedupService *services.ModelDedupService
	aliasService *services.AliasService
}

func NewDedupHandler(
	dedupService *services.ModelDedupService,
	aliasService *services.AliasService,
) *DedupHandler {
	return &DedupHandler{
		dedupService: dedupService,
		aliasService: aliasService,
	}
}

// GetModels returns every candidate model from non-aggregate groups,
// grouped by derived family. Used by the Aliases page's "快速整理" tab.
func (h *DedupHandler) GetModels(c *gin.Context) {
	families, err := h.dedupService.GetModelsByFamily(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"families": families})
}

// CreateAliasUnification accepts an alias name plus the candidate
// (group_id, real_model) pairs the user confirmed and inserts them
// atomically. Either every candidate is created, or none are — there
// is no partial-commit state to clean up.
type CreateAliasUnificationRequest struct {
	Alias      string                       `json:"alias"`
	Candidates []CreateAliasUnificationItem `json:"candidates"`
}

type CreateAliasUnificationItem struct {
	GroupID   uint   `json:"group_id"`
	RealModel string `json:"real_model"`
}

func (h *DedupHandler) CreateAliasUnification(c *gin.Context) {
	var req CreateAliasUnificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    app_errors.ErrInvalidJSON.Code,
			"message": err.Error(),
		})
		return
	}
	alias := strings.TrimSpace(req.Alias)
	if alias == "" || len(req.Candidates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    app_errors.ErrValidation.Code,
			"message": "alias and at least one candidate are required",
		})
		return
	}

	aliasReqs := make([]services.AliasCreateRequest, 0, len(req.Candidates))
	for _, cand := range req.Candidates {
		aliasReqs = append(aliasReqs, services.AliasCreateRequest{
			Alias:     alias,
			GroupID:   cand.GroupID,
			RealModel: cand.RealModel,
		})
	}

	rows, err := h.aliasService.CreateMany(c.Request.Context(), aliasReqs)
	if err != nil {
		if apiErr, ok := err.(*app_errors.APIError); ok {
			c.JSON(apiErr.HTTPStatus, gin.H{
				"success": false,
				"code":    apiErr.Code,
				"message": apiErr.Message,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"code":    app_errors.ErrInternalServer.Code,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"alias":   alias,
		"created": len(rows),
	})
}
