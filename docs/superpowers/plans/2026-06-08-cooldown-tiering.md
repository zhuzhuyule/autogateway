# 冷却分级（Cooldown Tiering）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 AutoGateway 的两套冷却机制按错误性质分级——瞬时限流短冷却、日额耗尽长隔离、402 余额不足 24h、5xx 升级阶梯——共享同一个纯函数决策器。

**Architecture:** 新建 `internal/failover/cooldown.go` 纯函数决策器（`Classify` + `CooldownPolicy.Decide`）。`router_engine.Selector`（机制1，alias/auto 路由）和 `services.SubGroupManager`（机制2，聚合路由）各自的冷却写入点改调它。proxy 层把已解析的 `parsedError` 和 `Retry-After` 透传给两个写入点。无 DB/API/前端改动。

**Tech Stack:** Go 1.25、标准库（`strings`/`time`/`math/rand`）、`go test` 表驱动测试。

**Spec:** `docs/superpowers/specs/2026-06-08-cooldown-tiering-design.md`

---

## File Structure

- **Create** `internal/failover/cooldown.go` — 纯函数冷却决策器：`CooldownClass`、`CooldownPolicy`、`DefaultCooldownPolicy`、`Classify`、`(CooldownPolicy).Decide`。无状态、无副作用。
- **Create** `internal/failover/cooldown_test.go` — 表驱动单测，覆盖 `Classify` 各类输入与 `Decide` 各 class 时长/升级。
- **Modify** `internal/router_engine/selector.go` — `Selector` 加 `policy` 字段；`cooldownStore` 加 `apply` 方法替代 `bump`；`MarkResponse` 签名加 `parsedError`/`retryAfter`。
- **Modify** `internal/services/subgroup_manager.go` — `selector` 加 `policy` 字段；`recordResult` 与 `RecordSubGroupResult` 签名加 `parsedError`；冷却分支改调决策器；删除被取代的常量。
- **Modify** `internal/proxy/server.go` — `markRoutingCandidate` 签名扩展；三处 `RecordSubGroupResult` 调用补参。

---

## Task 1: 冷却决策器核心（Classify + Decide）

**Files:**
- Create: `internal/failover/cooldown.go`
- Test: `internal/failover/cooldown_test.go`

- [ ] **Step 1: 写失败测试 — Classify**

创建 `internal/failover/cooldown_test.go`：

```go
package failover

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		parsedErr  string
		want       CooldownClass
	}{
		{"openai per-minute 429", 429, "Rate limit reached for gpt-4 in organization org-x on requests per min (RPM): Limit 500", ClassTransient},
		{"groq per-day 429", 429, "Rate limit reached for model llama-3.3-70b ... requests per day (RPD) Limit 1000", ClassExhausted},
		{"gemini tokens-per-day", 429, "Resource has been exhausted: tokens per day quota", ClassExhausted},
		{"429 no detail", 429, "Too Many Requests", ClassTransient},
		{"402 payment", 402, "", ClassPayment},
		{"429 but billing", 429, "You exceeded your current quota, please check your plan and billing details", ClassPayment},
		{"insufficient balance via 400", 400, "Account balance is insufficient", ClassPayment},
		{"500 server", 500, "internal error", ClassServerError},
		{"502 server", 502, "bad gateway", ClassServerError},
		{"network (0)", 0, "context deadline exceeded", ClassServerError},
		{"403 forbidden", 403, "invalid api key", ClassNone},
		{"401 unauth", 401, "unauthorized", ClassNone},
		{"400 bad request", 400, "invalid 'messages'", ClassNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.statusCode, c.parsedErr); got != c.want {
				t.Fatalf("Classify(%d, %q) = %v, want %v", c.statusCode, c.parsedErr, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/failover/ -run TestClassify -v`
Expected: 编译失败（`undefined: Classify` / `undefined: CooldownClass` 等）。

