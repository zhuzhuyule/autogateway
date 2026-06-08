package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// firstChunkBufCap 是 header-hold 阶段缓冲上游字节的上限. 在见到首个有效
// data 帧之前最多缓冲这么多, 超过即视为"已确有数据"放行透传, 避免上游
// 一直发无效行 (注释/空行) 把网关内存撑爆.
const firstChunkBufCap = 64 * 1024

// streamOutcome 描述一次流式转发的结果. failed && !wroteToClient 时调用方
// (server.go 成功块) 可安全 failover (因为还没向客户端发头/发数据);
// wroteToClient=true 一旦置位, 即便后续出错也不能再 failover.
type streamOutcome struct {
	wroteToClient bool   // 是否已经向客户端发头并写过数据
	failed        bool   // 本次转发是否失败 (空流 / error 帧 / 读错误)
	statusCode    int    // failover 用的状态码 (0 = 网络/读错误)
	parsedError   string // failover 用的错误信息
	truncated     bool   // 流已写给客户端但未正常终止 (无 [DONE]/finish_reason)
}

// streamWithIntegrity 是 ProxyServer 上的方法封装, 供 server.go 成功块调用.
// 实际逻辑在包级 streamWithIntegrity 自由函数里 (无需 ps 状态, 便于单测).
func (ps *ProxyServer) streamWithIntegrity(c *gin.Context, resp *http.Response, isOpenAI bool, idleTimeout time.Duration) streamOutcome {
	return streamWithIntegrity(c, resp, isOpenAI, idleTimeout)
}

// streamWithIntegrity 实现 #10 header-hold + #11 空/error 帧检测 + #12 inactivity 超时 + 截断检测:
// 推迟发头, 缓冲到首个有效 data 帧才发头透传. 在发头前命中空流或 (OpenAI)
// error 帧 → 返回 failed 且 wroteToClient=false, 让调用方走无感 failover.
//
// idleTimeout > 0 时, 流中若超过 idleTimeout 无数据到达则关闭 body 并终止透传.
// isOpenAI 时, 透传结束若未见到 [DONE] 或 finish_reason 则标记 truncated=true.
func streamWithIntegrity(c *gin.Context, resp *http.Response, isOpenAI bool, idleTimeout time.Duration) streamOutcome {
	reader := bufio.NewReader(resp.Body)

	// buffered 缓存 header-hold 期间已从上游读出的全部原始字节, 一旦放行需
	// 原样写给客户端 (含被检视的首个 data 帧).
	var buffered bytes.Buffer

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			buffered.Write(line)

			// 提取这一行的 SSE data payload (去掉 "data:" 前缀与空白).
			payload := parseDataLine(line)
			if payload != nil && !bytes.Equal(payload, []byte("[DONE]")) {
				// 见到首个有效 data 帧.
				if isOpenAI {
					if statusCode, msg, isErr := parseSSEError(payload); isErr {
						// 首帧即 error: 还没发头, 安全 failover.
						return streamOutcome{
							failed:      true,
							statusCode:  statusCode,
							parsedError: msg,
						}
					}
				}
				// 有效首帧 → 发头并放行透传.
				return flushAndStream(c, resp, &buffered, reader, isOpenAI, idleTimeout)
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			// 读上游出错 (非 EOF), 且还没发头 → failover (statusCode 0 = 网络错误).
			return streamOutcome{failed: true, statusCode: 0, parsedError: err.Error()}
		}

		// 防止上游只发无效行 (注释/空行) 把缓冲撑爆: 超过上限就当作"确有数据"放行.
		if buffered.Len() >= firstChunkBufCap {
			return flushAndStream(c, resp, &buffered, reader, isOpenAI, idleTimeout)
		}
	}

	// 整流读完都没见到有效 data 帧.
	if buffered.Len() == 0 {
		// 完全空流.
		return streamOutcome{
			failed:      true,
			statusCode:  http.StatusBadGateway,
			parsedError: "empty upstream stream",
		}
	}
	// 非 OpenAI 等场景: 有字节但没有可识别的 data 帧. 仍视为"有数据"放行透传,
	// 避免误杀 (上游可能用非 OpenAI 的帧格式).
	return flushAndStream(c, resp, &buffered, reader, isOpenAI, idleTimeout)
}

