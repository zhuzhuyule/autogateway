package autoroute

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

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

func TestMiddleware_Disabled(t *testing.T) {
	configProvider := func() *RouteConfig {
		return &RouteConfig{Enabled: false}
	}

	router := gin.New()
	router.Use(Middleware(NewClassifier(nil), configProvider, nil))
	router.POST("/proxy/:group_name/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	body := `{"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/proxy/test-group/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestMiddleware_RedirectToSimpleGroup(t *testing.T) {
	configProvider := func() *RouteConfig {
		return &RouteConfig{
			Enabled: true,
			GroupMapping: map[string]GroupMapping{
				"gpt-4o": {
					SimpleGroup:  "gpt-4o-lite",
					MediumGroup:  "gpt-4o-pro",
					ComplexGroup: "gpt-4.1-max",
				},
			},
		}
	}

	var capturedGroupName string
	router := gin.New()
	router.Use(Middleware(NewClassifier(nil), configProvider, nil))
	router.POST("/proxy/:group_name/v1/chat/completions", func(c *gin.Context) {
		capturedGroupName = c.Param("group_name")
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	body := `{"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/proxy/gpt-4o/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if capturedGroupName != "gpt-4o-lite" {
		t.Errorf("expected group 'gpt-4o-lite', got '%s'", capturedGroupName)
	}
}

func TestMiddleware_RedirectToComplexGroup(t *testing.T) {
	configProvider := func() *RouteConfig {
		return &RouteConfig{
			Enabled: true,
			GroupMapping: map[string]GroupMapping{
				"gpt-4o": {
					SimpleGroup:  "gpt-4o-lite",
					MediumGroup:  "gpt-4o-pro",
					ComplexGroup: "gpt-4.1-max",
				},
			},
		}
	}

	var capturedGroupName string
	router := gin.New()
	router.Use(Middleware(NewClassifier(nil), configProvider, nil))
	router.POST("/proxy/:group_name/v1/chat/completions", func(c *gin.Context) {
		capturedGroupName = c.Param("group_name")
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":"http://example.com/image.png"}]}]}`
	req := httptest.NewRequest("POST", "/proxy/gpt-4o/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if capturedGroupName != "gpt-4.1-max" {
		t.Errorf("expected group 'gpt-4.1-max', got '%s'", capturedGroupName)
	}
}

func TestSelectTargetGroup(t *testing.T) {
	mapping := GroupMapping{
		SimpleGroup:  "lite",
		MediumGroup:  "pro",
		ComplexGroup: "max",
	}

	tests := []struct {
		level    ComplexityLevel
		expected string
	}{
		{Simple, "lite"},
		{Medium, "pro"},
		{Complex, "max"},
	}

	for _, tt := range tests {
		result := selectTargetGroup(mapping, tt.level)
		if result != tt.expected {
			t.Errorf("selectTargetGroup(%s): expected %s, got %s", tt.level, tt.expected, result)
		}
	}
}

func TestSetParam(t *testing.T) {
	params := gin.Params{
		{Key: "group_name", Value: "original"},
		{Key: "other", Value: "value"},
	}

	result := setParam(params, "group_name", "new")

	if result[0].Value != "new" {
		t.Errorf("expected 'new', got '%s'", result[0].Value)
	}
}

func TestIsChatCompletions(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/v1/chat/completions", true},
		{"/proxy/gpt-4o/v1/chat/completions", true},
		{"/v1/models", false},
		{"/v1/completions", false},
	}

	for _, tt := range tests {
		result := isChatCompletions(tt.path)
		if result != tt.expected {
			t.Errorf("isChatCompletions(%s): expected %v, got %v", tt.path, tt.expected, result)
		}
	}
}

func TestConfigManager_DefaultConfig(t *testing.T) {
	manager := NewConfigManager(NewMemoryConfigStore())

	err := manager.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := manager.GetConfig()
	if cfg.Enabled {
		t.Error("expected Enabled to be false by default")
	}
}

func TestConfigManager_SaveAndLoad(t *testing.T) {
	store := NewMemoryConfigStore()
	manager := NewConfigManager(store)

	cfg := &RouteConfig{
		Enabled:          true,
		SimpleThreshold:  3000,
		ComplexThreshold: 10000,
		GroupMapping: map[string]GroupMapping{
			"gpt-4o": {
				SimpleGroup:  "gpt-4o-lite",
				MediumGroup:  "gpt-4o-pro",
				ComplexGroup: "gpt-4.1-max",
			},
		},
	}

	err := manager.Save(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	manager2 := NewConfigManager(store)
	err = manager2.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg2 := manager2.GetConfig()
	if !cfg2.Enabled {
		t.Error("expected Enabled to be true")
	}
	if cfg2.SimpleThreshold != 3000 {
		t.Errorf("expected SimpleThreshold 3000, got %d", cfg2.SimpleThreshold)
	}
}

func TestRouteConfig_ToJSON(t *testing.T) {
	cfg := &RouteConfig{
		Enabled:         true,
		SimpleThreshold: 2000,
		GroupMapping: map[string]GroupMapping{
			"gpt-4o": {
				SimpleGroup:  "lite",
				MediumGroup:  "pro",
				ComplexGroup: "max",
			},
		},
	}

	jsonStr, err := cfg.ToJSON()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}

func TestRouteConfigFromJSON(t *testing.T) {
	jsonStr := `{"enabled":true,"simple_threshold":2000,"complex_threshold":8000,"group_mapping":{"gpt-4o":{"simple_group":"lite","medium_group":"pro","complex_group":"max"}}}`

	cfg, err := RouteConfigFromJSON(jsonStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.Enabled {
		t.Error("expected Enabled to be true")
	}
	if cfg.SimpleThreshold != 2000 {
		t.Errorf("expected 2000, got %d", cfg.SimpleThreshold)
	}
}

func generateLongText(length int) string {
	result := make([]byte, length)
	for i := range result {
		result[i] = 'a'
	}
	return string(result)
}
