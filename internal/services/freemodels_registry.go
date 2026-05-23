// Package services - FreeModels Registry consumes the open data hub at
// https://ofind.cn/FreeModels/data/views/free/models.json (project:
// github.com/zhuzhuyule/FreeModels) to centrally answer "is this model
// free?" / "what tier?" / "which other providers serve the same model
// family?" — replacing the hand-curated freeProviders.ts FREE_MODELS list.
//
// The CDN is refreshed daily at 02:00 UTC; we pull every 6h, cache in
// memory + on-disk (data/freemodels-cache.json) so a CDN outage doesn't
// blank the registry on restart.
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	// 选 data/models.json 而不是 data/views/all/models.json:
	// 两者 data 完全相同 (全量 563 条), 但只有聚合根 models.json 顶层带
	// `providers` map (含每家 provider 的 apiBaseUrl / channelType 等元
	// 数据), 视图文件没有. 我们要用 providerMeta 做 host 反查关联,
	// 必须拉聚合根.
	//
	// view=all 语义保留: isFree 区分 free/paid, freeTier 区分 full/trial,
	// 三态识别全在解析层完成 (parseUpstream).
	freeModelsURL          = "https://ofind.cn/FreeModels/data/models.json"
	freeModelsRefreshEvery = 6 * time.Hour
	freeModelsCachePath    = "data/freemodels-cache.json"
	freeModelsHTTPTimeout  = 15 * time.Second
)

// FreeModelMeta is the subset of fields we expose to the rest of the app.
// Field tags match the upstream JSON exactly; unused fields stay off the struct.
//
// 2026-05-19 schema upgrade: 上游 ofind.cn 加了一批新字段, 全量纳入. 旧字段
// 保持不变 (向后兼容). 字段覆盖率不同 (capabilities/performance_level/estimated_latency
// 在 697/697 模型都有, description 645/697, parameter_count 仅 261/697 等),
// 缺失值是零值 (空串/0/nil), 调用方自行判断.
type FreeModelMeta struct {
	Provider         string                 `json:"provider"`
	ModelID          string                 `json:"modelId"`
	Name             string                 `json:"name"`
	Description      string                 `json:"description,omitempty"` // 短描述, e.g. "语言模型" / "用于代码生成的模型"
	Created          int64                  `json:"created,omitempty"`     // Unix ts, 上游模型创建时间 (排序/"新"标签)
	IsFree           bool                   `json:"isFree"`
	IsExperienceable bool                   `json:"isExperienceable"` // gitee 体验额度 (可体验非完全免费)
	BillingMode      string                 `json:"billingMode"`      // "free" | "trial" | "paid"
	FreeTier         string                 `json:"freeTier"`         // "full" | "trial" | "none" | ""
	FreeKind         string                 `json:"freeKind"`         // "permanent" | "rate-limited" | ...
	FreeQuota        *FreeQuotaMeta         `json:"freeQuota,omitempty"`
	ContextSize      int                    `json:"contextSize"`
	ContextLabel     string                 `json:"contextLabel"`
	Tier             string                 `json:"tier"`             // "small" | "medium" | "large"
	Speed            string                 `json:"speed"`            // "fast" | "standard" | "premium"
	PerformanceLevel string                 `json:"performanceLevel"` // "entry" | "mid" | "high"
	EstimatedLatency string                 `json:"estimatedLatency"` // "< 1s" | "1-3s" | "3-10s"
	IsReasoning      bool                   `json:"isReasoning"`
	IsMultimodal     bool                   `json:"isMultimodal"`
	HasToolUse       bool                   `json:"hasToolUse"`
	Capabilities     []string               `json:"capabilities,omitempty"` // "chat" / "text-generation" / "tool-use" / "vision" / ...
	UseCase          []string               `json:"useCase,omitempty"`      // "conversation" / "q&a" / "code-completion" / ...
	ParameterCount   int64                  `json:"parameterCount,omitempty"`
	ModelVariant     string                 `json:"modelVariant,omitempty"`
	Quantization     string                 `json:"quantization,omitempty"`
	PriceInput       float64                `json:"priceInput"`
	PriceOutput      float64                `json:"priceOutput"`
	ModelFamily      string                 `json:"modelFamily"`
	Aliases          []string               `json:"aliases"` // 跨 provider 同名
	Tags             []string               `json:"tags"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"` // provider 原生 meta, 含 gitee 的 isExperienceable / isFullyFree / freeUse 等
}

