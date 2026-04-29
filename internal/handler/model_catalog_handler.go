package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"autogateway/internal/services"

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

	addModel := func(modelID, groupName string) {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			return
		}
		entry, ok := modelMap[modelID]
		if !ok {
			entry = &CatalogModel{
				ID:          modelID,
				DisplayName: formatDisplayName(modelID),
				Groups:      []string{},
				Providers:   []string{},
			}
			modelMap[modelID] = entry
		}
		entry.Groups = appendUnique(entry.Groups, groupName)
	}

	for _, group := range groups {
		if group.GroupType == "aggregate" {
			continue
		}

		// 1. ModelRedirectMap 的 source model — 客户端可调的"虚拟"名
		for sourceModel := range group.ModelRedirectMap {
			addModel(sourceModel, group.Name)
		}

		// 2. AvailableModels — 上游 /v1/models 真实拉到的清单 (主数据源)
		//    之前完全被忽略, 这是 catalog 缺模型的根因。
		if len(group.AvailableModels) > 0 {
			var available []string
			if err := json.Unmarshal(group.AvailableModels, &available); err == nil {
				for _, m := range available {
					addModel(m, group.Name)
				}
			}
		}

		// 3. TestModel 兜底 (仅当前两者都没东西时, 不要重复添加)
		if len(group.ModelRedirectMap) == 0 && len(group.AvailableModels) == 0 && group.TestModel != "" {
			addModel(group.TestModel, group.Name)
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
			"owned_by":     "autogateway",
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
