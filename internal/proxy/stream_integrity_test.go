package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// newStreamCtx 构造一个最小 gin.Context + ResponseRecorder, 供 streamWithIntegrity 单测.
func newStreamCtx() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c, rec
}

// fakeResp 构造一个带指定 body 字符串与状态码的 *http.Response.
func fakeResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestStreamWithIntegrity_ValidOpenAIFirstChunk(t *testing.T) {
	c, rec := newStreamCtx()
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: [DONE]\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true, 0)

	if out.failed {
		t.Fatalf("valid first chunk should not fail, got %+v", out)
	}
	if !out.wroteToClient {
		t.Fatalf("valid first chunk should write to client, got %+v", out)
	}
	if !strings.Contains(rec.Body.String(), "\"choices\"") {
		t.Fatalf("recorder body should contain forwarded data, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("recorder body should contain rest of stream, got %q", rec.Body.String())
	}
}

func TestStreamWithIntegrity_EmptyStream(t *testing.T) {
	c, rec := newStreamCtx()
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, ""), true, 0)

	if !out.failed {
		t.Fatalf("empty stream should fail, got %+v", out)
	}
	if out.wroteToClient {
		t.Fatalf("empty stream must not write to client, got %+v", out)
	}
	if out.statusCode != http.StatusBadGateway {
		t.Fatalf("empty stream statusCode should be 502, got %d", out.statusCode)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("recorder body should be empty, got %q", rec.Body.String())
	}
}

func TestStreamWithIntegrity_ErrorFrame(t *testing.T) {
	c, rec := newStreamCtx()
	body := "data: {\"error\":{\"message\":\"rate limited\",\"code\":429}}\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true, 0)

	if !out.failed {
		t.Fatalf("error frame should fail, got %+v", out)
	}
	if out.wroteToClient {
		t.Fatalf("error frame must not write to client, got %+v", out)
	}
	if out.statusCode != 429 {
		t.Fatalf("error frame statusCode should be 429, got %d", out.statusCode)
	}
	if out.parsedError != "rate limited" {
		t.Fatalf("error frame parsedError should be 'rate limited', got %q", out.parsedError)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("recorder body should be empty on error-frame failover, got %q", rec.Body.String())
	}
}

func TestStreamWithIntegrity_NonOpenAIPassthrough(t *testing.T) {
	c, rec := newStreamCtx()
	// 即便 payload 长得像 error 帧, 非 OpenAI 也不解析 → 透传.
	body := "data: {\"error\":{\"message\":\"rate limited\",\"code\":429}}\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), false, 0)

	if out.failed {
		t.Fatalf("non-openai with data should not fail, got %+v", out)
	}
	if !out.wroteToClient {
		t.Fatalf("non-openai with data should write to client, got %+v", out)
	}
	if !strings.Contains(rec.Body.String(), "rate limited") {
		t.Fatalf("non-openai should pass through data verbatim, got %q", rec.Body.String())
	}
}

func TestStreamWithIntegrity_NonOpenAIEmptyStream(t *testing.T) {
	c, _ := newStreamCtx()
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, ""), false, 0)

	if !out.failed {
		t.Fatalf("non-openai empty stream should fail, got %+v", out)
	}
	if out.wroteToClient {
		t.Fatalf("non-openai empty stream must not write to client, got %+v", out)
	}
}

func TestParseSSEError(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantErr  bool
		wantCode int
		wantMsg  string
	}{
		{"error with numeric code", `{"error":{"message":"rate limited","code":429}}`, true, 429, "rate limited"},
		{"error without code", `{"error":{"message":"boom"}}`, true, http.StatusBadGateway, "boom"},
		{"error code out of range", `{"error":{"message":"x","code":42}}`, true, http.StatusBadGateway, "x"},
		{"normal chunk", `{"choices":[{"delta":{"content":"hi"}}]}`, false, 0, ""},
		{"not json", `[DONE]`, false, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg, isErr := parseSSEError([]byte(tt.payload))
			if isErr != tt.wantErr {
				t.Fatalf("isErr = %v, want %v", isErr, tt.wantErr)
			}
			if isErr {
				if code != tt.wantCode {
					t.Errorf("code = %d, want %d", code, tt.wantCode)
				}
				if msg != tt.wantMsg {
					t.Errorf("msg = %q, want %q", msg, tt.wantMsg)
				}
			}
		})
	}
}

