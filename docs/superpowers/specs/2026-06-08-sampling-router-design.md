# 阶段 D：采样路由（Sampling Router，先验+后验融合）设计

> FreeLLMAPI 能力集成路线第 4 阶段。借鉴点 #1（Thompson 采样）、#2（多因子打分+护栏）、#3（指数衰减统计）。
> 前置：阶段 A 冷却分级、B 速率账本、C 粘性会话已合并。路线见记忆 `project_freellmapi_integration_roadmap`。

## 背景与现状

tier 内候选的最终选择用 `selector.go` 的 `swrr`（Smooth Weighted Round Robin）——按**静态基础权重**轮转，不感知候选的实时表现（某候选最近老失败/变慢，SWRR 照样按权重发给它）。

FreeLLMAPI 用「reliability + speed + intelligence 三因子加权 × 配额/限流双护栏」的打分替代静态权重。但映射到我们架构后做了关键重评（见对比记录）：

**我们的数据优势** —— `FreeModelsRegistry`（`internal/services/freemodels_registry.go`，运行时拉 `zhuzhuyule/FreeModels`，697 模型、零人工维护）已提供每个模型的：
- `PerformanceLevel`（entry/mid/high，697/697 覆盖）= 智能档
- `EstimatedLatency`（<1s / 1-3s / 3-10s，697/697 覆盖）+ `Speed` = 速度先验

但这些数据**目前路由完全没用**（`router_engine`/`subgroup_manager` 不读 registry，只 UI/isFree 用它）。FreeLLMAPI 要人工 seed 维护的 intelligence/speed 排名，我们从 registry 白捡。

### 关键现状（已探明）

- `Selector.swrr(key string, cands []Candidate) *Candidate` 按 `Candidate.Weight` 做 SWRR；`MarkResponse(c, status, parsedError, retryAfter)` 是请求结束反馈接缝（cooldown 走它），但**不带 latency**。
- `FreeModelsRegistry` 有 `Lookup(provider, modelID string) *FreeModelMeta`、内部 `byModelOnly map[string][]*FreeModelMeta` 索引；`FreeModelMeta` 含 `PerformanceLevel`/`EstimatedLatency`/`Speed`。
- `Candidate` = `{AliasID, Alias, GroupID, RealModel, Weight, Priority}`——有 `RealModel`（查 registry 用），无 provider 名（用 modelId-only 查）。
- 护栏已存在：cooldown（`filterCooldown` 在 Pick 链里排除）+ 速率账本（`SelectKey` 准入）。采样层不重复造护栏。
- `selector.go` 已 import `math/rand`（cooldown jitter 用），Beta 采样可复用。

## 目标

1. tier 内候选选择从静态 SWRR 升级为「先验 × 后验」加权采样：
   - **先验**（静态、来自 registry、零维护）：`PerformanceLevel` + `EstimatedLatency` → 初始权重，聪明且快的起点高。
   - **后验**（动态、实时学习）：`reliability`（Thompson Beta 采样自实时成功/失败）× `speed`（实时延迟 EWMA 修正）。
2. 激活沉睡的 registry 数据；reliability/speed 零人工维护（请求反馈自动产生）。
3. 默认平滑接入：无 registry / 无统计样本时退化为接近原 SWRR 行为（零回归风险可控）。

## 非目标（YAGNI）

- 不引入需人工维护的 intelligence seed 表（registry 已覆盖）。
- 不做跨实例统计强一致（各实例独立学习，采样本就概率性；要共享可后续放 Redis）。
- 不改 tier 分档逻辑（auto 仍先按 token 选 tier，本阶段只改 tier 内选择）。
- 不做 TPM/TPD（属阶段 B 增量）。

## 设计

### 1. 先验权重（registry，静态）

`router_engine` 定义小接口避免对 services 的强依赖 / 循环：

```go
// MetaProvider 提供模型的静态先验元数据。由 services.FreeModelsRegistry 适配实现。
type MetaProvider interface {
	// LookupMeta 按模型 id 返回先验档位；查不到返回 nil。
	LookupMeta(modelID string) *ModelMeta
}

// ModelMeta 是采样需要的先验子集。
type ModelMeta struct {
	PerformanceLevel string // "entry" | "mid" | "high" | ""
	EstimatedLatency string // "< 1s" | "1-3s" | "3-10s" | ""
}
```

`services.FreeModelsRegistry` 加适配方法（用现有 `byModelOnly` 取首个）：
```go
func (r *FreeModelsRegistry) LookupMeta(modelID string) *router_engine.ModelMeta // 或返回 router_engine 定义的类型
```
> 为避免 services→router_engine 反向依赖，`ModelMeta`/`MetaProvider` 定义在 router_engine，registry 在 container 适配时用一个 thin adapter（闭包或小 struct）把 `*FreeModelMeta` 映射成 `*ModelMeta`。实现者择最简方式（adapter struct 在 container 或 router_engine 内）。

先验分映射（纯函数，可调）：
```go
func perfScore(level string) float64 { // entry .7 / mid .85 / high 1.0 / 未知 .85
	switch level { case "high": return 1.0; case "mid": return 0.85; case "entry": return 0.7; default: return 0.85 }
}
func latencyScore(l string) float64 { // <1s 1.0 / 1-3s .85 / 3-10s .7 / 未知 .9
	switch l { case "< 1s": return 1.0; case "1-3s": return 0.85; case "3-10s": return 0.7; default: return 0.9 }
}
// priorWeight = perfScore × latencyScore；registry 为 nil 或查不到 → 1.0（中性）
```

### 2. 后验统计（内存，实时）

`Selector` 加：
```go
type candidateStat struct {
	success     int64
	fail        int64
	latencyEWMA float64 // 毫秒，0 = 无样本
}
// Selector 字段： stats map[string]*candidateStat; statsMu sync.Mutex; meta MetaProvider
```
key = `fmt.Sprintf("%d:%s", GroupID, RealModel)`。