- [ ] **Step 3: 写 cooldown.go 的类型与 Classify**

创建 `internal/failover/cooldown.go`：

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

// Classify 从 HTTP 状态码 + 上游错误文本推断冷却性质。纯函数。
// 约定：statusCode == 0 表示网络错误（无 HTTP 响应）。
func Classify(statusCode int, parsedError string) CooldownClass {
	low := strings.ToLower(parsedError)

	// 计费/额度类优先（部分 provider 用 429/400 表达余额耗尽）
	if statusCode == 402 ||
		strings.Contains(low, "insufficient") ||
		strings.Contains(low, "payment") ||
		strings.Contains(low, "billing") ||
		strings.Contains(low, "exceeded your current quota") {
		return ClassPayment
	}

	if statusCode == 429 {
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
		return ClassTransient
	}

	if statusCode >= 500 || statusCode == 0 {
		return ClassServerError
	}

	return ClassNone
}
```

- [ ] **Step 4: 运行测试确认 Classify 通过**

Run: `go test ./internal/failover/ -run TestClassify -v`
Expected: PASS（全部子用例）。

- [ ] **Step 5: 写失败测试 — Decide**

向 `cooldown_test.go` 追加：

```go
func TestDecide(t *testing.T) {
	p := DefaultCooldownPolicy()

	t.Run("transient default no retry-after", func(t *testing.T) {
		d, counts := p.Decide(ClassTransient, 0, 0)
		if d != 90*time.Second || counts {
			t.Fatalf("got (%v,%v), want (90s,false)", d, counts)
		}
	})
	t.Run("transient honors retry-after", func(t *testing.T) {
		d, _ := p.Decide(ClassTransient, 45*time.Second, 0)
		if d != 45*time.Second {
			t.Fatalf("got %v, want 45s", d)
		}
	})
	t.Run("transient clamps retry-after to max", func(t *testing.T) {
		d, _ := p.Decide(ClassTransient, 30*time.Minute, 0)
		if d != 10*time.Minute {
			t.Fatalf("got %v, want 10m", d)
		}
	})
	t.Run("exhausted default", func(t *testing.T) {
		d, counts := p.Decide(ClassExhausted, 0, 0)
		if d != 6*time.Hour || counts {
			t.Fatalf("got (%v,%v), want (6h,false)", d, counts)
		}
	})
	t.Run("exhausted honors retry-after", func(t *testing.T) {
		d, _ := p.Decide(ClassExhausted, 2*time.Hour, 0)
		if d != 2*time.Hour {
			t.Fatalf("got %v, want 2h", d)
		}
	})
	t.Run("payment fixed 24h", func(t *testing.T) {
		d, counts := p.Decide(ClassPayment, 0, 0)
		if d != 24*time.Hour || counts {
			t.Fatalf("got (%v,%v), want (24h,false)", d, counts)
		}
	})
	t.Run("server error escalation ladder", func(t *testing.T) {
		want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
		for hit, w := range want {
			d, counts := p.Decide(ClassServerError, 0, hit)
			if d != w || !counts {
				t.Fatalf("hit=%d got (%v,%v), want (%v,true)", hit, d, counts, w)
			}
		}
	})
	t.Run("none yields zero", func(t *testing.T) {
		d, counts := p.Decide(ClassNone, 0, 0)
		if d != 0 || counts {
			t.Fatalf("got (%v,%v), want (0,false)", d, counts)
		}
	})
}
```

- [ ] **Step 6: 运行测试确认失败**

Run: `go test ./internal/failover/ -run TestDecide -v`
Expected: 编译失败（`undefined: DefaultCooldownPolicy` / `Decide`）。

- [ ] **Step 7: 写 CooldownPolicy + DefaultCooldownPolicy + Decide**

向 `internal/failover/cooldown.go` 追加：

```go
// CooldownPolicy 各类冷却的时长参数。用 DefaultCooldownPolicy 构造。
type CooldownPolicy struct {
	TransientDefault time.Duration // 无 Retry-After 时的瞬时冷却
	TransientMax     time.Duration // Retry-After 上限 clamp
	ExhaustedDefault time.Duration // 无 Retry-After 时的耗尽冷却
	PaymentCooldown  time.Duration // 402 冷却
	ServerBase       time.Duration // 5xx 升级阶梯基数
	ServerMax        time.Duration // 5xx 升级阶梯上限
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

// Decide 返回本次冷却时长，以及该次是否计入升级计数（hitCount）。
// hitCount 仅 ClassServerError 使用，做指数升级；其余 class 时长由错误性质直接决定。
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
			return retryAfter, false
		}
		return p.ExhaustedDefault, false
	case ClassPayment:
		return p.PaymentCooldown, false
	case ClassServerError:
		shift := hitCount
		if shift > 4 {
			shift = 4
		}
		d := p.ServerBase << uint(shift)
		if d > p.ServerMax {
			d = p.ServerMax
		}
		return d, true
	default:
		return 0, false
	}
}
```

- [ ] **Step 8: 运行整个包测试确认通过**

Run: `go test ./internal/failover/ -v`
Expected: PASS（`TestClassify` + `TestDecide` + 既有 `status_code_matcher` 测试）。

- [ ] **Step 9: Commit**

```bash
git add internal/failover/cooldown.go internal/failover/cooldown_test.go
git commit -m "✨ feat(failover): 冷却分级纯函数决策器 Classify+Decide (#5-7)"
```

---

## Task 2: 机制1 接入（router_engine.Selector）

**Files:**
- Modify: `internal/router_engine/selector.go`（`Selector` 结构体 ~83-89、`NewSelector` ~91-100、`MarkResponse` ~263-273、`cooldownStore.bump` ~537-550）
- Test: `internal/router_engine/selector_test.go`

- [ ] **Step 1: 写失败测试 — MarkResponse 按 class 分级**

向 `internal/router_engine/selector_test.go` 追加（使用内存即可，cooldownStore 是进程内）：

```go
func TestMarkResponseTiering(t *testing.T) {
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
	}
	c := Candidate{GroupID: 1, RealModel: "m"}
	key := "1:m"
	now := time.Now()

	// per-day 429 → 长冷却（>1h）
	s.MarkResponse(c, 429, "requests per day limit reached", 0)
	if !s.cooldown.isCooling(key, now.Add(time.Hour)) {
		t.Fatal("per-day 429 should cool >1h")
	}
	// 成功 → reset
	s.MarkResponse(c, 200, "", 0)
	if s.cooldown.isCooling(key, now) {
		t.Fatal("200 should reset cooldown")
	}
	// 403 → ClassNone，不冷却
	s.MarkResponse(c, 403, "invalid api key", 0)
	if s.cooldown.isCooling(key, now) {
		t.Fatal("403 should not set model-level cooldown")
	}
}
```

在该测试文件 import 块补 `"autogateway/internal/failover"` 与 `"time"`（若缺）。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/router_engine/ -run TestMarkResponseTiering -v`
Expected: 编译失败（`Selector` 无 `policy` 字段 / `MarkResponse` 参数不匹配）。

