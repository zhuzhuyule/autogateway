# 流式 turn-integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 流式响应推迟发头到首个有效 chunk（#10），使 200-but-empty/error 帧能无感 failover（#11）；加 inactivity 超时 + 截断检测（#12）；OpenAI tool_call delta 缓冲补 finish_reason（#13）。最高风险阶段，用「characterization 测试 + 纯重构先行」缓解。

**Architecture:** Task 1 先把失败块的 failover 抽成 `handleAttemptFailure`（纯重构，行为不变）；Task 2 `streamWithIntegrity` 实现 header-hold + 空/error 检测并接入抽取的 failover；Task 3 inactivity 超时 + 截断检测 + 配置；Task 4 tool_call 缓冲。#10/#12-inactivity 协议无关，#11/#12-截断/#13 先 OpenAI。

**Tech Stack:** Go 1.25、gin、`bufio`（SSE 行解析）、`encoding/json`、`time`（read deadline）。

**Spec:** `docs/superpowers/specs/2026-06-08-stream-turn-integrity-design.md`

---

## File Structure

- **Modify** `internal/proxy/server.go` — Task1 抽 `handleAttemptFailure` + `attemptContext`；Task2 流式成功块改造接入。
- **Create** `internal/proxy/stream_integrity.go` + `stream_integrity_test.go` — `streamWithIntegrity`/`streamOutcome` + SSE 检测（#10/#11/#12/#13）。
- **Modify** `internal/proxy/response_handlers.go` — 旧 `handleStreamingResponse` 保留给非流式 fallback 或移除（按 Task2 决定）。
- **Modify** `internal/proxy/server_test.go`（characterization）。
- **Modify** `internal/types/types.go` + `internal/models/types.go` + i18n — Task3 `StreamIdleTimeout`。

---

## Task 1: failover 抽取（characterization 先行 + 纯重构）

**Files:**
- Test: `internal/proxy/server_test.go`（或新建 characterization 测试文件）
- Modify: `internal/proxy/server.go`（失败块 386-528）

- [ ] **Step 1: 先读懂失败块**

Read `internal/proxy/server.go` 的 `executeRequestWithRetry` 完整失败块（`if err != nil || isRetryableHTTPError {` 到该块结束，约 386-528）。列出它用到的所有局部变量、它的副作用（markRoutingCandidate / UpdateStatus / RecordSubGroupResult / logRequest / 递归 executeRequestWithRetry / 写错误响应）、以及 return 时机。

- [ ] **Step 2: 写 characterization 测试（锁现有行为）**

在 `internal/proxy/server_test.go` 补 failover 行为快照测试。先看现有 proxy 测试怎么构造 ProxyServer + mock 上游（`grep -n "httptest\|NewProxyServer\|func Test" internal/proxy/*_test.go`）。覆盖至少：
- 聚合分组：sub-group A 失败（500）→ 切到 sub-group B 成功，断言最终 200 + B 被调用。
- 标准分组：单 key 失败重试耗尽 → 返回上游错误状态码。
若现有测试已覆盖这些，标注复用、不重复写。

Run: `go test ./internal/proxy/ -v`（记录当前全绿基线）。

- [ ] **Step 3: 定义 attemptContext + handleAttemptFailure（搬移，不改逻辑）**

在 server.go 加：
```go
type attemptContext struct {
	c                  *gin.Context
	channelHandler     channel.ChannelProxy
	originalGroup      *models.Group
	group              *models.Group
	apiKey             *models.APIKey
	bodyBytes          []byte
	isStream           bool
	startTime          time.Time
	retryCount         int
	attemptedSubGroups map[string]bool
	requestedModel     string
}

// handleAttemptFailure 处理一次失败（HTTP错误/网络错误/流首-chunk无效）：记账 +
// cooldown 反馈 + failover/retry 决策。把现有失败块逻辑原样搬入，行为不变。
// statusCode==0 表示网络错误；resp 可为 nil。
func (ps *ProxyServer) handleAttemptFailure(ac *attemptContext, resp *http.Response, statusCode int, errorMessage, parsedError string) {
	// 把 386-528 失败块「解析后」的逻辑搬这里：
	//   markRoutingCandidate / UpdateStatus / canFailover 判定 / P4 candidate /
	//   SelectSubGroupForModelExcluding / isLastAttempt / 递归 executeRequestWithRetry。
	// 用 ac.xxx 替换原局部变量。递归调用 ps.executeRequestWithRetry(ac.c, ...) 保持原参数。
}
```

