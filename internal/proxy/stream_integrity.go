package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"autogateway/internal/usage"

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
	wroteToClient bool        // 是否已经向客户端发头并写过数据
	failed        bool        // 本次转发是否失败 (空流 / error 帧 / 读错误)
	statusCode    int         // failover 用的状态码 (0 = 网络/读错误)
	parsedError   string      // failover 用的错误信息
	truncated     bool        // 流已写给客户端但未正常终止 (无 [DONE]/finish_reason)
	usage         usage.Usage // ①成本可观测性: 从 SSE 帧累积的 token 用量 (末帧带, 逐帧 max 合并)
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
	// I1: 先转发上游响应头 (x-request-id, openai-*, x-ratelimit-* 等), 再用 SSE 专用头覆盖同名字段.
	for k, vals := range resp.Header {
		for _, v := range vals {
			c.Header(k, v)
		}
	}
	// SSE 专用头 (覆盖上游同名头).
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)

	flusher, _ := c.Writer.(http.Flusher)

	// 透传循环: 按行读取剩余上游流, 边读边写 (保持打字机体验).
	// inactivity 超时: idleTimeout > 0 时, 若超过 idleTimeout 无数据到达则关闭 body 终止循环.
	// 截断检测 (isOpenAI): 记录是否见到 [DONE] 或 finish_reason (非 null).
	// tool_call 补全检测 (isOpenAI): 记录是否见到 tool_calls delta 但无 finish_reason,
	// 若是则在流尾补发一个带 finish_reason:"tool_calls" 的 chunk.
	var (
		seenDone     bool
		sawToolCalls bool // 见过含 tool_calls 的 delta 帧
		sawFinish    bool // 见过 finish_reason 非 null 的帧
		idleTimer    *time.Timer
		// M1: atomic.Bool 避免 AfterFunc goroutine 与主循环的数据竞争.
		idleExpired atomic.Bool
		// ①成本可观测性: 逐帧累积 token 用量 (对所有渠道扫描, 非仅 OpenAI).
		usageAcc usage.Usage
	)

	if buffered.Len() > 0 {
		// 在写给客户端之前先扫描缓冲区 (含 header-hold 期间捕获的首帧).
		scanUsage(buffered.Bytes(), &usageAcc)
		if isOpenAI {
			bufferedBytes := buffered.Bytes()
			if !seenDone {
				seenDone = scanForCompletion(bufferedBytes)
			}
			tc, fr := scanToolCallsAndFinish(bufferedBytes)
			if tc {
				sawToolCalls = true
			}
			if fr {
				sawFinish = true
			}
		}
		if _, err := c.Writer.Write(buffered.Bytes()); err != nil {
			logUpstreamError("writing buffered stream to client", err)
			return streamOutcome{wroteToClient: true, failed: true, statusCode: 0, parsedError: err.Error()}
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	if idleTimeout > 0 {
		idleTimer = time.AfterFunc(idleTimeout, func() {
			// M1/M2: Store via atomic — AfterFunc goroutine 与主循环并发安全.
			// 超时竞态已知且可接受：最坏是流提前正常终止.
			idleExpired.Store(true)
			resp.Body.Close()
		})
		defer idleTimer.Stop()
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			// 成功读到数据 — 重置 idle 计时器.
			// M2: Reset 与 AfterFunc 存在竞态, 已知且可接受: 最坏是流提前正常终止.
			if idleTimer != nil {
				idleTimer.Reset(idleTimeout)
			}

			chunk := buf[:n]

			// ①成本可观测性: 扫描本批次里的 usage 帧 (max 合并, 幂等).
			scanUsage(chunk, &usageAcc)

			// 截断检测 + tool_call 补全检测: 扫描本批次字节里的 SSE data 行.
			if isOpenAI && !seenDone {
				seenDone = scanForCompletion(chunk)
			}
			if isOpenAI && !sawFinish {
				tc, fr := scanToolCallsAndFinish(chunk)
				if tc {
					sawToolCalls = true
				}
				if fr {
					sawFinish = true
				}
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
				if idleExpired.Load() {
					logrus.Warnf("[stream] inactivity timeout (%v) reached, stream forcefully closed", idleTimeout)
				} else {
					logUpstreamError("reading stream from upstream", err)
				}
			}
			break
		}
	}

	// tool_calls 补全: 若 OpenAI 流含 tool_calls delta 但结尾没有 finish_reason,
	// 补发一个带 finish_reason:"tool_calls" 的 chunk, 避免 IDE 客户端 (Cursor/Cline)
	// 因缺少 finish_reason 而解析失败.
	if isOpenAI && sawToolCalls && !sawFinish {
		const injectChunk = "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"
		_, _ = c.Writer.Write([]byte(injectChunk))
		if flusher != nil {
			flusher.Flush()
		}
		logrus.Info("[stream] injected finish_reason:tool_calls chunk (upstream omitted it)")
	}

	if flusher != nil {
		flusher.Flush()
	}

	outcome := streamOutcome{wroteToClient: true, usage: usageAcc}
	if isOpenAI && !seenDone {
		// 若补了 finish_reason (tool_calls 补全路径), 视为已规整, 不标 truncated.
		if sawToolCalls && !sawFinish {
			// 已补全, 不截断
		} else {
			outcome.truncated = true
			logrus.Warn("[stream] upstream stream ended without [DONE] or finish_reason — possible truncation")
		}
	}
	return outcome
}

// scanUsage 扫描一批 SSE 字节, 对每个 data 帧尝试解析 token 用量并 max 合并进
// acc。对所有渠道生效 (Anthropic/Gemini 流总带 usage; OpenAI 仅在客户端传
// stream_options.include_usage 时末帧才带)。max 合并保证幂等, 帧被重复扫描
// (如 buffered 首帧) 也不会重复计数。
//
// 已知局限: 单个 usage JSON 帧若恰好被 32KB 读边界劈成两半, 两半都解析不出 ——
// 与现有 scanForCompletion 同款权衡, 概率极低 (usage 帧短小且多在流尾单独到达)。
func scanUsage(chunk []byte, acc *usage.Usage) {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		payload := parseDataLine(line)
		if payload == nil || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if u, ok := usage.Extract(payload); ok {
			*acc = acc.Merge(u)
		}
	}
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

// scanToolCallsAndFinish 扫描一批 SSE 字节, 返回:
//   - sawTC: 本批次含有非空 tool_calls 数组的 delta 帧
//   - sawFR: 本批次含有 finish_reason 非 null 的帧
//
// 用于 flushAndStream 透传循环中的 tool_call 补全检测.
func scanToolCallsAndFinish(chunk []byte) (sawTC, sawFR bool) {
	lines := bytes.Split(chunk, []byte("\n"))
	for _, line := range lines {
		payload := parseDataLine(line)
		if payload == nil || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if !sawFR && hasFinishReason(payload) {
			sawFR = true
		}
		if !sawTC && hasToolCallsDelta(payload) {
			sawTC = true
		}
		if sawTC && sawFR {
			break
		}
	}
	return
}

// hasToolCallsDelta 解析 SSE data payload, 检查 choices[*].delta.tool_calls 是否存在且非空.
func hasToolCallsDelta(payload []byte) bool {
	var frame struct {
		Choices []struct {
			Delta struct {
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		return false
	}
	for _, ch := range frame.Choices {
		if len(ch.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
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
