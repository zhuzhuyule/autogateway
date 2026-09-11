package apicompat

// 流式转换:两个方向的事件粒度完全不同,必须用显式状态机对齐。
//
//   - Anthropic: message_start → content_block_start → content_block_delta*
//     → content_block_stop → message_delta → message_stop
//     内容块按**顺序 index** 引用,且生命周期严格有序 (start→delta*→stop)
//   - Chat:      chunk* → [DONE]
//     delta 是无生命周期的平铺增量,tool_call 用客户端维度的整数 index
//
// 两个状态机都提供幂等的 Finalize():上游没发终止事件就断开时,由它合成
// 终止事件。少了这一步,Claude Code 这类严格客户端会一直挂在那儿等。

// ---------------------------------------------------------------------------
// 流式事件 DTO
// ---------------------------------------------------------------------------

// AnthropicStreamEvent 是 Anthropic SSE 的一个事件。
// 各事件字段差异很大,故用可选字段组合而不是每种事件一个结构体。
type AnthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Index        *int                   `json:"index,omitempty"`
	Message      *AnthropicResponse     `json:"message,omitempty"`
	ContentBlock *AnthropicContentBlock `json:"content_block,omitempty"`
	Delta        map[string]any         `json:"delta,omitempty"`
	Usage        *AnthropicUsage        `json:"usage,omitempty"`
}

// ChatStreamChunk 是 OpenAI 流式响应的一个 chunk。
type ChatStreamChunk struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []ChatStreamChoice `json:"choices"`
	Usage   *ChatUsage         `json:"usage,omitempty"`
}