- [ ] **Step 4: 失败块改为调用 handleAttemptFailure**

把原失败块里「statusCode/errorMessage/parsedError 解析」之后的所有逻辑替换为：构造 `ac := &attemptContext{...}` + `ps.handleAttemptFailure(ac, resp, statusCode, errorMessage, parsedError); return`。解析部分（IsIgnorableError 早退、读 errorBody、ParseUpstreamError）保留在原地。

> 关键：这是纯搬移。逐变量核对 attemptContext 字段与原局部变量一一对应。递归 executeRequestWithRetry 的实参顺序不变。

- [ ] **Step 5: 验证行为不变**

Run: `go build ./... && go test ./internal/proxy/ -v`
Expected: characterization + 现有测试全绿，行为与 Step 2 基线一致。

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/server.go internal/proxy/server_test.go
git commit -m "♻️ refactor(proxy): 抽取 handleAttemptFailure (纯重构, 行为不变, characterization 锁定)"
```

---

## Task 2: #10 header-hold + #11 空/error 检测

**Files:**
- Create: `internal/proxy/stream_integrity.go` + `stream_integrity_test.go`
- Modify: `internal/proxy/server.go`（成功块流式分支）

- [ ] **Step 1: 写失败测试**

创建 `internal/proxy/stream_integrity_test.go`，用 `io.NopCloser(strings.NewReader(...))` 构造假上游流 + `httptest.NewRecorder()` 包 gin context。测：
```go
// 有效 OpenAI 首 chunk → wroteToClient=true, failed=false, 客户端收到数据
// 空流(EOF无data) → failed=true, wroteToClient=false
// error 帧 data: {"error":{"message":"rate","code":429}} → failed=true, statusCode 解析
// 非 OpenAI(传 ch=nil 或非 openai) 空流 → failed；有数据 → 透传不解析 error
```
（具体构造参考现有 proxy 测试的 gin context 搭法。）

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/proxy/ -run TestStreamIntegrity -v` → 编译失败。

- [ ] **Step 3: 实现 streamWithIntegrity**

创建 `internal/proxy/stream_integrity.go`：
```go
package proxy

type streamOutcome struct {
	wroteToClient bool
	failed        bool
	statusCode    int
	parsedError   string
}

const firstChunkBufCap = 64 * 1024 // 首-chunk 验证缓冲上限，防 TTFB 拖垮

// streamWithIntegrity 读上游流，先验证首个有效 chunk 再发头。
// isOpenAI 决定是否做内容级 error/空帧解析（其他协议仅字节层空流判定）。
func (ps *ProxyServer) streamWithIntegrity(c *gin.Context, resp *http.Response, isOpenAI bool) streamOutcome {
	reader := bufio.NewReader(resp.Body)
	var buf bytes.Buffer
	sawData := false
	// 1) 缓冲读，直到：见到首个有效 data 帧 / EOF / 超过 firstChunkBufCap
	for buf.Len() < firstChunkBufCap {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			buf.Write(line)
			if isOpenAI {
				if trimmed := bytes.TrimSpace(line); bytes.HasPrefix(trimmed, []byte("data:")) {
					payload := bytes.TrimSpace(trimmed[len("data:"):])
					if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
						// #11: 检测 error 帧
						if sc, msg, isErr := parseSSEError(payload); isErr {
							return streamOutcome{failed: true, statusCode: sc, parsedError: msg}
						}
						sawData = true
						break // 有效首 chunk
					}
				}
			} else if len(bytes.TrimSpace(line)) > 0 {
				sawData = true
				break
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return streamOutcome{failed: true, statusCode: 0, parsedError: err.Error()}
		}
	}
	if !sawData && buf.Len() == 0 {
		// #11: 空流
		return streamOutcome{failed: true, statusCode: 502, parsedError: "empty upstream stream"}
	}
	// 2) 有效 → 发头 + flush 缓冲 + 继续透传
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)
	flusher, _ := c.Writer.(http.Flusher)
	c.Writer.Write(buf.Bytes())
	if flusher != nil { flusher.Flush() }
	// 透传剩余（Task3 在此加 inactivity 超时 / 截断检测；Task4 加 tool_call 缓冲）
	io.Copy(c.Writer, reader)
	if flusher != nil { flusher.Flush() }
	return streamOutcome{wroteToClient: true}
}

// parseSSEError 解析 OpenAI SSE data 帧是否为 error；是则返回状态码+消息。
func parseSSEError(payload []byte) (statusCode int, message string, isErr bool) {
	var probe struct {
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil || probe.Error == nil {
		return 0, "", false
	}
	sc := 502
	if s, ok := probe.Error.Code.(float64); ok && s >= 400 && s < 600 {
		sc = int(s)
	}
	return sc, probe.Error.Message, true
}
```
（import：bufio/bytes/encoding/json/io/net/http；gin。)

