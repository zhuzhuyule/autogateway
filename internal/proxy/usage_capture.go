package proxy

import (
	"strconv"

	"autogateway/internal/usage"

	"github.com/gin-gonic/gin"
)

// ctxKeyUsage 是把响应解析到的 token 用量挂到 gin ctx 的键。响应处理阶段
// (handleNormalResponse / streamWithIntegrity) 写入, logRequest 读取落库。
const ctxKeyUsage = "ac_response_usage"

// stashUsage 把解析到的 token 用量挂到 gin ctx (零值忽略)。
func stashUsage(c *gin.Context, u usage.Usage) {
	if u.IsZero() {
		return
	}
	c.Set(ctxKeyUsage, u)
}

// ctxUsage 读取 stashUsage 存的用量。
func ctxUsage(c *gin.Context) (usage.Usage, bool) {
	v, ok := c.Get(ctxKeyUsage)
	if !ok {
		return usage.Usage{}, false
	}
	u, ok := v.(usage.Usage)
	return u, ok
}

// setUsageHeaders 在响应体写出前设置 X-AC-* 用量/成本头。
//
// 仅非流式可行:流式在拿到 usage (末帧) 之前响应头早已 flush, 无法回填 ——
// 流式的用量只落库, 不进响应头。
func setUsageHeaders(c *gin.Context, u usage.Usage, costUSD float64) {
	c.Header("X-AC-Prompt-Tokens", strconv.Itoa(u.PromptTokens))
	c.Header("X-AC-Completion-Tokens", strconv.Itoa(u.CompletionTokens))
	c.Header("X-AC-Total-Tokens", strconv.Itoa(u.TotalTokens))
	if costUSD > 0 {
		c.Header("X-AC-Cost-USD", strconv.FormatFloat(costUSD, 'f', 6, 64))
	}
}
