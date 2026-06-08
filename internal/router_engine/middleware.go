package router_engine

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"autogateway/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// AliasResolver is the interface routes use to look up a group name by id
// and (when checking exposed_models) the original group by name.
// Implemented by services.GroupManager.
type AliasResolver interface {
	GetGroupNameByID(id uint) (string, bool)
	GetGroupByName(name string) (*models.Group, error)
}

// Middleware swaps the request's group name based on the alias / auto /
// exact-name decision tree. Mounted on /proxy/:group_name and the system
// shortcut routes (/openai, /anthropic, /gemini).
//
// Body is read once, parsed for the `model` field, then re-armed so the
// downstream proxy handler can re-read it. Body rewriting (substituting
// the alias with the real model name) is delegated to the existing
// Group.ModelRedirectRules pipeline — alias targets can register a
// redirect mapping if substitution is needed.
//
// On 429 / non-2xx responses, future revisions will call
// Selector.MarkResponse so cooldown can engage. For now it's a no-op
// placeholder; the structure is in place so we can wire it without
// touching call sites.
func Middleware(s *Selector, resolver AliasResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || resolver == nil {
			c.Next()
			return
		}
		if !isChatCompletionsPath(c.Request.URL.Path) {
			c.Next()
			return
		}

		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Next()
			return
		}
		c.Request.Body.Close()
		// Re-arm body so the downstream proxy handler can read it.
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		var probe struct {
			Model    string          `json:"model"`
			Messages json.RawMessage `json:"messages"`
			Tools    json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(bodyBytes, &probe); err != nil {
			c.Next()
			return
		}
		model := strings.TrimSpace(probe.Model)
		// 没传 model 字段 → 默认走 auto 智能路由 (按 token 估算选 tier)
		if model == "" {
			model = "auto"
		}

		sessionKey, isMultiTurn := parseSession(bodyBytes, c.Request.Header)
		var stickyKey string
		var picked *Candidate
		if isMultiTurn && sessionKey != "" {
			stickyKey = stickyStoreKey(sessionKey, model)
			if cand := s.GetSticky(stickyKey); cand != nil && s.IsCandidateAlive(c.Request.Context(), cand) {
				picked = cand
			}
		}

		if picked == nil {
			switch {
			case model == "auto":
				est := estimateTokensFromBody(bodyBytes)
				picked, err = s.PickForAuto(c.Request.Context(), est)
			default:
				// Try alias first, fall through to exact-name lookup.
				picked, err = s.PickByAlias(c.Request.Context(), model)
				if err != nil || picked == nil {
					picked, err = s.PickByExactName(c.Request.Context(), model)
				}
			}
		}
		if err != nil || picked == nil {
			// alias / exact-name 都没命中,检查原 group 的准入策略:
			// 1. blocked_models 命中 → 直接 405 (无视 mode)
			// 2. specified + model 不在 exposed_models → 405
			if originalGroup, _ := resolver.GetGroupByName(c.Param("group_name")); originalGroup != nil {
				if modelIsExposed(originalGroup.BlockedModels, model) {
					c.AbortWithStatusJSON(http.StatusMethodNotAllowed, gin.H{
						"error": gin.H{
							"code":    "model_blocked",
							"message": "model " + model + " is blocked in group " + originalGroup.Name,
						},
					})
					return
				}
				if originalGroup.ModelRoutingMode == "specified" &&
					!modelIsExposed(originalGroup.ExposedModels, model) {
					c.AbortWithStatusJSON(http.StatusMethodNotAllowed, gin.H{
						"error": gin.H{
							"code":    "model_not_exposed",
							"message": "model " + model + " is not in the exposed list of group " + originalGroup.Name,
						},
					})
					return
				}
			}
			// 否则保持原行为:让请求穿透,由上游处理.
			return
		}

		groupName, ok := resolver.GetGroupNameByID(picked.GroupID)
		if !ok || groupName == "" {
			return
		}
		// 也要同步改写 URL path 里的 group 段:从 /proxy/{old}/... 改成
		// /proxy/{new}/...,否则 BuildUpstreamURL 用新 groupName 算 proxyPrefix
		// 时 strip 失败,会拼出 /openai/proxy/openai/... 之类的错误路径。
		// 系统快捷路由 (/openai/* /anthropic/* /gemini/*) 也同步替换前缀。
		oldGroupName := c.Param("group_name")
		if oldGroupName != "" && oldGroupName != groupName {
			oldPrefix := "/proxy/" + oldGroupName
			if strings.HasPrefix(c.Request.URL.Path, oldPrefix) {
				c.Request.URL.Path = "/proxy/" + groupName + strings.TrimPrefix(c.Request.URL.Path, oldPrefix)
			} else {
				// 系统快捷路由: /openai /anthropic /gemini
				for _, sc := range []string{"/openai", "/anthropic", "/gemini"} {
					if strings.HasPrefix(c.Request.URL.Path, sc+"/") || c.Request.URL.Path == sc {
						c.Request.URL.Path = "/proxy/" + groupName + strings.TrimPrefix(c.Request.URL.Path, sc)
						break
					}
				}
			}
		}
		c.Params = setParam(c.Params, "group_name", groupName)
		c.Set("router_engine.candidate", picked)

		// 刷新/写入粘性锁：多轮会话命中后，延长 TTL；首次多轮选出 picked 后也写入。
		if stickyKey != "" {
			s.SetSticky(stickyKey, picked)
		}

		// 如果原 model 与 picked.RealModel 不一致(alias 解析 / auto 智能路由),
		// 改写 body 让上游收到真实模型名,避免 400。
		if picked.RealModel != "" && picked.RealModel != model {
			if rewritten, ok := rewriteBodyModel(bodyBytes, picked.RealModel); ok {
				c.Request.Body = io.NopCloser(bytes.NewReader(rewritten))
				c.Request.ContentLength = int64(len(rewritten))
			}
		}

		logrus.WithFields(logrus.Fields{
			"requested_model": model,
			"target_group":    groupName,
			"target_model":    picked.RealModel,
			"alias":           picked.Alias,
			"weight":          picked.Weight,
		}).Debug("router_engine: routed request")
	}
}