- [ ] **Step 3: Selector 加 policy 字段 + import**

在 `internal/router_engine/selector.go` import 块加 `"autogateway/internal/failover"`。

`Selector` 结构体（约 83-89）由：

```go
type Selector struct {
	db        *gorm.DB
	cooldown  *cooldownStore
	swrrState *swrrStateMap
	settings  Settings
	mu        sync.RWMutex
}
```

改为追加 `policy` 字段：

```go
type Selector struct {
	db        *gorm.DB
	cooldown  *cooldownStore
	swrrState *swrrStateMap
	settings  Settings
	policy    failover.CooldownPolicy
	mu        sync.RWMutex
}
```

`NewSelector`（约 91-100）的结构体字面量追加 `policy: failover.DefaultCooldownPolicy(),`：

```go
func NewSelector(db *gorm.DB) *Selector {
	s := &Selector{
		db:        db,
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
	}
	s.loadSettingsFromDB()
	return s
}
```

- [ ] **Step 4: cooldownStore 加 apply 方法**

在 `internal/router_engine/selector.go` 的 `cooldownStore` 方法区（`bump` 附近，约 537）新增 `apply`，并删除旧 `bump`：

```go
// apply 按冷却性质设置冷却窗口；仅 ServerError 用 failures 做升级。
// dur<=0（ClassNone）不设置冷却。
func (c *cooldownStore) apply(key string, class failover.CooldownClass, retryAfter time.Duration, policy failover.CooldownPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.data[key]
	dur, counts := policy.Decide(class, retryAfter, e.failures)
	if dur <= 0 {
		return
	}
	if counts {
		e.failures++
	}
	jit := time.Duration(rand.Int63n(int64(dur)/5 + 1))
	e.until = time.Now().Add(dur + jit)
	c.data[key] = e
}
```

