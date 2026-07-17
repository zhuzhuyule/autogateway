// Package errtriage 实现「LLM 错误归因兜底」(② 处理支柱)。
//
// 当内置规则(app_errors.Classify)判不出一个上游错误是「客户端请求错」还是
// 「key/账号错」(落到 CategoryUnknown)、且分组显式 opt-in 时,keypool 会经
// keypool.ErrorTriager 接口回调到这里,请一个便宜模型二次判定,避免把请求错
// 误算到 key 头上而熔断好 key。
//
// 保守约束:
//   - 默认关(EnableLLMErrorTriage=false),只对疑难错触发;
//   - 同种错按签名缓存,只问模型一次;
//   - 任何失败(未配置/取 key 失败/网络错/回词不可识别)一律 ok=false,
//     由调用方回退到「计入」的保守策略,绝不因归因服务不可用就放过坏 key;
//   - 递归防护:triage 分组自身的失败不再触发 triage。
package errtriage

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"autogateway/internal/channel"
	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"
	"autogateway/internal/ratelimit"
	"autogateway/internal/store"

	"github.com/sirupsen/logrus"
)

// cacheTTL 是归因结果的缓存有效期。同一签名的错误在此期间只问一次模型。
const cacheTTL = 6 * time.Hour

// GroupResolver 按名解析分组(由 *services.GroupManager 满足)。
type GroupResolver interface {
	GetGroupByName(name string) (*models.Group, error)
}

// KeySelector 从分组池里取一个可用 key(由 *keypool.KeyProvider 满足)。
type KeySelector interface {
	SelectKey(groupID uint, limits ratelimit.Limits) (*models.APIKey, error)
}

// Triager 实现 keypool.ErrorTriager。
type Triager struct {
	groups   GroupResolver
	keys     KeySelector
	channels *channel.Factory
	store    store.Store
}

// New 构造 Triager。
func New(groups GroupResolver, keys KeySelector, channels *channel.Factory, st store.Store) *Triager {
	return &Triager{groups: groups, keys: keys, channels: channels, store: st}
}

// ShouldCountAgainstKey 判定该失败是否应计入 key 失败。ok=false 表示无法判定,
// 调用方须回退保守。
func (t *Triager) ShouldCountAgainstKey(ctx context.Context, group *models.Group, statusCode int, message string) (count bool, ok bool) {
	cfg := group.EffectiveConfig
	triageGroupName := strings.TrimSpace(cfg.LLMErrorTriageGroup)
	triageModel := strings.TrimSpace(cfg.LLMErrorTriageModel)
	if triageGroupName == "" || triageModel == "" {
		return false, false // 未配置分组/模型 → 无法判定
	}
	// 递归防护:绝不对 triage 分组自身的失败再触发 triage。
	if group.Name == triageGroupName {
		return false, false
	}

	sig := signature(statusCode, message)
	cacheKey := "errtriage:sig:" + sig
	if raw, err := t.store.Get(cacheKey); err == nil && len(raw) > 0 {
		if cat, okp := app_errors.ParseCategory(string(raw)); okp {
			return cat.CountsAgainstKey(), true
		}
	}

	tg, err := t.groups.GetGroupByName(triageGroupName)
	if err != nil || tg == nil {
		logrus.WithField("triage_group", triageGroupName).Debug("errtriage: triage group not found, falling back")
		return false, false
	}

	cat, err := t.classifyViaLLM(ctx, tg, triageModel, statusCode, message)
	if err != nil {
		logrus.WithError(err).WithField("triage_group", triageGroupName).Debug("errtriage: LLM classify failed, falling back")
		return false, false
	}

	// 仅缓存成功判定(不缓存失败,失败可能是瞬态)。
	_ = t.store.Set(cacheKey, []byte(cat.String()), cacheTTL)
	logrus.WithFields(logrus.Fields{
		"status":   statusCode,
		"category": cat.String(),
		"count":    cat.CountsAgainstKey(),
	}).Debug("errtriage: LLM adjudicated error category")
	return cat.CountsAgainstKey(), true
}

// classifyViaLLM 用 triage 分组的一个 key 发一次 OpenAI 风格 chat,让模型给出归因。
// 只用 ChannelProxy 接口暴露的方法(BuildUpstreamURL/ModifyRequest/GetHTTPClient),
// 不走完整代理管线(避免递归/污染请求日志)。要求 triage 分组为 OpenAI 兼容。
func (t *Triager) classifyViaLLM(ctx context.Context, tg *models.Group, model string, statusCode int, message string) (app_errors.Category, error) {
	key, err := t.keys.SelectKey(tg.ID, ratelimit.Limits{})
	if err != nil {
		return app_errors.CategoryUnknown, fmt.Errorf("select key: %w", err)
	}
	ch, err := t.channels.GetChannel(tg)
	if err != nil {
		return app_errors.CategoryUnknown, fmt.Errorf("get channel: %w", err)
	}
	reqURL, err := ch.BuildUpstreamURL(&neturl.URL{Path: "/v1/chat/completions"}, tg.Name)
	if err != nil {
		return app_errors.CategoryUnknown, fmt.Errorf("build url: %w", err)
	}

	body, err := json.Marshal(map[string]any{
		"model":       model,
		"temperature": 0,
		"max_tokens":  16,
		"messages": []map[string]string{
			{"role": "system", "content": triagePrompt},
			{"role": "user", "content": fmt.Sprintf("HTTP status: %d\nError message: %s\n\nCategory:", statusCode, truncate(message, 1500))},
		},
	})
	if err != nil {
		return app_errors.CategoryUnknown, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return app_errors.CategoryUnknown, err
	}
	req.Header.Set("Content-Type", "application/json")
	ch.ModifyRequest(req, key, tg) // 注入 Authorization 等

	resp, err := ch.GetHTTPClient().Do(req)
	if err != nil {
		return app_errors.CategoryUnknown, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return app_errors.CategoryUnknown, fmt.Errorf("triage upstream status %d", resp.StatusCode)
	}

	content := extractContent(respBody)
	if content == "" {
		return app_errors.CategoryUnknown, fmt.Errorf("empty triage response")
	}
	cat, ok := app_errors.ParseCategory(content)
	if !ok {
		return app_errors.CategoryUnknown, fmt.Errorf("unrecognized category %q", truncate(content, 80))
	}
	return cat, nil
}

const triagePrompt = `You are an API error classifier for an LLM API gateway. Classify the upstream error into EXACTLY ONE category and reply with ONLY that category word, nothing else:
- key_error: the API key or account is at fault (invalid/expired/revoked key, no permission, out of quota or credits, billing issue, suspended account).
- request_error: the client's request itself is invalid (bad format, invalid/unsupported parameters or input, context too long, model not found).
- rate_limited: rate limiting or temporary capacity limit (429, too many requests, server overloaded).
- provider_error: an internal upstream failure (5xx, internal server error).`

// extractContent 从 OpenAI chat completion 响应里取 choices[0].message.content。
func extractContent(body []byte) string {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content)
}

// signature 把错误归一成一个稳定签名:剥掉数字(id/token 计数/时间戳会让同类错
// 看起来各不相同),再对 状态码+归一文本 取 sha1。让同种错只问模型一次。
func signature(statusCode int, message string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(message) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteByte('#')
		case r == '"' || r == '\'' || r == '`':
			// 剥引号,避免带引号的值影响签名
		default:
			b.WriteRune(r)
		}
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%d|%s", statusCode, b.String())))
	return fmt.Sprintf("%x", sum)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