// --- Task 3 新增测试 ---

// blockingReader 是一个自定义 io.ReadCloser:
// 先返回 firstData, 然后阻塞直到 Close() 被调用或 blockFor 超时.
// 用于测试 inactivity 超时场景 (time.AfterFunc 关闭 body 后 Read 应立即返回错误).
type blockingReader struct {
	mu        sync.Mutex
	first     []byte
	firstSent bool
	rest      []byte
	blockFor  time.Duration
	closeCh   chan struct{}
	closeOnce sync.Once
}

func newBlockingReader(first, rest string, blockFor time.Duration) *blockingReader {
	return &blockingReader{
		first:    []byte(first),
		rest:     []byte(rest),
		blockFor: blockFor,
		closeCh:  make(chan struct{}),
	}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	select {
	case <-r.closeCh:
		r.mu.Unlock()
		return 0, io.ErrClosedPipe
	default:
	}
	if !r.firstSent && len(r.first) > 0 {
		n := copy(p, r.first)
		r.first = r.first[n:]
		if len(r.first) == 0 {
			r.firstSent = true
		}
		r.mu.Unlock()
		return n, nil
	}
	r.mu.Unlock()

	// 阻塞模拟 inactivity — 等待 Close() 信号或 blockFor 超时 (后者兜底).
	timer := time.NewTimer(r.blockFor)
	defer timer.Stop()
	select {
	case <-r.closeCh:
		return 0, io.ErrClosedPipe
	case <-timer.C:
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.rest) > 0 {
		n := copy(p, r.rest)
		r.rest = r.rest[n:]
		return n, nil
	}
	return 0, io.EOF
}

func (r *blockingReader) Close() error {
	r.closeOnce.Do(func() {
		close(r.closeCh)
	})
	return nil
}

// TestStreamWithIntegrity_InactivityTimeout 验证:
// 有效首帧已写客户端后, 后续 Read 阻塞超过 idleTimeout → 流终止,
// wroteToClient=true, 不 panic.
func TestStreamWithIntegrity_InactivityTimeout(t *testing.T) {
	c, rec := newStreamCtx()

	// 首帧: 有效 SSE data 行 (已换行)
	firstData := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	// blockFor 大于 idleTimeout, 模拟挂死.
	br := newBlockingReader(firstData, "", 5*time.Second)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       br,
	}

	idleTimeout := 100 * time.Millisecond
	start := time.Now()
	out := streamWithIntegrity(c, resp, true, idleTimeout)
	elapsed := time.Since(start)

	// 已写客户端 (首帧已发)
	if !out.wroteToClient {
		t.Fatalf("expected wroteToClient=true after valid first frame, got %+v", out)
	}
	// 不应标记 failed (已写客户端场景 failed 无意义)
	// 但不 panic 才是关键断言 — 能到这里即通过.

	// 超时应在约 idleTimeout 内触发 (宽松：< 3s)
	if elapsed > 3*time.Second {
		t.Fatalf("inactivity timeout took too long: %v (expected ~%v)", elapsed, idleTimeout)
	}

	// 首帧数据已透传给客户端
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("expected forwarded first frame in recorder, got %q", rec.Body.String())
	}
}

// TestStreamWithIntegrity_TruncatedOpenAI 验证:
// 有有效首帧但无 [DONE] 也无 finish_reason → outcome.truncated=true.
func TestStreamWithIntegrity_TruncatedOpenAI(t *testing.T) {
	c, _ := newStreamCtx()
	// 有 data 帧, 但没有 [DONE] 也没有 finish_reason.
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true, 0)

	if !out.wroteToClient {
		t.Fatalf("expected wroteToClient=true, got %+v", out)
	}
	if !out.truncated {
		t.Fatalf("expected truncated=true (no [DONE]/finish_reason), got %+v", out)
	}
}

