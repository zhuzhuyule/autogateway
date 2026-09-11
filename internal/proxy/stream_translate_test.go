package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autogateway/internal/models"
	"autogateway/internal/store"
)

// 流式转译的端到端测试。与非流式 E2E 的区别在于:这里的断言对象是**客户端
// 收到的 SSE 字节**,而不是转换函数的返回值 —— 只有走完整转发链路才能证明
// header-hold、断流兜底、usage 累积这些环节串起来是对的。

// sseUpstream 起一个会按帧刷新的 SSE 上游。
func sseUpstream(t *testing.T, capture *string, frames []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			*capture = r.URL.Path
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, f := range frames {
			_, _ = w.Write([]byte(f))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}

// openaiChunkFrames 是标准 OpenAI 流式帧序列(含末帧 usage 与 [DONE])。
func openaiChunkFrames() []string {
	return []string{
		`data: {"id":"c1","object":"chat.completion.chunk","model":"llama-3.3-70b",` +
			`"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","model":"llama-3.3-70b",` +
			`"choices":[{"index":0,"delta":{"content":"he"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","model":"llama-3.3-70b",` +
			`"choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","model":"llama-3.3-70b",` +
			`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":30,"completion_tokens":10,"total_tokens":40}}` + "\n\n",
		"data: [DONE]\n\n",
	}
}

// anthropicEventFrames 是标准 Anthropic 流式事件序列。
func anthropicEventFrames() []string {
	return []string{
		`event: message_start` + "\n" +
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant",` +
			`"model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":30,"output_tokens":1}}}` + "\n\n",
		`event: content_block_start` + "\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		`event: content_block_delta` + "\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"he"}}` + "\n\n",
		`event: content_block_delta` + "\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"llo"}}` + "\n\n",
		`event: content_block_stop` + "\n" +
			`data: {"type":"content_block_stop","index":0}` + "\n\n",
		`event: message_delta` + "\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},` +
			`"usage":{"output_tokens":10}}` + "\n\n",
		`event: message_stop` + "\n" +
			`data: {"type":"message_stop"}` + "\n\n",
	}
}

// ---------- Anthropic 客户端 → OpenAI 上游(流式) ----------

