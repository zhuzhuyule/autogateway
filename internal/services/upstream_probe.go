package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// probeVersionTailRe 匹配 baseUrl 末尾的版本段, 如 /v1 / /v1beta / /v2 — 在
// 拼 probe path 前剥掉, 避免 https://ai.gitee.com/v1 + /v1/models 拼成
// https://ai.gitee.com/v1/v1/models 全协议 404.
var probeVersionTailRe = regexp.MustCompile(`/v\d+(?:beta\d*)?$`)

// ProbeResult is what /api/upstream/probe returns.
type ProbeResult struct {
	ChannelType   string `json:"channel_type"`   // openai | anthropic | gemini
	VersionPrefix string `json:"version_prefix"` // /v1 or /v1beta
	NormalizedURL string `json:"normalized_url"` // user-facing canonical base URL
	StatusCode    int    `json:"status_code"`    // upstream status (often 401/403, that's OK)
	LatencyMs     int64  `json:"latency_ms"`
}

var probeClient = &http.Client{Timeout: 3 * time.Second}

// ProbeUpstream fans out three HEAD-equivalent GETs to the OpenAI, Anthropic
// and Gemini model-list endpoints and returns the first that responds with a
// non-network error. 401 / 403 count as "endpoint exists" — we just have no
// auth credentials. We deliberately do NOT require a successful 2xx because
// that would make probing unauthenticated upstreams impossible.
//
// Many Chinese providers (智谱/bigmodel, deepseek 等) 同时实现了 OpenAI 和
// Anthropic 兼容协议, 三个 probe 都会成功. 此时若调用方已经选定 channel_type,
// 应通过 prefer 显式优先返回该协议结果, 而不是沿用全局 rank 把 anthropic 先于
// openai 错误地匹配上去.
func ProbeUpstream(ctx context.Context, rawURL, prefer string) (*ProbeResult, error) {
	// "#" 是用户级 escape 标记 (持久化进库), 探测时不参与, 否则 url.Parse 会把 "#"
	// 后内容当 fragment 丢掉后拼出 ".../foo//v1/models" 的双斜杠.
	cleaned := strings.TrimRight(strings.TrimRight(rawURL, "#"), "/")
	u, err := url.Parse(cleaned)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http/https probing is allowed, got %q", u.Scheme)
	}
	base := u.String()
	// 剥掉 baseUrl 末尾的版本段 (/v1, /v1beta, /v2 ...), 让每个协议 probe 自己
	// 拼回需要的版本前缀, 避免双 /v1 等错误. NormalizedURL 仍返回用户原始 base.
	probeBase := probeVersionTailRe.ReplaceAllString(base, "")

	type attempt struct {
		channel string
		path    string
		header  map[string]string
	}
	attempts := []attempt{
		{"anthropic", "/v1/messages", map[string]string{"anthropic-version": "2023-06-01"}},
		{"gemini", "/v1beta/models", nil},
		{"openai", "/v1/models", nil},
	}

	results := make(chan outcome, len(attempts))
	var wg sync.WaitGroup
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	for _, a := range attempts {
		a := a
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- runOne(probeCtx, probeBase, a.channel, a.path, a.header)
		}()
	}
	go func() { wg.Wait(); close(results) }()

	hits := make(map[string]*ProbeResult)
	for r := range results {
		if r.err != nil || r.res == nil {
			continue
		}
		r.res.NormalizedURL = base
		hits[r.res.ChannelType] = r.res
	}
	if prefer != "" && hits[prefer] != nil {
		return hits[prefer], nil
	}
	// Pick highest-ranked remaining hit: anthropic > gemini > openai.
	// anthropic 排首位是为了识别真实 api.anthropic.com (它的 /v1/models 也返回 401,
	// 否则会被错认为 openai). 多协议 provider (智谱等) 通过 prefer 显式指定 channel_type
	// 来覆盖, 不依赖默认 rank.
	rank := []string{"anthropic", "gemini", "openai"}
	for _, ch := range rank {
		if hits[ch] != nil {
			return hits[ch], nil
		}
	}
	// 没有任何协议命中 — 不是错误, 而是"未识别". 返回 200 + 空 channel_type,
	// 让前端 UI 显示 ⚠ unknown, 避免 dev tools 红色 502 + 全局 toast.
	// 真异常 (无效 URL / 非法 scheme) 仍走 error 路径返回给上层.
	return &ProbeResult{NormalizedURL: base}, nil
}

func runOne(ctx context.Context, base, channel, path string, headers map[string]string) outcome {
	endpoint := base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return outcome{nil, err}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := probeClient.Do(req)
	if err != nil {
		return outcome{nil, err}
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return outcome{nil, fmt.Errorf("%d", resp.StatusCode)}
	}
	prefix := "/v1"
	if strings.HasPrefix(path, "/v1beta") {
		prefix = "/v1beta"
	}
	return outcome{&ProbeResult{
		ChannelType:   channel,
		VersionPrefix: prefix,
		NormalizedURL: base,
		StatusCode:    resp.StatusCode,
		LatencyMs:     time.Since(start).Milliseconds(),
	}, nil}
}

// ProbeUpstreamModels 用临时 base_url + key 拉上游模型清单。channelType 决定
// 端点路径与响应解析格式。复用本文件 base_url 规整逻辑(剥末尾版本段)以及
// upstream_models.go 中已有的 fetch*Models 解析函数。
func ProbeUpstreamModels(ctx context.Context, baseURL, channelType, key string) ([]string, error) {
	// 规整 base_url: 去尾部 "/" 和 "#" 转义标记, 再剥版本尾段 (/v1, /v1beta, ...)
	cleaned := strings.TrimRight(strings.TrimRight(baseURL, "#"), "/")
	u, err := url.Parse(cleaned)
	if err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http/https is allowed, got %q", u.Scheme)
	}
	// 剥版本尾段, 避免 /v1/v1/models 双重拼接
	probeBase := probeVersionTailRe.ReplaceAllString(u.String(), "")

	// 用 upstream_models.go 的独立 fetch 函数, 传入规整后的 probeBase.
	// 这些函数内部再通过 joinModelsPath 追加正确的路径, 所以 probeBase 不带版本.
	switch channelType {
	case "openai", "openai-response":
		return fetchOpenAIModels(ctx, probeBase, key)
	case "gemini":
		return fetchGeminiModels(ctx, probeBase, key)
	case "anthropic":
		return fetchAnthropicModels(ctx, probeBase, key)
	default:
		// 未知类型: 尝试 OpenAI 兼容端点作为 best-effort
		return fetchOpenAIModels(ctx, probeBase, key)
	}
}

type outcome struct {
	res *ProbeResult
	err error
}
