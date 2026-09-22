package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"autogateway/internal/models"

	"gorm.io/datatypes"
)

func mustJSONMap(t *testing.T, raw string) datatypes.JSONMap {
	t.Helper()
	m := datatypes.JSONMap{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	return m
}

func TestApplyOverridesFlatLegacyShape(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"gpt-4o","temperature":0.9}`)
	g := &models.Group{ParamOverrides: mustJSONMap(t, `{"temperature":0.3,"top_p":0.5}`)}

	out, err := ps.applyParamOverrides(body, g, "gpt-4o")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["temperature"] != 0.3 {
		t.Errorf("flat override should set temperature=0.3, got %v", got["temperature"])
	}
	if got["top_p"] != 0.5 {
		t.Errorf("flat override should set top_p=0.5, got %v", got["top_p"])
	}
}

func TestApplyOverridesNestedShapeStarPlusModel(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"gpt-5","messages":[]}`)
	g := &models.Group{
		ParamOverrides: mustJSONMap(t, `{
			"*": {"temperature": 0.2},
			"gpt-5": {"reasoning_effort": "high"}
		}`),
	}

	out, err := ps.applyParamOverrides(body, g, "gpt-4o")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["temperature"] != 0.2 {
		t.Errorf("expected * fallback temperature=0.2, got %v", got["temperature"])
	}
	if got["reasoning_effort"] != "high" {
		t.Errorf("expected gpt-5 override reasoning_effort=high, got %v", got["reasoning_effort"])
	}
}

func TestApplyOverridesNestedModelOverridesStar(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"gpt-5"}`)
	g := &models.Group{
		ParamOverrides: mustJSONMap(t, `{
			"*": {"temperature": 0.2},
			"gpt-5": {"temperature": 0.9}
		}`),
	}
	out, _ := ps.applyParamOverrides(body, g, "gpt-4o")
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["temperature"] != 0.9 {
		t.Errorf("model-specific must beat *, got %v", got["temperature"])
	}
}

func TestApplyOverridesNestedNoMatchUsesStarOnly(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"gpt-4o-mini"}`)
	g := &models.Group{
		ParamOverrides: mustJSONMap(t, `{
			"*": {"temperature": 0.2},
			"gpt-5": {"reasoning_effort": "high"}
		}`),
	}
	out, _ := ps.applyParamOverrides(body, g, "gpt-4o")
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["temperature"] != 0.2 {
		t.Errorf("expected * fallback, got %v", got["temperature"])
	}
	if _, ok := got["reasoning_effort"]; ok {
		t.Errorf("non-matching model should not get gpt-5 overrides, got %v", got)
	}
}

func TestApplyOverridesEmptyBody(t *testing.T) {
	ps := &ProxyServer{}
	g := &models.Group{ParamOverrides: mustJSONMap(t, `{"temperature":0.3}`)}
	out, err := ps.applyParamOverrides([]byte(""), g, "")
	if err != nil || !reflect.DeepEqual(out, []byte("")) {
		t.Errorf("empty body should pass through unchanged: %v %v", out, err)
	}
}

// ---------- advanced 模式 (paramops) ----------

func TestApplyOverridesAdvancedOperations(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"Qwen3-8B","messages":[{"role":"user","content":"hi"}],"max_tokens":2000}`)
	g := &models.Group{
		ParamOverrides: mustJSONMap(t, `{"operations":[
			{"mode":"prepend","path":"messages","value":{"role":"system","content":"中文回答"}},
			{"mode":"set","path":"temperature","value":0.3}
		]}`),
	}
	out, err := ps.applyParamOverrides(body, g, "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("system message should be prepended, got %d messages", len(msgs))
	}
	if got["temperature"] != 0.3 {
		t.Errorf("temperature = %v, want 0.3", got["temperature"])
	}
}

// advanced 规则里的内置变量要能拿到"客户端原始模型名"
func TestApplyOverridesAdvancedUsesOriginalModelVar(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"Qwen3-8B","messages":[]}`)
	g := &models.Group{
		ParamOverrides: mustJSONMap(t, `{"operations":[
			{"mode":"set","path":"top_k","value":10,
			 "conditions":[{"path":"original_model","mode":"prefix","value":"claude"}]}
		]}`),
	}

	// 原始模型是 claude-* → 命中
	out, err := ps.applyParamOverrides(body, g, "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(string(out), `"top_k":10`) {
		t.Errorf("condition on original_model should hit: %s", out)
	}

	// 原始模型不是 claude-* → 不命中
	out, err = ps.applyParamOverrides(body, g, "gpt-4o")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(string(out), "top_k") {
		t.Errorf("condition on original_model should miss: %s", out)
	}
}

