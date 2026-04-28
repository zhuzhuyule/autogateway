package channel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"autogateway/internal/models"
	"autogateway/internal/types"
	"autogateway/internal/utils"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
)

// versionSegmentRe 匹配单个路径段是 v1 / v2 / v1beta / v1beta1 等 API 版本号。
var versionSegmentRe = regexp.MustCompile(`^v\d+(?:beta\d*)?$`)

// UpstreamInfo holds the information for a single upstream server, including its weight.
type UpstreamInfo struct {
	URL           *url.URL
	Weight        int
	CurrentWeight int
}

// BaseChannel provides common functionality for channel proxies.
type BaseChannel struct {
	Name               string
	Upstreams          []UpstreamInfo
	HTTPClient         *http.Client
	StreamClient       *http.Client
	TestModel          string
	ValidationEndpoint string
	upstreamLock       sync.Mutex

	// Cached fields from the group for stale check
	channelType         string
	groupUpstreams      datatypes.JSON
	effectiveConfig     *types.SystemSettings
	modelRedirectRules  datatypes.JSONMap
	modelRedirectStrict bool
}

// getUpstreamURL selects an upstream URL using a smooth weighted round-robin algorithm.
func (b *BaseChannel) getUpstreamURL() *url.URL {
	b.upstreamLock.Lock()
	defer b.upstreamLock.Unlock()

	if len(b.Upstreams) == 0 {
		return nil
	}
	if len(b.Upstreams) == 1 {
		return b.Upstreams[0].URL
	}

	totalWeight := 0
	var best *UpstreamInfo

	for i := range b.Upstreams {
		up := &b.Upstreams[i]
		totalWeight += up.Weight
		up.CurrentWeight += up.Weight

		if best == nil || up.CurrentWeight > best.CurrentWeight {
			best = up
		}
	}

	if best == nil {
		return b.Upstreams[0].URL // 降级到第一个可用的
	}

	best.CurrentWeight -= totalWeight
	return best.URL
}

// BuildUpstreamURL constructs the target URL for the upstream service.
//
// 智能去重 API 版本段:如果 baseURL 末段是 /vN (e.g. /v1, /v1beta) 且 request
// 路径开头也带相同段,合并以避免出现 /v1/v1/chat/completions。这样无论用户
// 把 baseUrl 写成 https://api.example.com 还是 https://api.example.com/v1,
// 都能正确路由,降低配置出错率。
func (b *BaseChannel) BuildUpstreamURL(originalURL *url.URL, groupName string) (string, error) {
	base := b.getUpstreamURL()
	if base == nil {
		return "", fmt.Errorf("no upstream URL configured for channel %s", b.Name)
	}

	finalURL := *base
	proxyPrefix := "/proxy/" + groupName
	requestPath := originalURL.Path
	requestPath = strings.TrimPrefix(requestPath, proxyPrefix)

	basePath := strings.TrimRight(finalURL.Path, "/")
	requestPath = dedupeVersionSegment(basePath, requestPath)
	finalURL.Path = basePath + requestPath

	finalURL.RawQuery = originalURL.RawQuery

	return finalURL.String(), nil
}

// dedupeVersionSegment 在 baseURL 末段是 /vN 而 request 开头也是同一段时,
// 把 request 那段去掉,避免双 /v1。
//   - base="/v1"      req="/v1/chat/completions" → "/chat/completions"
//   - base="/v1"      req="/chat/completions"    → "/chat/completions" (不变)
//   - base="/openai"  req="/v1/chat/completions" → "/v1/chat/completions" (不变)
//   - base=""         req="/v1/chat/completions" → "/v1/chat/completions" (不变)
//   - base="/v1beta"  req="/v1beta/models"       → "/models"
func dedupeVersionSegment(basePath, requestPath string) string {
	if basePath == "" || requestPath == "" {
		return requestPath
	}
	segs := strings.Split(strings.TrimPrefix(basePath, "/"), "/")
	if len(segs) == 0 {
		return requestPath
	}
	lastSeg := segs[len(segs)-1]
	if !versionSegmentRe.MatchString(lastSeg) {
		return requestPath
	}
	expected := "/" + lastSeg
	if requestPath == expected {
		return ""
	}
	if strings.HasPrefix(requestPath, expected+"/") {
		return strings.TrimPrefix(requestPath, expected)
	}
	return requestPath
}

