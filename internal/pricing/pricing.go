// Package pricing 把 token 用量估算成美元成本 (list price)。
//
// 这里存的是各模型的公开挂牌价 ($/1M token, 输入/输出分别计)。它的用途是
// 给每条请求日志打一个「按官方定价折算的成本」—— 即使这次请求走的是免费源
// (成本实际为 0), 这个数字也代表「等价付费价值」, 是仪表盘算「本月靠免费源
// 省了多少钱」的口径。
//
// 定价是近似值且会随厂商调整漂移, 故:
//   - 只作 seed 表, 命中不了的模型返回 0 (免费源/未知模型天然为 0)。
//   - 按「小写模型名包含某关键子串」匹配, 更具体的前缀排在前面。
//   - 后续可做成可配置 (DB / 配置文件覆盖), 这里先给静态基线。
package pricing

import (
	"strings"

	"autogateway/internal/usage"
)

// Rate 是单个模型的挂牌价, 单位 USD / 1M token。
type Rate struct {
	InputPerM  float64
	OutputPerM float64
}

// entry 关联一个匹配子串与其定价。顺序敏感: 先命中的生效, 故更具体的
// (gpt-4o-mini) 必须排在更泛的 (gpt-4o) 之前。
type entry struct {
	match string
	rate  Rate
}

// table 是挂牌价基线。价格取各厂商公开文档的常见档位 (2026 年前后), 仅作估算。
var table = []entry{
	// OpenAI —— 具体在前。
	{"gpt-4o-mini", Rate{0.15, 0.60}},
	{"gpt-4o", Rate{2.50, 10.00}},
	{"gpt-4.1-mini", Rate{0.40, 1.60}},
	{"gpt-4.1-nano", Rate{0.10, 0.40}},
	{"gpt-4.1", Rate{2.00, 8.00}},
	{"o4-mini", Rate{1.10, 4.40}},
	{"o3-mini", Rate{1.10, 4.40}},
	{"o3", Rate{2.00, 8.00}},
	{"o1-mini", Rate{1.10, 4.40}},
	{"o1", Rate{15.00, 60.00}},

	// Anthropic Claude —— haiku/opus 在 sonnet 之外单列。
	{"haiku", Rate{0.80, 4.00}},
	{"opus", Rate{15.00, 75.00}},
	{"sonnet", Rate{3.00, 15.00}},

	// Google Gemini。
	{"gemini-2.5-pro", Rate{1.25, 10.00}},
	{"gemini-1.5-pro", Rate{1.25, 5.00}},
	{"gemini-2.0-flash", Rate{0.10, 0.40}},
	{"gemini-2.5-flash", Rate{0.30, 2.50}},
	{"gemini-1.5-flash", Rate{0.075, 0.30}},
	{"flash", Rate{0.10, 0.40}}, // 其它 flash 变体兜底

	// DeepSeek。
	{"deepseek-reasoner", Rate{0.55, 2.19}},
	{"deepseek-chat", Rate{0.27, 1.10}},

	// 其它常见。
	{"mistral-large", Rate{2.00, 6.00}},
	{"grok", Rate{2.00, 10.00}},
}

// Lookup 返回模型的挂牌价。未命中返回 (Rate{}, false)。
func Lookup(model string) (Rate, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return Rate{}, false
	}
	for _, e := range table {
		if strings.Contains(m, e.match) {
			return e.rate, true
		}
	}
	return Rate{}, false
}

// cacheMultFor 返回缓存输入相对满额输入价的倍率 (按 provider 家族)。缓存读
// 计费各家不同: Anthropic ~0.1x / OpenAI ~0.5x / Gemini ~0.25x / DeepSeek ~0.1x。
// 未知家族用保守默认 0.5。定价本就是估算, 这里也只求量级正确。
func cacheMultFor(model string) float64 {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"), strings.Contains(m, "sonnet"),
		strings.Contains(m, "haiku"), strings.Contains(m, "opus"):
		return 0.1
	case strings.Contains(m, "deepseek"):
		return 0.1
	case strings.Contains(m, "gemini"), strings.Contains(m, "flash"):
		return 0.25
	default:
		return 0.5
	}
}

// Cost 按挂牌价折算一次请求的美元成本。模型未知返回 0。
//
// 缓存命中的输入 (u.CachedPromptTokens) 按 cacheMultFor 打折计费 —— 修正对
// 缓存密集型工作负载 (编码工具反复带同一大 prompt) 的成本高估。
func Cost(model string, u usage.Usage) float64 {
	rate, ok := Lookup(model)
	if !ok {
		return 0
	}
	const perMillion = 1_000_000.0

	cached := u.CachedPromptTokens
	if cached > u.PromptTokens {
		cached = u.PromptTokens
	}
	nonCached := u.PromptTokens - cached
	cacheMult := cacheMultFor(model)

	return float64(nonCached)/perMillion*rate.InputPerM +
		float64(cached)/perMillion*rate.InputPerM*cacheMult +
		float64(u.CompletionTokens)/perMillion*rate.OutputPerM
}
