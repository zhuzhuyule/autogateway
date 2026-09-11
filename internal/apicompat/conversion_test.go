package apicompat

import (
	"encoding/json"
	"testing"
)

// ---------- Anthropic 请求 → Chat 请求 ----------

func TestAnthropicRequestToChat_Basic(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-5",
		"system": "You are helpful",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "hello"},
			{"role": "user", "content": "thanks"}
		]
	}`)

	var areq AnthropicRequest
	if err := json.Unmarshal(body, &areq); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := ConvertAnthropicRequestToChat(&areq)

	if got.Model != "claude-sonnet-4-5" {
		t.Errorf("model = %q", got.Model)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (system + 3)", len(got.Messages))
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != "You are helpful" {
		t.Errorf("system message = %+v", got.Messages[0])
	}
	if got.MaxTokens == nil || *got.MaxTokens != 256 {
		t.Errorf("max_tokens = %v, want 256", got.MaxTokens)
	}
	for i, want := range []string{"user", "assistant", "user"} {
		if got.Messages[i+1].Role != want {
			t.Errorf("messages[%d].role = %q, want %q", i+1, got.Messages[i+1].Role, want)
		}
	}
}

func TestAnthropicRequestToChat_MaxTokensDefault(t *testing.T) {
	// Chat 侧 max_tokens 可选,但多数上游缺省会给很小的上限导致长回答被截断。
	// 未指定时必须补默认值。
	got := ConvertAnthropicRequestToChat(&AnthropicRequest{Model: "m"})
	if got.MaxTokens == nil || *got.MaxTokens != defaultMaxTokens {
		t.Fatalf("max_tokens = %v, want default %d", got.MaxTokens, defaultMaxTokens)
	}
}

func TestAnthropicRequestToChat_ToolUseBecomesToolCalls(t *testing.T) {
	body := []byte(`{
		"model": "m",
		"tools": [{"name": "get_weather", "description": "d", "input_schema": {"type": "object"}}],
		"messages": [
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "let me check"},
				{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "BJ"}}
			]}
		]
	}`)
	var areq AnthropicRequest
	if err := json.Unmarshal(body, &areq); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := ConvertAnthropicRequestToChat(&areq)

	last := got.Messages[len(got.Messages)-1]
	if last.Role != "assistant" {
		t.Fatalf("last role = %q", last.Role)
	}
	if len(last.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(last.ToolCalls))
	}
	tc := last.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Function.Name != "get_weather" {
		t.Errorf("tool_call = %+v", tc)
	}
	if tc.Function.Arguments != `{"city":"BJ"}` {
		t.Errorf("arguments = %q", tc.Function.Arguments)
	}
	// schema 必须被兜底补上 properties,否则 OpenAI 系上游报错
	if len(got.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(got.Tools))
	}
	if _, ok := got.Tools[0].Function.Parameters["properties"]; !ok {
		t.Errorf("tool schema missing properties: %+v", got.Tools[0].Function.Parameters)
	}
}

func TestAnthropicRequestToChat_ToolResultBecomesToolMessage(t *testing.T) {
	body := []byte(`{
		"model": "m",
		"messages": [
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_1", "name": "f", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": "sunny"}
			]}
		]
	}`)
	var areq AnthropicRequest
	if err := json.Unmarshal(body, &areq); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := ConvertAnthropicRequestToChat(&areq)

	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(got.Messages))
	}
	toolMsg := got.Messages[1]
	if toolMsg.Role != "tool" {
		t.Fatalf("second role = %q, want tool", toolMsg.Role)
	}
	if toolMsg.ToolCallID != "toolu_1" {
		t.Errorf("tool_call_id = %q", toolMsg.ToolCallID)
	}
	if toolMsg.Content != "sunny" {
		t.Errorf("content = %v", toolMsg.Content)
	}
}

func TestAnthropicRequestToChat_EmptyToolResultFilled(t *testing.T) {
	// 空 tool_result 会被 Anthropic 拒,必须补占位串。
	body := []byte(`{
		"model": "m",
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": ""}
			]}
		]
	}`)
	var areq AnthropicRequest
	if err := json.Unmarshal(body, &areq); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := ConvertAnthropicRequestToChat(&areq)
	if got.Messages[0].Content != emptyToolOutput {
		t.Errorf("content = %v, want %q", got.Messages[0].Content, emptyToolOutput)
	}
}

func TestAnthropicRequestToChat_ThinkingMapsToReasoningEffort(t *testing.T) {
	got := ConvertAnthropicRequestToChat(&AnthropicRequest{
		Model:    "m",
		Thinking: &AnthropicThinking{Type: "enabled", BudgetTokens: 5000},
	})
	if got.ReasoningEffort != "medium" {
		t.Errorf("reasoning_effort = %q, want medium", got.ReasoningEffort)
	}
}

// ---------- Chat 请求 → Anthropic 请求 ----------

func TestChatRequestToAnthropic_SystemExtracted(t *testing.T) {
	got := ConvertChatRequestToAnthropic(&ChatRequest{
		Model: "m",
		Messages: []ChatMessage{
			{Role: "system", Content: "a"},
			{Role: "system", Content: "b"},
			{Role: "user", Content: "hi"},
		},
	})
	if got.System != "a\n\nb" {
		t.Errorf("system = %v, want %q", got.System, "a\n\nb")
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want single user", got.Messages)
	}
}

func TestChatRequestToAnthropic_ToolCallsBecomeBlocks(t *testing.T) {
	got := ConvertChatRequestToAnthropic(&ChatRequest{
		Model: "m",
		Messages: []ChatMessage{
			{Role: "user", Content: "weather?"},
			{Role: "assistant", Content: "checking", ToolCalls: []ChatToolCall{
				{ID: "call_1", Type: "function", Function: ChatFunctionCall{Name: "get_weather", Arguments: `{"city":"BJ"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "sunny"},
		},
	})

	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user / assistant / user+tool_result)", len(got.Messages))
	}
	assistant := got.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("messages[1].role = %q", assistant.Role)
	}
	blocks := anthropicBlocks(assistant.Content)
	var uses int
	for _, b := range blocks {
		if b.Type == "tool_use" {
			uses++
			if b.ID != "call_1" || b.Name != "get_weather" {
				t.Errorf("tool_use = %+v", b)
			}
			if city, _ := b.Input["city"].(string); city != "BJ" {
				t.Errorf("tool_use input = %+v", b.Input)
			}
		}
	}
	if uses != 1 {
		t.Fatalf("tool_use blocks = %d, want 1", uses)
	}

	// tool_result 必须紧跟在含 tool_use 的 assistant 之后
	resultMsg := got.Messages[2]
	if resultMsg.Role != "user" {
		t.Fatalf("messages[2].role = %q, want user", resultMsg.Role)
	}
	resBlocks := anthropicBlocks(resultMsg.Content)
	if len(resBlocks) != 1 || resBlocks[0].Type != "tool_result" || resBlocks[0].ToolUseID != "call_1" {
		t.Fatalf("tool_result blocks = %+v", resBlocks)
	}
}