// IsConfigStale checks if the channel's configuration is stale compared to the provided group.
func (b *BaseChannel) IsConfigStale(group *models.Group) bool {
	if b.channelType != group.ChannelType {
		return true
	}
	if b.TestModel != group.TestModel {
		return true
	}
	if b.ValidationEndpoint != utils.GetValidationEndpoint(group) {
		return true
	}
	if !bytes.Equal(b.groupUpstreams, group.Upstreams) {
		return true
	}
	if !reflect.DeepEqual(b.effectiveConfig, &group.EffectiveConfig) {
		return true
	}
	// Check for model redirect rules changes
	if !reflect.DeepEqual(b.modelRedirectRules, group.ModelRedirectRules) {
		return true
	}
	if b.modelRedirectStrict != group.ModelRedirectStrict {
		return true
	}
	return false
}

// GetHTTPClient returns the client for standard requests.
func (b *BaseChannel) GetHTTPClient() *http.Client {
	return b.HTTPClient
}

// GetStreamClient returns the client for streaming requests.
func (b *BaseChannel) GetStreamClient() *http.Client {
	return b.StreamClient
}

// ApplyModelRedirect applies model redirection based on the group's redirect rules.
func (b *BaseChannel) ApplyModelRedirect(req *http.Request, bodyBytes []byte, group *models.Group) ([]byte, error) {
	if len(group.ModelRedirectMap) == 0 || len(bodyBytes) == 0 {
		return bodyBytes, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		return bodyBytes, nil
	}

	modelValue, exists := requestData["model"]
	if !exists {
		return bodyBytes, nil
	}

	model, ok := modelValue.(string)
	if !ok {
		return bodyBytes, nil
	}

	// Direct match without any prefix processing
	if targetModel, found := group.ModelRedirectMap[model]; found {
		requestData["model"] = targetModel

		// Log the redirection for audit
		logrus.WithFields(logrus.Fields{
			"group":          group.Name,
			"original_model": model,
			"target_model":   targetModel,
			"channel":        "json_body",
		}).Debug("Model redirected")

		return json.Marshal(requestData)
	}

	if group.ModelRedirectStrict {
		return nil, fmt.Errorf("model '%s' is not configured in redirect rules", model)
	}

	return bodyBytes, nil
}

// TransformModelList transforms the model list response based on redirect rules.
func (b *BaseChannel) TransformModelList(req *http.Request, bodyBytes []byte, group *models.Group) (map[string]any, error) {
	var response map[string]any
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		logrus.WithError(err).Debug("Failed to parse model list response, returning empty")
		return nil, err
	}

	dataInterface, exists := response["data"]
	if !exists {
		return response, nil
	}

	upstreamModels, ok := dataInterface.([]any)
	if !ok {
		return response, nil
	}

	// Build configured source models list (common logic for both modes)
	configuredModels := buildConfiguredModels(group.ModelRedirectMap)

	// Strict mode: return only configured models (whitelist)
	if group.ModelRedirectStrict {
		response["data"] = configuredModels

		logrus.WithFields(logrus.Fields{
			"group":       group.Name,
			"model_count": len(configuredModels),
			"strict_mode": true,
		}).Debug("Model list returned (strict mode - configured models only)")

		return response, nil
	}

	// Non-strict mode: merge upstream + configured models (upstream priority)
	merged := mergeModelLists(upstreamModels, configuredModels)
	response["data"] = merged

	logrus.WithFields(logrus.Fields{
		"group":            group.Name,
		"upstream_count":   len(upstreamModels),
		"configured_count": len(configuredModels),
		"merged_count":     len(merged),
		"strict_mode":      false,
	}).Debug("Model list merged (non-strict mode)")

	return response, nil
}

// buildConfiguredModels builds a list of models from redirect rules
func buildConfiguredModels(redirectMap map[string]string) []any {
	if len(redirectMap) == 0 {
		return []any{}
	}

	models := make([]any, 0, len(redirectMap))
	for sourceModel := range redirectMap {
		models = append(models, map[string]any{
			"id":       sourceModel,
			"object":   "model",
			"created":  0,
			"owned_by": "system",
		})
	}
	return models
}

// mergeModelLists merges upstream and configured model lists
func mergeModelLists(upstream []any, configured []any) []any {
	// Create set of upstream model IDs
	upstreamIDs := make(map[string]bool)
	for _, item := range upstream {
		if modelObj, ok := item.(map[string]any); ok {
			if modelID, ok := modelObj["id"].(string); ok {
				upstreamIDs[modelID] = true
			}
		}
	}

	// Start with all upstream models
	result := make([]any, len(upstream))
	copy(result, upstream)

	// Add configured models that don't exist in upstream
	for _, item := range configured {
		if modelObj, ok := item.(map[string]any); ok {
			if modelID, ok := modelObj["id"].(string); ok {
				if !upstreamIDs[modelID] {
					result = append(result, item)
				}
			}
		}
	}

	return result
}
