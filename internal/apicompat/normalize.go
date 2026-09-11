package apicompat

import "encoding/json"

// 防御性规范化常量。每一条都对应某个真实上游的 400/422 —— 详见各常量注释。
const (
	// emptyToolArgs 是工具调用参数为空时的兜底值。OpenAI 系上游收到空字符串
	// 会报 "Invalid 'tool_calls[0].function.arguments'"。
	emptyToolArgs = "{}"

	// emptyToolOutput 是工具结果为空时的兜底值。Anthropic 拒绝空 content。
	emptyToolOutput = "(empty)"

	// defaultMaxTokens 是 Chat → Anthropic 时 max_tokens 的缺省值。
	// Anthropic 的 max_tokens 是**必填**,而 Chat 侧完全可以不传。
	defaultMaxTokens = 8192

	// maxTokensCap 是 max_tokens 的上限。超过这个值的请求上游普遍不接受,
	// 不钳制会让整个请求 400,而不是拿到一个略短的回答。
	maxTokensCap = 64000
)

// clampMaxTokens 把 n 收敛到 [1, maxTokensCap]。n<=0 时返回 defaultMaxTokens。
func clampMaxTokens(n int) int {
	switch {
	case n <= 0:
		return defaultMaxTokens
	case n > maxTokensCap:
		return maxTokensCap
	default:
		return n
	}
}

// normalizeToolSchema 兜底工具的 JSON Schema。
//
// 两个方向的真实故障:
//   - OpenAI 侧缺 properties 会直接报错
//   - Anthropic 兼容端点遇到 null schema 返回 422
//
// 故空 / null 一律补成最宽松的 object schema。
func normalizeToolSchema(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if _, ok := schema["properties"]; !ok {
		// 保留调用方给的其它字段(如 required / $defs),只补缺失的 properties。
		out := make(map[string]any, len(schema)+1)
		for k, v := range schema {
			out[k] = v
		}
		out["properties"] = map[string]any{}
		if _, ok := out["type"]; !ok {
			out["type"] = "object"
		}
		return out
	}
	return schema
}

// normalizeToolArgs 保证工具调用参数是非空 JSON 字符串。
func normalizeToolArgs(args string) string {
	if args == "" {
		return emptyToolArgs
	}
	if !json.Valid([]byte(args)) {
		// 不是合法 JSON 就整体丢弃,避免把半截字符串发给上游。
		return emptyToolArgs
	}
	return args
}

// normalizeToolOutput 保证工具结果非空。
func normalizeToolOutput(text string) string {
	if text == "" {
		return emptyToolOutput
	}
	return text
}

// jsonRoundTrip 把任意值(通常是 json.Unmarshal 出来的 map[string]any 或
// []any)重新序列化成 T。用于把"弱类型的 any 字段"收成强类型切片。
// 失败返回零值与 false,调用方按"没有这段内容"处理。
func jsonRoundTrip[T any](v any) (T, bool) {
	var out T
	if v == nil {
		return out, false
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, false
	}
	return out, true
}

// anthropicBlocks 把 AnthropicMessage.Content(可能是 string / []block /
// []any)统一收成 []AnthropicContentBlock。
func anthropicBlocks(content any) []AnthropicContentBlock {
	switch v := content.(type) {
	case nil:
		return nil
	case string:
		if v == "" {
			return nil
		}
		return []AnthropicContentBlock{{Type: "text", Text: v}}
	case []AnthropicContentBlock:
		return v
	case []any:
		if len(v) == 0 {
			return nil
		}
		out := make([]AnthropicContentBlock, 0, len(v))
		for _, item := range v {
			if b, ok := jsonRoundTrip[AnthropicContentBlock](item); ok {
				out = append(out, b)
			}
		}
		return out
	default:
		out, ok := jsonRoundTrip[[]AnthropicContentBlock](v)
		if !ok {
			return nil
		}
		return out
	}
}

// anthropicSystemText 把顶层 system(string 或 block 数组)收敛成纯文本。
// 多个文本块用空行拼接 —— 与 Anthropic 自身的行为一致。
func anthropicSystemText(system any) string {
	switch v := system.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		blocks := anthropicBlocks(v)
		var out string
		for _, b := range blocks {
			if b.Type != "text" || b.Text == "" {
				continue
			}
			if out != "" {
				out += "\n\n"
			}
			out += b.Text
		}
		return out
	}
}

