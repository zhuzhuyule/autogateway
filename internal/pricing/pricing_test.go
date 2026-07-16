package pricing

import (
	"math"
	"testing"

	"autogateway/internal/usage"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCost(t *testing.T) {
	cases := []struct {
		name  string
		model string
		u     usage.Usage
		want  float64
	}{
		{
			// gpt-4o: 1M in @ $2.5, 1M out @ $10 → 12.5
			name:  "gpt-4o full million",
			model: "gpt-4o",
			u:     usage.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
			want:  12.5,
		},
		{
			// 具体前缀优先: gpt-4o-mini 不应命中 gpt-4o。
			name:  "gpt-4o-mini takes precedence",
			model: "gpt-4o-mini-2024-07-18",
			u:     usage.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
			want:  0.75,
		},
		{
			// 带日期后缀的 claude sonnet 仍应命中 sonnet。
			name:  "claude sonnet versioned",
			model: "claude-3-5-sonnet-20241022",
			u:     usage.Usage{PromptTokens: 1_000_000, CompletionTokens: 0},
			want:  3.0,
		},
		{
			name:  "unknown model → 0",
			model: "some-free-qwen3-coder",
			u:     usage.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
			want:  0,
		},
		{
			name:  "empty model → 0",
			model: "",
			u:     usage.Usage{PromptTokens: 100, CompletionTokens: 100},
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Cost(tc.model, tc.u)
			if !approx(got, tc.want) {
				t.Fatalf("Cost(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestLookupPrecedence(t *testing.T) {
	// opus / haiku 不能被 sonnet 抢先。
	if r, _ := Lookup("claude-3-opus-20240229"); r.OutputPerM != 75.0 {
		t.Fatalf("opus output = %v, want 75", r.OutputPerM)
	}
	if r, _ := Lookup("claude-3-5-haiku-latest"); r.OutputPerM != 4.0 {
		t.Fatalf("haiku output = %v, want 4", r.OutputPerM)
	}
}