// FreeQuotaMeta 是 free_quota 字段的结构 (上游 2026-05 起为 dict, 主要含 notes 描述).
// 之前是 string, 解析时按 dict 兼容; 缺时 nil (而非空 struct), 避免无意义 JSON {}.
type FreeQuotaMeta struct {
	Notes string `json:"notes,omitempty"`
}

// FreeProviderMeta 是单个 provider 的元数据 (从 FreeModels providerMeta
// map 解析而来). 让 api-center 能直接拿到 baseUrl / channelType, 不再
// 在前端 freeProviders.ts 重复维护一份.
type FreeProviderMeta struct {
	Name          string `json:"name"`
	DisplayName   string `json:"displayName"`
	Website       string `json:"website,omitempty"`
	LogoURL       string `json:"logoUrl,omitempty"`
	APIBaseURL    string `json:"apiBaseUrl,omitempty"`    // 反查锚点; 派生 host 跟用户分组 upstream 比较
	ChannelType   string `json:"channelType,omitempty"`   // openai / anthropic / gemini
	PriceCurrency string `json:"priceCurrency,omitempty"` // USD / CNY (原本 per-model, 已上提)
	PriceUnit     string `json:"priceUnit,omitempty"`     // per_million_tokens
}

// freeModelsEnvelope is the **outward** shape we serve to the frontend
// (kept camelCase + legacy field names so frontend stays decoupled from
// upstream schema drift). 内部解析见 upstreamEnvelope.
type freeModelsEnvelope struct {
	View         string                      `json:"view"`
	UpdatedAt    string                      `json:"updatedAt"`
	TotalModels  int                         `json:"totalModels"`
	Models       []FreeModelMeta             `json:"models"`
	ProviderMeta map[string]FreeProviderMeta `json:"providerMeta,omitempty"`
}

// upstreamEnvelope mirrors the OpenAI-compatible schema served by
// https://ofind.cn/FreeModels/data/models.json — snake_case +
// {object, total, data, providers}. We parse it here and remap into
// our stable FreeModelMeta shape so any further upstream schema changes
// only touch this file.
//
// 2026-05 schema 调整:
//   - model 上的 object / model_id / owned_by / price_currency / price_unit 已上游删除
//   - providerMeta 顶层新增 apiBaseUrl / channelType / priceCurrency / priceUnit
type upstreamEnvelope struct {
	Object    string                          `json:"object"`
	View      string                          `json:"view"`
	UpdatedAt string                          `json:"updated_at"`
	Total     int                             `json:"total"`
	Providers map[string]upstreamProviderMeta `json:"providers"`
	Data      []upstreamModel                 `json:"data"`
}

type upstreamProviderMeta struct {
	Name          string `json:"name"`
	DisplayName   string `json:"displayName"`
	Website       string `json:"website"`
	LogoURL       string `json:"logoUrl"`
	APIBaseURL    string `json:"apiBaseUrl"`
	ChannelType   string `json:"channelType"`
	PriceCurrency string `json:"priceCurrency"`
	PriceUnit     string `json:"priceUnit"`
}

