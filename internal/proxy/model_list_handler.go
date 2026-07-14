package proxy

import (
	"autogateway/internal/channel"
	"autogateway/internal/models"
	"autogateway/internal/utils"
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

	if ps.aliasService != nil {
		aliasNames, err := ps.aliasService.ListEnabledAliasNames(c.Request.Context())
		if err != nil {
			logrus.WithError(err).Warn("Failed to load aliases for model list")
		} else {
			appendAliasModels(response, aliasNames)
		}
	}

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
	if ps.aliasService != nil {
		aliasNames, err := ps.aliasService.ListEnabledAliasNames(c.Request.Context())
		if err != nil {
			logrus.WithError(err).Warn("Failed to load aliases for aggregate model list")
		} else {
			appendAliasModels(response, aliasNames)
		}
	}
	c.JSON(http.StatusOK, response)
}