> 注：删除原 `func (c *cooldownStore) bump(key string)`（约 535-550）。`reset` 与 `isCooling` 保留不变。`min` 辅助函数若仅被 `bump` 使用则一并删除（编译器会报 unused 时再删）。

- [ ] **Step 5: 改 MarkResponse 签名与实现**

`MarkResponse`（约 263-273）由：

```go
func (s *Selector) MarkResponse(c Candidate, status int) {
	if c.GroupID == 0 || c.RealModel == "" {
		return
	}
	key := fmt.Sprintf("%d:%s", c.GroupID, c.RealModel)
	if status >= 200 && status < 400 {
		s.cooldown.reset(key)
	} else if status >= 400 {
		s.cooldown.bump(key)
	}
}
```

改为：

```go
// MarkResponse 把上游响应反馈进冷却。parsedError 为上游错误文本（用于分类），
// retryAfter 为 Retry-After 头解析值（0 表示无）。2xx/3xx 清除冷却。
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
	s.cooldown.apply(key, class, retryAfter, s.policy)
}
```

- [ ] **Step 6: 运行测试确认通过**

Run: `go test ./internal/router_engine/ -v`
Expected: PASS。若 `selector_test.go` 既有用例调用了旧 `MarkResponse(c, status)`，按新签名补 `, "", 0`。

- [ ] **Step 7: Commit**

```bash
git add internal/router_engine/selector.go internal/router_engine/selector_test.go
git commit -m "✨ feat(router_engine): MarkResponse 接入冷却分级决策器"
```

---

## Task 3: 机制2 接入（services.SubGroupManager）

**Files:**
- Modify: `internal/services/subgroup_manager.go`（常量 ~17-24、`selector` 结构体、`NewSubGroupManager`/selector 构造处、`RecordSubGroupResult` ~165-176、`recordResult` ~458-510）

- [ ] **Step 1: selector 结构体加 policy 字段**

在 `internal/services/subgroup_manager.go` import 块加 `"autogateway/internal/failover"`。

找到内部 `type selector struct {...}` 定义（聚合路由的 selector，非 router_engine 的），追加字段：

```go
	policy failover.CooldownPolicy
```

在每处构造该 `selector` 的位置（`SubGroupManager` 懒加载 selector 处，通常在 `getOrCreateSelector` 或 `SelectSubGroup` 内 `&selector{...}` 字面量）追加 `policy: failover.DefaultCooldownPolicy(),`。

> 实现提示：用 `grep -n "&selector{" internal/services/subgroup_manager.go` 定位所有构造点，逐一补字段。

- [ ] **Step 2: 改 RecordSubGroupResult 签名透传 parsedError**

`RecordSubGroupResult`（165-176）签名与末行调用改为带 `parsedError`：

