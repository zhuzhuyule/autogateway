package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"autogateway/internal/models"
)

// versionPathRe matches a trailing /vN segment so we don't double-append /v1.
var versionPathRe = regexp.MustCompile(`/v\d+(?:beta\d*)?$`)

// joinModelsPath appends the OpenAI-style models endpoint to baseURL,
// avoiding /v1/v1/models when baseURL already ends with /vN.
func joinModelsPath(baseURL, fallbackVersionPath string) string {
	base := strings.TrimRight(baseURL, "/")
	u, err := url.Parse(base)
	if err == nil && versionPathRe.MatchString(u.Path) {
		return base + "/models"
	}
	return base + fallbackVersionPath
}

// FetchUpstreamModels 调用 group 第一个 upstream 的 /v1/models (或 channel 等价端点)
// 返回模型 ID 列表. 不支持的 channel 返回错误.
func FetchUpstreamModels(ctx context.Context, group *models.Group, apiKey string) ([]string, error) {
	if group == nil {
		return nil, fmt.Errorf("group is nil")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("api key is empty, cannot fetch models")
	}

	baseURL, err := firstUpstreamURL(group)
	if err != nil {
		return nil, err
	}

	switch group.ChannelType {
	case "openai", "openai-response":
		return fetchOpenAIModels(ctx, baseURL, apiKey)
	case "anthropic":
		return fetchAnthropicModels(ctx, baseURL, apiKey)
	case "gemini":
		return fetchGeminiModels(ctx, baseURL, apiKey)
	default:
		return nil, fmt.Errorf("unsupported channel type for model fetch: %s", group.ChannelType)
	}
}

func firstUpstreamURL(group *models.Group) (string, error) {
	var defs []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(group.Upstreams, &defs); err != nil {
		return "", fmt.Errorf("invalid upstreams json: %w", err)
	}
	if len(defs) == 0 {
		return "", fmt.Errorf("group has no upstreams")
	}
	u, err := url.Parse(strings.TrimRight(defs[0].URL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid upstream url %q: %w", defs[0].URL, err)
	}
	return u.String(), nil
}

func fetchOpenAIModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	endpoint := joinModelsPath(baseURL, "/v1/models")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	body, err := doRequest(req)
	if err != nil {
		return nil, err
	}

	// OpenAI 兼容三种返回形态:
	//   1. { "data":   [{"id":"..."}] }   ← 标准 OpenAI / Groq / Cerebras
	//   2. { "models": [{"id":"..."}] }   ← 部分国内 provider
	//   3. [ {"id":"..."} ]                ← Together.ai 等裸数组
	out := make([]string, 0)

	// peek 第一个非空字符 — 区分 envelope (object) 还是裸数组
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		// 裸数组形态
		var arr []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &arr); err != nil {
			return nil, fmt.Errorf("parse openai models (array): %w", err)
		}
		for _, m := range arr {
			id := m.ID
			if id == "" {
				id = m.Name // 极少数 provider 用 name 字段
			}
			if id != "" {
				out = append(out, id)
			}
		}
		return out, nil
	}

	// envelope 形态
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse openai models: %w", err)
	}
	for _, m := range resp.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	for _, m := range resp.Models {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

func fetchAnthropicModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	endpoint := joinModelsPath(baseURL, "/v1/models")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Accept", "application/json")

	body, err := doRequest(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse anthropic models: %w", err)
	}
	out := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

func fetchGeminiModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	endpoint := joinModelsPath(baseURL, "/v1beta/models") + "?key=" + url.QueryEscape(apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	body, err := doRequest(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Models []struct {
			Name string `json:"name"` // "models/gemini-2.0-flash"
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse gemini models: %w", err)
	}
	out := make([]string, 0, len(resp.Models))
	for _, m := range resp.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		if id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

var modelFetchClient = &http.Client{Timeout: 15 * time.Second}

func doRequest(req *http.Request) ([]byte, error) {
	resp, err := modelFetchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2 MiB cap
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