type upstreamModel struct {
	ID               string         `json:"id"`       // "<provider>/<rawId>"
	Provider         string         `json:"provider"` // owned_by 已删除 (≡ provider)
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	Created          int64          `json:"created"` // Unix ts
	ContextSize      int            `json:"context_size"`
	ContextLabel     string         `json:"context_label"`
	PriceInput       float64        `json:"price_input"`
	PriceOutput      float64        `json:"price_output"`
	IsFree           bool           `json:"is_free"`
	FreeMechanism    *string        `json:"free_mechanism"` // permanent / rate-limited / preview / trial-credits / daily-tokens / null
	FreeQuota        *FreeQuotaMeta `json:"free_quota"`     // {notes: "..."} | null
	TrialScope       string         `json:"trial_scope"`    // "all" / "specific" / "none"
	IsReasoning      bool           `json:"is_reasoning"`
	IsMultimodal     bool           `json:"is_multimodal"`
	HasToolUse       bool           `json:"has_tool_use"`
	Capabilities     []string       `json:"capabilities"`      // "chat" / "tool-use" / "vision" / "reasoning" / ...
	UseCase          []string       `json:"use_case"`          // "conversation" / "q&a" / "code-completion" / ...
	PerformanceLevel string         `json:"performance_level"` // "entry" / "mid" / "high"
	EstimatedLatency string         `json:"estimated_latency"` // "< 1s" / "1-3s" / "3-10s"
	ParameterCount   int64          `json:"parameter_count"`
	ModelVariant     string         `json:"model_variant"`
	Quantization     string         `json:"quantization"`
	ModelFamily      string         `json:"model_family"`
	Aliases          []string       `json:"aliases"`
	Tags             []string       `json:"tags"`
	Tier             string         `json:"tier"`
	Speed            string         `json:"speed"`
}

// FreeModelsRegistry holds an in-memory copy of the upstream registry,
// indexed by `(provider, modelId)`, bare `modelId`, and api host (for
// reverse-lookup from a user's group baseUrl → providerId).
type FreeModelsRegistry struct {
	mu             sync.RWMutex
	envelope       freeModelsEnvelope
	byProvMod      map[string]*FreeModelMeta    // key: "<provider>/<lower-modelId>"
	byModelOnly    map[string][]*FreeModelMeta
	byHost         map[string]string            // host (lowercase) → providerId
	providerByName map[string]*FreeProviderMeta // providerId → meta
	stopCh         chan struct{}
	httpClient     *http.Client
}

func NewFreeModelsRegistry() *FreeModelsRegistry {
	return &FreeModelsRegistry{
		byProvMod:      make(map[string]*FreeModelMeta),
		byModelOnly:    make(map[string][]*FreeModelMeta),
		byHost:         make(map[string]string),
		providerByName: make(map[string]*FreeProviderMeta),
		stopCh:         make(chan struct{}),
		httpClient:     &http.Client{Timeout: freeModelsHTTPTimeout},
	}
}

// Start kicks off an immediate fetch (best-effort: cache → network) then a
// background goroutine that refreshes every 6h. Non-fatal: if both cache
// and network miss, the registry stays empty and callers fall back to
// frontend's static list.
func (r *FreeModelsRegistry) Start(ctx context.Context) {
	if err := r.loadFromDisk(); err != nil {
		logrus.WithError(err).Debug("freemodels: no on-disk cache (first run?)")
	} else {
		logrus.Infof("freemodels: loaded %d models from disk cache", r.snapshotCount())
	}
	go r.refreshLoop(ctx)
}

func (r *FreeModelsRegistry) Stop() {
	close(r.stopCh)
}

func (r *FreeModelsRegistry) refreshLoop(ctx context.Context) {
	// Initial fetch — try once on startup, then on the regular cadence.
	if err := r.fetchAndStore(ctx); err != nil {
		logrus.WithError(err).Warn("freemodels: initial fetch failed (will retry on schedule)")
	}
	t := time.NewTicker(freeModelsRefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-t.C:
			if err := r.fetchAndStore(ctx); err != nil {
				logrus.WithError(err).Warn("freemodels: scheduled fetch failed")
			}
		}
	}
}

