package apicompat

import (
	"encoding/json"
	"fmt"
)

// ConvertChatRequestToAnthropic 把 OpenAI /v1/chat/completions 请求转成
// Anthropic /v1/messages 请求。
//
// 三个关键差异在这里被抹平:
//  1. system 从 messages[] 提到顶层 system
//  2. assistant 的 tool_calls[] 展开成独立的 tool_use 内容块
//  3. role=tool 消息收进 user 消息的 tool_result 块,并修复配对(见 normalizeAnthropicMessages)
//
// max_tokens 在 Anthropic 是必填而 Chat 可选,故缺失时补默认值。
func ConvertChatRequestToAnthropic(req *ChatRequest) *AnthropicRequest {
	if req == nil {
		return nil
	}

	out := &AnthropicRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	var systemText string
	msgs := make([]AnthropicMessage, 0, len(req.Messages))
	// pendingResults 累积连续的 role=tool 消息。OpenAI 允许多条连续的 tool
	// 消息,Anthropic 要求它们聚在同一条 user 消息里。
	var pendingResults []AnthropicContentBlock
	flushResults := func() {
		if len(pendingResults) == 0 {
			return
		}
		msgs = append(msgs, AnthropicMessage{Role: "user", Content: pendingResults})
		pendingResults = nil
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			text, _ := chatContent(m.Content)
			if text == "" {
				continue
			}
			if systemText != "" {
				systemText += "\n\n"
			}
			systemText += text
			continue

		case "tool":
			pendingResults = append(pendingResults, AnthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   normalizeToolOutput(chatText(m.Content)),
			})
			continue
		}

		flushResults()

		switch m.Role {
		case "assistant":
			blocks := make([]AnthropicContentBlock, 0, 1+len(m.ToolCalls))
			if text, _ := chatContent(m.Content); text != "" {
				blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: text})
			}
			if m.ReasoningContent != "" {
				blocks = append(blocks, AnthropicContentBlock{Type: "thinking", Thinking: m.ReasoningContent})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, AnthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: decodeToolArgs(tc.Function.Arguments),
				})
			}
			if len(blocks) == 0 {
				continue
			}
			msgs = append(msgs, AnthropicMessage{Role: "assistant", Content: blocks})

		default: // user 以及未知角色一律按 user 处理
			blocks := make([]AnthropicContentBlock, 0, 1)
			text, parts := chatContent(m.Content)
			if text != "" {
				blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: text})
			}
			for _, p := range parts {
				if p.ImageURL == nil || p.ImageURL.URL == "" {
					continue
				}
				blocks = append(blocks, AnthropicContentBlock{Type: "image", Source: urlToAnthropicImageSource(p.ImageURL.URL)})
			}
			if len(blocks) == 0 {
				continue
			}
			msgs = append(msgs, AnthropicMessage{Role: "user", Content: blocks})
		}
	}
	flushResults()

	if systemText != "" {
		out.System = systemText
	}
	out.Messages = normalizeAnthropicMessages(msgs)

	if req.MaxCompletionTokens != nil {
		out.MaxTokens = clampMaxTokens(*req.MaxCompletionTokens)
	} else if req.MaxTokens != nil {
		out.MaxTokens = clampMaxTokens(*req.MaxTokens)
	} else {
		out.MaxTokens = defaultMaxTokens
	}

	if stops := chatStopToSequences(req.Stop); len(stops) > 0 {
		out.StopSequences = stops
	}
	if len(req.Tools) > 0 {
		tools := make([]AnthropicTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, AnthropicTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: normalizeToolSchema(t.Function.Parameters),
			})
		}
		out.Tools = tools
	}
	out.ToolChoice = chatToolChoiceToAnthropic(req.ToolChoice)
	if req.ReasoningEffort != "" {
		budget := reasoningEffortToBudgetTokens(req.ReasoningEffort)
		// 两道钳制:budget 不超过 max_tokens 的一半(给可见回答留空间),
		// 且不低于 Anthropic 的最低要求 1024。
		if budget > out.MaxTokens/2 {
			budget = out.MaxTokens / 2
		}
		if budget < 1024 {
			budget = 1024
		}
		out.Thinking = &AnthropicThinking{Type: "enabled", BudgetTokens: budget}
	}

	return out
}

// chatContent 把 ChatMessage.Content(string 或 part 数组)拆成纯文本与图片分片。
func chatContent(content any) (string, []ChatContentPart) {
	switch v := content.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []ChatContentPart:
		var text string
		var parts []ChatContentPart
		for _, p := range v {
			if p.Type == "text" {
				text += p.Text
				continue
			}
			parts = append(parts, p)
		}
		return text, parts
	default:
		converted, ok := jsonRoundTrip[[]ChatContentPart](content)
		if !ok {
			return "", nil
		}
		return chatContent(converted)
	}
}

// chatText 只取消息里的文本部分(忽略图片),用于 tool 消息的 content。
func chatText(content any) string {
	text, _ := chatContent(content)
	return text
}

// chatStopToSequences 把 Chat 的 stop(string 或 []string)归一成 []string。
func chatStopToSequences(stop any) []string {
	switch v := stop.(type) {
	case nil:
		return nil
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []string:
		return v
	default:
		out, ok := jsonRoundTrip[[]string](stop)
		if !ok {
			return nil
		}
		return out
	}
}

