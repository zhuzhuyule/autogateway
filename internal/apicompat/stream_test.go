package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }

// ---------- Chat 流 → Anthropic 流 ----------

func TestChatToAnthropicStream_Text(t *testing.T) {
	s := NewChatToAnthropicStream()

	var types []string
	var text string
	collect := func(evs []AnthropicStreamEvent) {
		for _, ev := range evs {
			types = append(types, ev.Type)
			if ev.Type == "content_block_delta" {
				if d, _ := ev.Delta["type"].(string); d == "text_delta" {
					text += ev.Delta["text"].(string)
				}
			}
		}
	}

	collect(s.Process(&ChatStreamChunk{ID: "c1", Model: "m", Choices: []ChatStreamChoice{
		{Delta: ChatStreamDelta{Role: "assistant"}},
	}}))
	collect(s.Process(&ChatStreamChunk{ID: "c1", Model: "m", Choices: []ChatStreamChoice{
		{Delta: ChatStreamDelta{Content: "he"}},
	}}))
	collect(s.Process(&ChatStreamChunk{ID: "c1", Model: "m", Choices: []ChatStreamChoice{
		{Delta: ChatStreamDelta{Content: "llo"}},
	}}))
	collect(s.Process(&ChatStreamChunk{ID: "c1", Model: "m", Choices: []ChatStreamChoice{
		{Delta: ChatStreamDelta{}, FinishReason: strPtr("stop")},
	}, Usage: &ChatUsage{PromptTokens: 10, CompletionTokens: 5}}))
	collect(s.Finalize())

	if text != "hello" {
		t.Errorf("text = %q, want hello", text)
	}

	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	}
	if got := strings.Join(types, ","); got != strings.Join(want, ",") {
		t.Fatalf("event sequence =\n %s\nwant\n %s", got, strings.Join(want, ","))
	}
}

func TestChatToAnthropicStream_ToolCall(t *testing.T) {
	s := NewChatToAnthropicStream()
	s.Process(&ChatStreamChunk{ID: "c1", Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{Role: "assistant"}}}})

	// 首帧带 id/name,后续帧只带 arguments 分片
	evs := s.Process(&ChatStreamChunk{ID: "c1", Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{
		ToolCalls: []ChatStreamToolCall{{Index: 0, ID: "call_1", Type: "function",
			Function: &ChatFunctionCall{Name: "get_weather", Arguments: `{"ci`}}},
	}}}})
	evs = append(evs, s.Process(&ChatStreamChunk{ID: "c1", Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{
		ToolCalls: []ChatStreamToolCall{{Index: 0, Function: &ChatFunctionCall{Arguments: `ty":"BJ"}`}}},
	}}}})...)

	var args string
	var sawStart bool
	for _, ev := range evs {
		if ev.Type == "content_block_start" && ev.ContentBlock != nil {
			sawStart = true
			if ev.ContentBlock.Type != "tool_use" || ev.ContentBlock.ID != "call_1" || ev.ContentBlock.Name != "get_weather" {
				t.Errorf("tool_use block = %+v", ev.ContentBlock)
			}
		}
		if ev.Type == "content_block_delta" {
			if p, _ := ev.Delta["partial_json"].(string); p != "" {
				args += p
			}
		}
	}
	if !sawStart {
		t.Fatal("no tool_use content_block_start emitted")
	}
	if args != `{"city":"BJ"}` {
		t.Errorf("aggregated arguments = %q", args)
	}

	// 带 tool_use 时 stop_reason 必须是 tool_use
	s.Process(&ChatStreamChunk{ID: "c1", Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{}, FinishReason: strPtr("tool_calls")}}})
	final := s.Finalize()
	var stop string
	for _, ev := range final {
		if ev.Type == "message_delta" {
			if d, ok := ev.Delta["stop_reason"].(string); ok {
				stop = d
			}
		}
	}
	if stop != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", stop)
	}
}