// 关键安全点: 含 operations 键时绝不能退回 legacy flat 分支 ——
// 那会把 operations 数组当普通字段塞进出站 body。
func TestApplyOverridesAdvancedNeverFallsBackToLegacy(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"m"}`)
	g := &models.Group{ParamOverrides: mustJSONMap(t, `{"operations":[{"mode":"nope"}]}`)}

	out, err := ps.applyParamOverrides(body, g, "m")
	if err == nil {
		t.Fatalf("malformed operations must error, not silently degrade; got %s", out)
	}
	if strings.Contains(string(out), "operations") {
		t.Errorf("operations must never be injected into the outbound body: %s", out)
	}
}

// 规则本身合法但运行时失败(路径不存在) → 报错且 body 保持原样
func TestApplyOverridesAdvancedRuntimeErrorKeepsBody(t *testing.T) {
	ps := &ProxyServer{}
	body := []byte(`{"model":"m"}`)
	g := &models.Group{
		ParamOverrides: mustJSONMap(t, `{"operations":[{"mode":"append","path":"missing","value":"x"}]}`),
	}
	out, err := ps.applyParamOverrides(body, g, "m")
	if err == nil {
		t.Fatal("expected an error for a missing path")
	}
	if string(out) != `{"model":"m"}` {
		t.Errorf("on error the body must be returned unchanged, got %s", out)
	}
}

func TestShouldValidateJSONSuccess(t *testing.T) {
	if !shouldValidateJSONSuccess("/proxy/openai/v1/chat/completions", false) {
		t.Fatalf("expected chat completions to require JSON validation")
	}
	if shouldValidateJSONSuccess("/proxy/openai/v1/chat/completions", true) {
		t.Fatalf("expected streaming responses to skip JSON validation")
	}
	if shouldValidateJSONSuccess("/proxy/openai/v1/models", false) {
		t.Fatalf("expected models endpoint to skip chat JSON validation")
	}
	if !shouldValidateJSONSuccess("/proxy/openai/v1/images/generations", false) {
		t.Fatalf("expected images generations to require JSON validation")
	}
	if shouldValidateJSONSuccess("/proxy/openai/v1/images/generations", true) {
		t.Fatalf("expected streaming responses to skip JSON validation")
	}
}

func TestValidateJSONSuccessResponseRejectsHTML(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader("<html></html>")),
	}

	if err := validateJSONSuccessResponse(resp); err == nil {
		t.Fatalf("expected HTML success response to be rejected")
	}
}

// hasIncludeUsage 解析 body 判断 stream_options.include_usage 是否为 true。
func hasIncludeUsage(t *testing.T, body []byte) bool {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	so, ok := obj["stream_options"].(map[string]any)
	if !ok {
		return false
	}
	v, _ := so["include_usage"].(bool)
	return v
}

func TestInjectStreamUsage(t *testing.T) {
	t.Run("chat request gets include_usage", func(t *testing.T) {
		in := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`)
		out := injectStreamUsage(in)
		if !hasIncludeUsage(t, out) {
			t.Fatalf("expected include_usage=true, got %s", out)
		}
	})

	t.Run("existing stream_options augmented", func(t *testing.T) {
		in := []byte(`{"messages":[{"role":"user","content":"hi"}],"stream_options":{"foo":1}}`)
		out := injectStreamUsage(in)
		if !hasIncludeUsage(t, out) {
			t.Fatalf("expected include_usage=true, got %s", out)
		}
		var obj map[string]any
		_ = json.Unmarshal(out, &obj)
		if so, _ := obj["stream_options"].(map[string]any); so["foo"] == nil {
			t.Fatalf("existing stream_options key lost: %s", out)
		}
	})

	t.Run("client explicit include_usage=false respected", func(t *testing.T) {
		in := []byte(`{"messages":[{"role":"user"}],"stream_options":{"include_usage":false}}`)
		out := injectStreamUsage(in)
		if hasIncludeUsage(t, out) {
			t.Fatalf("must not override client's include_usage=false: %s", out)
		}
	})

	t.Run("non-chat request (no messages) untouched", func(t *testing.T) {
		in := []byte(`{"model":"text-embedding-3-small","input":"hi"}`)
		out := injectStreamUsage(in)
		if !bytes.Equal(in, out) {
			t.Fatalf("non-chat request should be untouched, got %s", out)
		}
	})

	t.Run("invalid json passthrough", func(t *testing.T) {
		in := []byte(`not json`)
		if out := injectStreamUsage(in); !bytes.Equal(in, out) {
			t.Fatalf("invalid json should pass through unchanged")
		}
	})
}
