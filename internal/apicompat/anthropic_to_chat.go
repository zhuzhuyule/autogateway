package apicompat

import (
	"encoding/json"
	"fmt"
)

// ConvertAnthropicRequestToChat 把 Anthropic /v1/messages 请求转成
// OpenAI /v1/chat/completions 请求。
//
// 这是 MVP 的主方向:Anthropic 客户端(Claude Code 等)→ OpenAI 上游。
// 转换是尽力而为的,不会返回错误 —— 无法表达的字段(cache_control 等)直接丢弃,
// 缺失的必填字段(max_tokens)补默认值,保证上游不会因为"我们生成的请求不合法"而 400。
func ConvertAnthropicRequestToChat(req *AnthropicRequest) *ChatRequest {
	if req == nil {
		return nil
	}

	out := &ChatRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	msgs := make([]ChatMessage, 0, len(req.Messages)+1)
	if sys := anthropicSystemText(req.System); sys != "" {
		msgs = append(msgs, ChatMessage{Role: "system", Content: sys})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, anthropicMessageToChatMessages(m)...)
	}
	out.Messages = msgs

	// Anthropic 的 max_tokens 可选,Chat 侧多数上游也把它当可选,但部分端点
	// 缺省会给一个很小的上限,导致长回答被截断。这里显式补上钳制后的默认值。
	maxTokens := clampMaxTokens(req.MaxTokens)
	out.MaxTokens = &maxTokens

	if len(req.StopSequences) > 0 {
		out.Stop = req.StopSequences
	}
	if len(req.Tools) > 0 {
		tools := make([]ChatTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, ChatTool{
				Type: "function",
				Function: ChatFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  normalizeToolSchema(t.InputSchema),
				},
			})
		}
		out.Tools = tools
	}
	if tc := anthropicToolChoiceToChat(req.ToolChoice); tc != nil {
		out.ToolChoice = tc
	}
	if req.Thinking != nil && req.Thinking.Type == "enabled" {
		out.ReasoningEffort = budgetTokensToReasoningEffort(req.Thinking.BudgetTokens)
	}

	return out
}

// anthropicMessageToChatMessages 把一条 Anthropic 消息展开成 1..N 条 Chat 消息。
// 展开场景:一条 user 消息里同时有文本和多个 tool_result —— Anthropic 允许,
// OpenAI 要求每个 tool_result 是独立的 role=tool 消息。
func anthropicMessageToChatMessages(m AnthropicMessage) []ChatMessage {
	blocks := anthropicBlocks(m.Content)
	out := make([]ChatMessage, 0, 1)

	if m.Role == "assistant" {
		var text, reasoning string
		var toolCalls []ChatToolCall
		for _, b := range blocks {
			switch b.Type {
			case "text":
				text += b.Text
			case "thinking":
				reasoning += b.Thinking
			case "tool_use":
				toolCalls = append(toolCalls, ChatToolCall{
					ID:   b.ID,
					Type: "function",
					Function: ChatFunctionCall{
						Name:      b.Name,
						Arguments: normalizeToolArgs(marshalToolInput(b.Input)),
					},
				})
			}
		}
		msg := ChatMessage{Role: "assistant"}
		if text != "" {
			msg.Content = text
		}
		if reasoning != "" {
			msg.ReasoningContent = reasoning
		}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
		}
		return append(out, msg)
	}

	// user:文本 / 图片合成一条,每个 tool_result 单独成一条 role=tool 消息。
	var parts []ChatContentPart
	var text string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text += b.Text
		case "image":
			if src := anthropicImageSourceToURL(b.Source); src != "" {
				parts = append(parts, ChatContentPart{Type: "image_url", ImageURL: &ChatImageURL{URL: src}})
			}
		case "tool_result":
			content := normalizeToolOutput(toolResultText(b.Content))
			if b.IsError {
				content = "Error: " + content
			}
			out = append(out, ChatMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: content})
		}
	}
	if text != "" {
		parts = append([]ChatContentPart{{Type: "text", Text: text}}, parts...)
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		out = append([]ChatMessage{{Role: "user", Content: text}}, out...)
	} else if len(parts) > 0 {
		out = append([]ChatMessage{{Role: "user", Content: parts}}, out...)
	}
	return out
}

// anthropicImageSourceToURL 把 Anthropic 图片块还原成 URL 或 data URI。
func anthropicImageSourceToURL(src *AnthropicImageSource) string {
	if src == nil {
		return ""
	}
	if src.URL != "" {
		return src.URL
	}
	if src.Type == "base64" && src.Data != "" {
		mediaType := src.MediaType
		if mediaType == "" {
			mediaType = "image/png"
		}
		return "data:" + mediaType + ";base64," + src.Data
	}
	return ""
}

