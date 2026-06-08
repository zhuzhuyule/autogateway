# 采样路由（Sampling Router）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** tier 内候选选择从静态 SWRR 升级为「registry 先验 × 实时后验」加权采样——`有效权重 = 基础权重 × priorWeight(performanceLevel+latency) × Thompson采样(实时可靠性) × 实时速度修正`，激活沉睡的 FreeModels registry 数据，reliability/speed 零人工维护。

**Architecture:** 纯函数采样层（Beta/Gamma 采样 + 先验/速度映射）→ Selector 持有 stats(内存) + MetaProvider(registry 适配) → MarkResponse 记录成功/失败/延迟 → swrr 用 effectiveWeight 选择。护栏复用 cooldown+账本，分档复用 tier。默认无 registry/无样本时退化为接近原 SWRR。

**Tech Stack:** Go 1.25、`math/rand`（Beta/Gamma 采样）、gin、FreeModelsRegistry。

**Spec:** `docs/superpowers/specs/2026-06-08-sampling-router-design.md`

---

## File Structure

- **Create** `internal/router_engine/sampling.go` + `sampling_test.go` — 纯函数：`sampleBeta`/`sampleGamma`/`perfScore`/`latencyScore`/`speedFactor`/`priorWeight` + 类型 `ModelMeta`/`MetaProvider`。
- **Modify** `internal/router_engine/selector.go` — `Selector` 加 `stats`/`statsMu`/`meta` 字段；`NewSelector` 加 meta 参数；`recordStat`；`MarkResponse` 加 latency；`swrr` 用 effectiveWeight。
- **Modify** `internal/services/freemodels_registry.go` — 加 `LookupMeta` 适配。
- **Modify** `internal/container/container.go` — 注册接线（registry adapter → Selector）。
- **Modify** `internal/proxy/server.go` — `markRoutingCandidate` 传 latency。

---

## Task 1: 采样核心纯函数

**Files:**
- Create: `internal/router_engine/sampling.go`
- Test: `internal/router_engine/sampling_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/router_engine/sampling_test.go`（`package router_engine`）：

```go
package router_engine

import (
	"math"
	"testing"
)

func avgBeta(a, b float64, n int) float64 {
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += sampleBeta(a, b)
	}
	return sum / float64(n)
}

func TestSampleBetaMeans(t *testing.T) {
	if m := avgBeta(100, 1, 5000); m < 0.95 {
		t.Fatalf("Beta(100,1) mean=%.3f, want >0.95", m)
	}
	if m := avgBeta(1, 100, 5000); m > 0.05 {
		t.Fatalf("Beta(1,100) mean=%.3f, want <0.05", m)
	}
	if m := avgBeta(1, 1, 5000); math.Abs(m-0.5) > 0.05 {
		t.Fatalf("Beta(1,1) mean=%.3f, want ~0.5", m)
	}
}

func TestSampleBetaRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		v := sampleBeta(2, 5)
		if v < 0 || v > 1 {
			t.Fatalf("sampleBeta out of [0,1]: %v", v)
		}
	}
}

func TestPriorScores(t *testing.T) {
	if perfScore("high") != 1.0 || perfScore("entry") != 0.7 || perfScore("") != 0.85 {
		t.Fatal("perfScore mapping wrong")
	}
	if latencyScore("< 1s") != 1.0 || latencyScore("3-10s") != 0.7 || latencyScore("") != 0.9 {
		t.Fatal("latencyScore mapping wrong")
	}
}

func TestPriorWeightNilMeta(t *testing.T) {
	if w := priorWeight(nil); w != 1.0 {
		t.Fatalf("priorWeight(nil)=%v, want 1.0", w)
	}
	w := priorWeight(&ModelMeta{PerformanceLevel: "high", EstimatedLatency: "< 1s"})
	if w != 1.0 {
		t.Fatalf("priorWeight(high,<1s)=%v, want 1.0", w)
	}
}

func TestSpeedFactor(t *testing.T) {
	if speedFactor(0) != 1.0 {
		t.Fatal("no-sample speedFactor should be 1.0")
	}
	fast, slow := speedFactor(500), speedFactor(8000)
	if !(fast > slow) {
		t.Fatalf("faster should score higher: fast=%v slow=%v", fast, slow)
	}
	if slow < 0.5 {
		t.Fatalf("speedFactor should clamp >=0.5, got %v", slow)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/router_engine/ -run "TestSampleBeta|TestPrior|TestSpeedFactor" -v`