// parseSession 提取会话标识与是否多轮。sessionKey 优先 X-Session-Id 头，
// 否则取首条 user 消息内容的 SHA1。isMultiTurn = messages 含 assistant。
func parseSession(bodyBytes []byte, header http.Header) (sessionKey string, isMultiTurn bool) {
	if v := strings.TrimSpace(header.Get("X-Session-Id")); v != "" {
		sessionKey = "hdr:" + v
	}
	var probe struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &probe); err != nil {
		return sessionKey, false
	}
	firstUser := ""
	for _, m := range probe.Messages {
		if m.Role == "assistant" {
			isMultiTurn = true
		}
		if firstUser == "" && m.Role == "user" {
			firstUser = flattenContent(m.Content)
		}
	}
	if sessionKey == "" && firstUser != "" {
		sum := sha1.Sum([]byte(firstUser))
		sessionKey = "msg:" + hex.EncodeToString(sum[:])
	}
	return sessionKey, isMultiTurn
}

// flattenContent 把 message content（string 或 array-of-blocks）拼成纯文本。
func flattenContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, part := range v {
			if pm, ok := part.(map[string]any); ok {
				if t, _ := pm["type"].(string); t == "text" {
					if s, _ := pm["text"].(string); s != "" {
						sb.WriteString(s)
					}
				}
			}
		}
		return sb.String()
	}
	return ""
}

// stickyStoreKey 由 sessionKey + requestedModel 派生稳定的 store key。
func stickyStoreKey(sessionKey, requestedModel string) string {
	sum := sha1.Sum([]byte(sessionKey + "|" + requestedModel))
	return "sticky:" + hex.EncodeToString(sum[:])
}

// rewriteBodyModel 把 JSON body 里的 "model" 字段改成 newModel。
// 没有 model 字段就插一个;返回 (新 body, 是否成功)。
func rewriteBodyModel(bodyBytes []byte, newModel string) ([]byte, bool) {
	var data map[string]any
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return nil, false
	}
	data["model"] = newModel
	out, err := json.Marshal(data)
	if err != nil {
		return nil, false
	}
	return out, true
}

// modelIsExposed reports whether `model` appears in the JSON-encoded
// exposed_models array. Empty / null exposed_models means no model is
// exposed under specified mode (intentional: forces user to pick).
func modelIsExposed(exposed []byte, model string) bool {
	if len(exposed) == 0 || model == "" {
		return false
	}
	q := `"` + model + `"`
	return bytes.Contains(exposed, []byte(q))
}

func isChatCompletionsPath(p string) bool {
	return strings.Contains(p, "chat/completions") ||
		strings.Contains(p, "messages") ||
		strings.Contains(p, "generateContent")
}

func setParam(params gin.Params, key, value string) gin.Params {
	for i, p := range params {
		if p.Key == key {
			params[i].Value = value
			return params
		}
	}
	return append(params, gin.Param{Key: key, Value: value})
}

// estimateTokensFromBody is a coarse approximation reused for the smart
// `auto` keyword routing. We deliberately avoid importing the legacy
// autoroute classifier so the new package has no cross-dependencies.
func estimateTokensFromBody(body []byte) int {
	var probe struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return 0
	}
	total := 0
	for _, m := range probe.Messages {
		switch v := m.Content.(type) {
		case string:
			total += utf8.RuneCountInString(v) * 4 / 5
		case []any:
			for _, part := range v {
				if pm, ok := part.(map[string]any); ok {
					if t, _ := pm["type"].(string); t == "text" {
						if s, _ := pm["text"].(string); s != "" {
							total += utf8.RuneCountInString(s) * 4 / 5
						}
					} else if t == "image_url" || t == "image" {
						total += 1000 // vision weight
					}
				}
			}
		}
	}
	total += len(probe.Tools) * 500
	return total
}