// ChatStreamChoice 是一个候选的流式增量。
type ChatStreamChoice struct {
	Index        int             `json:"index"`
	Delta        ChatStreamDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

// ChatStreamDelta 是增量内容。ToolCalls[].Index 是客户端维度的整数下标。
type ChatStreamDelta struct {
	Role             string               `json:"role,omitempty"`
	Content          string               `json:"content,omitempty"`
	ReasoningContent string               `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatStreamToolCall `json:"tool_calls,omitempty"`
}

// ChatStreamToolCall 是流式工具调用增量。首帧带 ID/Name,后续帧只带 arguments 分片。
type ChatStreamToolCall struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function *ChatFunctionCall `json:"function,omitempty"`
}

// chatCompletionChunkObject 是流式 chunk 的 object 字段固定值。
const chatCompletionChunkObject = "chat.completion.chunk"

// ---------------------------------------------------------------------------
// Chat 流 → Anthropic 流 (上游说 Chat,客户端说 Anthropic)
// ---------------------------------------------------------------------------

// ChatToAnthropicStream 把 OpenAI 流式 chunk 序列转换成 Anthropic 事件序列。
type ChatToAnthropicStream struct {
	started   bool
	finalized bool

	id    string
	model string

	blockIndex int
	openKind   string      // "" | "thinking" | "text" | "tool_use"
	toolBlocks map[int]int // Chat tool index → Anthropic block index

	inputTokens  int
	outputTokens int
	stopReason   string
}

// NewChatToAnthropicStream 创建一个新的转换状态机。
func NewChatToAnthropicStream() *ChatToAnthropicStream {
	return &ChatToAnthropicStream{toolBlocks: make(map[int]int)}
}

// Process 消费一个上游 chunk,返回应发给客户端的 Anthropic 事件(可能为空)。
func (s *ChatToAnthropicStream) Process(chunk *ChatStreamChunk) []AnthropicStreamEvent {
	if chunk == nil {
		return nil
	}
	var out []AnthropicStreamEvent

	if chunk.ID != "" {
		s.id = chunk.ID
	}
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.Usage != nil {
		s.inputTokens = max(s.inputTokens, chunk.Usage.PromptTokens)
		s.outputTokens = max(s.outputTokens, chunk.Usage.CompletionTokens)
	}

	if !s.started {
		s.started = true
		out = append(out, AnthropicStreamEvent{
			Type: "message_start",
			Message: &AnthropicResponse{
				ID:      s.id,
				Type:    "message",
				Role:    "assistant",
				Model:   s.model,
				Content: []AnthropicContentBlock{},
				Usage:   AnthropicUsage{InputTokens: s.inputTokens, OutputTokens: 1},
			},
		})
	}

	for _, choice := range chunk.Choices {
		d := choice.Delta

		if d.ReasoningContent != "" {
			if s.openKind != "thinking" {
				out = append(out, s.closeBlock()...)
				out = append(out, s.openBlock("thinking")...)
			}
			out = append(out, s.deltaEvent(map[string]any{
				"type":     "thinking_delta",
				"thinking": d.ReasoningContent,
			})...)
		}

		if d.Content != "" {
			if s.openKind != "text" {
				out = append(out, s.closeBlock()...)
				out = append(out, s.openBlock("text")...)
			}
			out = append(out, s.deltaEvent(map[string]any{
				"type": "text_delta",
				"text": d.Content,
			})...)
		}

		for _, tc := range d.ToolCalls {
			blockIdx, known := s.toolBlocks[tc.Index]
			if !known {
				name := ""
				if tc.Function != nil {
					name = tc.Function.Name
				}
				out = append(out, s.closeBlock()...)
				blockIdx, evs := s.openToolBlock(tc.ID, name)
				s.toolBlocks[tc.Index] = blockIdx
				out = append(out, evs...)
			}
			if tc.Function != nil && tc.Function.Arguments != "" {
				out = append(out, deltaEventAt(blockIdx, map[string]any{
					"type":         "input_json_delta",
					"partial_json": tc.Function.Arguments,
				})...)
			}
		}

		if choice.FinishReason != nil && *choice.FinishReason != "" {
			s.stopReason = chatFinishToAnthropicStopReason(*choice.FinishReason, len(s.toolBlocks))
		}
	}

	return out
}

// Finalize 在流结束时调用,补齐未关闭的块与终止事件。
//
// 幂等:重复调用不产出事件。这是断流兜底 —— 上游没发 [DONE] 就断开时,
// 没有这一步客户端会永远等下去。
func (s *ChatToAnthropicStream) Finalize() []AnthropicStreamEvent {
	if s.finalized {
		return nil
	}
	s.finalized = true
	if !s.started {
		return nil // 什么都没发过,不要产出孤儿事件
	}

	out := s.closeBlock()
	stopReason := s.stopReason
	if stopReason == "" {
		stopReason = "end_turn"
	}
	out = append(out, AnthropicStreamEvent{
		Type:  "message_delta",
		Delta: map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		Usage: &AnthropicUsage{OutputTokens: s.outputTokens},
	})
	out = append(out, AnthropicStreamEvent{Type: "message_stop"})
	return out
}

func (s *ChatToAnthropicStream) allocBlock() int {
	idx := s.blockIndex
	s.blockIndex++
	return idx
}

func (s *ChatToAnthropicStream) closeBlock() []AnthropicStreamEvent {
	if s.openKind == "" {
		return nil
	}
	idx := s.blockIndex - 1
	s.openKind = ""
	return []AnthropicStreamEvent{{Type: "content_block_stop", Index: &idx}}
}

func (s *ChatToAnthropicStream) openBlock(kind string) []AnthropicStreamEvent {
	idx := s.allocBlock()
	s.openKind = kind
	block := &AnthropicContentBlock{Type: kind}
	switch kind {
	case "text":
		block.Text = ""
	case "thinking":
		block.Thinking = ""
	}
	return []AnthropicStreamEvent{{
		Type:         "content_block_start",
		Index:        &idx,
		ContentBlock: block,
	}}
}

func (s *ChatToAnthropicStream) openToolBlock(id, name string) (int, []AnthropicStreamEvent) {
	idx := s.allocBlock()
	s.openKind = "tool_use"
	ev := AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx,
		ContentBlock: &AnthropicContentBlock{
			Type:  "tool_use",
			ID:    id,
			Name:  name,
			Input: map[string]any{},
		},
	}
	return idx, []AnthropicStreamEvent{ev}
}

func (s *ChatToAnthropicStream) deltaEvent(delta map[string]any) []AnthropicStreamEvent {
	idx := s.blockIndex - 1
	if idx < 0 {
		idx = 0
	}
	return deltaEventAt(idx, delta)
}

func deltaEventAt(idx int, delta map[string]any) []AnthropicStreamEvent {
	i := idx
	return []AnthropicStreamEvent{{Type: "content_block_delta", Index: &i, Delta: delta}}
}

// ---------------------------------------------------------------------------
// Anthropic 流 → Chat 流 (上游说 Anthropic,客户端说 Chat)
// ---------------------------------------------------------------------------

// AnthropicToChatStream 把 Anthropic 事件序列转换成 OpenAI chunk 序列。
type AnthropicToChatStream struct {
	started   bool
	roleSent  bool
	finalized bool

	id    string
	model string

	toolIndex   int         // 客户端维度的 tool index 计数器
	blockToTool map[int]int // Anthropic block index → Chat tool index

	// usage 原始分量(Anthropic 互斥桶),finish 时投影成包容桶
	inputTokens  int
	outputTokens int
	cacheRead    int
	cacheCreate  int

	finishReason string
}

// NewAnthropicToChatStream 创建一个新的转换状态机。
func NewAnthropicToChatStream() *AnthropicToChatStream {
	return &AnthropicToChatStream{blockToTool: make(map[int]int)}
}

// Process 消费一个上游事件,返回应发给客户端的 chunk。
// 第二个返回值表示是否已到流尾(调用方据此发 [DONE])。
func (s *AnthropicToChatStream) Process(ev *AnthropicStreamEvent) ([]ChatStreamChunk, bool) {
	if ev == nil {
		return nil, false
	}
	var out []ChatStreamChunk

	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			if ev.Message.ID != "" {
				s.id = ev.Message.ID
			}
			if ev.Message.Model != "" {
				s.model = ev.Message.Model
			}
			s.inputTokens = ev.Message.Usage.InputTokens
			s.cacheRead = ev.Message.Usage.CacheReadInputTokens
			s.cacheCreate = ev.Message.Usage.CacheCreationInputTokens
		}
		s.started = true

	case "content_block_start":
		if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
			idx := 0
			if ev.Index != nil {
				idx = *ev.Index
			}
			toolIdx := s.toolIndex
			s.toolIndex++
			s.blockToTool[idx] = toolIdx
			out = append(out, s.chunk(ChatStreamDelta{
				ToolCalls: []ChatStreamToolCall{{
					Index:    toolIdx,
					ID:       ev.ContentBlock.ID,
					Type:     "function",
					Function: &ChatFunctionCall{Name: ev.ContentBlock.Name, Arguments: ""},
				}},
			}))
		}

	case "content_block_delta":
		if ev.Delta == nil {
			return out, false
		}
		switch ev.Delta["type"] {
		case "text_delta":
			if text, _ := ev.Delta["text"].(string); text != "" {
				out = append(out, s.chunk(ChatStreamDelta{Content: text}))
			}
		case "thinking_delta":
			if thinking, _ := ev.Delta["thinking"].(string); thinking != "" {
				out = append(out, s.chunk(ChatStreamDelta{ReasoningContent: thinking}))
			}
		case "input_json_delta":
			partial, _ := ev.Delta["partial_json"].(string)
			idx := 0
			if ev.Index != nil {
				idx = *ev.Index
			}
			toolIdx, ok := s.blockToTool[idx]
			if !ok {
				// 没见过 start 的孤儿分片 —— 直接丢弃,防止乱序 delta 污染输出。
				return out, false
			}
			if partial != "" {
				out = append(out, s.chunk(ChatStreamDelta{
					ToolCalls: []ChatStreamToolCall{{
						Index:    toolIdx,
						Function: &ChatFunctionCall{Arguments: partial},
					}},
				}))
			}
		}

	case "message_delta":
		if ev.Delta != nil {
			if sr, ok := ev.Delta["stop_reason"].(string); ok && sr != "" {
				s.finishReason = anthropicStopReasonToChatFinish(sr, s.toolIndex)
			}
		}
		if ev.Usage != nil {
			s.outputTokens = max(s.outputTokens, ev.Usage.OutputTokens)
		}

	case "message_stop":
		return s.finish(), true
	}

	return out, false
}

// Finalize 在流结束时调用,补齐 finish chunk 与 [DONE]。幂等。
func (s *AnthropicToChatStream) Finalize() ([]ChatStreamChunk, bool) {
	if s.finalized {
		return nil, false
	}
	s.finalized = true
	if !s.started {
		return nil, false
	}
	return s.finish(), true
}

func (s *AnthropicToChatStream) finish() []ChatStreamChunk {
	reason := s.finishReason
	if reason == "" {
		reason = "stop"
	}
	chunk := s.chunk(ChatStreamDelta{})
	chunk.Choices[0].FinishReason = &reason

	// 互斥桶 → 包容桶
	prompt := s.inputTokens + s.cacheRead + s.cacheCreate
	u := &ChatUsage{
		PromptTokens:     prompt,
		CompletionTokens: s.outputTokens,
		TotalTokens:      prompt + s.outputTokens,
	}
	if s.cacheRead > 0 {
		u.PromptTokensDetails = &ChatPromptTokensDetails{CachedTokens: s.cacheRead}
	}
	chunk.Usage = u
	return []ChatStreamChunk{chunk}
}

func (s *AnthropicToChatStream) chunk(delta ChatStreamDelta) ChatStreamChunk {
	// 首个 chunk 必须带 role=assistant,否则部分客户端(含 OpenAI SDK)不认。
	if !s.roleSent {
		s.roleSent = true
		delta.Role = "assistant"
	}
	return ChatStreamChunk{
		ID:      s.id,
		Object:  chatCompletionChunkObject,
		Model:   s.model,
		Choices: []ChatStreamChoice{{Index: 0, Delta: delta}},
	}
}