Expected: 编译失败（函数/类型未定义）。

- [ ] **Step 3: 实现 sampling.go**

创建 `internal/router_engine/sampling.go`：

```go
package router_engine

import (
	"math"
	"math/rand"
)

// ModelMeta 是采样需要的先验子集（来自 FreeModels registry）。
type ModelMeta struct {
	PerformanceLevel string // "entry" | "mid" | "high" | ""
	EstimatedLatency string // "< 1s" | "1-3s" | "3-10s" | ""
}

// MetaProvider 提供模型静态先验元数据。由 services.FreeModelsRegistry 适配实现。
type MetaProvider interface {
	LookupMeta(modelID string) *ModelMeta
}

func perfScore(level string) float64 {
	switch level {
	case "high":
		return 1.0
	case "mid":
		return 0.85
	case "entry":
		return 0.7
	default:
		return 0.85
	}
}

func latencyScore(l string) float64 {
	switch l {
	case "< 1s":
		return 1.0
	case "1-3s":
		return 0.85
	case "3-10s":
		return 0.7
	default:
		return 0.9
	}
}

// priorWeight = perfScore × latencyScore；meta 为 nil → 1.0（中性）。
func priorWeight(m *ModelMeta) float64 {
	if m == nil {
		return 1.0
	}
	return perfScore(m.PerformanceLevel) * latencyScore(m.EstimatedLatency)
}

// speedFactor 把实时延迟 EWMA(ms) 映射成 [0.5,1.0] 乘子；0(无样本) → 1.0。
func speedFactor(ewmaMs float64) float64 {
	if ewmaMs <= 0 {
		return 1.0
	}
	// 饱和衰减：~0.5s→~0.95, ~3s→~0.7, ~8s→~0.5。clamp [0.5,1.0]。
	f := 1.0 - 0.5*(1.0-math.Exp(-ewmaMs/4000.0))
	if f < 0.5 {
		return 0.5
	}
	if f > 1.0 {
		return 1.0
	}
	return f
}

// sampleGamma 从 Gamma(k,1) 采样（Marsaglia-Tsang）。k>=1 主路径；k<1 用 boosting。
func sampleGamma(k float64) float64 {
	if k < 1 {
		// Johnk/boosting：Gamma(k) = Gamma(k+1) * U^(1/k)
		u := rand.Float64()
		return sampleGamma(k+1) * math.Pow(u, 1.0/k)
	}
	d := k - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)
	for {
		x := rand.NormFloat64()
		v := 1.0 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rand.Float64()
		if u < 1.0-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1.0-v+math.Log(v)) {
			return d * v
		}
	}
}

// sampleBeta 从 Beta(alpha,beta) 采样：X/(X+Y), X~Gamma(a), Y~Gamma(b)。
func sampleBeta(alpha, beta float64) float64 {
	x := sampleGamma(alpha)
	y := sampleGamma(beta)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/router_engine/ -run "TestSampleBeta|TestPrior|TestSpeedFactor" -v`
Expected: PASS（统计均值类断言用了大样本 + 容差）。

- [ ] **Step 5: Commit**

```bash
git add internal/router_engine/sampling.go internal/router_engine/sampling_test.go
git commit -m "✨ feat(router_engine): 采样核心纯函数 (Beta/Gamma 采样 + 先验/速度映射)"
```

---

## Task 2: Selector stats + meta + 接线

**Files:**
- Modify: `internal/router_engine/selector.go`（Selector struct、NewSelector、新增 recordStat）
- Modify: `internal/services/freemodels_registry.go`（LookupMeta 适配）
- Modify: `internal/container/container.go`（注册接线）
- Test: `internal/router_engine/selector_test.go`

- [ ] **Step 1: 写失败测试**

向 `selector_test.go` 追加：

```go
func TestRecordStat(t *testing.T) {
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		stats:     make(map[string]*candidateStat),
	}
	c := Candidate{GroupID: 1, RealModel: "m"}
	s.recordStat(c, true, 1000)
	s.recordStat(c, true, 2000)
	s.recordStat(c, false, 0)
	st := s.stats["1:m"]
	if st == nil || st.success != 2 || st.fail != 1 {
		t.Fatalf("stat = %+v, want success=2 fail=1", st)
	}
	if st.latencyEWMA <= 0 {
		t.Fatal("latencyEWMA should be set after success")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/router_engine/ -run TestRecordStat -v`