func TestChatRequestToAnthropic_ConsecutiveToolResultsGrouped(t *testing.T) {
	// OpenAI 允许多条连续的 role=tool 消息,Anthropic 要求聚在同一条 user 里。
	got := ConvertChatRequestToAnthropic(&ChatRequest{
		Model: "m",
		Messages: []ChatMessage{
			{Role: "assistant", ToolCalls: []ChatToolCall{
				{ID: "c1", Function: ChatFunctionCall{Name: "f", Arguments: "{}"}},
				{ID: "c2", Function: ChatFunctionCall{Name: "f", Arguments: "{}"}},
			}},
			{Role: "tool", ToolCallID: "c1", Content: "r1"},
			{Role: "tool", ToolCallID: "c2", Content: "r2"},
		},
	})
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(got.Messages))
	}
	blocks := anthropicBlocks(got.Messages[1].Content)
	if len(blocks) != 2 {
		t.Fatalf("tool_result blocks = %d, want 2", len(blocks))
	}
}

func TestChatRequestToAnthropic_MaxTokensAlwaysSet(t *testing.T) {
	// Anthropic 的 max_tokens 是必填,Chat 侧可以不传。
	got := ConvertChatRequestToAnthropic(&ChatRequest{Model: "m"})
	if got.MaxTokens != defaultMaxTokens {
		t.Fatalf("max_tokens = %d, want %d", got.MaxTokens, defaultMaxTokens)
	}

	huge := 999999
	got = ConvertChatRequestToAnthropic(&ChatRequest{Model: "m", MaxTokens: &huge})
	if got.MaxTokens != maxTokensCap {
		t.Fatalf("max_tokens = %d, want cap %d", got.MaxTokens, maxTokensCap)
	}
}