// flushAndStream 设置 SSE 响应头, 发 c.Status, 写已缓冲字节并 flush,
// 再手写循环透传剩余上游字节 (含 inactivity 超时 + 截断检测).
// 返回 wroteToClient=true.
func flushAndStream(c *gin.Context, resp *http.Response, buffered *bytes.Buffer, reader *bufio.Reader, isOpenAI bool, idleTimeout time.Duration) streamOutcome {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)

	flusher, _ := c.Writer.(http.Flusher)

	if buffered.Len() > 0 {
		if _, err := c.Writer.Write(buffered.Bytes()); err != nil {
			logUpstreamError("writing buffered stream to client", err)
			return streamOutcome{wroteToClient: true, failed: true, statusCode: 0, parsedError: err.Error()}
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	// 透传循环: 按行读取剩余上游流, 边读边写 (保持打字机体验).
	// inactivity 超时: idleTimeout > 0 时, 若超过 idleTimeout 无数据到达则关闭 body 终止循环.
	// 截断检测 (isOpenAI): 记录是否见到 [DONE] 或 finish_reason (非 null).
	var (
		seenDone    bool
		idleTimer   *time.Timer
		idleExpired bool
	)

	if idleTimeout > 0 {
		idleTimer = time.AfterFunc(idleTimeout, func() {
			idleExpired = true
			resp.Body.Close()
		})
		defer idleTimer.Stop()
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			// 成功读到数据 — 重置 idle 计时器.
			if idleTimer != nil {
				idleTimer.Reset(idleTimeout)
			}

			chunk := buf[:n]

			// 截断检测: 扫描本批次字节里的 SSE data 行.
			if isOpenAI && !seenDone {
				seenDone = scanForCompletion(chunk)
			}

			if _, werr := c.Writer.Write(chunk); werr != nil {
				logUpstreamError("writing stream chunk to client", werr)
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}

		if err != nil {
			if err != io.EOF {
				if idleExpired {
					logrus.Warnf("[stream] inactivity timeout (%v) reached, stream forcefully closed", idleTimeout)
				} else {
					logUpstreamError("reading stream from upstream", err)
				}
			}
			break
		}
	}

	if flusher != nil {
		flusher.Flush()
	}

	outcome := streamOutcome{wroteToClient: true}
	if isOpenAI && !seenDone {
		outcome.truncated = true
		logrus.Warn("[stream] upstream stream ended without [DONE] or finish_reason — possible truncation")
	}
	return outcome
}

// scanForCompletion 检查一批 SSE 字节里是否含有流正常结束的信号:
//   - data: [DONE]
//   - JSON 帧中 finish_reason 字段值非 null (e.g. "stop","length","tool_calls")
func scanForCompletion(chunk []byte) bool {
	// 按行扫描.
	lines := bytes.Split(chunk, []byte("\n"))
	for _, line := range lines {
		payload := parseDataLine(line)
		if payload == nil {
			continue
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			return true
		}
		if hasFinishReason(payload) {
			return true
		}
	}
	return false
}

// hasFinishReason 解析 SSE data payload, 检查 choices[*].finish_reason 是否非 null.
func hasFinishReason(payload []byte) bool {
	var frame struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		return false
	}
	for _, ch := range frame.Choices {
		if ch.FinishReason != nil {
			return true
		}
	}
	return false
}

// parseDataLine 从一行 SSE 文本提取 data payload. 非 data 行 (注释 ":"、
// event:、空行等) 返回 nil. 返回的 payload 已去掉 "data:" 前缀及首尾空白.
func parseDataLine(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 {
		return nil
	}
	return payload
}

// parseSSEError 解析单个 data 帧 payload. 若 JSON 里含 error 对象则 isErr=true,
// 从 error.code (数字, 范围 400-599) 取状态码, 否则回退 502; message 取 error.message.
// 非 JSON / 无 error 字段 → isErr=false.
func parseSSEError(payload []byte) (statusCode int, message string, isErr bool) {
	var frame struct {
		Error *struct {
			Message string          `json:"message"`
			Code    json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		return 0, "", false
	}
	if frame.Error == nil {
		return 0, "", false
	}

	statusCode = http.StatusBadGateway
	if len(frame.Error.Code) > 0 {
		var codeNum int
		if err := json.Unmarshal(frame.Error.Code, &codeNum); err == nil && codeNum >= 400 && codeNum <= 599 {
			statusCode = codeNum
		}
	}
	return statusCode, frame.Error.Message, true
}