Expected: 编译失败（`stats` 字段 / `candidateStat` / `recordStat` 未定义）。

- [ ] **Step 3: Selector 加字段 + candidateStat + recordStat**

在 `selector.go`：`Selector` struct 加（在 store 之后）：
```go
	meta     MetaProvider
	stats    map[string]*candidateStat
	statsMu  sync.Mutex
```
新增类型与方法：
```go
type candidateStat struct {
	success     int64
	fail        int64
	latencyEWMA float64 // ms, 0 = 无样本
}

const statRollThreshold = 200 // success+fail 超此值整体减半，保留近期占比（指数衰减近似）

func (s *Selector) statKey(c Candidate) string {
	return fmt.Sprintf("%d:%s", c.GroupID, c.RealModel)
}

func (s *Selector) recordStat(c Candidate, success bool, latency time.Duration) {
	if c.GroupID == 0 || c.RealModel == "" {
		return
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.stats == nil {
		s.stats = make(map[string]*candidateStat)
	}
	k := s.statKey(c)
	st := s.stats[k]
	if st == nil {
		st = &candidateStat{}
		s.stats[k] = st
	}
	if success {
		st.success++
		ms := float64(latency.Milliseconds())
		if ms > 0 {
			if st.latencyEWMA == 0 {
				st.latencyEWMA = ms
			} else {
				st.latencyEWMA = 0.3*ms + 0.7*st.latencyEWMA // α=0.3，复用 SubGroupManager 同款
			}
		}
	} else {
		st.fail++
	}
	if st.success+st.fail > statRollThreshold { // 滚动减半，近期数据主导
		st.success /= 2
		st.fail /= 2
	}
}

// sampleEffectiveWeight 计算候选的有效权重（先验 × 后验采样 × 速度），最低 1。
func (s *Selector) sampleEffectiveWeight(c Candidate) int {
	prior := 1.0
	if s.meta != nil {
		prior = priorWeight(s.meta.LookupMeta(c.RealModel))
	}
	s.statsMu.Lock()
	st := s.stats[s.statKey(c)]
	var succ, fail, ewma float64
	if st != nil {
		succ, fail, ewma = float64(st.success), float64(st.fail), st.latencyEWMA
	}
	s.statsMu.Unlock()
	rel := sampleBeta(succ+1, fail+1)
	spd := speedFactor(ewma)
	w := float64(c.Weight) * prior * rel * spd
	iw := int(w + 0.5)
	if iw < 1 {
		return 1
	}
	return iw
}
```
确认 `selector.go` 已 import `"sync"`、`"fmt"`、`"time"`（应有）。

- [ ] **Step 4: NewSelector 加 meta 参数 + 初始化 stats**

`NewSelector` 签名改为 `func NewSelector(db *gorm.DB, st store.Store, meta MetaProvider) *Selector`，字面量加 `meta: meta,` 和 `stats: make(map[string]*candidateStat),`。

- [ ] **Step 5: registry 加 LookupMeta 适配**

在 `internal/services/freemodels_registry.go` 加方法，把内部 `byModelOnly` 首个映射成 `router_engine.ModelMeta`。**注意依赖方向**：services 不应反向 import router_engine。两种解法择一（实现者判断哪个不引入循环）：
- (a) 在 registry 加 `LookupMetaRaw(modelID string) (perfLevel, estLatency string, found bool)`（返回基础类型，无跨包类型依赖），由 container 里的 adapter 包成 `router_engine.ModelMeta`。
- (b) 若 router_engine 已被 services 间接依赖不通，则用 (a)。

推荐 (a)：
```go
// LookupMetaRaw 返回模型先验档位（performanceLevel, estimatedLatency）。查不到 found=false。
func (r *FreeModelsRegistry) LookupMetaRaw(modelID string) (perfLevel, estLatency string, found bool) {
	r.mu.RLock(); defer r.mu.RUnlock()
	list := r.byModelOnly[strings.ToLower(modelID)]
	if len(list) == 0 { return "", "", false }
	m := list[0]
	return m.PerformanceLevel, m.EstimatedLatency, true
}
```
（按 registry 实际锁字段名/index 名调整。)

- [ ] **Step 6: container 接线 adapter**

