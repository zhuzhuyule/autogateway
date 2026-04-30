package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

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
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http/https probing is allowed, got %q", u.Scheme)
	}
	base := u.String()

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
			results <- runOne(probeCtx, base, a.channel, a.path, a.header)
		}()
	}
	go func() { wg.Wait(); close(results) }()

	hits := make(map[string]*ProbeResult)
	for r := range results {
		if r.err != nil || r.res == nil {
			continue
		}
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
	return nil, fmt.Errorf("no known upstream protocol responded at %s", base)
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

type outcome struct {
	res *ProbeResult
	err error
}