func (r *FreeModelsRegistry) fetchAndStore(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, freeModelsHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, freeModelsURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "autogateway-freemodels-registry/1.0")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upstream %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MiB cap
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	env, err := parseUpstream(body)
	if err != nil {
		return fmt.Errorf("parse upstream: %w", err)
	}
	r.replaceIndex(env)
	// 持久化的是 normalized envelope (camelCase, 跟前端保持稳定),
	// 不是 raw upstream — 避免下次启动 schema 漂移时旧 cache 解析失败.
	cached, _ := json.Marshal(env)
	if err := r.saveToDisk(cached); err != nil {
		logrus.WithError(err).Warn("freemodels: failed to write disk cache (continuing)")
	}
	logrus.Infof("freemodels: fetched %d models from upstream", env.TotalModels)
	return nil
}

// parseUpstream 解析 FreeModels CDN 当前的 OpenAI-compat schema
// (snake_case + {object, total, data, providers} 包装) 转换成我们对外
// 稳定的 camelCase envelope. 上游 schema 漂移只需改这一个函数.
func parseUpstream(body []byte) (freeModelsEnvelope, error) {
	var up upstreamEnvelope
	if err := json.Unmarshal(body, &up); err != nil {
		return freeModelsEnvelope{}, err
	}
	out := freeModelsEnvelope{
		View:         up.View,
		UpdatedAt:    up.UpdatedAt,
		TotalModels:  up.Total,
		Models:       make([]FreeModelMeta, 0, len(up.Data)),
		ProviderMeta: make(map[string]FreeProviderMeta, len(up.Providers)),
	}
	for name, p := range up.Providers {
		out.ProviderMeta[name] = FreeProviderMeta{
			Name:          p.Name,
			DisplayName:   p.DisplayName,
			Website:       p.Website,
			LogoURL:       p.LogoURL,
			APIBaseURL:    p.APIBaseURL,
			ChannelType:   p.ChannelType,
			PriceCurrency: p.PriceCurrency,
			PriceUnit:     p.PriceUnit,
		}
	}
	for _, u := range up.Data {
		// is_free + free_mechanism + trial_scope 决定 freeTier:
		//   永久免费 / 限速 / 预览 → "full"
		//   trial-credits / daily-tokens (longcat每日额度, nvidia credits) → "trial"
		//   付费但 trial_scope=all (Gitee 145 个体验模型) → "trial" + isFree=true
		//   其它 → ""
		isFree := u.IsFree
		freeTier := ""
		freeKind := ""
		isExperienceable := false
		if u.FreeMechanism != nil {
			freeKind = *u.FreeMechanism
			switch freeKind {
			case "permanent", "rate-limited", "preview":
				freeTier = "full"
			case "trial-credits", "daily-tokens":
				freeTier = "trial"
			}
		}
		// is_free=false 但所有用户有体验额度 (Gitee) — 标记为 trial 并视为可用
		if !isFree && u.TrialScope == "all" {
			isFree = true
			freeTier = "trial"
			isExperienceable = true
		}
		// model_id / owned_by 已从上游 schema 删除 (与 id / provider 100% 重复).
		// 直接用 id 作为 modelID, provider 作为 provider.
		out.Models = append(out.Models, FreeModelMeta{
			Provider:         u.Provider,
			ModelID:          u.ID,
			Name:             u.Name,
			Description:      u.Description,
			Created:          u.Created,
			IsFree:           isFree,
			IsExperienceable: isExperienceable,
			BillingMode:      "", // 上游已不再提供, 留空
			FreeTier:         freeTier,
			FreeKind:         freeKind,
			FreeQuota:        u.FreeQuota,
			ContextSize:      u.ContextSize,
			ContextLabel:     u.ContextLabel,
			Tier:             u.Tier,
			Speed:            u.Speed,
			PerformanceLevel: u.PerformanceLevel,
			EstimatedLatency: u.EstimatedLatency,
			IsReasoning:      u.IsReasoning,
			IsMultimodal:     u.IsMultimodal,
			HasToolUse:       u.HasToolUse,
			Capabilities:     u.Capabilities,
			UseCase:          u.UseCase,
			ParameterCount:   u.ParameterCount,
			ModelVariant:     u.ModelVariant,
			Quantization:     u.Quantization,
			PriceInput:       u.PriceInput,
			PriceOutput:      u.PriceOutput,
			ModelFamily:      u.ModelFamily,
			Aliases:          u.Aliases,
			Tags:             u.Tags,
			Metadata:         nil,
		})
	}
	return out, nil
}

