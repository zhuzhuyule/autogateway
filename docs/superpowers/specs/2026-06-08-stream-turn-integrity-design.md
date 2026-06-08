# 阶段 E：流式 turn-integrity 设计

> FreeLLMAPI 能力集成路线第 5 阶段（最后一个）。借鉴点 #10（header-hold）、#11（空回复/error 帧检测）、#12（SSE 超时+截断检测）、#13（tool_call delta 缓冲）。
> 前置：阶段 A-D 已合并。路线见记忆 `project_freellmapi_integration_roadmap`。
> **这是整个集成里唯一动主代理控制流的阶段，风险最高。** 用「先纯重构、后加功能」的顺序降低风险。

## 背景与现状

`handleStreamingResponse`（`internal/proxy/response_handlers.go:11`）朴素透传：设 SSE 头 → 4KB buffer 循环读上游写客户端 + flush。进入它之前 `server.go:546` 已 `c.Status(resp.StatusCode)` 发头。

`executeRequestWithRetry`（`server.go:282-561`）两大分支：
- **失败块**（line 386 `if err != nil || isRetryableHTTPError`，约 140 行）：含全部 failover——聚合切 sub-group、P4 candidate pool、retry 计数、记账、cooldown 反馈。`canFailover` 含 `!c.Writer.Written()` 守卫。
- **成功块**（line 530+）：copy headers → `c.Status` → `handleStreamingResponse`/`handleNormalResponse`。

**核心痛点**：一旦进成功块发了头，「流式 200 但首 chunk 是 error/空/超时」就无法回退（免费 provider 的典型故障：接受连接、回 200、流里翻车）。还有：上游流挂死时客户端干等；流截断（无 `[DONE]`/`finish_reason`）客户端误判完整；tool_call delta 格式不规范导致 IDE 解析失败。

## 目标（#10-13，全做）

1. **#10 header-hold**：流式响应推迟发头到读到首个有效 chunk，使「发头前失败」能无感 failover。
2. **#11 空回复/error 帧检测**：识别 200-but-empty、流内 error 帧 → 转可重试，走 failover（并联动 A 冷却）。
3. **#12 inactivity 超时 + 截断检测**：读循环加无数据超时；流结束检查有无 `[DONE]`/`finish_reason`。
4. **#13 tool_call delta 缓冲**：缓冲流式工具调用 delta、补 `finish_reason`，修 IDE 客户端解析。

### 协议范围
- **#10 header-hold、#12 inactivity 超时**：**协议无关（字节流层）** —— OpenAI/Anthropic/Gemini 三协议自动受益。
- **#11 error 帧检测、#12 截断检测、#13 tool_call 缓冲**：需解析 SSE 内容，**先实现 OpenAI**（`data: {...}` SSE + `[DONE]`），架构预留 per-channel 解析扩展点；非 OpenAI 协议仍享受 #10/#12-inactivity 的字节层保护，内容检测降级为「不检测」（不误判、保持透传）。

## 非目标（YAGNI）

- 不做 Anthropic/Gemini 的内容级 error/截断/tool_call 解析（仅留扩展接口）。
- 不缓冲整个流（只缓冲到能判定首 chunk 有效性 + tool_call 段；正文 chunk 仍流式透传，保持打字机体验）。
- 不改非流式路径（`handleNormalResponse` 已有 `validateJSONSuccessResponse` 校验，不动）。

## 设计

### 阶段 0（Task 1）：failover 决策抽取（纯重构，行为不变）

把失败块（386-528）的「一次尝试失败后的处理」抽成一个方法，让流式首-chunk 失败也能复用。先建 **characterization 测试**锁住现有 failover 行为，再重构，保证零行为变化。

```go
// attemptContext 打包一次转发尝试的上下文（失败处理/failover 所需）。
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

// handleAttemptFailure 处理一次失败（HTTP 错误 / 网络错误 / 流首-chunk 无效），
// 执行记账 + cooldown 反馈 + failover/retry 决策。返回是否已接管后续（递归/已写错误响应）。
// status==0 表示网络错误；resp 可为 nil。把现有失败块逻辑原样搬入，不改行为。
func (ps *ProxyServer) handleAttemptFailure(ac *attemptContext, resp *http.Response, statusCode int, errorMessage, parsedError string) {
	// ...现有 386-528 的失败块逻辑搬这里...
}
```
失败块原地替换为构造 `attemptContext` + 调 `handleAttemptFailure`。**Task 1 完成后行为与重构前逐字节一致**（characterization 测试 + 现有 proxy 测试全绿验证）。

### 阶段 1（Task 2）：#10 header-hold + #11 空/error 检测

新函数取代成功块对流式的处理：
```go
// streamOutcome 描述流式首-chunk 验证结果。
type streamOutcome struct {
	wroteToClient bool   // 是否已向客户端发头/写数据（true 则不可再 failover）
	failed        bool   // 首 chunk 前判定失败
	statusCode    int    // 失败时的伪状态码（如 502 / 从 error 帧解析）
	parsedError   string
}

// streamWithIntegrity 读上游流，先验证首个有效 chunk 再发头。
//   - 读到有效 chunk → c.Status + 写头 + flush 缓冲 + 继续透传（含 #12/#13）→ wroteToClient=true
//   - 首 chunk 前 EOF/空/error帧/超时 → failed=true, wroteToClient=false（未发头，可 failover）
func (ps *ProxyServer) streamWithIntegrity(c *gin.Context, resp *http.Response, ch channel.ChannelProxy) streamOutcome
```