- [ ] **Step 4: server.go 成功块接入**

Read server.go 成功块（流式分支约 553-557）。把：
```go
if isStream {
	ps.handleStreamingResponse(c, resp)
} else {
	ps.handleNormalResponse(c, resp)
}
```
改为：
```go
if isStream {
	isOpenAI := isOpenAIChannel(channelHandler) // 见下注
	out := ps.streamWithIntegrity(c, resp, isOpenAI)
	if out.failed && !out.wroteToClient {
		ac := &attemptContext{c: c, channelHandler: channelHandler, originalGroup: originalGroup, group: group, apiKey: apiKey, bodyBytes: bodyBytes, isStream: isStream, startTime: startTime, retryCount: retryCount, attemptedSubGroups: attemptedSubGroups, requestedModel: requestedModel}
		ps.handleAttemptFailure(ac, resp, out.statusCode, out.parsedError, out.parsedError)
		return
	}
} else {
	ps.handleNormalResponse(c, resp)
}
```
> `isOpenAIChannel`：判断 channelHandler 是否 OpenAI 类型。最简单按 group.ChannelType=="openai" 判断（用 `group.ChannelType` 或现有方法）；Read 确认怎么取 channel 类型。注意：成功块此前已 `copy headers + c.Status(resp.StatusCode)`（约 546-551）——header-hold 要求**流式分支不要提前 c.Status**，需把那段 copy/Status 移到非流式分支，或改为流式跳过（streamWithIntegrity 内部才发头）。Read 并调整：流式时不在成功块提前发头。

- [ ] **Step 5: 验证**

