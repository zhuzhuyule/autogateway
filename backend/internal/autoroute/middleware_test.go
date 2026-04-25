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

func TestMiddleware_NonChatCompletions(t *testing.T) {
	configProvider := func() *RouteConfig {
		return &RouteConfig{Enabled: true}
	}

	router := gin.New()
	router.Use(Middleware(NewClassifier(nil), configProvider, nil))
	router.GET("/proxy/:group_name/v1/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"models": []string{}})
	})

	req := httptest.NewRequest("GET", "/proxy/test-group/v1/models", nil)
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

func TestMiddleware_NoMapping(t *testing.T) {
	configProvider := func() *RouteConfig {
		return &RouteConfig{
			Enabled: true,
			GroupMapping: map[string]GroupMapping{
				"other-model": {
					SimpleGroup:  "other-lite",
					MediumGroup:  "other-pro",
					ComplexGroup: "other-max",
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

	if capturedGroupName != "gpt-4o" {
		t.Errorf("expected group 'gpt-4o' (unchanged), got '%s'", capturedGroupName)
	}
}

type mockGroupProvider struct {
	availableGroups map[string]bool
}

func (m *mockGroupProvider) IsGroupAvailable(groupName string) bool {
	if m.availableGroups == nil {
		return true
	}
	return m.availableGroups[groupName]
}

func TestMiddleware_Fallback(t *testing.T) {
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

	groupProvider := &mockGroupProvider{
		availableGroups: map[string]bool{
			"gpt-4o-lite": false,
			"gpt-4o-pro":  true,
			"gpt-4.1-max": false,
		},
	}

	var capturedGroupName string
	router := gin.New()
	router.Use(Middleware(NewClassifier(nil), configProvider, groupProvider))
	router.POST("/proxy/:group_name/v1/chat/completions", func(c *gin.Context) {
		capturedGroupName = c.Param("group_name")
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	body := `{"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/proxy/gpt-4o/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if capturedGroupName != "gpt-4o-pro" {
		t.Errorf("expected fallback group 'gpt-4o-pro', got '%s'", capturedGroupName)
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

func TestMiddleware_EmptyGroupName(t *testing.T) {
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

	router := gin.New()
	router.Use(Middleware(NewClassifier(nil), configProvider, nil))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	body := `{"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestGetFallbackGroup(t *testing.T) {
	mapping := GroupMapping{
		SimpleGroup:  "lite",
		MediumGroup:  "pro",
		ComplexGroup: "max",
	}

	provider := &mockGroupProvider{
		availableGroups: map[string]bool{
			"lite": true,
			"pro":  false,
			"max":  true,
		},
	}

	fallback := getFallbackGroup(mapping, Complex, provider)
	if fallback != "lite" {
		t.Errorf("expected fallback 'lite', got '%s'", fallback)
	}
}

func TestMiddleware_AnalysisResult(t *testing.T) {
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

	classifier := NewClassifier(nil)

	var analysis *RequestAnalysis
	router := gin.New()
	router.Use(Middleware(classifier, configProvider, nil))
	router.POST("/proxy/:group_name/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	body := `{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"func1"}]}`
	req := httptest.NewRequest("POST", "/proxy/gpt-4o/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	_ = analysis
}
