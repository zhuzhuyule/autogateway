package keypool

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestBuildModalityRequestImage(t *testing.T) {
	path, body, contentType, err := buildModalityRequest("image", "dall-e-3")
	if err != nil {
		t.Fatalf("image probe build failed: %v", err)
	}
	if path != "/v1/images/generations" || contentType != "application/json" {
		t.Fatalf("unexpected path/content-type: %s %s", path, contentType)
	}
	raw, _ := io.ReadAll(body)
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if payload["model"] != "dall-e-3" {
		t.Errorf("model = %v, want dall-e-3", payload["model"])
	}
	if payload["prompt"] == "" || payload["prompt"] != "a tiny red circle on white background" {
		t.Errorf("prompt = %v, want fixed probe prompt", payload["prompt"])
	}
	if payload["n"] != float64(1) {
		t.Errorf("n = %v, want 1", payload["n"])
	}
}

func TestBuildModalityRequestVision(t *testing.T) {
	path, body, _, err := buildModalityRequest("vision", "gpt-4o")
	if err != nil {
		t.Fatalf("vision probe build failed: %v", err)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("unexpected path: %s", path)
	}
	raw, _ := io.ReadAll(body)
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	msgs, ok := payload["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v, want 1 entry", payload["messages"])
	}
	content, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("content = %v, want [text, image_url] parts", msgs[0])
	}
	if content[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("second part type = %v, want image_url", content[1])
	}
	// The embedded PNG must be a real base64 data URL so vision models can
	// decode it; a corrupted image would 400 for the wrong reason.
	url := content[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") || len(url) < 64 {
		t.Errorf("image data url looks invalid: %.50s", url)
	}
}

func TestBuildModalityRequestUnsupported(t *testing.T) {
	if _, _, _, err := buildModalityRequest("smell", "m"); err == nil {
		t.Fatal("expected error for unsupported modality")
	}
}

func TestAsrRejectedProbeInput(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"too-short audio proves reachability", 400, "Audio file is too short", true},
		{"unsupported audio format", 422, "Unsupported audio format: wav", true},
		{"payload over limit", 413, "request entity too large", true},
		{"missing endpoint is a failure", 404, "not found", false},
		{"bad key is a failure", 401, "Invalid API key", false},
		{"rate limit is a failure", 429, "Too Many Requests", false},
		{"upstream outage is a failure", 503, "service unavailable", false},
		{"400 about an unknown model is a failure", 400, "model not found", false},
		{"same, snake_case spelling", 400, `{"error":{"code":"model_not_found"}}`, false},
		{"empty body on 400 still proves the route answered", 400, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := asrRejectedProbeInput(tc.status, tc.body); got != tc.want {
				t.Fatalf("asrRejectedProbeInput(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}