// TestE2E_AnthropicStreamInboundToOpenAIUpstream 验证:客户端用 Anthropic 协议
// 发起流式请求,打到只有 OpenAI 节点的分组时,能拿到合法的 Anthropic SSE 事件流。
// 这是本特性最主要的使用场景(Claude Code / Codex 直连 OpenAI 池)。
func TestE2E_AnthropicStreamInboundToOpenAIUpstream(t *testing.T) {
	db := charTestDB(t)
	st := store.NewMemoryStore()

	var gotPath string
	srv := sseUpstream(t, &gotPath, openaiChunkFrames())
	defer srv.Close()

	std := &models.Group{
		Name: "groq-stream", GroupType: "standard", ChannelType: "openai",
		TestModel: "llama-3.3-70b",
		Upstreams: []byte(`[{"url":"` + srv.URL + `","weight":1}]`),
	}
	if err := db.Create(std).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

	ps := buildProxyWithRealClients(t, db, st)
	group, err := ps.groupManager.GetGroupByName("groq-stream")
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	seedKeyIntoStore(t, st, group.ID, 911, "keyS1")
	handler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	inbound := `{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	c, rec := newGinCtxWithPath("/anthropic/v1/messages", inbound)
	ps.executeRequestWithRetry(c, handler, group, group, []byte(inbound), true, time.Now(), 0, map[string]bool{}, "claude-sonnet-4-5")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /v1/chat/completions", gotPath)
	}

	body := rec.Body.String()
	// 必须是 Anthropic 事件形状,不能漏出 Chat chunk
	if strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("Chat chunk leaked to Anthropic client: %s", body)
	}
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in:\n%s", want, body)
		}
	}
	// 文本必须完整拼接,且顺序正确
	if text := collectAnthropicText(body); text != "hello" {
		t.Errorf("aggregated text = %q, want hello", text)
	}
	// stop_reason 必须从 finish_reason=stop 映射过来
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Errorf("stop_reason not mapped: %s", body)
	}
}

// ---------- Chat 客户端 → Anthropic 上游(流式) ----------

// TestE2E_ChatStreamInboundToAnthropicUpstream 是反方向:OpenAI 客户端打到只有
// Anthropic 节点的分组。客户端必须收到 [DONE] 结尾的 Chat chunk 流。
func TestE2E_ChatStreamInboundToAnthropicUpstream(t *testing.T) {
	db := charTestDB(t)
	st := store.NewMemoryStore()

	var gotPath string
	srv := sseUpstream(t, &gotPath, anthropicEventFrames())
	defer srv.Close()

	std := &models.Group{
		Name: "claude-stream", GroupType: "standard", ChannelType: "anthropic",
		TestModel: "claude-sonnet-4-5",
		Upstreams: []byte(`[{"url":"` + srv.URL + `","weight":1}]`),
	}
	if err := db.Create(std).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

	ps := buildProxyWithRealClients(t, db, st)
	group, err := ps.groupManager.GetGroupByName("claude-stream")
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	seedKeyIntoStore(t, st, group.ID, 912, "keyS2")
	handler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	inbound := `{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	c, rec := newGinCtxWithPath("/openai/v1/chat/completions", inbound)
	ps.executeRequestWithRetry(c, handler, group, group, []byte(inbound), true, time.Now(), 0, map[string]bool{}, "claude-sonnet-4-5")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages", gotPath)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("stream not terminated with [DONE]:\n%s", body)
	}
	if strings.Contains(body, "message_stop") {
		t.Errorf("Anthropic event leaked to Chat client: %s", body)
	}
	if text := collectChatDeltaContent(body); text != "hello" {
		t.Errorf("aggregated content = %q, want hello", text)
	}
	// 首个 chunk 必须带 role=assistant,否则 OpenAI SDK 不认
	first := firstChatChunkFrame(t, body)
	if len(first.Choices) == 0 || first.Choices[0].Delta.Role != "assistant" {
		t.Errorf("first chunk missing role=assistant: %s", body)
	}
	// 末帧必须带 finish_reason,否则客户端不会收尾
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("missing finish_reason:\n%s", body)
	}
}

// ---------- 断流兜底 ----------