func TestChatToAnthropicStream_FinalizeIdempotent(t *testing.T) {
	s := NewChatToAnthropicStream()
	s.Process(&ChatStreamChunk{ID: "c1", Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{Content: "x"}}}})

	first := s.Finalize()
	second := s.Finalize()
	if len(first) == 0 {
		t.Fatal("first Finalize produced nothing")
	}
	if len(second) != 0 {
		t.Errorf("Finalize is not idempotent, second call produced %d events", len(second))
	}
}

func TestChatToAnthropicStream_FinalizeWithoutStart(t *testing.T) {
	// 什么都没收到就结束 —— 不要产出孤儿事件。
	if evs := NewChatToAnthropicStream().Finalize(); len(evs) != 0 {
		t.Errorf("Finalize on empty stream = %+v", evs)
	}
}

func TestChatToAnthropicStream_ThinkingBeforeText(t *testing.T) {
	s := NewChatToAnthropicStream()
	s.Process(&ChatStreamChunk{ID: "c1", Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{Role: "assistant"}}}})
	s.Process(&ChatStreamChunk{ID: "c1", Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{ReasoningContent: "hmm"}}}})
	evs := s.Process(&ChatStreamChunk{ID: "c1", Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{Content: "ans"}}}})

	// 从 thinking 切到 text 必须先 close 前一个块 —— Anthropic 要求块生命周期严格有序。
	var closed, opened bool
	for _, ev := range evs {
		if ev.Type == "content_block_stop" {
			closed = true
		}
		if ev.Type == "content_block_start" && ev.ContentBlock != nil && ev.ContentBlock.Type == "text" {
			opened = true
		}
	}
	if !closed || !opened {
		t.Errorf("block lifecycle not respected: %+v", evs)
	}
}

// ---------- Anthropic 流 → Chat 流 ----------

func TestAnthropicToChatStream_Text(t *testing.T) {
	s := NewAnthropicToChatStream()

	var content string
	var done bool
	var seenFirst bool
	consume := func(chunks []ChatStreamChunk, isDone bool) {
		for _, ch := range chunks {
			for _, c := range ch.Choices {
				content += c.Delta.Content
				// OpenAI 规范:只有首个 chunk 带 role,后续 chunk 必须省略。
				if !seenFirst {
					seenFirst = true
					if c.Delta.Role != "assistant" {
						t.Errorf("first chunk missing role=assistant: %+v", c)
					}
				} else if c.Delta.Role != "" {
					t.Errorf("non-first chunk carries role: %+v", c)
				}
			}
		}
		done = done || isDone
	}

	idx := 0
	s.Process(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{
		ID: "msg_1", Model: "m", Usage: AnthropicUsage{InputTokens: 10},
	}})
	s.Process(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx,
		ContentBlock: &AnthropicContentBlock{Type: "text", Text: ""}})
	consume(s.Process(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx,
		Delta: map[string]any{"type": "text_delta", "text": "he"}}))
	consume(s.Process(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx,
		Delta: map[string]any{"type": "text_delta", "text": "llo"}}))
	s.Process(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	s.Process(&AnthropicStreamEvent{Type: "message_delta",
		Delta: map[string]any{"stop_reason": "end_turn"},
		Usage: &AnthropicUsage{OutputTokens: 7}})
	consume(s.Process(&AnthropicStreamEvent{Type: "message_stop"}))

	if content != "hello" {
		t.Errorf("content = %q, want hello", content)
	}
	if !done {
		t.Error("message_stop did not signal done")
	}
}

func TestAnthropicToChatStream_UsageProjection(t *testing.T) {
	s := NewAnthropicToChatStream()
	s.Process(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{
		ID: "msg_1", Usage: AnthropicUsage{InputTokens: 100, CacheReadInputTokens: 900},
	}})
	s.Process(&AnthropicStreamEvent{Type: "message_delta", Usage: &AnthropicUsage{OutputTokens: 20}})
	chunks, _ := s.Process(&AnthropicStreamEvent{Type: "message_stop"})

	if len(chunks) != 1 || chunks[0].Usage == nil {
		t.Fatalf("no final chunk with usage: %+v", chunks)
	}
	u := chunks[0].Usage
	// 互斥桶 → 包容桶
	if u.PromptTokens != 1000 {
		t.Errorf("prompt_tokens = %d, want 1000", u.PromptTokens)
	}
	if u.PromptTokensDetails == nil || u.PromptTokensDetails.CachedTokens != 900 {
		t.Errorf("cached_tokens not projected: %+v", u.PromptTokensDetails)
	}
}