// toolResultText 从 tool_result 的 content(string 或 block 数组)取出文本。
func toolResultText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		var out string
		for _, b := range anthropicBlocks(content) {
			if b.Type == "text" {
				out += b.Text
			}
		}
		return out
	}
}

// marshalToolInput 把 tool_use 的 input 对象序列化成 OpenAI 要求的 JSON 字符串。
func marshalToolInput(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(raw)
}

// anthropicToolChoiceToChat 把 Anthropic 的 tool_choice 映射成 Chat 的 tool_choice。
// Anthropic: auto / any / tool / none;OpenAI: auto / none / required / {function}。
func anthropicToolChoiceToChat(tc *AnthropicToolChoice) any {
	if tc == nil {
		return nil
	}
	switch tc.Type {
	case "auto":
		return "auto"
	case "none":
		return "none"
	case "any":
		return "required"
	case "tool":
		if tc.Name == "" {
			return "required"
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}
	default:
		return nil
	}
}

// budgetTokensToReasoningEffort 把 Anthropic 的 thinking budget 折算成 OpenAI 的
// reasoning_effort 档位。只是一个粗映射 —— 两个协议的思考强度不可精确对应。
func budgetTokensToReasoningEffort(budget int) string {
	switch {
	case budget <= 0:
		return "medium"
	case budget <= 1024:
		return "low"
	case budget <= 8192:
		return "medium"
	default:
		return "high"
	}
}

// ConvertAnthropicResponseToChat 把 Anthropic 非流式响应转成 Chat 响应。
//
// usage 从 Anthropic 的**互斥桶**(input_tokens 不含缓存)投影到 OpenAI 的
// **包容桶**(prompt_tokens 含缓存),保证两侧口径一致。
func ConvertAnthropicResponseToChat(resp *AnthropicResponse) *ChatResponse {
	if resp == nil {
		return nil
	}

	msg := ChatMessage{Role: "assistant"}
	var text string
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			text += b.Text
		case "thinking":
			msg.ReasoningContent += b.Thinking
		case "tool_use":
			msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{
				ID:   b.ID,
				Type: "function",
				Function: ChatFunctionCall{
					Name:      b.Name,
					Arguments: normalizeToolArgs(marshalToolInput(b.Input)),
				},
			})
		}
	}
	if text != "" {
		msg.Content = text
	}

	out := &ChatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Model:   resp.Model,
		Choices: []ChatChoice{{Index: 0, Message: msg, FinishReason: anthropicStopReasonToChatFinish(resp.StopReason, len(msg.ToolCalls))}},
	}

	prompt := resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens
	out.Usage = &ChatUsage{
		PromptTokens:     prompt,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      prompt + resp.Usage.OutputTokens,
	}
	if resp.Usage.CacheReadInputTokens > 0 {
		out.Usage.PromptTokensDetails = &ChatPromptTokensDetails{CachedTokens: resp.Usage.CacheReadInputTokens}
	}
	return out
}

// anthropicStopReasonToChatFinish 映射停止原因。
// toolCalls > 0 时优先判 tool_calls —— 有些上游 stop_reason 没给准但确实产出了调用。
func anthropicStopReasonToChatFinish(stopReason string, toolCalls int) string {
	if toolCalls > 0 {
		return "tool_calls"
	}
	switch stopReason {
	case "max_tokens", "pause_turn":
		return "length"
	case "tool_use":
		return "tool_calls"
	default: // end_turn / stop_sequence / refusal / 未知
		return "stop"
	}
}

// AnthropicRequestToChatJSON 是面向转发层的字节级入口:吃 Anthropic 请求体,
// 吐 Chat 请求体。解析失败时返回错误,调用方应原样转发(不转译)而不是报错中断,
// 因为"解析不了"通常意味着这不是一个我们认识的 Anthropic 请求。
func AnthropicRequestToChatJSON(body []byte) ([]byte, error) {
	var req AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("apicompat: parse anthropic request: %w", err)
	}
	return json.Marshal(ConvertAnthropicRequestToChat(&req))
}

// AnthropicResponseToChatJSON 是响应方向的字节级入口。
func AnthropicResponseToChatJSON(body []byte) ([]byte, error) {
	var resp AnthropicResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apicompat: parse anthropic response: %w", err)
	}
	return json.Marshal(ConvertAnthropicResponseToChat(&resp))
}
