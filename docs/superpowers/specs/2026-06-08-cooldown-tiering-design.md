# 阶段 A：冷却分级（Cooldown Tiering）设计

> 集成 FreeLLMAPI 调度能力路线的第 1 阶段。借鉴点 #5（升级阶梯）、#6（瞬时限流 vs 真耗尽区分）、#7（402 余额不足长冷却）。
> 整体路线分 5 阶段（A 冷却分级 / B 四维速率账本 / C 粘性会话 / D Thompson 采样 / E 流式 turn-integrity），逐阶段独立 spec、独立上线。本文件只覆盖阶段 A。

## 背景与现状

AutoGateway 当前有**两套独立的冷却机制**，触发点不同、行为不一致：

| 机制 | 文件 | 触发点 | 现有行为 | 缺口 |
|---|---|---|---|---|
| 机制 1 | `internal/router_engine/selector.go` 的 `cooldownStore` | alias / auto 路由的 `Selector.MarkResponse` | 纯指数退避 60s→120s→240s→480s（cap 5min）+ ±10% jitter，按 `group:model` key，**不区分错误类型**（所有 `status>=400` 都 `bump`） | 无错误分级、无 402、无瞬时/耗尽区分；对 401/403 这类 key 问题也错误地做 model 级冷却 |
| 机制 2 | `internal/services/subgroup_manager.go` 的 `selector.recordResult` | 聚合路由的 `RecordSubGroupResult` | **已区分** 429（按 `Retry-After`，默认 60s，clamp 10min，不算连续失败）vs 5xx/network（连续失败 + 30s cooldown），按 sub-group name | 429 内部不分 RPM/TPM 瞬时 vs RPD/TPD 耗尽；无 402；429 无升级阶梯 |

上游错误文本可获取：`internal/errors/parser.go` 的 `ParseUpstreamError` 已能从各家响应体提取 message（如 `"Rate limit reached ... requests per day"`），proxy 层 `executeRequestWithRetry` 里已有 `parsedError` 变量。但目前 `RecordSubGroupResult` / `MarkResponse` 调用都**没把 parsedError 传下去**。

## 目标

1. **#6** 区分"瞬时限流"（RPM/TPM 429，秒级恢复）与"真耗尽"（RPD/TPD，长隔离），避免 tpm 紧/rpd 松的 provider 被瞬时 429 误关数小时。
2. **#7** 402 余额不足 → 24h 长冷却，付费 key 余额耗尽不反复撞。
3. **#5** 反复 5xx 故障时冷却时间升级（已有雏形，统一到决策器）。
4. 两套冷却机制**行为一致**，分类/时长逻辑只此一处。

## 非目标（YAGNI）

- 不做冷却参数的 UI 配置面板（先用常量默认，后续需要再进 routing_settings）。
- 不统一两套冷却的**存储**——各自仍用进程内 state，只共享"该冷却多久"的纯函数决策。
- 不把 model 级冷却改成 Redis 共享跨实例同步（冷却本应快速本地决策；mesh 同步的是 key 状态，不是 model 冷却）。这超出阶段 A。

## 设计

### 1. 新建纯函数决策器 `internal/failover/cooldown.go`

放在 `failover` 包（与 `status_code_matcher.go` 同包，职责相邻）。**无副作用、无状态**，便于单测。

