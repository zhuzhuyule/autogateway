package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"autogateway/internal/apicompat"
	"autogateway/internal/usage"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// streamTranslated 在流式转发的同时做协议转换。
//
// 与 streamWithIntegrity 的关系:两者共用"header-hold + 首帧检视 + usage
// 累积 + 截断检测"这套契约,区别只在于写给客户端的字节是上游原样还是
// 转换后的事件。所以这里的 header-hold 也 hold 到**转换后的首个事件**,
// 而不是上游的原始帧 —— 转换可能把前几帧吞掉(如等待 tool 参数聚合)。
//
// 断流兜底:无论上游是否正常结束,都会调 Finalize() 合成终止事件。少了
// 这一步,严格客户端(Claude Code)会一直挂着等 message_stop。
func streamTranslated(c *gin.Context, resp *http.Response, tr translation, idleTimeout time.Duration) streamOutcome {
	out := streamOutcome{}

	reader := bufio.NewReader(resp.Body)
	var buffered bytes.Buffer // header-hold 期间缓冲待发的转换结果

	// 首字节兜底超时,语义与 streamWithIntegrity 一致:上游返回 200 头但
	// body 迟迟不来时不能永久挂住。
	const firstByteFloor = 300 * time.Second
	headerHoldTimeout := idleTimeout
	if headerHoldTimeout <= 0 {
		headerHoldTimeout = firstByteFloor
	}
	hhTimer := time.AfterFunc(headerHoldTimeout, func() {
		resp.Body.Close()
	})
	defer hhTimer.Stop()

	writeHeaders := func() {
		if out.wroteToClient {
			return
		}
		out.wroteToClient = true
		hhTimer.Stop() // 已进入透传,别再误关 body
		for key, values := range resp.Header {
			// 长度与编码随内容变化,交给 gin 重新计算
			if key == "Content-Length" || key == "Content-Encoding" {
				continue
			}
			for _, value := range values {
				c.Header(key, value)
			}
		}
		// 与 streamWithIntegrity 的 flushAndStream 保持一致: 这几个头不能指望
		// 上游给对。尤其 X-Accel-Buffering —— 少了它, 前置 nginx 会把整条 SSE
		// 缓冲到流结束才吐给客户端, 流式就废了。
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(resp.StatusCode)
		if _, err := c.Writer.Write(buffered.Bytes()); err != nil {
			logrus.WithError(err).Debug("write buffered translated stream head")
		}
		buffered.Reset()
	}

	emit := func(frame []byte) {
		if len(frame) == 0 {
			return
		}
		if out.wroteToClient {
			if _, err := c.Writer.Write(frame); err != nil {
				logrus.WithError(err).Debug("write translated stream frame")
			}
			c.Writer.Flush()
			return
		}
		buffered.Write(frame)
		// 首个有效事件已就绪 —— 放行。
		writeHeaders()
	}

	var (
		chatToAnthropic *apicompat.ChatToAnthropicStream
		anthropicToChat *apicompat.AnthropicToChatStream
	)
	switch {
	case tr.from == apicompat.FormatAnthropic && tr.to == apicompat.FormatChatCompletions:
		chatToAnthropic = apicompat.NewChatToAnthropicStream()
	case tr.from == apicompat.FormatChatCompletions && tr.to == apicompat.FormatAnthropic:
		anthropicToChat = apicompat.NewAnthropicToChatStream()
	default:
		// 不该发生(planTranslation 已过滤),兜底透传。
		return streamWithIntegrity(c, resp, false, idleTimeout)
	}

	sawTerminator := false

	handlePayload := func(payload []byte) {
		// usage 累积用上游原始帧 —— 与转译前口径一致(上游实际消耗)。
		if u, ok := usage.Extract(payload); ok {
			out.usage = out.usage.Merge(u)
		}

		switch {
		case chatToAnthropic != nil:
			if isChatDone(payload) {
				sawTerminator = true
				emit(encodeAnthropicEvents(chatToAnthropic.Finalize()))
				return
			}
			var chunk apicompat.ChatStreamChunk
			if err := json.Unmarshal(payload, &chunk); err != nil {
				return // 解析不了的帧跳过,不中断整条流
			}
			emit(encodeAnthropicEvents(chatToAnthropic.Process(&chunk)))

		case anthropicToChat != nil:
			var ev apicompat.AnthropicStreamEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				return
			}
			chunks, done := anthropicToChat.Process(&ev)
			emit(encodeChatChunks(chunks))
			if done {
				sawTerminator = true
				emit([]byte("data: [DONE]\n\n"))
			}
		}
	}

	for {
		line, readErr := reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed != "" {
			if payload, ok := ssePayload(trimmed); ok {
				handlePayload(payload)
			}
		}
		if readErr != nil {
			if readErr != io.EOF && out.parsedError == "" {
				out.parsedError = readErr.Error()
			}
			break
		}
	}

	// 断流兜底:上游没发终止事件就结束,由 Finalize 合成。
	if !sawTerminator {
		out.truncated = true
		switch {
		case chatToAnthropic != nil:
			emit(encodeAnthropicEvents(chatToAnthropic.Finalize()))
		case anthropicToChat != nil:
			chunks, _ := anthropicToChat.Finalize()
			emit(encodeChatChunks(chunks))
			emit([]byte("data: [DONE]\n\n"))
		}
	}

	if !out.wroteToClient {
		// 一个有效事件都没产出 —— 视为失败,让调用方走无感 failover。
		out.failed = true
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			out.statusCode = http.StatusBadGateway
		} else {
			out.statusCode = resp.StatusCode
		}
		if out.parsedError == "" {
			out.parsedError = "translated upstream stream produced no events"
		}
		return out
	}

	c.Writer.Flush()
	return out
}

// ssePayload 从一行 SSE 里取出 data 负载。返回 false 表示这行不是 data。
func ssePayload(line string) ([]byte, bool) {
	if !strings.HasPrefix(line, "data:") {
		return nil, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" {
		return nil, false
	}
	return []byte(payload), true
}

// isChatDone 判断是否为 OpenAI 流的结束标记。
func isChatDone(payload []byte) bool {
	return bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]"))
}

// encodeAnthropicEvents 把 Anthropic 事件序列化成 SSE(带 event: 行)。
func encodeAnthropicEvents(events []apicompat.AnthropicStreamEvent) []byte {
	if len(events) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		buf.WriteString("event: ")
		buf.WriteString(ev.Type)
		buf.WriteString("\ndata: ")
		buf.Write(raw)
		buf.WriteString("\n\n")
	}
	return buf.Bytes()
}

// encodeChatChunks 把 Chat chunk 序列化成 SSE。
func encodeChatChunks(chunks []apicompat.ChatStreamChunk) []byte {
	if len(chunks) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, ch := range chunks {
		raw, err := json.Marshal(ch)
		if err != nil {
			continue
		}
		buf.WriteString("data: ")
		buf.Write(raw)
		buf.WriteString("\n\n")
	}
	return buf.Bytes()
}