// TestE2E_TranslatedStream_TruncatedUpstream 验证上游没发终止标记就断开时,
// 客户端仍能收到完整终止事件 —— 严格客户端(Claude Code)不会挂死。
func TestE2E_TranslatedStream_TruncatedUpstream(t *testing.T) {
	db := charTestDB(t)
	st := store.NewMemoryStore()

	// 只有前 3 帧,没有 [DONE],上游 handler 返回后连接直接关闭。
	frames := openaiChunkFrames()[:3]
	srv := sseUpstream(t, nil, frames)
	defer srv.Close()

	std := &models.Group{
		Name: "groq-trunc", GroupType: "standard", ChannelType: "openai",
		TestModel: "llama-3.3-70b",
		Upstreams: []byte(`[{"url":"` + srv.URL + `","weight":1}]`),
	}
	if err := db.Create(std).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

	ps := buildProxyWithRealClients(t, db, st)
	group, err := ps.groupManager.GetGroupByName("groq-trunc")
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	seedKeyIntoStore(t, st, group.ID, 913, "keyS3")
	handler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	inbound := `{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	c, rec := newGinCtxWithPath("/anthropic/v1/messages", inbound)
	ps.executeRequestWithRetry(c, handler, group, group, []byte(inbound), true, time.Now(), 0, map[string]bool{}, "claude-sonnet-4-5")

	body := rec.Body.String()
	// 已发头就不能 failover,但必须补齐终止事件
	if !strings.Contains(body, "event: message_stop") {
		t.Errorf("truncated stream did not synthesize message_stop:\n%s", body)
	}
	if !strings.Contains(body, "event: content_block_stop") {
		t.Errorf("truncated stream did not close open content block:\n%s", body)
	}
}

// TestE2E_TranslatedStream_EmptyUpstream 验证上游 200 但一个有效事件都没有时,
// 不会把半个流写给客户端 —— 应当走无感 failover。
func TestE2E_TranslatedStream_EmptyUpstream(t *testing.T) {
	db := charTestDB(t)
	st := store.NewMemoryStore()

	srv := sseUpstream(t, nil, nil) // 只有响应头,body 空
	defer srv.Close()

	std := &models.Group{
		Name: "groq-empty", GroupType: "standard", ChannelType: "openai",
		TestModel: "llama-3.3-70b",
		Upstreams: []byte(`[{"url":"` + srv.URL + `","weight":1}]`),
	}
	if err := db.Create(std).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

	ps := buildProxyWithRealClients(t, db, st)
	group, err := ps.groupManager.GetGroupByName("groq-empty")
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	seedKeyIntoStore(t, st, group.ID, 914, "keyS4")
	handler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	inbound := `{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	c, rec := newGinCtxWithPath("/anthropic/v1/messages", inbound)
	ps.executeRequestWithRetry(c, handler, group, group, []byte(inbound), true, time.Now(), 0, map[string]bool{}, "claude-sonnet-4-5")

	body := rec.Body.String()
	// 关键:绝不能出现只写了一半的 SSE 流(message_start 而无 message_stop)
	if strings.Contains(body, "message_start") {
		t.Errorf("empty upstream wrote a partial stream to client:\n%s", body)
	}
	if rec.Code == http.StatusOK {
		t.Errorf("empty upstream should not be reported as success: %d %s", rec.Code, body)
	}
}

// ---------- 同协议流式回归护栏 ----------

// TestE2E_SameProtocolStreamUntouched 回归护栏:不触发转换时,流式响应必须
// 逐字节原样透传(接入转换层之前的行为)。
func TestE2E_SameProtocolStreamUntouched(t *testing.T) {
	db := charTestDB(t)
	st := store.NewMemoryStore()

	frames := openaiChunkFrames()
	srv := sseUpstream(t, nil, frames)
	defer srv.Close()

	std := &models.Group{
		Name: "plain-openai-stream", GroupType: "standard", ChannelType: "openai",
		TestModel: "gpt-4o",
		Upstreams: []byte(`[{"url":"` + srv.URL + `","weight":1}]`),
	}
	if err := db.Create(std).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

	ps := buildProxyWithRealClients(t, db, st)
	group, err := ps.groupManager.GetGroupByName("plain-openai-stream")
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	seedKeyIntoStore(t, st, group.ID, 915, "keyS5")
	handler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	inbound := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	c, rec := newGinCtxWithPath("/openai/v1/chat/completions", inbound)
	ps.executeRequestWithRetry(c, handler, group, group, []byte(inbound), true, time.Now(), 0, map[string]bool{}, "gpt-4o")

	want := strings.Join(frames, "")
	if got := rec.Body.String(); got != want {
		t.Errorf("same-protocol stream was altered:\n got: %q\nwant: %q", got, want)
	}
}

// ---------- 解析辅助 ----------

// collectAnthropicText 从客户端收到的 Anthropic SSE 里拼出文本内容。
func collectAnthropicText(body string) string {
	var text string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		if ev.Type == "content_block_delta" && ev.Delta.Type == "text_delta" {
			text += ev.Delta.Text
		}
	}
	return text
}

// collectChatDeltaContent 从客户端收到的 Chat SSE 里拼出 delta.content。
func collectChatDeltaContent(body string) string {
	var content string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &ch); err != nil {
			continue
		}
		for _, c := range ch.Choices {
			content += c.Delta.Content
		}
	}
	return content
}

// firstChatChunkFrame 取出客户端收到的第一个 Chat chunk 帧。
func firstChatChunkFrame(t *testing.T, body string) struct {
	Choices []struct {
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
} {
	t.Helper()
	var out struct {
		Choices []struct {
			Delta struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		if err := json.Unmarshal([]byte(payload), &out); err != nil {
			t.Fatalf("bad chunk frame %q: %v", payload, err)
		}
		return out
	}
	t.Fatalf("no chat chunk frames in body:\n%s", body)
	return out
}
