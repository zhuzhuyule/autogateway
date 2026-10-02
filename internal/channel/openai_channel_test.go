package channel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newIsStreamCtx(accept string, query string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions"+query, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	c.Request = req
	return c
}

func TestOpenAIIsStreamRequest(t *testing.T) {
	ch := &OpenAIChannel{}
	for _, tc := range []struct {
		name  string
		accept string
		query string
		body  string
		want  bool
	}{
		{"json body stream=true", "", "", `{"model":"gpt","stream":true}`, true},
		{"json body stream=false", "", "", `{"model":"gpt","stream":false}`, false},
		{"accept event-stream", "text/event-stream", "", `{"model":"gpt"}`, true},
		{"query stream=true", "", "?stream=true", `{"model":"gpt"}`, true},
		// TTS: stream_format 表达的流式意图 (可能不带 stream 字段) 必须被认出,
		// 否则 /v1/audio/speech 流式会被当普通请求整读.
		{"tts stream_format only", "", "", `{"model":"tts-1","input":"hi","stream_format":"audio"}`, true},
		{"tts stream+stream_format", "", "", `{"model":"tts-1","input":"hi","stream":true,"stream_format":"sse"}`, true},
		{"tts non-stream", "", "", `{"model":"tts-1","input":"hi","voice":"alloy"}`, false},
		{"multipart body not json", "", "", `--boundary`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newIsStreamCtx(tc.accept, tc.query)
			if got := ch.IsStreamRequest(c, []byte(tc.body)); got != tc.want {
				t.Fatalf("IsStreamRequest = %v, want %v", got, tc.want)
			}
		})
	}
}