```go
func (m *SubGroupManager) RecordSubGroupResult(aggregateGroupID uint, subGroupName string, success bool, statusCode int, parsedError string, retryAfter time.Duration) {
	if subGroupName == "" {
		return
	}
	m.mu.RLock()
	sel, ok := m.selectors[aggregateGroupID]
	m.mu.RUnlock()
	if !ok || sel == nil {
		return
	}
	sel.recordResult(subGroupName, success, statusCode, parsedError, retryAfter)
}
```

- [ ] **Step 3: 改 recordResult 用决策器**

`recordResult`（约 458-510）的失败分支由现有「`if statusCode == 429 {...} else {5xx 连续失败}`」替换为统一决策。成功分支（清 `consecutiveFailures` + `cooldownUntil`）保持不变。失败分支改为：

```go
	// 失败：分类决定冷却
	class := failover.Classify(statusCode, parsedError)
	if class == failover.ClassNone {
		return // 400/401/403 不进 sub-group 冷却
	}
	it.consecutiveFailures++
	dur, _ := s.policy.Decide(class, retryAfter, it.consecutiveFailures-1)
	if dur > 0 {
		it.cooldownUntil = time.Now().Add(dur)
	}
	logrus.WithFields(logrus.Fields{
		"sub_group":        name,
		"class":            int(class),
		"status_code":      statusCode,
		"cooldown_seconds": int(dur.Seconds()),
	}).Debug("sub-group cooldown applied")
```

完整新 `recordResult` 签名：

```go
func (s *selector) recordResult(name string, success bool, statusCode int, parsedError string, retryAfter time.Duration) {
```

- [ ] **Step 4: 删除被取代的常量**

删除（17-24 区）`rateLimitDefaultCooldown`、`rateLimitMaxCooldown`、`subGroupBreakerCooldown` 三个常量（已由 `CooldownPolicy` 取代）。保留 `subGroupBreakerThreshold`（仍用于 selectByWeight 跳过判定，若有引用）。

> 实现提示：`grep -n "rateLimitDefaultCooldown\|rateLimitMaxCooldown\|subGroupBreakerCooldown" internal/services/subgroup_manager.go` 确认无残留引用后再删。

- [ ] **Step 5: 编译（调用点未改，预期 proxy 报错）**

Run: `go build ./internal/services/...`
Expected: `internal/services` 自身 PASS。`go build ./...` 会在 `internal/proxy` 报 `RecordSubGroupResult` 参数不匹配 —— Task 4 修复。

- [ ] **Step 6: Commit**

```bash
git add internal/services/subgroup_manager.go
git commit -m "✨ feat(subgroup): recordResult 接入冷却分级决策器"
```

---

## Task 4: proxy 调用点透传 parsedError

**Files:**
- Modify: `internal/proxy/server.go`（`markRoutingCandidate` ~563-576、三处 `RecordSubGroupResult` 调用 ~443/516/538、四处 `markRoutingCandidate` 调用 ~299/422/533）

- [ ] **Step 1: 改 markRoutingCandidate 签名**

`markRoutingCandidate`（563-576）由：

```go
func (ps *ProxyServer) markRoutingCandidate(c *gin.Context, statusCode int) {
	if ps.selector == nil {
		return
	}
	raw, ok := c.Get("router_engine.candidate")
	if !ok {
		return
	}
	candidate, ok := raw.(*router_engine.Candidate)
	if !ok || candidate == nil {
		return
	}
	ps.selector.MarkResponse(*candidate, statusCode)
}
```

改为：

```go
func (ps *ProxyServer) markRoutingCandidate(c *gin.Context, statusCode int, parsedError string, retryAfter time.Duration) {
	if ps.selector == nil {
		return
	}
	raw, ok := c.Get("router_engine.candidate")
	if !ok {
		return
	}
	candidate, ok := raw.(*router_engine.Candidate)
	if !ok || candidate == nil {
		return
	}
	ps.selector.MarkResponse(*candidate, statusCode, parsedError, retryAfter)
}
```