func (r *FreeModelsRegistry) replaceIndex(env freeModelsEnvelope) {
	// freeTier / isFree / freeKind normalize 已在 parseUpstream 完成,
	// 这里只负责建索引.
	byProvMod := make(map[string]*FreeModelMeta, len(env.Models))
	byModelOnly := make(map[string][]*FreeModelMeta)
	for i := range env.Models {
		m := &env.Models[i]
		key := provModKey(m.Provider, m.ModelID)
		byProvMod[key] = m
		bare := strings.ToLower(m.ModelID)
		byModelOnly[bare] = append(byModelOnly[bare], m)
	}
	// 从 providerMeta.apiBaseUrl 派生 host → providerId 索引, 让前端反查
	// 用户分组 upstream URL 时能在 O(1) 命中. 缺 apiBaseUrl 的 provider
	// (e.g. cloudflare 的 account_id 路径) 不进表, 由前端 fallback 走
	// freeProviders.ts.upstreamHosts 兜底.
	byHost := make(map[string]string, len(env.ProviderMeta))
	providerByName := make(map[string]*FreeProviderMeta, len(env.ProviderMeta))
	for name, meta := range env.ProviderMeta {
		m := meta
		providerByName[name] = &m
		host := extractHost(meta.APIBaseURL)
		if host != "" {
			byHost[host] = name
		}
	}
	r.mu.Lock()
	r.envelope = env
	r.byProvMod = byProvMod
	r.byModelOnly = byModelOnly
	r.byHost = byHost
	r.providerByName = providerByName
	r.mu.Unlock()
}

// extractHost parses a URL and returns its lowercase host, or "" on parse
// failure / empty input. Mirrors the frontend extractHost helper.
func extractHost(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// 简单解析: 跳过 scheme, 切到首个 '/' 或 '?' 之前.
	s := rawURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	for i, c := range s {
		if c == '/' || c == '?' || c == '#' {
			s = s[:i]
			break
		}
	}
	// 去掉端口
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

func (r *FreeModelsRegistry) loadFromDisk() error {
	body, err := os.ReadFile(freeModelsCachePath)
	if err != nil {
		return err
	}
	var env freeModelsEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("parse cached json: %w", err)
	}
	r.replaceIndex(env)
	return nil
}

func (r *FreeModelsRegistry) saveToDisk(body []byte) error {
	if err := os.MkdirAll(filepath.Dir(freeModelsCachePath), 0o755); err != nil {
		return err
	}
	tmp := freeModelsCachePath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, freeModelsCachePath)
}

func (r *FreeModelsRegistry) snapshotCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.envelope.TotalModels
}

