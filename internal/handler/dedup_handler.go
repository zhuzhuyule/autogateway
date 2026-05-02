package handler

import (
	"net/http"
	"strings"

	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type DedupHandler struct {
	dedupService *services.ModelDedupService
	aliasService *services.AliasService
	db           *gorm.DB
}

func NewDedupHandler(
	dedupService *services.ModelDedupService,
	aliasService *services.AliasService,
	db *gorm.DB,
) *DedupHandler {
	return &DedupHandler{
		dedupService: dedupService,
		aliasService: aliasService,
		db:           db,
	}
}

// GetModels returns every candidate model from non-aggregate groups,
// grouped by derived family. Used by the Aliases page's "快速整理" tab.
func (h *DedupHandler) GetModels(c *gin.Context) {
	families, err := h.dedupService.GetModelsByFamily(h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"families": families})
}

// Deprecated: legacy duplicate-only feed; superseded by GetModels.
// Kept for one release to avoid breaking any consumer; remove after
// frontend stops calling it (no callers as of this change).
func (h *DedupHandler) GetSuggestions(c *gin.Context) {
	suggestions := h.dedupService.GetDedupSuggestions()
	c.JSON(http.StatusOK, suggestions)
}

// CreateAliasUnification accepts an alias name plus the candidate
// (group_id, real_model) pairs the user confirmed and inserts one
// model_aliases row per candidate via AliasService.Create.
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
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}
	alias := strings.TrimSpace(req.Alias)
	if alias == "" || len(req.Candidates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "alias and at least one candidate are required",
		})
		return
	}

	created := 0
	failures := []string{}
	for _, cand := range req.Candidates {
		if cand.GroupID == 0 || strings.TrimSpace(cand.RealModel) == "" {
			failures = append(failures, "invalid candidate: missing group_id or real_model")
			continue
		}
		_, err := h.aliasService.Create(c.Request.Context(), services.AliasCreateRequest{
			Alias:     alias,
			GroupID:   cand.GroupID,
			RealModel: cand.RealModel,
		})
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		created++
	}

	if created == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success":  false,
			"created":  0,
			"failures": failures,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"alias":    alias,
		"created":  created,
		"failures": failures,
	})
}
