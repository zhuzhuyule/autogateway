package router_engine

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// AliasResolver is the interface routes use to look up a group name by id.
// Implemented by services.GroupManager.
type AliasResolver interface {
	GetGroupNameByID(id uint) (string, bool)
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
		if model == "" {
			c.Next()
			return
		}

		var picked *Candidate
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
		if err != nil || picked == nil {
			// No match — leave the request alone so existing routing
			// (group/proxy_keys) handles it.
			return
		}

		groupName, ok := resolver.GetGroupNameByID(picked.GroupID)
		if !ok || groupName == "" {
			return
		}
		c.Params = setParam(c.Params, "group_name", groupName)
		c.Set("router_engine.candidate", picked)

		logrus.WithFields(logrus.Fields{
			"requested_model": model,
			"target_group":    groupName,
			"target_model":    picked.RealModel,
			"alias":           picked.Alias,
			"weight":          picked.Weight,
		}).Debug("router_engine: routed request")
	}
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