`MarkResponse` 扩展加 `latency time.Duration` 参数，在现有 cooldown 反馈基础上更新 stats：
```go
func (s *Selector) MarkResponse(c Candidate, status int, parsedError string, retryAfter time.Duration, latency time.Duration) {
	// ...现有 cooldown reset/apply 不变...
	s.recordStat(c, status >= 200 && status < 400, latency)
}
```
`recordStat`：成功 success++ 且更新 latencyEWMA（α=0.3，复用 SubGroupManager 同款）；失败 fail++。**指数衰减（#3）用 EWMA + 可选的周期性老化实现**（首版只 EWMA 延迟；reliability 的衰减用「计数上限滚动」简单近似：success+fail 超过阈值 N（如 100）时整体减半，保留近期占比，避免老数据永久主导）。

### 3. Thompson 采样（Beta 分布）

```go
// sampleBeta 从 Beta(alpha,beta) 采样：两个 Gamma 相除。Marsaglia-Tsang。
func sampleBeta(alpha, beta float64) float64
func sampleGamma(k float64) float64 // Marsaglia-Tsang，用 math/rand
```
reliability = `sampleBeta(success+1, fail+1)`（拉普拉斯平滑，无样本→Beta(1,1)=均匀，给探索）。

### 4. 速度后验修正

```go
// speedFactor 把实时 latencyEWMA 映射成 [0.5,1.0] 乘子；无样本 → 1.0。
func speedFactor(ewmaMs float64) float64 // 例如 1s→~1.0, 5s→~0.6，饱和曲线，clamp ≥0.5
```

### 5. 融合进 swrr

`swrr` 计算总权重/挑选时，把每个候选的 `Weight` 替换为有效权重：
```go
effectiveWeight(c) = max(1, round( float64(c.Weight) × priorWeight(c) × reliabilitySample(c) × speedFactor(c) ))
```
- `priorWeight`：查 registry meta（nil→1.0）。
- `reliabilitySample`：`sampleBeta(stat.success+1, stat.fail+1)`（无 stat→Beta(1,1)）。
- `speedFactor`：`speedFactor(stat.latencyEWMA)`（无 stat→1.0）。
- 保留 SWRR 的平滑轮转框架（current_weight 数组），只是每轮用 effectiveWeight 累加。`max(1,...)` 防止权重归零饿死候选。
- 单候选时直接返回（现有快路径不变）。

> 因为含随机采样，SWRR 的 current_weight 状态语义略变（每轮权重不同）。可接受：Thompson 的随机性本就是设计目标；平滑性由 priorWeight/speedFactor 的确定性部分保证。

### 6. 接线

- `NewSelector(db, st)` → `NewSelector(db, st, meta MetaProvider)`（meta 可 nil）；`container.go` 注册补传 registry 适配器。
- `MarkResponse` 调用点（`proxy/server.go` 的 `markRoutingCandidate`）补传 `latency`：成功路径传 `time.Since(startTime)`，失败路径传 0 或实际耗时。

## 错误处理

- registry 为 nil / 查不到 meta → priorWeight=1.0（中性），不报错。
- 无统计样本 → reliability=Beta(1,1) 采样、speedFactor=1.0，行为接近原 SWRR + 轻微探索抖动。
- stats map 并发：statsMu 保护读写。
- 采样函数数值安全：alpha/beta ≥1（已加平滑），Gamma 采样 k≥1 分支。

## 测试

1. **先验映射**（纯函数）：perfScore/latencyScore 各档值；priorWeight 组合；未知档中性。
2. **sampleBeta/sampleGamma**：统计性质——`sampleBeta(100,1)` 均值≈0.99、`sampleBeta(1,100)`≈0.01、`sampleBeta(1,1)` 在 [0,1] 且均值≈0.5（多次采样取均值，容差断言）。
3. **recordStat**：成功累加 success + 更新 latencyEWMA；失败累加 fail；滚动减半阈值触发。
4. **speedFactor**：低延迟→~1.0、高延迟→下降并 clamp≥0.5、无样本→1.0。
5. **effectiveWeight / swrr 融合**：构造两个候选，一个 stat 全失败、一个全成功，多次选择统计分布——成功的被选概率显著更高；reliabilitysample 让全失败候选仍偶尔被选（探索不饿死）；`max(1,...)` 保证最低权重。
6. **MarkResponse 集成**：调用后 stats 正确更新；nil meta / 无 registry 不 panic。
7. 回归：`selector_test.go` 按 `NewSelector` 新签名补 meta 实参（nil）；`MarkResponse` 调用补 latency 实参；proxy 测试随之更新。

## 验收标准

- `go build ./...`、`go test ./...` 通过。
- 无 registry + 无样本时行为接近原 SWRR（仅采样引入有界探索抖动），无饿死、无 panic。
- 有样本时：连续成功的候选被选概率上升、连续失败的下降但不被永久冻结（Thompson 探索）。
- registry 命中的候选：高 performanceLevel + 低延迟档起点权重更高。
- registry 数据被路由实际读取（不再沉睡）。

## 影响面

- 改 `internal/router_engine/selector.go`（stats/meta 字段 + 采样/先验/融合 + NewSelector/MarkResponse 签名）、`internal/router_engine/middleware.go`（如需，MarkResponse 调用不在此）、`internal/services/freemodels_registry.go`（加 LookupMeta 适配）、`internal/container/container.go`（注册接线）、`internal/proxy/server.go`（markRoutingCandidate 传 latency）、相关 `_test.go`。
- 无 DB schema、无 API、无前端改动。
- 默认（无 registry/无样本）平滑退化，灰度安全。