// decodeToolArgs 把 OpenAI 的 arguments(JSON 字符串)解析成 Anthropic 的 input 对象。
// 解析失败退化成空对象 —— 空 input 比非法 JSON 更容易被上游接受。
func decodeToolArgs(args string) map[string]any {
	if args == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(args), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// urlToAnthropicImageSource 把 URL 或 data URI 还原成 Anthropic 的 image source。
func urlToAnthropicImageSource(url string) *AnthropicImageSource {
	const prefix = "data:"
	if len(url) > len(prefix) && url[:len(prefix)] == prefix {
		rest := url[len(prefix):]
		// data:<mediaType>;base64,<data>
		semi := -1
		comma := -1
		for i, c := range rest {
			if c == ';' && semi < 0 {
				semi = i
			}
			if c == ',' && comma < 0 {
				comma = i
				break
			}
		}
		if comma > 0 {
			mediaType := ""
			if semi > 0 {
				mediaType = rest[:semi]
			}
			return &AnthropicImageSource{Type: "base64", MediaType: mediaType, Data: rest[comma+1:]}
		}
	}
	return &AnthropicImageSource{Type: "url", URL: url}
}

// chatToolChoiceToAnthropic 映射工具选择策略(Chat → Anthropic)。
func chatToolChoiceToAnthropic(tc any) *AnthropicToolChoice {
	switch v := tc.(type) {
	case nil:
		return nil
	case string:
		switch v {
		case "auto":
			return &AnthropicToolChoice{Type: "auto"}
		case "none":
			return &AnthropicToolChoice{Type: "none"}
		case "required", "any":
			return &AnthropicToolChoice{Type: "any"}
		default:
			return nil
		}
	case map[string]any:
		fn, _ := v["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "" {
			return &AnthropicToolChoice{Type: "any"}
		}
		return &AnthropicToolChoice{Type: "tool", Name: name}
	default:
		// 结构体形态(如 ChatToolChoice 未来新增类型)走一轮 JSON 归一化。
		normalized, ok := jsonRoundTrip[map[string]any](tc)
		if !ok {
			return nil
		}
		return chatToolChoiceToAnthropic(normalized)
	}
}

// reasoningEffortToBudgetTokens 把 OpenAI 的 reasoning_effort 档位折算成
// Anthropic 的 thinking budget。与 budgetTokensToReasoningEffort 互为逆映射的近似。
func reasoningEffortToBudgetTokens(effort string) int {
	switch effort {
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high", "xhigh":
		return 10240
	default:
		return 4096
	}
}

// ConvertChatResponseToAnthropic 把 Chat 非流式响应转成 Anthropic 响应。
//
// usage 从 OpenAI 的**包容桶**投影回 Anthropic 的**互斥桶**:
// input_tokens 剔除缓存读,缓存读单独放 cache_read_input_tokens。
// 不做这个投影,客户端看到的输入 token 会被系统性高估。
func ConvertChatResponseToAnthropic(resp *ChatResponse) *AnthropicResponse {
	if resp == nil {
		return nil
	}

	out := &AnthropicResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: resp.Model,
	}

	if len(resp.Choices) > 0 {
		msg := resp.Choices[0].Message
		blocks := make([]AnthropicContentBlock, 0, 1+len(msg.ToolCalls))
		if text, _ := chatContent(msg.Content); text != "" {
			blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: text})
		}
		if msg.ReasoningContent != "" {
			blocks = append(blocks, AnthropicContentBlock{Type: "thinking", Thinking: msg.ReasoningContent})
		}
		for _, tc := range msg.ToolCalls {
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: decodeToolArgs(tc.Function.Arguments),
			})
		}
		out.Content = blocks

		// 上游没给 finish_reason 时按正常结束处理,避免下游拿到空 stop_reason。
		finish := resp.Choices[0].FinishReason
		if finish == "" {
			finish = "stop"
		}
		out.StopReason = chatFinishToAnthropicStopReason(finish, len(msg.ToolCalls))
	} else {
		out.Content = []AnthropicContentBlock{}
	}

	if resp.Usage != nil {
		cached := 0
		if resp.Usage.PromptTokensDetails != nil {
			cached = resp.Usage.PromptTokensDetails.CachedTokens
		}
		if cached > resp.Usage.PromptTokens {
			cached = resp.Usage.PromptTokens // 防脏数据:缓存子集不可能超过总输入
		}
		out.Usage = AnthropicUsage{
			InputTokens:          resp.Usage.PromptTokens - cached,
			OutputTokens:         resp.Usage.CompletionTokens,
			CacheReadInputTokens: cached,
		}
	}
	return out
}

// chatFinishToAnthropicStopReason 映射停止原因(Chat → Anthropic)。
func chatFinishToAnthropicStopReason(finishReason string, toolCalls int) string {
	if toolCalls > 0 {
		return "tool_use"
	}
	switch finishReason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default: // stop / content_filter / 未知
		return "end_turn"
	}
}

// ChatRequestToAnthropicJSON 是请求方向的字节级入口。
func ChatRequestToAnthropicJSON(body []byte) ([]byte, error) {
	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("apicompat: parse chat request: %w", err)
	}
	return json.Marshal(ConvertChatRequestToAnthropic(&req))
}

// ChatResponseToAnthropicJSON 是响应方向的字节级入口。
func ChatResponseToAnthropicJSON(body []byte) ([]byte, error) {
	var resp ChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apicompat: parse chat response: %w", err)
	}
	return json.Marshal(ConvertChatResponseToAnthropic(&resp))
}