```go
package failover

import (
	"strings"
	"time"
)

// CooldownClass 冷却性质分类。
type CooldownClass int

const (
	ClassNone        CooldownClass = iota // 不做 model 级冷却（400/401/403 等）
	ClassTransient                        // RPM/TPM 瞬时限流，秒级恢复
	ClassExhausted                        // RPD/TPD 日额耗尽，长隔离
	ClassPayment                          // 402 余额/额度不足
	ClassServerError                      // 5xx / network，故障升级
)

// CooldownPolicy 各类冷却的时长参数。零值无意义，用 DefaultCooldownPolicy。
type CooldownPolicy struct {
	TransientDefault time.Duration // 无 Retry-After 时的瞬时冷却，默认 90s
	TransientMax     time.Duration // Retry-After 上限 clamp，默认 10min
	ExhaustedDefault time.Duration // 无 Retry-After 时的耗尽冷却，默认 6h
	PaymentCooldown  time.Duration // 402 冷却，默认 24h
	ServerBase       time.Duration // 5xx 升级阶梯基数，默认 30s
	ServerMax        time.Duration // 5xx 升级阶梯上限，默认 5min
}

func DefaultCooldownPolicy() CooldownPolicy {
	return CooldownPolicy{
		TransientDefault: 90 * time.Second,
		TransientMax:     10 * time.Minute,
		ExhaustedDefault: 6 * time.Hour,
		PaymentCooldown:  24 * time.Hour,
		ServerBase:       30 * time.Second,
		ServerMax:        5 * time.Minute,
	}
}

// Classify 从 HTTP 状态码 + 上游错误文本推断冷却性质。纯函数。
func Classify(statusCode int, parsedError string) CooldownClass {
	low := strings.ToLower(parsedError)

	// 402 或计费/额度类关键词 → Payment（优先于 429，部分 provider 用 429 表达余额耗尽）
	if statusCode == 402 ||
		strings.Contains(low, "insufficient") ||
		strings.Contains(low, "payment") ||
		strings.Contains(low, "billing") ||
		strings.Contains(low, "exceeded your current quota") {
		return ClassPayment
	}

	if statusCode == 429 {
		// 日额耗尽信号
		if strings.Contains(low, "per day") ||
			strings.Contains(low, "per-day") ||
			strings.Contains(low, "/day") ||
			strings.Contains(low, "daily") ||
			strings.Contains(low, "rpd") ||
			strings.Contains(low, "tpd") ||
			strings.Contains(low, "requests per day") ||
			strings.Contains(low, "tokens per day") {
			return ClassExhausted
		}
		// 其余 429（per minute / RPM / TPM / 无明确标识）按瞬时处理
		return ClassTransient
	}

	if statusCode >= 500 || statusCode == 0 { // 0 = network error（调用方约定）
		return ClassServerError
	}

	// 400 / 401 / 403 等：不做 model 级冷却，交给 key 熔断或直接返回
	return ClassNone
}

// Decide 返回本次冷却时长，以及该次是否计入升级计数（hitCount）。
//   hitCount: 调用方维护的、该目标在升级窗口内已累计的"计入升级"次数（从 0 起）。
//   仅 ClassServerError 使用 hitCount 做指数升级；其余类别时长由错误性质直接决定。
func (p CooldownPolicy) Decide(class CooldownClass, retryAfter time.Duration, hitCount int) (dur time.Duration, countsTowardEscalation bool) {
	switch class {
	case ClassTransient:
		if retryAfter > 0 {
			if retryAfter > p.TransientMax {
				return p.TransientMax, false
			}
			return retryAfter, false
		}
		return p.TransientDefault, false
	case ClassExhausted:
		if retryAfter > 0 {
			return retryAfter, false // 上游明确给了恢复时间就信它
		}
		return p.ExhaustedDefault, false
	case ClassPayment:
		return p.PaymentCooldown, false
	case ClassServerError:
		shift := hitCount
		if shift > 4 {
			shift = 4
		}
		d := p.ServerBase << uint(shift) // 30s,1m,2m,4m,8m → cap
		if d > p.ServerMax {
			d = p.ServerMax
		}
		return d, true
	default: // ClassNone
		return 0, false
	}
}
```

设计要点：
- **升级阶梯只对 `ServerError`**（反复 5xx 越等越久）。`Transient/Exhausted/Payment` 的时长由错误性质直接定，无需升级计数 → 不引入新的"升级窗口"状态，直接复用两套机制已有的失败计数。
- `ClassNone` 返回 `0`：调用方收到 `dur==0` 时**不设置冷却**（这是机制 1 的行为收窄——目前它对所有 ≥400 都退避，包含 401/403；新逻辑下 401/403 不再做 model 级冷却，由 key 熔断处理）。
- 网络错误约定用 `statusCode == 0` 表达（proxy 层 err != nil 时当前置 statusCode=500，接入时改为传 0 或保持 500 均归 `ServerError`，行为一致）。

### 2. 机制 1 接入（`router_engine`）

`cooldownStore`：新增按 class 设置冷却时长的方法，保留升级仅用于 ServerError。

```go
// 替换原 bump(key)
func (c *cooldownStore) apply(key string, class failover.CooldownClass, retryAfter time.Duration, policy failover.CooldownPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.data[key]
	dur, counts := policy.Decide(class, retryAfter, e.failures)
	if dur <= 0 {
		return // ClassNone：不冷却
	}
	if counts {
		e.failures++
	}
	jit := time.Duration(rand.Int63n(int64(dur)/5 + 1))
	e.until = time.Now().Add(dur + jit)
	c.data[key] = e
}
```

