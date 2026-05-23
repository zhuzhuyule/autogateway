package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"autogateway/internal/models"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

type ModelCatalogHandler struct {
	groupManager *services.GroupManager
	freeRegistry *services.FreeModelsRegistry
	cache        *sync.Map
	cacheTTL     time.Duration
}

func NewModelCatalogHandler(
	groupManager *services.GroupManager,
	freeRegistry *services.FreeModelsRegistry,
) *ModelCatalogHandler {
	return &ModelCatalogHandler{
		groupManager: groupManager,
		freeRegistry: freeRegistry,
		cache:        &sync.Map{},
		cacheTTL:     5 * time.Minute,
	}
}

// CatalogModel 跟前端契约的 model 元数据。is_free/tier/speed 等来自
// FreeModelsRegistry, 同一 model 跨多 provider 出现时取并集 (IsFree=true
// 优先, tier/speed 取第一次出现的非空值).
type CatalogModel struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"display_name"`
	Groups       []string `json:"groups"`
	Providers    []string `json:"providers"`
	IsFree       bool     `json:"is_free"`
	FreeTier     string   `json:"free_tier,omitempty"`
	Tier         string   `json:"tier,omitempty"`
	Speed        string   `json:"speed,omitempty"`
	IsReasoning  bool     `json:"is_reasoning,omitempty"`
	IsMultimodal bool     `json:"is_multimodal,omitempty"`
	HasToolUse   bool     `json:"has_tool_use,omitempty"`
	ContextLabel string   `json:"context_label,omitempty"`
}

// resolveProviderHint 从 group 的第一个 upstream URL 解析 host, 然后
// 用 FreeModelsRegistry.LookupProviderByHost 反查 provider id, 提高
// (provider, modelId) 精确匹配率. 解析失败回退 group.Name (大多数命名约定
// 跟 FreeModels provider id 重叠).
func (h *ModelCatalogHandler) resolveProviderHint(g *models.Group) string {
	if h.freeRegistry != nil && len(g.Upstreams) > 0 {
		var defs []struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(g.Upstreams, &defs); err == nil && len(defs) > 0 {
			if u, perr := url.Parse(defs[0].URL); perr == nil && u.Hostname() != "" {
				if id, _, ok := h.freeRegistry.LookupProviderByHost(u.Hostname()); ok {
					return id
				}
			}
		}
	}
	return g.Name
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

	addModel := func(modelID, groupName, providerHint string) {
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

		// Free-models enrichment: 同一 modelID 跨 provider 出现时取并集 —
		// IsFree=true 优先 (任意 provider 免费即标免费); 其他字段首个非空值胜出.
		if h.freeRegistry != nil {
			if meta := h.freeRegistry.Lookup(providerHint, modelID); meta != nil {
				if meta.IsFree {
					entry.IsFree = true
				}
				if entry.FreeTier == "" {
					entry.FreeTier = meta.FreeTier
				}
				if entry.Tier == "" {
					entry.Tier = meta.Tier
				}
				if entry.Speed == "" {
					entry.Speed = meta.Speed
				}
				if meta.IsReasoning {
					entry.IsReasoning = true
				}
				if meta.IsMultimodal {
					entry.IsMultimodal = true
				}
				if meta.HasToolUse {
					entry.HasToolUse = true
				}
				if entry.ContextLabel == "" {
					entry.ContextLabel = meta.ContextLabel
				}
			}
		}
	}

	for _, group := range groups {
		if group.GroupType == "aggregate" {
			continue
		}
		providerHint := h.resolveProviderHint(group)

		// 1. ModelRedirectMap 的 source model — 客户端可调的"虚拟"名
		for sourceModel := range group.ModelRedirectMap {
			addModel(sourceModel, group.Name, providerHint)
		}

		// 2. AvailableModels — 上游 /v1/models 真实拉到的清单 (主数据源)
		if len(group.AvailableModels) > 0 {
			var available []string
			if err := json.Unmarshal(group.AvailableModels, &available); err == nil {
				for _, m := range available {
					addModel(m, group.Name, providerHint)
				}
			}
		}

		// 3. TestModel 兜底
		if len(group.ModelRedirectMap) == 0 && len(group.AvailableModels) == 0 && group.TestModel != "" {
			addModel(group.TestModel, group.Name, providerHint)
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
		entry := gin.H{
			"id":           m.ID,
			"object":       "model",
			"created":      0,
			"owned_by":     "autogateway",
			"display_name": m.DisplayName,
			"groups":       m.Groups,
			"is_free":      m.IsFree,
		}
		// 可选字段空就不输出, 让前端 JSON 更紧凑
		if m.FreeTier != "" {
			entry["free_tier"] = m.FreeTier
		}
		if m.Tier != "" {
			entry["tier"] = m.Tier
		}
		if m.Speed != "" {
			entry["speed"] = m.Speed
		}
		if m.IsReasoning {
			entry["is_reasoning"] = true
		}
		if m.IsMultimodal {
			entry["is_multimodal"] = true
		}
		if m.HasToolUse {
			entry["has_tool_use"] = true
		}
		if m.ContextLabel != "" {
			entry["context_label"] = m.ContextLabel
		}
		models[i] = entry
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