func TestAnthropicToChatStream_ToolUse(t *testing.T) {
	s := NewAnthropicToChatStream()
	s.Process(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})

	idx := 0
	chunks, _ := s.Process(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx,
		ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "toolu_1", Name: "f"}})
	if len(chunks) != 1 || len(chunks[0].Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("tool call start chunk = %+v", chunks)
	}
	tc := chunks[0].Choices[0].Delta.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Function == nil || tc.Function.Name != "f" {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.Index != 0 {
		t.Errorf("tool index = %d, want 0", tc.Index)
	}

	chunks, _ = s.Process(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx,
		Delta: map[string]any{"type": "input_json_delta", "partial_json": `{"a":1}`}})
	if len(chunks) != 1 || chunks[0].Choices[0].Delta.ToolCalls[0].Function.Arguments != `{"a":1}` {
		t.Errorf("arguments delta = %+v", chunks)
	}
}

func TestAnthropicToChatStream_OrphanDeltaDropped(t *testing.T) {
	// 没见过 start 的 input_json_delta 必须丢弃,否则会产出无 id 的工具调用。
	s := NewAnthropicToChatStream()
	s.Process(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	idx := 7
	chunks, done := s.Process(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx,
		Delta: map[string]any{"type": "input_json_delta", "partial_json": `{"a":1}`}})
	if len(chunks) != 0 || done {
		t.Errorf("orphan delta produced output: %+v", chunks)
	}
}

func TestAnthropicToChatStream_FinalizeOnTruncatedStream(t *testing.T) {
	// 上游没发 message_stop 就断开 —— Finalize 必须补齐 finish chunk。
	s := NewAnthropicToChatStream()
	s.Process(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	chunks, done := s.Finalize()
	if !done {
		t.Error("Finalize did not signal done")
	}
	if len(chunks) != 1 || chunks[0].Choices[0].FinishReason == nil {
		t.Fatalf("finalize chunks = %+v", chunks)
	}
	if *chunks[0].Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", *chunks[0].Choices[0].FinishReason)
	}
	// 幂等
	if again, _ := s.Finalize(); len(again) != 0 {
		t.Errorf("Finalize not idempotent: %+v", again)
	}
}

// ---------- 往返 ----------

func TestStreamRoundTrip_Text(t *testing.T) {
	// Chat 流 → Anthropic 事件 → 再喂回反向状态机 → 文本应保持一致。
	up := NewChatToAnthropicStream()
	down := NewAnthropicToChatStream()

	var text string
	feed := func(evs []AnthropicStreamEvent) {
		for _, ev := range evs {
			chunks, _ := down.Process(&ev)
			for _, ch := range chunks {
				for _, c := range ch.Choices {
					text += c.Delta.Content
				}
			}
		}
	}

	feed(up.Process(&ChatStreamChunk{ID: "c1", Model: "m", Choices: []ChatStreamChoice{
		{Delta: ChatStreamDelta{Role: "assistant"}}}}))
	feed(up.Process(&ChatStreamChunk{ID: "c1", Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{Content: "hello"}}}}))
	feed(up.Finalize())

	if text != "hello" {
		t.Errorf("round-trip text = %q, want hello", text)
	}
}

// ---------- SSE 编码辅助 ----------