// mergeConsecutiveAnthropic 合并相邻同角色的消息,保证 user/assistant 交替。
// Anthropic 不接受连续两条同角色消息(会返回 400)。
func mergeConsecutiveAnthropic(msgs []AnthropicMessage) []AnthropicMessage {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]AnthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		if len(out) > 0 && out[len(out)-1].Role == m.Role {
			prev := &out[len(out)-1]
			merged := append(anthropicBlocks(prev.Content), anthropicBlocks(m.Content)...)
			prev.Content = merged
			continue
		}
		out = append(out, m)
	}
	return out
}

// hasAnthropicToolInteraction 报告这批消息里是否存在工具调用或工具结果。
func hasAnthropicToolInteraction(msgs []AnthropicMessage) bool {
	for _, m := range msgs {
		for _, b := range anthropicBlocks(m.Content) {
			if b.Type == "tool_use" || b.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}

// normalizeAnthropicMessages 修复 tool_use / tool_result 的配对关系。
//
// 这是 Chat → Anthropic 方向最核心的防御逻辑。Anthropic 有三条不变式:
//  1. 每个 tool_result 必须紧跟在含对应 tool_use 的 assistant 消息之后
//  2. 每个 tool_use 必须被下一条 user 消息里的 tool_result 应答(未应答会 400)
//  3. user / assistant 必须交替
//
// 朴素逐条转换会破坏它们,因为网关看到的客户端历史是不可控的:Anthropic 侧
// 允许 tool_result 出现在任意位置,OpenAI 侧允许连续多条 tool 消息,还可能
// 有被截断的半截历史。
//
// 算法:先按 tool_use_id 索引全部 tool_result(last wins);重组时 assistant
// 只保留**有应答**的 tool_use,孤立调用整体删除,并在其后紧接一条装载配对
// tool_result 的 user 消息;孤儿 result 直接丢弃。前后各跑一遍同角色合并。
func normalizeAnthropicMessages(msgs []AnthropicMessage) []AnthropicMessage {
	if len(msgs) == 0 {
		return nil
	}
	msgs = mergeConsecutiveAnthropic(msgs)

	// 没有任何工具交互时直接返回,保持消息原本的表达形式(纯文本消息仍是
	// 字符串,不被无谓地展开成 block 数组)。注意判据是"有没有 tool_use 或
	// tool_result",不能只看 tool_result —— 只有调用没有结果同样是残缺历史,
	// 必须走下面的重组把孤立调用删掉。
	if !hasAnthropicToolInteraction(msgs) {
		return msgs
	}

	resultsByUseID := make(map[string]AnthropicContentBlock)
	for _, m := range msgs {
		for _, b := range anthropicBlocks(m.Content) {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				resultsByUseID[b.ToolUseID] = b
			}
		}
	}

	out := make([]AnthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		blocks := anthropicBlocks(m.Content)
		if m.Role == "assistant" {
			kept := make([]AnthropicContentBlock, 0, len(blocks))
			pending := make([]AnthropicContentBlock, 0, len(blocks))
			for _, b := range blocks {
				if b.Type == "tool_use" {
					res, ok := resultsByUseID[b.ID]
					if !ok {
						continue // 孤立调用:整体删除,否则上游 400
					}
					kept = append(kept, b)
					pending = append(pending, res)
					continue
				}
				kept = append(kept, b)
			}
			if len(kept) == 0 {
				continue
			}
			m.Content = kept
			out = append(out, m)
			if len(pending) > 0 {
				out = append(out, AnthropicMessage{Role: "user", Content: pending})
			}
			continue
		}

		// user 消息里的 tool_result 已经在上面重新挂载过了,这里只保留其它块,
		// 避免出现重复或孤儿 result。
		kept := make([]AnthropicContentBlock, 0, len(blocks))
		for _, b := range blocks {
			if b.Type == "tool_result" {
				continue
			}
			kept = append(kept, b)
		}
		if len(kept) == 0 {
			continue
		}
		m.Content = kept
		out = append(out, m)
	}
	return mergeConsecutiveAnthropic(out)
}
