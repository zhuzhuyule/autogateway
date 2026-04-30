package proxy

import (
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

	out, err := ps.applyParamOverrides(body, g)
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

	out, err := ps.applyParamOverrides(body, g)
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
	out, _ := ps.applyParamOverrides(body, g)
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
	out, _ := ps.applyParamOverrides(body, g)
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
	out, err := ps.applyParamOverrides([]byte(""), g)
	if err != nil || !reflect.DeepEqual(out, []byte("")) {
		t.Errorf("empty body should pass through unchanged: %v %v", out, err)
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
