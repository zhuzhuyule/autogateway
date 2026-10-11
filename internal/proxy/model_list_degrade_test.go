package proxy

import (
	"autogateway/internal/channel"
	"autogateway/internal/models"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
)

// stubChannel 复用 BaseChannel.TransformModelList (只用入参, 不依赖内部状态),
// 其余方法给最小实现, 只为满足 channel.ChannelProxy 接口.
type stubChannel struct {
	*channel.BaseChannel
}

func (s *stubChannel) ModifyRequest(*http.Request, *models.APIKey, *models.Group) {}
func (s *stubChannel) IsStreamRequest(*gin.Context, []byte) bool                  { return false }
func (s *stubChannel) ExtractModel(*gin.Context, []byte) string                   { return "" }
func (s *stubChannel) ValidateKey(context.Context, *models.APIKey, *models.Group) channel.ValidateResult {
	return channel.ValidateResult{}
}

var _ channel.ChannelProxy = (*stubChannel)(nil)

func degradeGroup(available []string, exposed, blocked datatypes.JSON, mode string) *models.Group {
	raw, _ := json.Marshal(available)
	return &models.Group{
		Name:             "smk",
		ChannelType:      "openai",
		GroupType:        "standard",
		AvailableModels:  datatypes.JSON(raw),
		ExposedModels:    exposed,
		BlockedModels:    blocked,
		ModelRoutingMode: mode,
	}
}

func TestBuildCachedModelListBodyOpenAIShape(t *testing.T) {
	body := buildCachedModelListBody("/proxy/g/v1/models", []string{"a", "b"})
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if parsed["object"] != "list" {
		t.Fatalf("expected object=list, got %#v", parsed["object"])
	}
	data, ok := parsed["data"].([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("expected 2 entries in data, got %#v", parsed["data"])
	}
	first := data[0].(map[string]any)
	if first["id"] != "a" || first["object"] != "model" {
		t.Fatalf("unexpected entry %#v", first)
	}
}

func TestBuildCachedModelListBodyGeminiNativeShape(t *testing.T) {
	body := buildCachedModelListBody("/proxy/g/v1beta/models", []string{"gemini-2.0-flash"})
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if _, hasData := parsed["data"]; hasData {
		t.Fatalf("gemini native path must not emit openai data field: %s", body)
	}
	modelsArr, ok := parsed["models"].([]any)
	if !ok || len(modelsArr) != 1 {
		t.Fatalf("expected models[1], got %#v", parsed["models"])
	}
	entry := modelsArr[0].(map[string]any)
	if entry["name"] != "models/gemini-2.0-flash" || entry["displayName"] != "gemini-2.0-flash" {
		t.Fatalf("unexpected gemini entry %#v", entry)
	}
}

// gemini 的 openai 兼容端点仍要 OpenAI 形状.
func TestBuildCachedModelListBodyGeminiOpenAICompat(t *testing.T) {
	body := buildCachedModelListBody("/proxy/g/v1beta/openai/v1/models", []string{"x"})
	if !strings.Contains(string(body), `"object":"list"`) {
		t.Fatalf("expected openai shape, got %s", body)
	}
}

func TestModelListDegradeIgnoresNonModelListRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/smk/v1/chat/completions", nil)

	ps := &ProxyServer{}
	if ps.modelListDegrade(c, degradeGroup([]string{"a"}, nil, nil, ""), &stubChannel{}) {
		t.Fatal("chat request must not be degraded")
	}
}

func TestModelListDegradeKeepsErrorWhenCacheEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/proxy/smk/v1/models", nil)

	ps := &ProxyServer{}
	if ps.modelListDegrade(c, degradeGroup(nil, nil, nil, ""), &stubChannel{}) {
		t.Fatal("empty cache must fall through to the original error")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("nothing should be written, got %d", w.Code)
	}
}

func TestModelListDegradeServesCachedList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/proxy/smk/v1/models", nil)

	ps := &ProxyServer{}
	group := degradeGroup([]string{"zeta", "alpha"}, nil, nil, "")
	if !ps.modelListDegrade(c, group, &stubChannel{}) {
		t.Fatal("expected degrade to serve the cached list")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-Autogateway-Models-Source"); got != "cache" {
		t.Fatalf("expected provenance header, got %q", got)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("bad body %s: %v", w.Body.String(), err)
	}
	if len(parsed.Data) != 2 || parsed.Data[0].ID != "alpha" || parsed.Data[1].ID != "zeta" {
		t.Fatalf("expected sorted alpha,zeta, got %#v", parsed.Data)
	}
}

// specified 模式 + blocked 必须和聚合分组同一口径.
func TestModelListDegradeRespectsRoutingMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/proxy/smk/v1/models", nil)

	group := degradeGroup(
		[]string{"keep", "drop", "not-exposed"},
		datatypes.JSON(`["keep","not-exposed"]`),
		datatypes.JSON(`["not-exposed"]`),
		"specified",
	)
	ps := &ProxyServer{}
	if !ps.modelListDegrade(c, group, &stubChannel{}) {
		t.Fatal("expected degrade to serve")
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	if len(parsed.Data) != 1 || parsed.Data[0].ID != "keep" {
		t.Fatalf("expected only keep, got %#v", parsed.Data)
	}
}

func TestMain(m *testing.M) {
	logrus.SetLevel(logrus.PanicLevel)
	m.Run()
}