在 `internal/container/container.go` 注册 Selector 处，构造一个实现 `router_engine.MetaProvider` 的 adapter（thin struct 包 `*services.FreeModelsRegistry`），传给 `NewSelector(db, st, adapter)`。adapter 定义在 container 或 router_engine：
```go
type registryMetaAdapter struct{ reg *services.FreeModelsRegistry }
func (a registryMetaAdapter) LookupMeta(modelID string) *router_engine.ModelMeta {
	if a.reg == nil { return nil }
	p, l, ok := a.reg.LookupMetaRaw(modelID)
	if !ok { return nil }
	return &router_engine.ModelMeta{PerformanceLevel: p, EstimatedLatency: l}
}
```
注册：`return router_engine.NewSelector(db, st, registryMetaAdapter{reg: reg})`（reg 从 dig 拿，确认 FreeModelsRegistry 已注册）。

- [ ] **Step 7: 修全部 NewSelector 调用**

`grep -rn "NewSelector(" internal/`，非测试调用补 adapter；测试调用补 `nil`（第三参）。

- [ ] **Step 8: 构建 + 测试**

Run: `go build ./... && go test ./internal/router_engine/ -run TestRecordStat -v`
Expected: 通过。`go build ./...` 全绿（注意 MarkResponse 还没改，Task 3 改；本 Task 不动 MarkResponse/swrr）。

- [ ] **Step 9: Commit**

```bash
git add internal/router_engine/selector.go internal/services/freemodels_registry.go internal/container/container.go internal/router_engine/selector_test.go
git commit -m "✨ feat(router_engine): Selector 加 stats/meta + recordStat + registry 先验接线"
```

---

## Task 3: MarkResponse 加 latency + swrr 融合

**Files:**
- Modify: `internal/router_engine/selector.go`（`MarkResponse`、`swrr`）
- Modify: `internal/proxy/server.go`（`markRoutingCandidate` + 调用点）
- Test: `internal/router_engine/selector_test.go`

- [ ] **Step 1: 写失败测试（swrr 偏向高可靠候选）**

向 `selector_test.go` 追加：

```go
func TestSwrrFavorsReliable(t *testing.T) {
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		stats:     make(map[string]*candidateStat),
	}
	good := Candidate{GroupID: 1, RealModel: "good", Weight: 1, AliasID: 1}
	bad := Candidate{GroupID: 2, RealModel: "bad", Weight: 1, AliasID: 2}
	// good 全成功，bad 全失败
	for i := 0; i < 50; i++ {
		s.recordStat(good, true, 500)
		s.recordStat(bad, false, 0)
	}
	cands := []Candidate{good, bad}
	goodCount := 0
	for i := 0; i < 200; i++ {
		picked := s.swrr("k", cands)
		if picked.RealModel == "good" {
			goodCount++
		}
	}
	if goodCount < 120 { // 应显著偏向 good（>60%），但 bad 仍偶有探索
		t.Fatalf("good picked %d/200, want >120 (favor reliable)", goodCount)
	}
	if goodCount == 200 {
		t.Fatal("bad should still be explored occasionally (Thompson)")
	}
}

func TestMarkResponseRecordsStat(t *testing.T) {
	s := &Selector{cooldown: newCooldownStore(), swrrState: newSWRRStateMap(), settings: DefaultSettings(), policy: failover.DefaultCooldownPolicy(), stats: make(map[string]*candidateStat)}
	c := Candidate{GroupID: 1, RealModel: "m"}
	s.MarkResponse(c, 200, "", 0, 800)
	if s.stats["1:m"] == nil || s.stats["1:m"].success != 1 {
		t.Fatal("MarkResponse(200) should record success")
	}
	s.MarkResponse(c, 500, "err", 0, 0)
	if s.stats["1:m"].fail != 1 {
		t.Fatal("MarkResponse(500) should record fail")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/router_engine/ -run "TestSwrrFavors|TestMarkResponseRecords" -v`
Expected: 失败（MarkResponse 签名不符 / swrr 未用 effectiveWeight）。

- [ ] **Step 3: MarkResponse 加 latency 参数**

把 `MarkResponse` 签名改为：
```go
func (s *Selector) MarkResponse(c Candidate, status int, parsedError string, retryAfter time.Duration, latency time.Duration) {
```
在现有 cooldown reset/apply 逻辑之后追加：
```go
	s.recordStat(c, status >= 200 && status < 400, latency)
```
（保留现有 cooldown 分支不动。）

