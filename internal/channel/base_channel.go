package channel

import (
	"autogateway/internal/models"
	"autogateway/internal/types"
	"autogateway/internal/utils"
	"bytes"
	"encoding/json"
	"fmt"
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

// JoinUpstreamURL joins requestPath onto base and returns the resolved URL.
//
// 单一职责: 所有上游请求 (代理转发 + ValidateKey 测试) 都必须走这个方法, 避免
// 不同入口实现各自的拼接逻辑导致路径双 /v1.
//
// 行为:
//  1. 丢弃 fragment ("#" 是用户级 UI escape, 持久化进库但 HTTP 不发送)
//  2. baseURL 末段是 /vN → request 开头若也是版本段 (任意版本号) 一律去掉,
//     base 的版本视为权威. 避免:
//       base=/v1   + req=/v1/chat/completions → /v1/chat/completions
//       base=/v2   + req=/v1/chat/completions → /v2/chat/completions  (xfyun)
//       base=/v1beta + req=/v1/chat/completions → /v1beta/chat/completions
//  3. RawQuery 不在此处理, 由调用方自行 set/append
func (b *BaseChannel) JoinUpstreamURL(base *url.URL, requestPath string) *url.URL {
	final := *base
	final.Fragment = ""
	final.RawFragment = ""
	basePath := strings.TrimRight(final.Path, "/")
	requestPath = dedupeVersionSegment(basePath, requestPath)
	final.Path = basePath + requestPath
	return &final
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

	requestPath := stripGatewayPrefix(originalURL.Path, groupName)
	finalURL := b.JoinUpstreamURL(base, requestPath)
	finalURL.RawQuery = originalURL.RawQuery

	return finalURL.String(), nil
}

func stripGatewayPrefix(requestPath, groupName string) string {
	proxyPrefix := "/proxy/" + groupName
	if strings.HasPrefix(requestPath, proxyPrefix) {
		return strings.TrimPrefix(requestPath, proxyPrefix)
	}

	// System shortcut routes are mounted without /proxy:
	// /openai/*, /anthropic/*, /gemini/*. The router injects group_name, but
	// non-chat endpoints such as /openai/v1/models do not pass through the
	// model-routing middleware that rewrites the path to /proxy/{group}/*.
	for _, shortcut := range []string{"/openai", "/anthropic", "/gemini"} {
		if requestPath == shortcut {
			return ""
		}
		if strings.HasPrefix(requestPath, shortcut+"/") {
			return strings.TrimPrefix(requestPath, shortcut)
		}
	}

	return requestPath
}

// dedupeVersionSegment 在 baseURL 末段是 /vN 时把 request 开头的版本段也去掉,
// base 的版本视为权威 (用户配置的 baseUrl 是上游真实 API 版本, 而 endpoint
// 模板里的 /v1 只是 OpenAI 默认 schema 的占位).
//
//   - base="/v1"      req="/v1/chat/completions"  → "/chat/completions"
//   - base="/v2"      req="/v1/chat/completions"  → "/chat/completions"  (xfyun /v2)
//   - base="/v1beta"  req="/v1beta/models"        → "/models"
//   - base="/v1beta"  req="/v1/chat/completions"  → "/chat/completions"  (gemini openai-compat)
//   - base="/v1"      req="/chat/completions"     → "/chat/completions"  (不变)
//   - base="/openai"  req="/v1/chat/completions"  → "/v1/chat/completions" (base 末段非版本号, 不变)
//   - base=""         req="/v1/chat/completions"  → "/v1/chat/completions" (不变)
func dedupeVersionSegment(basePath, requestPath string) string {
	if basePath == "" || requestPath == "" {
		return requestPath
	}
	baseSegs := strings.Split(strings.TrimPrefix(basePath, "/"), "/")
	if len(baseSegs) == 0 {
		return requestPath
	}
	if !versionSegmentRe.MatchString(baseSegs[len(baseSegs)-1]) {
		return requestPath
	}

	// base 已经带版本 → 无论 request 的版本号是否相同, 都把它的第一段版本号砍掉.
	trimmed := strings.TrimPrefix(requestPath, "/")
	if trimmed == "" {
		return requestPath
	}
	slash := strings.Index(trimmed, "/")
	var firstSeg, rest string
	if slash < 0 {
		firstSeg = trimmed
		rest = ""
	} else {
		firstSeg = trimmed[:slash]
		rest = trimmed[slash:] // 保留前导 "/"
	}
	if !versionSegmentRe.MatchString(firstSeg) {
		return requestPath
	}
	return rest
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
