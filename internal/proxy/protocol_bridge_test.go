package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autogateway/internal/apicompat"
	"autogateway/internal/channel"
	"autogateway/internal/config"
	"autogateway/internal/encryption"
	"autogateway/internal/httpclient"
	"autogateway/internal/keypool"
	"autogateway/internal/models"
	"autogateway/internal/services"
	"autogateway/internal/store"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------- 转换规划 ----------

func TestPlanTranslation(t *testing.T) {
	cases := []struct {
		name        string
		path        string
		channelType string
		wantNeeded  bool
		wantFrom    apicompat.Format
		wantTo      apicompat.Format
	}{
		{
			name: "Anthropic 客户端打到 openai 节点 —— 需要转换",
			path: "/anthropic/v1/messages", channelType: "openai",
			wantNeeded: true, wantFrom: apicompat.FormatAnthropic, wantTo: apicompat.FormatChatCompletions,
		},
		{
			name: "Chat 客户端打到 anthropic 节点 —— 需要转换",
			path: "/openai/v1/chat/completions", channelType: "anthropic",
			wantNeeded: true, wantFrom: apicompat.FormatChatCompletions, wantTo: apicompat.FormatAnthropic,
		},
		{
			name: "同协议 —— 不转换(主流量零开销)",
			path: "/openai/v1/chat/completions", channelType: "openai",
			wantNeeded: false,
		},
		{
			name: "同协议 —— Anthropic 走原生节点",
			path: "/anthropic/v1/messages", channelType: "anthropic",
			wantNeeded: false,
		},
		{
			name: "未实现转换的协议对 —— 不转换",
			path: "/anthropic/v1/messages", channelType: "gemini",
			wantNeeded: false,
		},
		{
			name: "无法识别的入站路径 —— 不转换",
			path: "/openai/v1/embeddings", channelType: "anthropic",
			wantNeeded: false,
		},
		{
			// 自定义命名分组走 /proxy/{group}/... 前缀,判定逻辑必须与系统快捷
			// 路由一致 —— 否则"用系统路径能转、用自定义路径就不转"会非常难查。
			name: "自定义命名分组 /proxy/{group}/v1/messages —— 同样识别为 Anthropic",
			path: "/proxy/my-agg/v1/messages", channelType: "openai",
			wantNeeded: true, wantFrom: apicompat.FormatAnthropic, wantTo: apicompat.FormatChatCompletions,
		},
		{
			name: "自定义命名分组 /proxy/{group}/v1/chat/completions —— 打到 anthropic 节点",
			path: "/proxy/my-agg/v1/chat/completions", channelType: "anthropic",
			wantNeeded: true, wantFrom: apicompat.FormatChatCompletions, wantTo: apicompat.FormatAnthropic,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planTranslation(c.path, c.channelType)
			if got.needed != c.wantNeeded {
				t.Fatalf("needed = %v, want %v", got.needed, c.wantNeeded)
			}
			if !c.wantNeeded {
				return
			}
			if got.from != c.wantFrom || got.to != c.wantTo {
				t.Fatalf("from/to = %q/%q, want %q/%q", got.from, got.to, c.wantFrom, c.wantTo)
			}
		})
	}
}

func TestTranslationUpstreamPath(t *testing.T) {
	toChat := planTranslation("/anthropic/v1/messages", "openai")
	if got := toChat.upstreamPath("/anthropic/v1/messages"); got != "/v1/chat/completions" {
		t.Errorf("upstreamPath = %q", got)
	}
	toAnthropic := planTranslation("/openai/v1/chat/completions", "anthropic")
	if got := toAnthropic.upstreamPath("/openai/v1/chat/completions"); got != "/v1/messages" {
		t.Errorf("upstreamPath = %q", got)
	}
	// 不转换时路径必须原样返回
	if got := (translation{}).upstreamPath("/openai/v1/models"); got != "/openai/v1/models" {
		t.Errorf("no-op upstreamPath = %q", got)
	}
}

func TestConvertRequest_FailureKeepsOriginal(t *testing.T) {
	// 转换层不该成为新的失败源:解析不了就原样转发。
	tr := planTranslation("/anthropic/v1/messages", "openai")
	original := []byte(`not json at all`)
	if got := tr.convertRequest(original); string(got) != string(original) {
		t.Errorf("body changed on failure: %q", got)
	}
	if got := tr.convertRequest(nil); got != nil {
		t.Errorf("nil body = %v", got)
	}
}

// ---------- 响应转换(非流式) ----------

func TestHandleNormalResponse_TranslatesToAnthropic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := `{"id":"c1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"hello there"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`

	tr := planTranslation("/anthropic/v1/messages", "openai")
	ps := &ProxyServer{}
	ps.handleNormalResponse(c, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}, "gpt-4o", tr)

	body := rec.Body.String()
	for _, want := range []string{`"type":"message"`, `"role":"assistant"`, `"stop_reason":"end_turn"`, `"hello there"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %s: %s", want, body)
		}
	}
	// 必须是 Anthropic 形状,不能残留 OpenAI 字段
	if strings.Contains(body, `"choices"`) {
		t.Errorf("OpenAI shape leaked: %s", body)
	}
	// usage 投影:包容桶 → 互斥桶
	if !strings.Contains(body, `"input_tokens":100`) || !strings.Contains(body, `"output_tokens":20`) {
		t.Errorf("usage not projected: %s", body)
	}
}

func TestHandleNormalResponse_NoTranslationWhenSameProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`
	ps := &ProxyServer{}
	ps.handleNormalResponse(c, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}, "gpt-4o", translation{})

	if rec.Body.String() != upstream {
		t.Errorf("body changed without translation:\n got: %s\nwant: %s", rec.Body.String(), upstream)
	}
}

