package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// audioResp 构造一个带 Content-Type: audio/* 的假上游响应.
func audioResp(status int, body string) *http.Response {
	resp := fakeResp(status, body)
	resp.Header.Set("Content-Type", "audio/mpeg")
	return resp
}

func TestStreamAudioPassthrough_PreservesAudioContentType(t *testing.T) {
	c, rec := newStreamCtx()
	payload := "\x00\x01FF\x0Abinary-audio-bytes"
	out := streamAudioPassthrough(c, audioResp(http.StatusOK, payload), 0)

	if out.failed || !out.wroteToClient {
		t.Fatalf("audio stream with bytes should pass through, got %+v", out)
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("audio Content-Type must be preserved, got %q", got)
	}
	if rec.Body.String() != payload {
		t.Fatalf("body should be forwarded verbatim, got %q", rec.Body.String())
	}
}

func TestStreamAudioPassthrough_EmptyStreamFailsForFailover(t *testing.T) {
	c, rec := newStreamCtx()
	out := streamAudioPassthrough(c, audioResp(http.StatusOK, ""), 0)

	if !out.failed {
		t.Fatalf("empty audio stream should fail, got %+v", out)
	}
	if out.wroteToClient {
		t.Fatalf("empty audio stream must not write to client (failover window)")
	}
	if out.statusCode != http.StatusBadGateway {
		t.Fatalf("empty audio stream statusCode should be 502, got %d", out.statusCode)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("recorder body should stay empty, got %q", rec.Body.String())
	}
}

func TestStreamAudioPassthrough_MultipartReadsAllBytes(t *testing.T) {
	// 上游分两块吐字节: 首块触发发头, 后续块继续透传.
	c, rec := newStreamCtx()
	resp := fakeResp(http.StatusOK, "")
	resp.Header.Set("Content-Type", "audio/wav")
	resp.Body = io.NopCloser(io.MultiReader(
		strings.NewReader("part1-"), strings.NewReader("part2"),
	))

	out := streamAudioPassthrough(c, resp, 0)
	if out.failed || !out.wroteToClient {
		t.Fatalf("two-part audio stream should pass through, got %+v", out)
	}
	if got := rec.Body.String(); got != "part1-part2" {
		t.Fatalf("all parts should be forwarded, got %q", got)
	}
}

func TestIsAudioStreamContentType(t *testing.T) {
	for _, tc := range []struct {
		ctype string
		want  bool
	}{
		{"audio/mpeg", true},
		{"AUDIO/MP3; charset=binary", true},
		{"application/octet-stream", false},
		{"text/event-stream", false},
		{"", false},
	} {
		resp := fakeResp(http.StatusOK, "")
		resp.Header.Set("Content-Type", tc.ctype)
		if got := isAudioStreamContentType(resp); got != tc.want {
			t.Fatalf("isAudioStreamContentType(%q) = %v, want %v", tc.ctype, got, tc.want)
		}
	}
}