func TestAnthropicStreamEventMarshal(t *testing.T) {
	idx := 0
	raw, err := json.Marshal(AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &idx,
		Delta: map[string]any{"type": "text_delta", "text": "hi"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// index=0 必须出现在 wire 上 —— 严格客户端(Codex/Claude Code)按 index 定位块。
	if probe["index"] != float64(0) {
		t.Errorf("index zero dropped: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// 回归: message_delta 必须带权威 usage
//
// 线上现象: Anthropic 侧收到 message_delta.usage.input_tokens = 0。
// 原因是 OpenAI 的 usage 帧在**流尾**才到, message_start 时还不知道 input;
// message_delta 是 Anthropic 协议里唯一能报总量的地方, 漏掉 InputTokens
// 客户端就只能读到零值(AnthropicUsage.InputTokens 没有 omitempty)。
// 同时 OpenAI 的 prompt_tokens 是包容桶(含缓存), Anthropic 的 input_tokens
// 是互斥桶 —— 必须减去缓存命中, 否则开 prompt cache 的上游会重复计量。
// ---------------------------------------------------------------------------

func TestChatToAnthropicStream_MessageDeltaCarriesFinalUsage(t *testing.T) {
	s := NewChatToAnthropicStream()

	s.Process(&ChatStreamChunk{ID: "c1", Model: "m", Choices: []ChatStreamChoice{
		{Delta: ChatStreamDelta{Role: "assistant"}},
	}})
	s.Process(&ChatStreamChunk{ID: "c1", Model: "m", Choices: []ChatStreamChoice{
		{Delta: ChatStreamDelta{Content: "hi"}},
	}})

	// OpenAI 流尾的 usage 帧: prompt=100 其中 30 命中缓存, completion=7。
	// Anthropic 互斥桶口径 → input_tokens = 100 - 30 = 70。
	final := s.Process(&ChatStreamChunk{
		ID: "c1", Model: "m",
		Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{}, FinishReason: strPtr("stop")}},
		Usage: &ChatUsage{
			PromptTokens:        100,
			CompletionTokens:    7,
			PromptTokensDetails: &ChatPromptTokensDetails{CachedTokens: 30},
		},
	})
	final = append(final, s.Finalize()...)

	var got *AnthropicUsage
	for _, ev := range final {
		if ev.Type == "message_delta" {
			got = ev.Usage
		}
	}
	if got == nil {
		t.Fatal("no message_delta emitted")
	}
	if got.InputTokens != 70 {
		t.Errorf("message_delta usage.input_tokens = %d, want 70 (100 prompt - 30 cached)", got.InputTokens)
	}
	if got.OutputTokens != 7 {
		t.Errorf("message_delta usage.output_tokens = %d, want 7", got.OutputTokens)
	}
	if got.CacheReadInputTokens != 30 {
		t.Errorf("message_delta usage.cache_read_input_tokens = %d, want 30", got.CacheReadInputTokens)
	}
}

// TestChatToAnthropicStream_UsageSurvivesMissingCacheDetails 保证上游不给
// prompt_tokens_details 时 input_tokens 就是 prompt_tokens 全量, 不被误减。
func TestChatToAnthropicStream_UsageSurvivesMissingCacheDetails(t *testing.T) {
	s := NewChatToAnthropicStream()

	s.Process(&ChatStreamChunk{ID: "c1", Model: "m", Choices: []ChatStreamChoice{
		{Delta: ChatStreamDelta{Role: "assistant"}},
	}})
	final := s.Process(&ChatStreamChunk{
		ID: "c1", Model: "m",
		Choices: []ChatStreamChoice{{Delta: ChatStreamDelta{}, FinishReason: strPtr("stop")}},
		Usage:   &ChatUsage{PromptTokens: 42, CompletionTokens: 3},
	})
	final = append(final, s.Finalize()...)

	var got *AnthropicUsage
	for _, ev := range final {
		if ev.Type == "message_delta" {
			got = ev.Usage
		}
	}
	if got == nil {
		t.Fatal("no message_delta emitted")
	}
	if got.InputTokens != 42 {
		t.Errorf("input_tokens = %d, want 42 (no cache details → full prompt)", got.InputTokens)
	}
	if got.CacheReadInputTokens != 0 {
		t.Errorf("cache_read_input_tokens = %d, want 0", got.CacheReadInputTokens)
	}
}
