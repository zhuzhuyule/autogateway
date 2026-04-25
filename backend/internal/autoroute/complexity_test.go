package autoroute

import (
	"testing"
)

func TestClassifier_Analyze_Simple(t *testing.T) {
	classifier := NewClassifier(nil)

	tests := []struct {
		name     string
		body     string
		expected ComplexityLevel
	}{
		{
			name:     "simple text request",
			body:     `{"messages":[{"role":"user","content":"hello"}]}`,
			expected: Simple,
		},
		{
			name:     "empty messages",
			body:     `{"messages":[]}`,
			expected: Simple,
		},
		{
			name:     "short text",
			body:     `{"messages":[{"role":"user","content":"hi"}]}`,
			expected: Simple,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis, err := classifier.Analyze([]byte(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if analysis.Level != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, analysis.Level)
			}
		})
	}
}

func TestClassifier_Analyze_Medium(t *testing.T) {
	classifier := NewClassifier(nil)

	tests := []struct {
		name     string
		body     string
		expected ComplexityLevel
	}{
		{
			name:     "request with tools",
			body:     `{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"func1"}]}`,
			expected: Medium,
		},
		{
			name:     "long text exceeds simple threshold",
			body:     `{"messages":[{"role":"user","content":"` + generateLongText(3000) + `"}]}`,
			expected: Medium,
		},
		{
			name:     "many messages",
			body:     `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hi"},{"role":"user","content":"hi"},{"role":"assistant","content":"hi"},{"role":"user","content":"hi"},{"role":"assistant","content":"hi"},{"role":"user","content":"hi"},{"role":"assistant","content":"hi"},{"role":"user","content":"hi"},{"role":"assistant","content":"hi"},{"role":"user","content":"hi"}]}`,
			expected: Medium,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis, err := classifier.Analyze([]byte(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if analysis.Level != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, analysis.Level)
			}
		})
	}
}

func TestClassifier_Analyze_Complex(t *testing.T) {
	classifier := NewClassifier(nil)

	tests := []struct {
		name     string
		body     string
		expected ComplexityLevel
	}{
		{
			name:     "request with vision",
			body:     `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":"http://example.com/image.png"}]}]}`,
			expected: Complex,
		},
		{
			name:     "many tools",
			body:     `{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"func1"},{"name":"func2"},{"name":"func3"},{"name":"func4"}]}`,
			expected: Complex,
		},
		{
			name:     "very long text exceeds complex threshold",
			body:     `{"messages":[{"role":"user","content":"` + generateLongText(12000) + `"}]}`,
			expected: Complex,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis, err := classifier.Analyze([]byte(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if analysis.Level != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, analysis.Level)
			}
		})
	}
}

func TestClassifier_Analyze_Error(t *testing.T) {
	classifier := NewClassifier(nil)

	invalidJSON := `{"messages: [{`
	_, err := classifier.Analyze([]byte(invalidJSON))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClassifier_Analyze_HasTools(t *testing.T) {
	classifier := NewClassifier(nil)

	body := `{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"func1"},{"name":"func2"}]}`
	analysis, err := classifier.Analyze([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !analysis.HasTools {
		t.Error("expected HasTools to be true")
	}
	if analysis.ToolCount != 2 {
		t.Errorf("expected ToolCount 2, got %d", analysis.ToolCount)
	}
}

func TestClassifier_Analyze_HasVision(t *testing.T) {
	classifier := NewClassifier(nil)

	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":"http://example.com/image.png"},{"type":"text","text":"describe this"}]}]}`
	analysis, err := classifier.Analyze([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !analysis.HasVision {
		t.Error("expected HasVision to be true")
	}
}

func TestClassifier_Analyze_TokenEstimation(t *testing.T) {
	classifier := NewClassifier(nil)

	body := `{"messages":[{"role":"user","content":"hello world"}]}`
	analysis, err := classifier.Analyze([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if analysis.EstimatedTokens <= 0 {
		t.Error("expected EstimatedTokens > 0")
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		runeCount int
		expected  int
	}{
		{0, 0},
		{10, 8},
		{100, 80},
	}

	for _, tt := range tests {
		result := estimateTokens(tt.runeCount)
		if result != tt.expected {
			t.Errorf("estimateTokens(%d): expected %d, got %d", tt.runeCount, tt.expected, result)
		}
	}
}

func generateLongText(length int) string {
	result := make([]byte, length)
	for i := range result {
		result[i] = 'a'
	}
	return string(result)
}
