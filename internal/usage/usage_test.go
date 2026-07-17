package usage

import "testing"

func TestExtract_NonStream(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    Usage
		wantOK  bool
	}{
		{
			name:    "openai chat",
			payload: `{"id":"x","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`,
			want:    Usage{10, 20, 30, 0},
			wantOK:  true,
		},
		{
			name:    "openai responses (input/output tokens)",
			payload: `{"usage":{"input_tokens":100,"output_tokens":40,"total_tokens":140}}`,
			want:    Usage{100, 40, 140, 0},
			wantOK:  true,
		},
		{
			name:    "anthropic (no total → computed)",
			payload: `{"type":"message","usage":{"input_tokens":25,"output_tokens":250}}`,
			want:    Usage{25, 250, 275, 0},
			wantOK:  true,
		},
		{
			name:    "gemini usageMetadata",
			payload: `{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":16,"totalTokenCount":24}}`,
			want:    Usage{8, 16, 24, 0},
			wantOK:  true,
		},
		{
			name:    "no usage field",
			payload: `{"id":"x","choices":[{"delta":{"content":"hi"}}]}`,
			wantOK:  false,
		},
		{
			name:    "usage present but all zero",
			payload: `{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
			wantOK:  false,
		},
		{
			name:    "invalid json",
			payload: `not json`,
			wantOK:  false,
		},
		{
			name:    "done sentinel",
			payload: `[DONE]`,
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Extract([]byte(tc.payload))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, tc.wantOK, got)
			}
			if ok && got != tc.want {
				t.Fatalf("usage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestExtract_AnthropicStream 模拟 Anthropic 流:input 在 message_start 的
// message.usage, output 在后续 message_delta 的顶层 usage 单调增。逐帧 Extract
// + Merge 应收敛到最终 (25, 250)。
func TestExtract_AnthropicStream(t *testing.T) {
	frames := []string{
		`{"type":"message_start","message":{"id":"m","usage":{"input_tokens":25,"output_tokens":1}}}`,
		`{"type":"content_block_delta","delta":{"text":"foo"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":120}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":250}}`,
	}
	var acc Usage
	for _, f := range frames {
		if u, ok := Extract([]byte(f)); ok {
			acc = acc.Merge(u)
		}
	}
	want := Usage{PromptTokens: 25, CompletionTokens: 250, TotalTokens: 275}
	if acc != want {
		t.Fatalf("accumulated = %+v, want %+v", acc, want)
	}
}

// TestExtract_OpenAIStream 模拟 OpenAI 流:中间帧无 usage, 仅末帧
// (stream_options.include_usage) 给全量。
func TestExtract_OpenAIStream(t *testing.T) {
	frames := []string{
		`{"choices":[{"delta":{"content":"he"}}]}`,
		`{"choices":[{"delta":{"content":"llo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`,
	}
	var acc Usage
	hits := 0
	for _, f := range frames {
		if u, ok := Extract([]byte(f)); ok {
			acc = acc.Merge(u)
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("expected exactly 1 usage frame, got %d", hits)
	}
	want := Usage{12, 8, 20, 0}
	if acc != want {
		t.Fatalf("accumulated = %+v, want %+v", acc, want)
	}
}

func TestMerge_MaxPerField(t *testing.T) {
	a := Usage{PromptTokens: 25, CompletionTokens: 100, TotalTokens: 0}
	b := Usage{PromptTokens: 25, CompletionTokens: 250, TotalTokens: 0}
	got := a.Merge(b)
	// total 派生为 max(0, 25+250)=275。
	if got != (Usage{25, 250, 275, 0}) {
		t.Fatalf("merge = %+v", got)
	}
}

// TestMerge_KeepsLargerReportedTotal 保证上报 total 大于 prompt+completion
// (reasoning / cache token 计入) 时不被覆盖。
func TestMerge_KeepsLargerReportedTotal(t *testing.T) {
	a := Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 100}
	got := a.Merge(Usage{})
	if got.TotalTokens != 100 {
		t.Fatalf("total = %d, want 100", got.TotalTokens)
	}
}

// TestExtract_CachedTokens 验证三家缓存字段解析 + 语义归一化:
// PromptTokens 始终是"总输入"(含缓存), CachedPromptTokens 是其中缓存读子集。
func TestExtract_CachedTokens(t *testing.T) {
	cases := []struct {
		name           string
		payload        string
		wantPrompt     int
		wantCached     int
		wantCompletion int
	}{
		{
			// OpenAI: prompt_tokens 已含缓存, cached_tokens 是子集。
			name:           "openai prompt_tokens_details.cached_tokens",
			payload:        `{"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100,"prompt_tokens_details":{"cached_tokens":800}}}`,
			wantPrompt:     1000,
			wantCached:     800,
			wantCompletion: 100,
		},
		{
			// Anthropic: input_tokens 不含缓存, 需把 cache_read+cache_creation 补进总输入。
			name:           "anthropic cache_read + cache_creation",
			payload:        `{"usage":{"input_tokens":200,"output_tokens":50,"cache_read_input_tokens":800,"cache_creation_input_tokens":100}}`,
			wantPrompt:     1100, // 200 + 800 + 100
			wantCached:     800,  // 仅缓存读打折
			wantCompletion: 50,
		},
		{
			// Gemini: promptTokenCount 已含缓存。
			name:           "gemini cachedContentTokenCount",
			payload:        `{"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":100,"totalTokenCount":1100,"cachedContentTokenCount":600}}`,
			wantPrompt:     1000,
			wantCached:     600,
			wantCompletion: 100,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Extract([]byte(tc.payload))
			if !ok {
				t.Fatalf("expected ok")
			}
			if got.PromptTokens != tc.wantPrompt {
				t.Fatalf("prompt = %d, want %d", got.PromptTokens, tc.wantPrompt)
			}
			if got.CachedPromptTokens != tc.wantCached {
				t.Fatalf("cached = %d, want %d", got.CachedPromptTokens, tc.wantCached)
			}
			if got.CompletionTokens != tc.wantCompletion {
				t.Fatalf("completion = %d, want %d", got.CompletionTokens, tc.wantCompletion)
			}
		})
	}
}
