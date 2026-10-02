package proxy

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// isAudioStreamContentType reports an upstream streaming response that carries
// raw audio bytes (OpenAI-compatible TTS: /v1/audio/speech with stream=true),
// not SSE frames.
func isAudioStreamContentType(resp *http.Response) bool {
	return strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "audio/")
}

// streamAudioPassthrough forwards a binary audio stream verbatim: upstream
// headers (Content-Type: audio/mpeg etc.) are preserved and bytes are copied
// with flushing. streamWithIntegrity must not be used for these responses —
// it rewrites Content-Type to text/event-stream and hunts for SSE frames that
// binary audio never contains, so players (and strict clients) break.
//
// Header-hold semantics match streamWithIntegrity: nothing is written to the
// client until the first upstream byte arrives, so an empty/stalled audio
// stream can still trigger transparent failover (failed && !wroteToClient).
// idleTimeout > 0 aborts an in-flight stream that goes silent that long.
func streamAudioPassthrough(c *gin.Context, resp *http.Response, idleTimeout time.Duration) streamOutcome {
	// 首字节最长等待: 与 streamWithIntegrity 同口径 (用户配置优先, 否则内置兜底),
	// 否则上游 200 后挂住 body 会永久占住连接。
	const firstByteFloor = 300 * time.Second
	holdTimeout := idleTimeout
	if holdTimeout <= 0 {
		holdTimeout = firstByteFloor
	}
	var hhExpired atomic.Bool
	hhTimer := time.AfterFunc(holdTimeout, func() {
		hhExpired.Store(true)
		resp.Body.Close()
	})

	buf := make([]byte, 32*1024)
	n, err := resp.Body.Read(buf)
	hhTimer.Stop()
	if n == 0 {
		msg := "empty upstream audio stream"
		sc := http.StatusBadGateway
		if err != nil && err != io.EOF {
			msg = err.Error()
			sc = 0
			if hhExpired.Load() {
				msg = "upstream idle before first byte (header-hold timeout)"
				sc = http.StatusGatewayTimeout
			}
		}
		return streamOutcome{failed: true, statusCode: sc, parsedError: msg}
	}

	// 首个字节已到 — 复制上游响应头并放行 (不再具备 failover 资格)。
	for k, vals := range resp.Header {
		for _, v := range vals {
			c.Header(k, v)
		}
	}
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)

	flusher, _ := c.Writer.(http.Flusher)
	if _, werr := c.Writer.Write(buf[:n]); werr != nil {
		logUpstreamError("writing audio stream to client", werr)
		return streamOutcome{wroteToClient: true, failed: true, statusCode: 0, parsedError: werr.Error()}
	}
	if flusher != nil {
		flusher.Flush()
	}

	var (
		idleTimer   *time.Timer
		idleExpired atomic.Bool
	)
	if idleTimeout > 0 {
		idleTimer = time.AfterFunc(idleTimeout, func() {
			idleExpired.Store(true)
			resp.Body.Close()
		})
		defer idleTimer.Stop()
	}

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if idleTimer != nil {
				idleTimer.Reset(idleTimeout)
			}
			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				logUpstreamError("writing audio stream chunk to client", werr)
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF {
				if idleExpired.Load() {
					logrus.Warnf("[stream-audio] inactivity timeout (%v) reached, stream closed", idleTimeout)
				} else {
					logUpstreamError("reading audio stream from upstream", err)
				}
			}
			break
		}
	}

	return streamOutcome{wroteToClient: true}
}