server.go 成功块改造（流式分支）：
```go
if isStream {
	out := ps.streamWithIntegrity(c, resp, channelHandler)
	if out.failed && !out.wroteToClient {
		// 200 但流无效，且未发头 → 当作一次尝试失败，复用 failover
		ps.handleAttemptFailure(ac, resp, out.statusCode, out.parsedError, out.parsedError)
		return
	}
	// 成功透传（或已写客户端无法回退）→ 正常收尾
}
```

**#11 检测**（OpenAI）：缓冲首 chunk 至能判定——
- 整流 EOF 前无任何 `data:` 行 → 空回复，failed（statusCode 502/伪码）。
- 首个 `data:` 帧 JSON 含 `error` 字段 → error 帧，failed（从 error 解析 statusCode/message，联动 cooldown）。
- 否则有效 → 发头透传。
非 OpenAI channel：只做「整流空 → failed」的字节层判定，不解析 error 帧。

**首字节缓冲上限**：设一个小上限（如 64KB 或首个完整 SSE 帧），超过即视为有效开始透传，避免无限缓冲拖垮 TTFB。

### 阶段 2（Task 3）：#12 inactivity 超时 + 截断检测

- **inactivity 超时**（协议无关）：透传读循环对每次 `resp.Body.Read` 设软超时（如 `EffectiveConfig` 新增 `StreamIdleTimeout`，默认 90s）。用 `resp.Body` 上的 read deadline 或一个配合 `time.Timer` + goroutine 的读包装。超时 → 若已写客户端则结束（记错误日志），未写则 failed 走 failover。
- **截断检测**（OpenAI）：透传过程中记录是否见过 `data: [DONE]` 或 `finish_reason`。整流结束（EOF）时若从未见过 → 截断。已写客户端时无法回退，记录 + 可向流尾追加一个 error 事件提示；未写客户端（理论少见）则 failed。

### 阶段 3（Task 4）：#13 tool_call delta 缓冲（OpenAI）

- 透传时若检测到 `tool_calls` delta，进入缓冲模式：累积 tool_call 片段，直到流结束或出现非 tool_call 内容，一次性发出规整后的 tool_call chunk 并确保带 `finish_reason: "tool_calls"`。
- 仅 OpenAI channel 启用；其他协议透传不变。
- 边界：缓冲 tool_call 不破坏正文流式（正文 delta 仍实时透传，仅 tool_call 段缓冲）。

### 配置

`SystemSettings` + `GroupConfig` 加 `StreamIdleTimeout int`（秒，默认 90，0=不启用 inactivity 超时），照 BlacklistThreshold 模式（tag 驱动前端 + 反射合并）。

## 错误处理

- 任何 integrity 检测的内部错误 → 降级为「按有效透传处理」（绝不因检测逻辑 bug 阻断正常流）。
- 已写客户端后的任何失败 → 只能记录 + 结束，不重试（不可能回退已发字节）。
- failover 复用 Task 1 抽取的 `handleAttemptFailure`，记账/cooldown/计数与非流式失败完全一致（不重复记账）。

## 测试

1. **Task 1 characterization**（重构前写，锁行为）：现有 failover 场景（聚合切 sub-group、retry 耗尽返回错误、单分组失败）的行为快照测试；重构后必须全绿、行为不变。
2. **streamWithIntegrity**（mock resp.Body）：有效首 chunk → wroteToClient=true 透传；空流 → failed/未写；OpenAI error 帧 → failed + 解析出 statusCode；缓冲上限触发即透传。
3. **inactivity 超时**：mock 一个读到一半阻塞的 body → 超时触发；未写客户端→failed，已写→结束。
4. **截断检测**：无 `[DONE]` 的 OpenAI 流 → 标记截断；有 `[DONE]` → 正常。
5. **tool_call 缓冲**：分片 tool_call delta → 输出含 finish_reason；正文 delta 不受影响仍实时。
6. **failover 接入**：流式 200-but-error 帧 → 走 handleAttemptFailure → 聚合切下一个 sub-group（端到端，复用 sticky/keypool 测试的 seed 方式）。
7. 回归：全量 `go test ./...`；非流式路径不变；三协议 IsStreamRequest 不受影响。

## 验收标准

- `go build ./...`、`go test ./...` 通过。
- Task 1 后 failover 行为与重构前一致（characterization 全绿）。
- 流式 200-but-empty / error 帧 → 未发头时无感 failover 到下一候选；已发头则优雅结束不损坏流。
- 上游挂死 → inactivity 超时切断，不让客户端干等。
- OpenAI 流截断 → 被识别。
- OpenAI tool_call 流 → 带 finish_reason，IDE 可解析。
- 非 OpenAI 协议：享受 header-hold（空流可回退）+ inactivity 超时；内容检测降级不误判。
- 非流式路径零回归。

## 影响面

- 改 `internal/proxy/server.go`（failover 抽取 + 流式成功块改造）、`internal/proxy/response_handlers.go`（streamWithIntegrity + 检测）、可能新增 `internal/proxy/stream_integrity.go`（流式检测逻辑独立文件）、`internal/channel/*`（可选：per-channel SSE 解析扩展点接口）、`internal/types/types.go`+`internal/models/types.go`（StreamIdleTimeout 配置）、相关测试。
- 无 DB schema、无 API 变更、前端仅配置项 i18n（tag 驱动）。
- 风险集中在 Task 1（failover 抽取）+ Task 2（header-hold 接入主流程）；用 characterization 测试 + 纯重构先行缓解。