Run: `go build ./... && go test ./internal/proxy/ -v`
Expected: 新测试 + characterization + 现有全绿。

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/
git commit -m "✨ feat(proxy): 流式 header-hold + 空/error帧检测无感failover (#10/#11)"
```

---

## Task 3: #12 inactivity 超时 + 截断检测 + 配置

**Files:**
- Modify: `internal/types/types.go`、`internal/models/types.go`、i18n（`internal/i18n/locales/*.go`）
- Modify: `internal/proxy/stream_integrity.go`
- Test: `internal/proxy/stream_integrity_test.go`

- [ ] **Step 1: 加 StreamIdleTimeout 配置（照 BlacklistThreshold 模式）**

`SystemSettings` 加 `StreamIdleTimeout int` (default 90, `category:"config.category.request"`, validate min=0)；`GroupConfig` 加 `*int`；EffectiveConfig 反射合并自动生效；三语 i18n（后端 `internal/i18n/locales/*.go`，照 `config.request_timeout` 旁边加 `config.stream_idle_timeout`/`_desc`）。

- [ ] **Step 2: 写测试**

测：读到一半阻塞的 body → inactivity 超时触发（用一个 Read 会 block 的 mock + 短超时）；OpenAI 流无 `[DONE]` → truncated 标记；有 `[DONE]` → 正常。

- [ ] **Step 3: 实现 inactivity 超时 + 截断检测**

`streamWithIntegrity` 透传循环（Step3 的 `io.Copy` 替换为手写循环）：每次读用 deadline（`time.AfterFunc` 关闭 body 或带超时的读包装），无数据超 `StreamIdleTimeout` → 已写则记录结束、未写则 failed。透传时记录是否见过 `data: [DONE]` 或 `"finish_reason"`；EOF 时若 OpenAI 且从未见过 → 记 truncated（已写客户端则记录日志 + 可追加 error 事件，未写则 failed）。`StreamIdleTimeout` 从 group.EffectiveConfig 传入 streamWithIntegrity。

- [ ] **Step 4: 验证 + Commit**

Run: `go build ./... && go test ./internal/proxy/ -v`
```bash
git add internal/proxy/ internal/types/types.go internal/models/types.go internal/i18n/locales/
git commit -m "✨ feat(proxy): 流式 inactivity 超时 + 截断检测 + StreamIdleTimeout 配置 (#12)"
```

---

## Task 4: #13 tool_call delta 缓冲（OpenAI）

**Files:**
- Modify: `internal/proxy/stream_integrity.go`
- Test: `internal/proxy/stream_integrity_test.go`

- [ ] **Step 1: 写测试**

测：分片 tool_call delta（多个 `data:` 帧含 `tool_calls` 增量）→ 输出最终带 `finish_reason:"tool_calls"`；纯正文 delta 不受影响（实时透传、不缓冲）。

- [ ] **Step 2: 实现 tool_call 缓冲**

在透传循环（OpenAI 分支）：检测 chunk 含 `tool_calls` delta 时累积；流结束或转为正文时发出规整 chunk 并确保带 `finish_reason`。仅 OpenAI 启用，正文 delta 仍实时透传。

- [ ] **Step 3: 验证 + Commit**

Run: `go build ./... && go test ./internal/proxy/ -v`
```bash
git add internal/proxy/
git commit -m "✨ feat(proxy): OpenAI tool_call delta 缓冲补 finish_reason (#13)"
```

---

## Task 5: 收尾验证

- [ ] **Step 1: 全量** `go vet ./... && go test ./...` → 全绿。
- [ ] **Step 2: 零回归** 非流式路径不变；非 OpenAI 流式享受 header-hold/inactivity、内容检测降级不误判；配置默认值不改变现有行为。
- [ ] **Step 3: 验收对照** spec 验收标准逐条。
- [ ] **Step 4: 合并** finishing-a-development-branch（分支 `feat/stream-turn-integrity`）。

---

## Self-Review 记录

- **Spec coverage**：#10 header-hold→Task2 streamWithIntegrity；#11 空/error→Task2；#12 超时+截断→Task3；#13 tool_call→Task4；failover 抽取（风险缓解）→Task1。✅
- **Placeholder scan**：核心代码给出；Task1 failover 搬移、Task2 server.go 成功块 c.Status 位置调整、isOpenAIChannel 判定用 Read 现场对齐（重构依赖现有结构，行号/变量名漂移）——有界指令。Task3/4 透传循环改造给了方向 + 测试约束。
- **Type consistency**：`attemptContext`、`handleAttemptFailure(ac, resp, statusCode, errorMessage, parsedError)`、`streamWithIntegrity(c, resp, isOpenAI)→streamOutcome`、`streamOutcome{wroteToClient,failed,statusCode,parsedError}`、`parseSSEError(payload)→(int,string,bool)`、`StreamIdleTimeout` 各 Task 间一致。✅
- **风险控制**：Task1 纯重构 + characterization 先行；流式失败仅在「未写客户端」时 failover；检测内部错误降级为透传。✅
