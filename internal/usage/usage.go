// Package usage 归一化不同上游协议的 token 用量 (usage) 解析。
//
// 各家字段名不同:
//   - OpenAI chat:      usage.prompt_tokens / completion_tokens / total_tokens
//   - OpenAI Responses: usage.input_tokens / output_tokens / total_tokens
//   - Anthropic:        usage.input_tokens / output_tokens (流式时 input 在
//     message_start 的 message.usage, output 在 message_delta 的顶层 usage)
//   - Gemini:           usageMetadata.promptTokenCount / candidatesTokenCount / totalTokenCount
//
// Extract 对上述所有形状做一次性宽容解析, 不依赖 channelType, 因此中转 / 协议
// 转译后的响应也能正确识别。流式场景对每个 SSE data 帧调用 Extract 并用 Merge
// (每字段取 max) 累积 —— 天然兼容三种累积语义:OpenAI 末帧给全量、Anthropic
// message_delta 递增 output、Gemini 每帧累积。
package usage

import "encoding/json"

// Usage 是归一化后的 token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// IsZero 报告是否三项全为 0 (即没有拿到任何有效用量)。
func (u Usage) IsZero() bool {
	return u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0
}

// Merge 按字段取 max 合并 o。用于流式逐帧累积:input_tokens 恒定、
// output_tokens 单调增, 取 max 即可得到最终值, 无需关心帧到达顺序。
//
// total 是派生量而非独立值 —— 单帧上报的 total 逐帧 max 会得到错误结果
// (例如末帧 output=250 那帧的 total 只反映该帧)。故合并后统一按
// total = max(上报 total, prompt+completion) 收敛:既给缺 total 的协议
// (Anthropic/Responses) 兜底, 又保留上报 total 更大的情况 (reasoning /
// cache token 计入 total)。
func (u Usage) Merge(o Usage) Usage {
	m := Usage{
		PromptTokens:     max(u.PromptTokens, o.PromptTokens),
		CompletionTokens: max(u.CompletionTokens, o.CompletionTokens),
		TotalTokens:      max(u.TotalTokens, o.TotalTokens),
	}
	m.TotalTokens = max(m.TotalTokens, m.PromptTokens+m.CompletionTokens)
	return m
}

// usageFields 覆盖 OpenAI chat / Responses / Anthropic 三套 usage 子对象的
// 全部可能字段名。同一响应里只会命中其中一组, 未命中的保持 0。
type usageFields struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
}

func (f usageFields) normalize() Usage {
	return Usage{
		PromptTokens:     max(f.PromptTokens, f.InputTokens),
		CompletionTokens: max(f.CompletionTokens, f.OutputTokens),
		TotalTokens:      f.TotalTokens,
	}
}

// Extract 从一段 JSON (非流式完整响应体, 或单个 SSE data payload) 解析 usage。
// 未找到任何非零用量时返回 (Usage{}, false)。
func Extract(payload []byte) (Usage, bool) {
	var raw struct {
		Usage         *usageFields `json:"usage"` // OpenAI chat / Responses / Anthropic delta 顶层
		UsageMetadata *struct {    // Gemini
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
		Message *struct { // Anthropic message_start 里嵌套 message.usage
			Usage *usageFields `json:"usage"`
		} `json:"message"`
		Response *struct { // OpenAI Responses 流式 response.completed 里嵌套 response.usage
			Usage *usageFields `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Usage{}, false
	}

	var (
		u     Usage
		found bool
	)
	if raw.Usage != nil {
		u = u.Merge(raw.Usage.normalize())
		found = true
	}
	if raw.Message != nil && raw.Message.Usage != nil {
		u = u.Merge(raw.Message.Usage.normalize())
		found = true
	}
	if raw.Response != nil && raw.Response.Usage != nil {
		u = u.Merge(raw.Response.Usage.normalize())
		found = true
	}
	if raw.UsageMetadata != nil {
		u = u.Merge(Usage{
			PromptTokens:     raw.UsageMetadata.PromptTokenCount,
			CompletionTokens: raw.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      raw.UsageMetadata.TotalTokenCount,
		})
		found = true
	}

	if !found || u.IsZero() {
		return Usage{}, false
	}
	// total 已由 Merge 按 max(上报, prompt+completion) 收敛, 无需再兜底。
	return u, true
}