// TestStreamWithIntegrity_NotTruncatedWithDONE 验证:
// 有 [DONE] → truncated=false.
func TestStreamWithIntegrity_NotTruncatedWithDONE(t *testing.T) {
	c, _ := newStreamCtx()
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: [DONE]\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true, 0)

	if !out.wroteToClient {
		t.Fatalf("expected wroteToClient=true, got %+v", out)
	}
	if out.truncated {
		t.Fatalf("expected truncated=false (has [DONE]), got %+v", out)
	}
}

// TestStreamWithIntegrity_NotTruncatedWithFinishReason 验证:
// 有 finish_reason (非 null) → truncated=false.
func TestStreamWithIntegrity_NotTruncatedWithFinishReason(t *testing.T) {
	c, _ := newStreamCtx()
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true, 0)

	if !out.wroteToClient {
		t.Fatalf("expected wroteToClient=true, got %+v", out)
	}
	if out.truncated {
		t.Fatalf("expected truncated=false (has finish_reason), got %+v", out)
	}
}

// --- Task 13 新增测试：tool_calls finish_reason 补全 ---

// TestStreamWithIntegrity_ToolCallsMissingFinishReason 验证:
// OpenAI 流含 tool_calls delta 但结束无 finish_reason、无 [DONE]
// → 客户端收到的字节里补上了 "finish_reason":"tool_calls" chunk.
func TestStreamWithIntegrity_ToolCallsMissingFinishReason(t *testing.T) {
	c, rec := newStreamCtx()
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_abc\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"Beijing\\\"}\"}}]}}]}\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true, 0)

	if !out.wroteToClient {
		t.Fatalf("expected wroteToClient=true, got %+v", out)
	}
	if out.truncated {
		t.Fatalf("tool_calls补全后不应标记 truncated，got %+v", out)
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected injected finish_reason:tool_calls chunk in body, got:\n%s", bodyStr)
	}
}

// TestStreamWithIntegrity_ToolCallsAlreadyHasFinishReason 验证:
// OpenAI 流有 tool_calls 且已有 finish_reason → 不重复补.
// body 里 finish_reason 的出现次数与原流一致 (=1), 不额外新增.
func TestStreamWithIntegrity_ToolCallsAlreadyHasFinishReason(t *testing.T) {
	c, rec := newStreamCtx()
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_xyz\",\"type\":\"function\",\"function\":{\"name\":\"foo\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true, 0)

	if !out.wroteToClient {
		t.Fatalf("expected wroteToClient=true, got %+v", out)
	}
	if out.truncated {
		t.Fatalf("stream with finish_reason should not be truncated, got %+v", out)
	}
	bodyStr := rec.Body.String()
	// 原流已有 finish_reason:tool_calls，不应再追加补全 chunk（count == 1）.
	count := strings.Count(bodyStr, `"finish_reason":"tool_calls"`)
	if count != 1 {
		t.Fatalf("expected exactly 1 occurrence of finish_reason:tool_calls, got %d in:\n%s", count, bodyStr)
	}
}

// TestStreamWithIntegrity_PlainTextNoFinishReason 验证:
// 纯正文流（无 tool_calls）→ 不补 finish_reason.
func TestStreamWithIntegrity_PlainTextNoFinishReason(t *testing.T) {
	c, rec := newStreamCtx()
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true, 0)

	if !out.wroteToClient {
		t.Fatalf("expected wroteToClient=true, got %+v", out)
	}
	// 没有 tool_calls → 不补 finish_reason → 仍 truncated（无 [DONE]/finish_reason）.
	if !out.truncated {
		t.Fatalf("plain text without finish_reason should still be truncated, got %+v", out)
	}
	bodyStr := rec.Body.String()
	if strings.Contains(bodyStr, `"finish_reason"`) {
		t.Fatalf("plain text stream should NOT have injected finish_reason, got:\n%s", bodyStr)
	}
}

