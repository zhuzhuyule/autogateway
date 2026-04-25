package handler

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
)

type ModelCatalogHandler struct {
	groupManager *services.GroupManager
	cache       *sync.Map
	cacheTTL    time.Duration
}

func NewModelCatalogHandler(groupManager *services.GroupManager) *ModelCatalogHandler {
	return &ModelCatalogHandler{
		groupManager: groupManager,
		cache:       &sync.Map{},
		cacheTTL:    5 * time.Minute,
	}
}

type CatalogModel struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Groups      []string `json:"groups"`
	Providers   []string `json:"providers"`
}

type cachedModels struct {
	models []gin.H
	expiry time.Time
}

func (h *ModelCatalogHandler) ListModels(c *gin.Context) {
	if cached, ok := h.cache.Load("models"); ok {
		if cm := cached.(*cachedModels); time.Now().Before(cm.expiry) {
			c.JSON(http.StatusOK, gin.H{
				"object": "list",
				"data":   cm.models,
			})
			return
		}
	}

	groups := h.groupManager.GetAllGroups()
	modelMap := make(map[string]*CatalogModel)

	for _, group := range groups {
		if group.GroupType == "aggregate" {
			continue
		}

		for sourceModel := range group.ModelRedirectMap {
			if _, exists := modelMap[sourceModel]; !exists {
				modelMap[sourceModel] = &CatalogModel{
					ID:          sourceModel,
					DisplayName: formatDisplayName(sourceModel),
					Groups:      []string{},
					Providers:   []string{},
				}
			}
			modelMap[sourceModel].Groups = appendUnique(
				modelMap[sourceModel].Groups, group.Name)
		}

		if len(group.ModelRedirectMap) == 0 && group.TestModel != "" {
			model := group.TestModel
			if _, exists := modelMap[model]; !exists {
				modelMap[model] = &CatalogModel{
					ID:          model,
					DisplayName: formatDisplayName(model),
					Groups:      []string{},
					Providers:   []string{},
				}
			}
			modelMap[model].Groups = appendUnique(
				modelMap[model].Groups, group.Name)
		}
	}

	result := make([]CatalogModel, 0, len(modelMap))
	for _, m := range modelMap {
		result = append(result, *m)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})

	models := make([]gin.H, len(result))
	for i, m := range result {
		models[i] = gin.H{
			"id":           m.ID,
			"object":       "model",
			"created":      0,
			"owned_by":     "gpt-load",
			"display_name": m.DisplayName,
			"groups":       m.Groups,
		}
	}

	h.cache.Store("models", &cachedModels{
		models: models,
		expiry: time.Now().Add(h.cacheTTL),
	})

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   models,
	})
}

func (h *ModelCatalogHandler) RefreshCache() {
	h.cache.Delete("models")
}

func formatDisplayName(modelID string) string {
	parts := strings.Split(modelID, "-")
	for i, part := range parts {
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}