// IsFree reports whether `(provider, modelId)` is known to be free.
// A miss returns (false, false): caller decides whether to fall back.
func (r *FreeModelsRegistry) IsFree(provider, modelID string) (isFree bool, found bool) {
	if modelID == "" {
		return false, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if provider != "" {
		if m, ok := r.byProvMod[provModKey(provider, modelID)]; ok {
			return m.IsFree, true
		}
	}
	if list, ok := r.byModelOnly[strings.ToLower(modelID)]; ok && len(list) > 0 {
		// 任一 provider 标了 free 就视为 free (跨 provider 别名)
		for _, m := range list {
			if m.IsFree {
				return true, true
			}
		}
		return false, true
	}
	return false, false
}

// Lookup returns the meta for `(provider, modelId)` or nil.
//
// 严格契约 (避免跨 provider 误匹配):
//   - provider != "" → 只在 byProvMod 精确匹配, 找不到返回 nil. 不再 fallback
//     到 byModelOnly 借另一个 provider 的元数据 (旧逻辑会把 "OpenRouter 上的
//     付费 model" 错标成 free, 因为某个其他 provider 同名 model 是免费的).
//   - provider == "" → byModelOnly 跨 provider 查 (调用方明确说"我不知道
//     provider, 给我猜一个"), 返回任意一条 (list[0]).
func (r *FreeModelsRegistry) Lookup(provider, modelID string) *FreeModelMeta {
	if modelID == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if provider != "" {
		if m, ok := r.byProvMod[provModKey(provider, modelID)]; ok {
			return m
		}
		return nil
	}
	if list, ok := r.byModelOnly[strings.ToLower(modelID)]; ok && len(list) > 0 {
		return list[0]
	}
	return nil
}

// ListModelIDsByProvider 返回该 provider 在 registry 中的所有 bare modelId
// (剥掉 "<provider>/" 前缀). 用于上游无 /v1/models 端点的 provider
// (e.g. iFlytek Spark) 的回退兜底. providerId 大小写不敏感.
func (r *FreeModelsRegistry) ListModelIDsByProvider(providerID string) []string {
	if providerID == "" {
		return nil
	}
	target := strings.ToLower(providerID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, 16)
	prefix := target + "/"
	for _, m := range r.envelope.Models {
		if strings.ToLower(m.Provider) != target {
			continue
		}
		id := m.ModelID
		// FreeModels 部分 provider (groq/cerebras/bigmodel/openrouter/xinghuo/
		// xingchen/longcat) 的 modelId 带 "<provider>/" 前缀, 上游 /v1/models
		// 返回的是裸 id, 这里统一剥掉以保持下游 group.available_models 一致.
		if strings.HasPrefix(strings.ToLower(id), prefix) {
			id = id[len(prefix):]
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

// LookupProviderByHost reverse-resolves a host (e.g. "api.groq.com") to the
// FreeModels provider id ("groq") + its meta. Returns ("", nil, false) on miss.
// Frontend findProviderByUpstreamUrl uses this as the primary lookup before
// falling back to local freeProviders.ts.upstreamHosts.
func (r *FreeModelsRegistry) LookupProviderByHost(host string) (string, *FreeProviderMeta, bool) {
	if host == "" {
		return "", nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byHost[strings.ToLower(host)]
	if !ok {
		return "", nil, false
	}
	meta, mok := r.providerByName[id]
	if !mok {
		return id, nil, true
	}
	return id, meta, true
}

// LookupProviderMeta returns the meta for a known provider id, or nil.
func (r *FreeModelsRegistry) LookupProviderMeta(providerID string) *FreeProviderMeta {
	if providerID == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providerByName[providerID]
}

// Snapshot returns the entire envelope (for the /api/freemodels/registry handler).
// Callers must not mutate the returned envelope.
func (r *FreeModelsRegistry) Snapshot() freeModelsEnvelope {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.envelope
}

// SnapshotJSON returns the cached upstream JSON bytes verbatim (or error
// if registry is empty). This lets the handler stream the original bytes
// instead of re-marshalling.
func (r *FreeModelsRegistry) SnapshotJSON() ([]byte, error) {
	r.mu.RLock()
	env := r.envelope
	r.mu.RUnlock()
	if env.TotalModels == 0 {
		return nil, errors.New("registry not initialized")
	}
	return json.Marshal(env)
}

func provModKey(provider, modelID string) string {
	return strings.ToLower(provider) + "/" + strings.ToLower(modelID)
}
