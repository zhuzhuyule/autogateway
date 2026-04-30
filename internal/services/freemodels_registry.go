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
	// view=all 比 view=free 多收录 trial 模型 (Gitee 体验模式 11 个)
	// 和 paid 模型. isFree 标志位决定是否免费, freeTier 区分 full/trial.
	// 拉 all 才能让前端正确识别这三态.
	freeModelsURL          = "https://ofind.cn/FreeModels/data/views/all/models.json"
	freeModelsRefreshEvery = 6 * time.Hour
	freeModelsCachePath    = "data/freemodels-cache.json"
	freeModelsHTTPTimeout  = 15 * time.Second
)

// FreeModelMeta is the subset of fields we expose to the rest of the app.
// Field tags match the upstream JSON exactly; unused fields stay off the struct.
type FreeModelMeta struct {
	Provider         string                 `json:"provider"`
	ModelID          string                 `json:"modelId"`
	Name             string                 `json:"name"`
	IsFree           bool                   `json:"isFree"`
	IsExperienceable bool                   `json:"isExperienceable"` // gitee 体验额度 (可体验非完全免费)
	BillingMode      string                 `json:"billingMode"`      // "free" | "trial" | "paid"
	FreeTier         string                 `json:"freeTier"`         // "full" | "trial" | "none" | ""
	FreeKind         string                 `json:"freeKind"`         // "permanent" | "rate-limited" | ...
	ContextSize      int                    `json:"contextSize"`
	ContextLabel     string                 `json:"contextLabel"`
	Tier             string                 `json:"tier"`  // "small" | "medium" | "large"
	Speed            string                 `json:"speed"` // "fast" | "balanced" | "slow"
	IsReasoning      bool                   `json:"isReasoning"`
	IsMultimodal     bool                   `json:"isMultimodal"`
	HasToolUse       bool                   `json:"hasToolUse"`
	PriceInput       float64                `json:"priceInput"`
	PriceOutput      float64                `json:"priceOutput"`
	ModelFamily      string                 `json:"modelFamily"`
	Aliases          []string               `json:"aliases"` // 跨 provider 同名
	Tags             []string               `json:"tags"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"` // provider 原生 meta, 含 gitee 的 isExperienceable / isFullyFree / freeUse 等
}

// freeModelsEnvelope is the **outward** shape we serve to the frontend
// (kept camelCase + legacy field names so frontend stays decoupled from
// upstream schema drift). 内部解析见 upstreamEnvelope.
type freeModelsEnvelope struct {
	View        string          `json:"view"`
	UpdatedAt   string          `json:"updatedAt"`
	TotalModels int             `json:"totalModels"`
	Models      []FreeModelMeta `json:"models"`
}

// upstreamEnvelope mirrors the OpenAI-compatible schema served by
// https://ofind.cn/FreeModels/data/views/all/models.json — switched to
// snake_case + {object, total, data} in 2026-04. We parse it here and
// remap into our stable FreeModelMeta shape so any further upstream
// schema changes only touch this file.
type upstreamEnvelope struct {
	Object    string          `json:"object"`
	View      string          `json:"view"`
	UpdatedAt string          `json:"updated_at"`
	Total     int             `json:"total"`
	Data      []upstreamModel `json:"data"`
}

type upstreamModel struct {
	ID            string   `json:"id"`              // "<provider>/<rawId>"
	ModelID       string   `json:"model_id"`        // 同 ID, 备用
	OwnedBy       string   `json:"owned_by"`        // provider 名
	Provider      string   `json:"provider"`
	Name          string   `json:"name"`
	ContextSize   int      `json:"context_size"`
	ContextLabel  string   `json:"context_label"`
	PriceCurrency string   `json:"price_currency"`
	PriceInput    float64  `json:"price_input"`
	PriceOutput   float64  `json:"price_output"`
	IsFree        bool     `json:"is_free"`
	FreeMechanism *string  `json:"free_mechanism"` // permanent / rate-limited / preview / trial-credits / daily-tokens / null
	TrialScope    string   `json:"trial_scope"`    // "all" / "specific" / "none"
	IsReasoning   bool     `json:"is_reasoning"`
	IsMultimodal  bool     `json:"is_multimodal"`
	HasToolUse    bool     `json:"has_tool_use"`
	ModelFamily   string   `json:"model_family"`
	Aliases       []string `json:"aliases"`
	Tags          []string `json:"tags"`
	Tier          string   `json:"tier"`
	Speed         string   `json:"speed"`
}

// FreeModelsRegistry holds an in-memory copy of the upstream registry,
// indexed by both `(provider, modelId)` and bare `modelId` (lowercase).
type FreeModelsRegistry struct {
	mu          sync.RWMutex
	envelope    freeModelsEnvelope
	byProvMod   map[string]*FreeModelMeta // key: "<provider>/<lower-modelId>"
	byModelOnly map[string][]*FreeModelMeta
	stopCh      chan struct{}
	httpClient  *http.Client
}

func NewFreeModelsRegistry() *FreeModelsRegistry {
	return &FreeModelsRegistry{
		byProvMod:   make(map[string]*FreeModelMeta),
		byModelOnly: make(map[string][]*FreeModelMeta),
		stopCh:      make(chan struct{}),
		httpClient:  &http.Client{Timeout: freeModelsHTTPTimeout},
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
// (snake_case + {object, total, data} 包装) 转换成我们对外稳定的
// camelCase envelope. 上游 schema 漂移只需改这一个函数.
func parseUpstream(body []byte) (freeModelsEnvelope, error) {
	var up upstreamEnvelope
	if err := json.Unmarshal(body, &up); err != nil {
		return freeModelsEnvelope{}, err
	}
	out := freeModelsEnvelope{
		View:        up.View,
		UpdatedAt:   up.UpdatedAt,
		TotalModels: up.Total,
		Models:      make([]FreeModelMeta, 0, len(up.Data)),
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
		modelID := u.ModelID
		if modelID == "" {
			modelID = u.ID
		}
		provider := u.Provider
		if provider == "" {
			provider = u.OwnedBy
		}
		out.Models = append(out.Models, FreeModelMeta{
			Provider:         provider,
			ModelID:          modelID,
			Name:             u.Name,
			IsFree:           isFree,
			IsExperienceable: isExperienceable,
			BillingMode:      "", // 上游已不再提供, 留空
			FreeTier:         freeTier,
			FreeKind:         freeKind,
			ContextSize:      u.ContextSize,
			ContextLabel:     u.ContextLabel,
			Tier:             u.Tier,
			Speed:            u.Speed,
			IsReasoning:      u.IsReasoning,
			IsMultimodal:     u.IsMultimodal,
			HasToolUse:       u.HasToolUse,
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
	r.mu.Lock()
	r.envelope = env
	r.byProvMod = byProvMod
	r.byModelOnly = byModelOnly
	r.mu.Unlock()
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
	}
	if list, ok := r.byModelOnly[strings.ToLower(modelID)]; ok && len(list) > 0 {
		return list[0]
	}
	return nil
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