`Selector.MarkResponse` 签名扩展：

```go
// 旧: MarkResponse(c Candidate, status int)
func (s *Selector) MarkResponse(c Candidate, status int, parsedError string, retryAfter time.Duration) {
	if c.GroupID == 0 || c.RealModel == "" {
		return
	}
	key := fmt.Sprintf("%d:%s", c.GroupID, c.RealModel)
	if status >= 200 && status < 400 {
		s.cooldown.reset(key)
		return
	}
	class := failover.Classify(status, parsedError)
	s.cooldown.apply(key, class, retryAfter, s.policy) // policy 存入 Selector，默认 DefaultCooldownPolicy
}
```

`Selector` 结构体新增 `policy failover.CooldownPolicy` 字段，`NewSelector` 里初始化为 `DefaultCooldownPolicy()`。

### 3. 机制 2 接入（`SubGroupManager`）

`recordResult` 与 `RecordSubGroupResult` 签名加 `parsedError string`。把现有的 `if statusCode == 429 {...} else {5xx}` 分支替换为统一决策：

```go
func (s *selector) recordResult(name string, success bool, statusCode int, parsedError string, retryAfter time.Duration) {
	// ... 成功路径不变（清 consecutiveFailures + cooldownUntil）...

	class := failover.Classify(statusCode, parsedError)
	if class == failover.ClassNone {
		// 400/401/403：不进 sub-group 冷却（保持原 5xx 之外不处理的语义）
		return
	}
	it.consecutiveFailures++
	dur, _ := s.policy.Decide(class, retryAfter, it.consecutiveFailures-1)
	if dur > 0 {
		it.cooldownUntil = time.Now().Add(dur)
	}
	// 日志带 class 名，便于观测
}
```

`SubGroupManager` / 其内部 `selector` 持有 `policy failover.CooldownPolicy`。原常量 `rateLimitDefaultCooldown` / `rateLimitMaxCooldown` / `subGroupBreakerCooldown` 由 policy 取代（保留常量定义或删除，二选一，实现时取删除以免双源）。

### 4. proxy 层调用点改造（`internal/proxy/server.go`）

- `markRoutingCandidate(c, statusCode)` → `markRoutingCandidate(c, statusCode, parsedError, retryAfter)`。成功路径 parsedError 传 `""`、retryAfter 传 0（走 reset）；失败路径传已有的 `parsedError` 和 `parseRetryAfter(resp.Header)`。
- 三处 `RecordSubGroupResult(...)` 调用（约 line 443 / 516 / 538）补 `parsedError` 实参；成功路径（538）传 `""`。

## 错误处理

- `Classify` 对空 `parsedError` + 429 → `ClassTransient`（最保守的短冷却，符合"宁可短冷却多探活，不误关一天"）。
- `Decide` 对未知 class → `ClassNone`（不冷却），防御性。
- Retry-After 解析失败（`parseRetryAfter` 返回 0）→ 各 class 走默认时长。

## 测试

新建 `internal/failover/cooldown_test.go`（纯函数，表驱动）：
1. **Classify**：喂入 OpenAI / Groq / Gemini / OpenRouter 真实 429 文本样本（per-minute、per-day、token-per-day）、402 body、500、403，断言 class。
2. **Decide**：
   - Transient：有/无 Retry-After、Retry-After 超 TransientMax 被 clamp、不计升级。
   - Exhausted：有 Retry-After 用真值、无则 6h、不计升级。
   - Payment：恒 24h。
   - ServerError：hitCount 0→4 的升级序列 30s/1m/2m/4m/5min（第 5 档 8m 被 ServerMax cap 到 5min）、计升级。
3. 回归：`router_engine/selector_test.go`（cooldown 相关）、subgroup 现有测试随签名更新。

## 验收标准

- 单测全绿（`go test ./internal/failover/ ./internal/router_engine/ ./internal/services/`）。
- `go build ./...` 通过。
- 手动验证（或集成测试桩）：模拟上游返回 per-day 429 → 该 (group,model)/sub-group 冷却 ≥6h；per-minute 429 → ≤90s；402 → 24h；连续 5xx → 时长递增。

## 影响面

改动文件：`internal/failover/cooldown.go`(新) + `cooldown_test.go`(新)、`internal/router_engine/selector.go`、`internal/services/subgroup_manager.go`、`internal/proxy/server.go`。无 DB schema 变更，无 API 变更，无前端改动。可独立上线。