// TestStreamWithIntegrity_NonOpenAINoFinishReason 验证:
// 非 OpenAI 流含 tool_calls 字样 → 不补 finish_reason.
func TestStreamWithIntegrity_NonOpenAINoFinishReason(t *testing.T) {
	c, rec := newStreamCtx()
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"x\"}]}}]}\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), false, 0)

	if !out.wroteToClient {
		t.Fatalf("expected wroteToClient=true, got %+v", out)
	}
	if out.truncated {
		t.Fatalf("non-openai should not be truncated, got %+v", out)
	}
	bodyStr := rec.Body.String()
	if strings.Contains(bodyStr, `"finish_reason"`) {
		t.Fatalf("non-openai should NOT have injected finish_reason, got:\n%s", bodyStr)
	}
}

// TestStreamWithIntegrity_NonOpenAINotTruncated 验证:
// 非 OpenAI 流即便没有 [DONE] 也不标记 truncated.
func TestStreamWithIntegrity_NonOpenAINotTruncated(t *testing.T) {
	c, _ := newStreamCtx()
	body := "data: some custom payload\n\n"
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), false, 0)

	if !out.wroteToClient {
		t.Fatalf("expected wroteToClient=true, got %+v", out)
	}
	if out.truncated {
		t.Fatalf("expected truncated=false for non-openai, got %+v", out)
	}
}

// TestStreamWithIntegrity_UpstreamHeadersForwarded 验证 I1:
// streamWithIntegrity 在写 SSE 头之前先把上游 resp.Header 转发给客户端.
// 自定义头 (如 X-Request-Id) 应出现在 recorder header 中;
// SSE 专用头 (Content-Type=text/event-stream) 应覆盖上游同名头.
func TestStreamWithIntegrity_UpstreamHeadersForwarded(t *testing.T) {
	c, rec := newStreamCtx()
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: [DONE]\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	resp.Header.Set("X-Request-Id", "abc-123")
	resp.Header.Set("Openai-Processing-Ms", "42")
	// 设置一个上游 Content-Type，应被 SSE 专用头覆盖.
	resp.Header.Set("Content-Type", "application/json")

	out := streamWithIntegrity(c, resp, true, 0)
	if out.failed {
		t.Fatalf("valid stream should not fail, got %+v", out)
	}
	if !out.wroteToClient {
		t.Fatalf("valid stream should write to client, got %+v", out)
	}

	// 上游自定义头应被透传.
	if got := rec.Header().Get("X-Request-Id"); got != "abc-123" {
		t.Errorf("X-Request-Id: got %q, want %q", got, "abc-123")
	}
	if got := rec.Header().Get("Openai-Processing-Ms"); got != "42" {
		t.Errorf("Openai-Processing-Ms: got %q, want %q", got, "42")
	}
	// SSE 头应覆盖上游 Content-Type.
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want %q (SSE header should override upstream)", got, "text/event-stream")
	}
}

// TestStreamWithIntegrity_ZeroIdleTimeoutNoTimeout 验证:
// idleTimeout=0 不启用超时 → 正常透传到 EOF, 不提前终止.
func TestStreamWithIntegrity_ZeroIdleTimeoutNoTimeout(t *testing.T) {
	c, rec := newStreamCtx()
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: [DONE]\n\n"
	// idleTimeout=0 should not trigger any timeout
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true, 0)

	if out.failed {
		t.Fatalf("zero idleTimeout should not fail, got %+v", out)
	}
	if !out.wroteToClient {
		t.Fatalf("zero idleTimeout should complete normally, got %+v", out)
	}
	if out.truncated {
		t.Fatalf("stream with [DONE] should not be truncated, got %+v", out)
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("expected [DONE] in recorder body, got %q", rec.Body.String())
	}
}