- [ ] **Step 4: swrr 用 effectiveWeight**

修改 `swrr`：把按 `c.Weight` 累加的地方改用 `s.sampleEffectiveWeight(c)`。Read 现有 `swrr` 看清 current_weight 累加结构，将 `total += c.Weight` 与 `state[i] += c.Weight` 中的 `c.Weight` 替换为预先算好的 effective weights 切片（每次调用算一次，避免重复采样）：
```go
	eff := make([]int, len(sorted))
	total := 0
	for i, c := range sorted {
		eff[i] = s.sampleEffectiveWeight(c)
		total += eff[i]
	}
	// 然后 current_weight 累加用 eff[i] 替代 c.Weight，selection 后 state[best] -= total
```
保留单候选快路径、`max(1,...)` 已在 sampleEffectiveWeight 内保证 total>0。stableSort 仍按基础 Weight/Priority（确定性排序），eff 与 sorted 索引对齐。

- [ ] **Step 5: proxy markRoutingCandidate 传 latency**

`internal/proxy/server.go` 的 `markRoutingCandidate(c, statusCode, parsedError, retryAfter)` 加 `latency time.Duration` 参数，内部传给 `MarkResponse`。三处调用：
- 成功路径：传 `time.Since(startTime)`。
- 失败路径 / SelectKey 失败：传 `time.Since(startTime)`（或 0；用 `time.Since(startTime)` 更真实）。
用 `grep -n "markRoutingCandidate(c" internal/proxy/server.go` 找全调用点逐一补 latency 实参。

- [ ] **Step 6: 构建 + 测试**

Run: `go build ./... && go test ./internal/router_engine/ ./internal/proxy/ -v`
Expected: 通过（`TestSwrrFavorsReliable` 验证偏向 + 探索）。

- [ ] **Step 7: Commit**

```bash
git add internal/router_engine/selector.go internal/proxy/server.go internal/router_engine/selector_test.go
git commit -m "✨ feat(router_engine): MarkResponse 记录延迟 + swrr 用采样有效权重选择"
```

---

## Task 4: 收尾验证

- [ ] **Step 1: 全量构建 + 测试**

Run: `go vet ./... && go test ./...`
Expected: 无 vet 报错；全绿。

- [ ] **Step 2: 零回归 + 退化确认**

确认：meta=nil + 无 stats 时，`sampleEffectiveWeight` ≈ `c.Weight × 1.0 × Beta(1,1) × 1.0`——引入有界探索抖动但不饿死、不 panic；单候选快路径不变。

- [ ] **Step 3: 验收对照**

对照 spec 验收标准：有样本时成功候选被选概率上升、失败候选下降但不冻结（`TestSwrrFavorsReliable`）；registry 命中高档候选起点权重高（`TestPriorWeight` + sampleEffectiveWeight）；registry 数据被读取（Task2 接线）。

- [ ] **Step 4: 合并/PR**

按 finishing-a-development-branch（分支 `feat/sampling-router`）。

---

## Self-Review 记录

- **Spec coverage**：#1 Thompson→Task1 sampleBeta + Task3 swrr 融合；#2 多因子(先验perf/latency + 后验rel/speed)→Task1 prior/speed + Task2 sampleEffectiveWeight；护栏复用 cooldown/账本(不在本阶段)；#3 指数衰减→Task2 latencyEWMA + statRollThreshold 滚动减半；registry 接线→Task2。✅
- **Placeholder scan**：纯函数/方法均给完整代码；registry LookupMetaRaw 的锁/index 名、swrr 的 current_weight 改造、proxy 调用点用 Read/grep 现场对齐（行号/字段名漂移）——有界指令。
- **Type consistency**：`NewSelector(db, st, meta)`、`MarkResponse(c, status, parsedError, retryAfter, latency)`、`MetaProvider.LookupMeta→*ModelMeta`、`candidateStat{success,fail,latencyEWMA}`、`recordStat(c, bool, time.Duration)`、`sampleEffectiveWeight(c)→int`、`sampleBeta/sampleGamma/priorWeight/speedFactor/perfScore/latencyScore` 各 Task 间一致。✅
- **依赖方向**：services 不 import router_engine——用 `LookupMetaRaw`(基础类型) + container adapter 解决。✅
