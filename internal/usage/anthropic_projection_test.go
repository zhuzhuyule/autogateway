package usage

import "testing"

// TestAnthropicProjection 验证包容桶 → 互斥桶的投影。
func TestAnthropicProjection(t *testing.T) {
	cases := []struct {
		name                  string
		u                     Usage
		wantInput, wantOutput int
		wantCache             int
	}{
		{
			name:      "无缓存",
			u:         Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
			wantInput: 100, wantOutput: 20, wantCache: 0,
		},
		{
			name:      "有缓存读: input 必须剔除缓存",
			u:         Usage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050, CachedPromptTokens: 900},
			wantInput: 100, wantOutput: 50, wantCache: 900,
		},
		{
			name:      "脏数据: 缓存超过总输入时钳制, 不产生负数",
			u:         Usage{PromptTokens: 100, CompletionTokens: 1, CachedPromptTokens: 500},
			wantInput: 0, wantOutput: 1, wantCache: 100,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input, output, cache := c.u.Anthropic()
			if input != c.wantInput || output != c.wantOutput || cache != c.wantCache {
				t.Fatalf("Anthropic() = (%d, %d, %d), want (%d, %d, %d)",
					input, output, cache, c.wantInput, c.wantOutput, c.wantCache)
			}
		})
	}
}

// TestAnthropicProjectionRoundTrip 验证投影是可逆的:
// 包容桶 → 互斥桶 → 包容桶 应该回到原值(normalize 的逆运算)。
func TestAnthropicProjectionRoundTrip(t *testing.T) {
	original := Usage{PromptTokens: 1000, CompletionTokens: 250, TotalTokens: 1250, CachedPromptTokens: 700}

	input, output, cache := original.Anthropic()
	// 还原成 Anthropic 的 wire 字段,再走 normalize 回到包容桶。
	back := usageFields{
		InputTokens:          input,
		OutputTokens:         output,
		CacheReadInputTokens: cache,
	}.normalize()

	if back.PromptTokens != original.PromptTokens {
		t.Errorf("PromptTokens = %d, want %d", back.PromptTokens, original.PromptTokens)
	}
	if back.CompletionTokens != original.CompletionTokens {
		t.Errorf("CompletionTokens = %d, want %d", back.CompletionTokens, original.CompletionTokens)
	}
	if back.CachedPromptTokens != original.CachedPromptTokens {
		t.Errorf("CachedPromptTokens = %d, want %d", back.CachedPromptTokens, original.CachedPromptTokens)
	}
}
