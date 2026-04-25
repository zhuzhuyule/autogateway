package handler

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/zhuzhuyule/b4a/internal/models"
)

type GroupManager interface {
	GetAllGroups() []models.GroupInfo
}

type ModelCatalogHandler struct {
	groupManager GroupManager
	cache       *sync.Map
	cacheTTL    time.Duration
}

type CatalogModel struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Groups      []string `json:"groups"`
	Providers   []string `json:"providers"`
}

type cachedModel struct {
	models []gin.H
	expiry time.Time
}

func NewModelCatalogHandler(groupManager GroupManager) *ModelCatalogHandler {
	if groupManager == nil {
		groupManager = &DefaultGroupManager{}
	}
	return &ModelCatalogHandler{
		groupManager: groupManager,
		cache:       &sync.Map{},
		cacheTTL:    5 * time.Minute,
	}
}

func (h *ModelCatalogHandler) ListModels(c *gin.Context) {
	if cached, ok := h.cache.Load("models"); ok {
		if cm := cached.(*cachedModel); time.Now().Before(cm.expiry) {
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

		for sourceModel := range group.ModelRedirect {
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

		if len(group.ModelRedirect) == 0 && group.TestModel != "" {
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

	h.cache.Store("models", &cachedModel{
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

type DefaultGroupManager struct{}

func (m *DefaultGroupManager) GetAllGroups() []models.GroupInfo {
	return []models.GroupInfo{}
}
