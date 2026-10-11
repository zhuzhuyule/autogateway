package proxy

import (
	"autogateway/internal/channel"
	"autogateway/internal/models"
	"autogateway/internal/services"
	"autogateway/internal/utils"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// shouldInterceptModelList checks if this is a model list request that should be intercepted
func shouldInterceptModelList(path string, method string) bool {
	if method != "GET" {
		return false
	}

	// Check various model list endpoints
	return strings.HasSuffix(path, "/v1/models") ||
		strings.HasSuffix(path, "/v1beta/models") ||
		strings.Contains(path, "/v1beta/openai/v1/models")
}

// handleModelListResponse processes the model list response and applies filtering based on redirect rules
func (ps *ProxyServer) handleModelListResponse(c *gin.Context, resp *http.Response, group *models.Group, channelHandler channel.ChannelProxy) {
	// Read the upstream response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logrus.WithError(err).Error("Failed to read model list response body")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read response"})
		return
	}

	// Decompress response data based on Content-Encoding
	contentEncoding := resp.Header.Get("Content-Encoding")
	decompressed, err := utils.DecompressResponse(contentEncoding, bodyBytes)
	if err != nil {
		logrus.WithError(err).Warn("Decompression failed, using original data")
		decompressed = bodyBytes
	}

	// Transform model list (returns map[string]any directly, no marshaling)
	response, err := channelHandler.TransformModelList(c.Request, decompressed, group)
	if err != nil {
		logrus.WithError(err).Error("Failed to transform model list")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process response"})
		return
	}

	// 不附加任何别名: alias 解析只发生在 aggregate 入口 (ModelResolver),
	// standard 分组入口收到 alias 名会原样透传给上游 → 调不通. 以前广播
	// 全局别名, 导致每个组的 /v1/models 都混进一堆本 URL 不可用的模型.

	c.JSON(http.StatusOK, response)
}

func appendAliasModels(response map[string]any, aliasNames []string) {
	if len(aliasNames) == 0 {
		return
	}

	data, ok := response["data"].([]any)
	if !ok {
		return
	}

	seen := make(map[string]struct{}, len(data)+len(aliasNames))
	for _, item := range data {
		modelObj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		modelID, ok := modelObj["id"].(string)
		if !ok || modelID == "" {
			continue
		}
		seen[modelID] = struct{}{}
	}

	for _, alias := range aliasNames {
		if alias == "" {
			continue
		}
		if _, exists := seen[alias]; exists {
			continue
		}
		data = append(data, map[string]any{
			"id":       alias,
			"object":   "model",
			"created":  0,
			"owned_by": "autogateway-alias",
		})
		seen[alias] = struct{}{}
	}

	response["data"] = data
}

// buildCachedModelListBody 把缓存的模型 id 列表拼成"看起来像上游返回"的 body,
// 好让 channelHandler.TransformModelList 复用同一套重定向规则/严格模式处理。
// gemini 原生端点 (/v1beta/models) 要 {"models":[{name,displayName,...}]},
// 其余 (含 gemini 的 /v1beta/openai/v1/models 兼容路径) 用 OpenAI 的 data 形状。
func buildCachedModelListBody(path string, ids []string) []byte {
	if strings.HasSuffix(path, "/v1beta/models") {
		entries := make([]any, 0, len(ids))
		for _, id := range ids {
			name := id
			if !strings.HasPrefix(name, "models/") {
				name = "models/" + name
			}
			entries = append(entries, map[string]any{
				"name":                       name,
				"displayName":                id,
				"supportedGenerationMethods": []string{"generateContent"},
			})
		}
		body, _ := json.Marshal(map[string]any{"models": entries})
		return body
	}

	entries := make([]any, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, map[string]any{
			"id":       id,
			"object":   "model",
			"created":  0,
			"owned_by": "autogateway-cache",
		})
	}
	body, _ := json.Marshal(map[string]any{"object": "list", "data": entries})
	return body
}

// modelListDegrade 在客户端拉模型列表、但上游这一趟没走通时 (没有可用 key, 或
// 重试/failover 全部耗尽), 用分组缓存的清单直接回 200, 而不是把上游错误抛给
// 客户端。缓存为空 (分组从没刷新过) 时返回 false, 让原错误照旧 —— 宁缺勿空。
//
// 请求日志仍按上游失败记账: 降级只改变客户端看到的响应, 不掩盖"上游没打通"这件事。
// 清单来源可能是上游真实列表或 FreeModels 兜底 (groups.models_source), 两种都
// 通过 X-Autogateway-Models-Source: cache 标明"这不是刚拿到的上游结果"。
func (ps *ProxyServer) modelListDegrade(c *gin.Context, group *models.Group, channelHandler channel.ChannelProxy) bool {
	if group == nil || channelHandler == nil || c.Writer.Written() {
		return false
	}
	if !shouldInterceptModelList(c.Request.URL.Path, c.Request.Method) {
		return false
	}
	ids := services.CandidateModelIDsForGroup(group)
	if len(ids) == 0 {
		return false
	}
	body := buildCachedModelListBody(c.Request.URL.Path, ids)
	response, err := channelHandler.TransformModelList(c.Request, body, group)
	if err != nil {
		logrus.WithError(err).WithField("group", group.Name).Warn("Failed to transform cached model list")
		return false
	}

	c.Header("X-Autogateway-Models-Source", "cache")
	logrus.WithFields(logrus.Fields{
		"group":  group.Name,
		"source": group.ModelsSource,
		"count":  len(ids),
	}).Warn("upstream model list unavailable, served cached list to client")
	c.JSON(http.StatusOK, response)
	return true
}

// handleAggregateModelList 为聚合分组的 model-list 请求返回所有子分组"可调模型"的并集,
// 再附加全局别名. 纯读缓存、不转发上游 —— 保证列表完整, 且每次请求返回一致(不再随
// SWRR 命中的单个子分组而漂移). available_models 缓存由后台定时刷新服务保持新鲜.
func (ps *ProxyServer) handleAggregateModelList(c *gin.Context, aggregateGroup *models.Group) {
	ids := ps.groupManager.AggregateCandidateModels(aggregateGroup)
	data := make([]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"created":  0,
			"owned_by": aggregateGroup.Name,
		})
	}
	response := map[string]any{
		"object": "list",
		"data":   data,
	}
	if ps.aliasService != nil && len(aggregateGroup.SubGroups) > 0 {
		// 只附加候选落在本聚合子分组内的别名 — 与 ModelResolver 的路由约束
		// 一致 (alias.GroupID 不在 SubGroups 内时命中 alias 也路由不出去).
		subIDs := make([]uint, 0, len(aggregateGroup.SubGroups))
		for _, sg := range aggregateGroup.SubGroups {
			subIDs = append(subIDs, sg.SubGroupID)
		}
		aliasNames, err := ps.aliasService.ListEnabledAliasNamesForGroups(c.Request.Context(), subIDs)
		if err != nil {
			logrus.WithError(err).Warn("Failed to load aliases for aggregate model list")
		} else {
			appendAliasModels(response, aliasNames)
		}
	}
	c.JSON(http.StatusOK, response)
}