// ---------- 响应方向 ----------

func TestChatResponseToAnthropic_Text(t *testing.T) {
	got := ConvertChatResponseToAnthropic(&ChatResponse{
		ID:    "chatcmpl-1",
		Model: "m",
		Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: "hello"},
			FinishReason: "stop",
		}},
		Usage: &ChatUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})

	if got.Type != "message" || got.Role != "assistant" {
		t.Errorf("type/role = %q/%q", got.Type, got.Role)
	}
	if len(got.Content) != 1 || got.Content[0].Type != "text" || got.Content[0].Text != "hello" {
		t.Fatalf("content = %+v", got.Content)
	}
	if got.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", got.StopReason)
	}
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", got.Usage)
	}
}

func TestChatResponseToAnthropic_CacheUsageExcluded(t *testing.T) {
	// 包容桶 → 互斥桶:input_tokens 必须剔除缓存读,否则客户端高估输入。
	got := ConvertChatResponseToAnthropic(&ChatResponse{
		Choices: []ChatChoice{{Message: ChatMessage{Content: "x"}, FinishReason: "stop"}},
		Usage: &ChatUsage{
			PromptTokens:        1000,
			CompletionTokens:    50,
			PromptTokensDetails: &ChatPromptTokensDetails{CachedTokens: 900},
		},
	})
	if got.Usage.InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100", got.Usage.InputTokens)
	}
	if got.Usage.CacheReadInputTokens != 900 {
		t.Errorf("cache_read_input_tokens = %d, want 900", got.Usage.CacheReadInputTokens)
	}
}

func TestChatResponseToAnthropic_ToolCalls(t *testing.T) {
	got := ConvertChatResponseToAnthropic(&ChatResponse{
		Choices: []ChatChoice{{
			Message: ChatMessage{Role: "assistant", ToolCalls: []ChatToolCall{
				{ID: "call_9", Function: ChatFunctionCall{Name: "f", Arguments: `{"a":1}`}},
			}},
			FinishReason: "tool_calls",
		}},
	})
	if got.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", got.StopReason)
	}
	if len(got.Content) != 1 || got.Content[0].Type != "tool_use" || got.Content[0].ID != "call_9" {
		t.Fatalf("content = %+v", got.Content)
	}
	if v, _ := got.Content[0].Input["a"].(float64); v != 1 {
		t.Errorf("tool_use input = %+v", got.Content[0].Input)
	}
}

func TestAnthropicResponseToChat_UsageInclusive(t *testing.T) {
	// 互斥桶 → 包容桶:prompt_tokens 必须含缓存读。
	got := ConvertAnthropicResponseToChat(&AnthropicResponse{
		ID:         "msg_1",
		Model:      "m",
		Content:    []AnthropicContentBlock{{Type: "text", Text: "hi"}},
		StopReason: "end_turn",
		Usage: AnthropicUsage{
			InputTokens:          100,
			OutputTokens:         20,
			CacheReadInputTokens: 900,
		},
	})
	if got.Usage.PromptTokens != 1000 {
		t.Errorf("prompt_tokens = %d, want 1000", got.Usage.PromptTokens)
	}
	if got.Usage.PromptTokensDetails == nil || got.Usage.PromptTokensDetails.CachedTokens != 900 {
		t.Errorf("cached_tokens = %+v", got.Usage.PromptTokensDetails)
	}
	if got.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q", got.Choices[0].FinishReason)
	}
}

