package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true)

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
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, ""), true)

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
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), true)

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
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, body), false)

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
	out := streamWithIntegrity(c, fakeResp(http.StatusOK, ""), false)

	if !out.failed {
		t.Fatalf("non-openai empty stream should fail, got %+v", out)
	}
	if out.wroteToClient {
		t.Fatalf("non-openai empty stream must not write to client, got %+v", out)
	}
}

func TestParseSSEError(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantErr   bool
		wantCode  int
		wantMsg   string
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