// ---------- 端到端:Anthropic 请求 → OpenAI 上游 ----------

// TestE2E_AnthropicInboundToOpenAIUpstream 走完整转发链路(真实 channel +
// 真实 HTTP 往返),验证三件事:
//  1. 上游收到的是 /v1/chat/completions,不是客户端的 /v1/messages
//  2. 上游收到的是 Chat 形状的 body(system 已并入 messages)
//  3. 客户端拿回的是 Anthropic 形状的响应
func TestE2E_AnthropicInboundToOpenAIUpstream(t *testing.T) {
	db := charTestDB(t)
	st := store.NewMemoryStore()

	var gotPath string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"llama-3.3-70b",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"from groq"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":30,"completion_tokens":10,"total_tokens":40}}`))
	}))
	defer srv.Close()

	std := &models.Group{
		Name: "groq-pool", GroupType: "standard", ChannelType: "openai",
		TestModel: "llama-3.3-70b",
		Upstreams: []byte(`[{"url":"` + srv.URL + `","weight":1}]`),
	}
	if err := db.Create(std).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

	ps := buildProxyWithRealClients(t, db, st)
	group, err := ps.groupManager.GetGroupByName("groq-pool")
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	seedKeyIntoStore(t, st, group.ID, 901, "keyT")

	handler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	inbound := `{"model":"claude-sonnet-4-5","system":"be brief","max_tokens":64,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	c, rec := newGinCtxWithPath("/anthropic/v1/messages", inbound)
	ps.executeRequestWithRetry(c, handler, group, group, []byte(inbound), false, time.Now(), 0, map[string]bool{}, "claude-sonnet-4-5")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// 1) 路径被改写成目标协议的端点
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /v1/chat/completions", gotPath)
	}
	// 2) 请求体被转成 Chat 形状:system 并入 messages,且补了 max_tokens
	if !strings.Contains(gotBody, `"role":"system"`) || !strings.Contains(gotBody, `"be brief"`) {
		t.Errorf("system not merged into messages: %s", gotBody)
	}
	if strings.Contains(gotBody, `"system":`) {
		t.Errorf("top-level system leaked into chat body: %s", gotBody)
	}
	// 3) 客户端拿回 Anthropic 形状
	out := rec.Body.String()
	for _, want := range []string{`"type":"message"`, `"stop_reason":"end_turn"`, `"from groq"`} {
		if !strings.Contains(out, want) {
			t.Errorf("client response missing %s: %s", want, out)
		}
	}
	if strings.Contains(out, `"choices"`) {
		t.Errorf("OpenAI shape leaked to client: %s", out)
	}
}

// TestE2E_SameProtocolUntouched 回归护栏:不触发转换时,请求体与路径必须与
// 接入转换层之前逐字节一致。
func TestE2E_SameProtocolUntouched(t *testing.T) {
	db := charTestDB(t)
	st := store.NewMemoryStore()

	var gotPath string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	std := &models.Group{
		Name: "plain-openai", GroupType: "standard", ChannelType: "openai",
		TestModel: "gpt-4o",
		Upstreams: []byte(`[{"url":"` + srv.URL + `","weight":1}]`),
	}
	if err := db.Create(std).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

	ps := buildProxyWithRealClients(t, db, st)
	group, err := ps.groupManager.GetGroupByName("plain-openai")
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	seedKeyIntoStore(t, st, group.ID, 902, "keyU")
	handler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	inbound := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	c, _ := newGinCtxWithPath("/openai/v1/chat/completions", inbound)
	ps.executeRequestWithRetry(c, handler, group, group, []byte(inbound), false, time.Now(), 0, map[string]bool{}, "gpt-4o")

	if gotBody != inbound {
		t.Errorf("body was modified without translation:\n got: %s\nwant: %s", gotBody, inbound)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
}

// ---------- 测试脚手架 ----------

// buildProxyWithRealClients 与 buildProxy 类似,但给 channel.Factory 配了真实的
// HTTPClientManager —— 本测试要用生产环境的 openai channel(它会走
// newBaseChannel),而不是 charTestChannel。
func buildProxyWithRealClients(t *testing.T, db *gorm.DB, st store.Store) *ProxyServer {
	t.Helper()

	encSvc, err := encryption.NewService("")
	if err != nil {
		t.Fatalf("encryption: %v", err)
	}
	settingsManager := config.NewSystemSettingsManager()
	subGroupManager := services.NewSubGroupManager(st)
	groupManager := services.NewGroupManager(db, st, settingsManager, subGroupManager)
	if err := groupManager.Initialize(); err != nil {
		t.Fatalf("group manager init: %v", err)
	}
	keyProvider := keypool.NewProvider(db, st, settingsManager, encSvc, nil)

	ps, err := NewProxyServer(
		keyProvider, groupManager, subGroupManager, settingsManager,
		channel.NewFactory(settingsManager, httpclient.NewHTTPClientManager()),
		nil, nil, nil, nil, encSvc,
	)
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	return ps
}

func newGinCtxWithPath(path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return c, rec
}