func TestAnthropicResponseToChat_MaxTokensMapsToLength(t *testing.T) {
	got := ConvertAnthropicResponseToChat(&AnthropicResponse{
		Content:    []AnthropicContentBlock{{Type: "text", Text: "cut"}},
		StopReason: "max_tokens",
	})
	if got.Choices[0].FinishReason != "length" {
		t.Errorf("finish_reason = %q, want length", got.Choices[0].FinishReason)
	}
}

// ---------- 往返 ----------

func TestRoundTrip_ChatRequest(t *testing.T) {
	original := &ChatRequest{
		Model: "m",
		Messages: []ChatMessage{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "q"},
			{Role: "assistant", Content: "a", ToolCalls: []ChatToolCall{
				{ID: "call_1", Function: ChatFunctionCall{Name: "f", Arguments: `{"k":"v"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "r"},
		},
	}

	back := ConvertAnthropicRequestToChat(ConvertChatRequestToAnthropic(original))

	// system 回到 messages[0],其余消息数不变
	if len(back.Messages) != len(original.Messages) {
		t.Fatalf("messages = %d, want %d", len(back.Messages), len(original.Messages))
	}
	if back.Messages[0].Role != "system" || back.Messages[0].Content != "sys" {
		t.Errorf("system = %+v", back.Messages[0])
	}
	var foundCall bool
	for _, m := range back.Messages {
		for _, tc := range m.ToolCalls {
			foundCall = true
			if tc.ID != "call_1" || tc.Function.Name != "f" {
				t.Errorf("tool_call = %+v", tc)
			}
			if tc.Function.Arguments != `{"k":"v"}` {
				t.Errorf("arguments = %q", tc.Function.Arguments)
			}
		}
	}
	if !foundCall {
		t.Error("tool call lost in round trip")
	}
}

func TestRoundTrip_ImageURL(t *testing.T) {
	original := &ChatRequest{
		Model: "m",
		Messages: []ChatMessage{{
			Role: "user",
			Content: []ChatContentPart{
				{Type: "text", Text: "what is this"},
				{Type: "image_url", ImageURL: &ChatImageURL{URL: "https://example.com/a.png"}},
			},
		}},
	}
	back := ConvertAnthropicRequestToChat(ConvertChatRequestToAnthropic(original))
	last := back.Messages[len(back.Messages)-1]
	parts, ok := last.Content.([]ChatContentPart)
	if !ok {
		// 只有文本时会被折叠成 string,说明图片丢了
		t.Fatalf("content = %T (%v), want parts with an image", last.Content, last.Content)
	}
	var images int
	for _, p := range parts {
		if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL == "https://example.com/a.png" {
			images++
		}
	}
	if images != 1 {
		t.Fatalf("images = %d, want 1 (parts=%+v)", images, parts)
	}
}

// ---------- 字节级入口 ----------

func TestJSONEntryPoints(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	out, err := AnthropicRequestToChatJSON(in)
	if err != nil {
		t.Fatalf("AnthropicRequestToChatJSON: %v", err)
	}
	var probe struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if len(probe.Messages) != 1 || probe.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v", probe.Messages)
	}

	if _, err := ChatRequestToAnthropicJSON(in); err != nil {
		t.Fatalf("ChatRequestToAnthropicJSON: %v", err)
	}
}

func TestJSONEntryPoints_ParseError(t *testing.T) {
	if _, err := AnthropicRequestToChatJSON([]byte(`not json`)); err == nil {
		t.Error("want error for malformed body")
	}
}

func TestNeedsTranslation(t *testing.T) {
	cases := []struct {
		src, dst Format
		want     bool
	}{
		{FormatChatCompletions, FormatChatCompletions, false},
		{FormatAnthropic, FormatChatCompletions, true},
		{FormatChatCompletions, FormatAnthropic, true},
		{"", FormatChatCompletions, false}, // 未知协议不转,保证零转换快路径
		{FormatAnthropic, "", false},
	}
	for _, c := range cases {
		if got := NeedsTranslation(c.src, c.dst); got != c.want {
			t.Errorf("NeedsTranslation(%q,%q) = %v, want %v", c.src, c.dst, got, c.want)
		}
	}
}