- [ ] **Step 2: 更新 markRoutingCandidate 的三处调用**

- 约 line 299（SelectKey 失败，无上游响应）：
  `ps.markRoutingCandidate(c, http.StatusServiceUnavailable)` → `ps.markRoutingCandidate(c, http.StatusServiceUnavailable, "", 0)`
- 约 line 422（HTTP/network 失败分支，此处已有 `parsedError` 变量；`resp` 可能非 nil）：
  `ps.markRoutingCandidate(c, statusCode)` →
  ```go
  var raCand time.Duration
  if resp != nil {
  	raCand = parseRetryAfter(resp.Header)
  }
  ps.markRoutingCandidate(c, statusCode, parsedError, raCand)
  ```
- 约 line 533（成功分支）：
  `ps.markRoutingCandidate(c, resp.StatusCode)` → `ps.markRoutingCandidate(c, resp.StatusCode, "", 0)`

- [ ] **Step 3: 更新三处 RecordSubGroupResult 调用补 parsedError**

- 约 line 443（failover 路径，已有 `parsedError`、`retryAfter`）：
  `ps.subGroupManager.RecordSubGroupResult(originalGroup.ID, group.Name, false, statusCode, retryAfter)` →
  `ps.subGroupManager.RecordSubGroupResult(originalGroup.ID, group.Name, false, statusCode, parsedError, retryAfter)`
- 约 line 516（最后一次失败，已有 `parsedError`、局部 `retryAfter`）：
  同上补 `parsedError` 实参（第 5 位）。
- 约 line 538（成功分支）：
  `ps.subGroupManager.RecordSubGroupResult(originalGroup.ID, group.Name, true, resp.StatusCode, 0)` →
  `ps.subGroupManager.RecordSubGroupResult(originalGroup.ID, group.Name, true, resp.StatusCode, "", 0)`

- [ ] **Step 4: 全量编译**

Run: `go build ./...`
Expected: PASS（无参数不匹配）。

- [ ] **Step 5: 全量测试**

Run: `go test ./internal/failover/ ./internal/router_engine/ ./internal/services/ ./internal/proxy/`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/server.go
git commit -m "✨ feat(proxy): 透传 parsedError/Retry-After 给冷却分级"
```

---

## Task 5: 收尾验证

- [ ] **Step 1: go vet + 全量测试**

Run: `go vet ./... && go test ./...`
Expected: 无 vet 报错；测试全绿。

- [ ] **Step 2: 确认 spec 验收标准**

对照 spec「验收标准」逐条核对：per-day 429 → ≥6h；per-minute 429 → ≤90s；402 → 24h；连续 5xx → 递增。这些已由 Task 1 的 `TestDecide` + Task 2 的 `TestMarkResponseTiering` 覆盖。

- [ ] **Step 3: 合并/PR**

按 finishing-a-development-branch 决定 merge 或 PR（分支 `feat/cooldown-tiering`）。

---

## Self-Review 记录

- **Spec coverage**：#5 升级阶梯→Task1 ServerError ladder；#6 瞬时vs耗尽→Task1 Classify+Decide；#7 402→Task1 ClassPayment；两套机制一致→Task2/3 共用决策器；proxy 透传→Task4。✅ 全覆盖。
- **Placeholder scan**：无 TBD/TODO；所有代码步骤含完整代码。涉及行号处标注「约」并给 grep 定位法（行号随上游改动会漂移，故以符号+grep 为准）。✅
- **Type consistency**：`MarkResponse(c, status, parsedError, retryAfter)`、`RecordSubGroupResult(..., parsedError, retryAfter)`、`recordResult(name, success, statusCode, parsedError, retryAfter)`、`apply(key, class, retryAfter, policy)`、`Decide(class, retryAfter, hitCount)`、`Classify(statusCode, parsedError)` 在各 Task 间签名一致。✅
