# 阶段 C：粘性会话（Sticky Session）设计

> FreeLLMAPI 能力集成路线第 3 阶段。借鉴点 #9（多轮对话锁定目标，防风格漂移）。
> 前置：阶段 A 冷却分级、阶段 B 速率账本已合并。路线见记忆 `project_freellmapi_integration_roadmap`。

## 背景与现状

`router_engine.Middleware`（`internal/router_engine/middleware.go`）在每次 chat 请求时按 model 解析目标 candidate：
- `model=="auto"` → `PickForAuto(估算token)` 按 tier（simple/medium/complex）选 → SWRR；
- 别名 → `PickByAlias` SWRR；exact-name → `PickByExactName` SWRR。
最终 `c.Set("router_engine.candidate", picked)` 并改写 group/body。

**问题**：同一个多轮对话，每一轮都重新走这套选择——
- auto 路由：随对话变长，估算 token 跨过阈值 → tier 切换（simple→complex）→ 换到完全不同的模型，对话风格/能力中途突变。
- alias/exact 路由：SWRR 每轮可能轮到不同 candidate（不同子分组/key），同样导致跨轮漂移。

FreeLLMAPI 的解法：用对话标识锁定首次选中的目标 30 分钟，多轮内复用，避免中途换模型。

### 关键现状（已探明）

- `Selector`（`selector.go`）当前**不持有 `store.Store`**（只有进程内 `cooldownStore` map）。`NewSelector(db *gorm.DB)` 在 `container.go:178` 注册，dig 可注入 `store.Store`（阶段 B 已确认 store 在容器注册）。
- `Selector` 已有候选过滤 helper：`filterByActiveKeys`、`filterCooldown`、`filterByExposed`（私有），可复用来验证某 candidate 是否仍可用。
- `Candidate` 结构：`{AliasID, Alias, GroupID, RealModel, Weight, Priority}`。
- `Middleware` 已读取并解析 body（`probe.Messages` 等），可在此提取会话标识。

## 目标

1. 多轮对话内，把首次选中的 `Candidate` 锁定复用，避免跨轮换模型/换分组。
2. 会话标识：优先请求头 `X-Session-Id`；否则取首条 user 消息内容的 SHA1（客户端每轮重发完整历史，首条 user 消息跨轮稳定）。
3. 仅多轮生效（messages 含 assistant 角色才锁；首轮不锁，避免单轮请求污染）。
4. 命中的 candidate 必须仍可用（有 active key、不在 cooldown、未被 exposed/blocked 拦），否则视为未命中走正常选择并刷新锁。
5. 多实例经 `store`（Redis）共享；单机 Memory store 进程内即可。

## 非目标（YAGNI）

- 不做粘性的 UI 开关/可视化（先默认开启，TTL 固定 30min；如需再加配置）。
- 不锁定具体 API key（key 级轮换仍由 keypool 负责，粘性只锁到 `(group, model)` 这层 candidate）。
- 不处理客户端不发完整历史的非常规场景（无 assistant 消息即视为首轮，自然降级为不锁）。

## 设计

### 1. Selector 持有 store

`Selector` 加字段 `store store.Store`。`NewSelector` 签名改为 `NewSelector(db *gorm.DB, st store.Store) *Selector`，`container.go:178` 注册处补传 store（dig 解析）。store 为 nil 时粘性功能整体降级为 no-op（向后兼容、便于测试）。

### 2. 会话标识解析（Middleware 内）

新增 `parseSession(bodyBytes []byte, header http.Header) (sessionKey string, isMultiTurn bool)`：
- `sessionKey`：若 `header.Get("X-Session-Id")` 非空 → `"hdr:" + 该值`；否则解析 messages，取**第一条 `role=="user"` 的消息**，其 content（string 或 array-of-blocks 拼接出的文本）做 `sha1` hex → `"msg:" + hex`。都取不到 → 返回空 sessionKey。
- `isMultiTurn`：messages 中存在 `role=="assistant"` 的消息即为 true。
- content 为 array-of-blocks（如 `[{type:text,text:...}]`）时需 flatten 出文本再 hash（与 `estimateTokensFromBody` 同样的解析方式），避免 opencode 风格客户端每轮 hash 不稳。

### 3. sticky store key

`stickyStoreKey(sessionKey, requestedModel string) string`：`"sticky:" + sha1hex(sessionKey + "|" + requestedModel)`。
- 含 `requestedModel` → 同一对话切换显式 model 时自然不命中旧锁（auto 路由下 model 恒为 "auto"，不受影响）。
- 用 sha1 收敛长度，避免 X-Session-Id 含特殊字符污染 key。

### 4. Selector 新增方法

```go
// GetSticky 读取并反序列化粘性 candidate；不存在/解析失败/store 为 nil → nil。
func (s *Selector) GetSticky(storeKey string) *Candidate

// SetSticky 序列化并写入粘性 candidate，TTL 固定 StickyTTL（30min）；store 为 nil → no-op。
func (s *Selector) SetSticky(storeKey string, c *Candidate)

// IsCandidateAlive 校验该 candidate 当前是否仍可用：所属 group 有 active key、
// 不在 cooldown、未被 exposed/blocked 拦。复用 filterByActiveKeys/filterCooldown/
// filterByExposed（传入单元素切片，结果非空即存活）。
func (s *Selector) IsCandidateAlive(ctx context.Context, c *Candidate) bool
```

`StickyTTL = 30 * time.Minute` 常量。candidate 用 `encoding/json` 序列化（字段已是导出的）。

### 5. Middleware 集成

在现有 `switch model` 选择逻辑外包一层粘性：

```go
sessionKey, isMultiTurn := parseSession(bodyBytes, c.Request.Header)
var stickyKey string
if isMultiTurn && sessionKey != "" {
    stickyKey = stickyStoreKey(sessionKey, model)
    if cand := s.GetSticky(stickyKey); cand != nil && s.IsCandidateAlive(c.Request.Context(), cand) {
        picked = cand // 命中：跳过正常选择
    }
}
if picked == nil {
    // ...现有 auto / alias / exact 选择逻辑...
}
// 选定后（无论命中与否，只要 picked 有效且是多轮）刷新锁：
if picked != nil && stickyKey != "" {
    s.SetSticky(stickyKey, picked)
}
```

- 命中且存活 → 直接用，跳过 PickForAuto/PickByAlias（这是核心：不再重新分档/轮换）。
- 未命中或命中已失效 → 正常选择 + 写新锁（失效自愈：旧 candidate 挂了，这轮选个新的并锁定它）。
- 首轮（!isMultiTurn）或无 sessionKey → 完全走现有逻辑，不读不写锁。
- 命中刷新（每轮 SetSticky）→ 活跃对话持续续期 30min，闲置 30min 后自然过期。

### 6. 作用域

auto / alias / exact 三种路由统一生效（sticky key 含 requestedModel 区分）。auto 路由收益最大（消除 tier 漂移），alias/exact 消除 SWRR 跨轮轮换。

## 错误处理

- store 读写错误（GetSticky/SetSticky）→ 静默降级（视为未命中 / 锁未写成功），绝不阻断请求；记 debug 日志。
- sessionKey 取不到（无 X-Session-Id 且无 user 消息）→ 不启用粘性，走正常选择。
- 命中 candidate 失效 → 自动走正常选择并刷新锁，对调用方透明。

## 测试

1. **parseSession**（`middleware_test.go` 或新增）：X-Session-Id 优先；无头时取首条 user 消息 SHA1（string content 与 array-of-blocks content 都覆盖、且同内容 hash 稳定）；isMultiTurn 判定（有/无 assistant）。
2. **stickyStoreKey**：同 (session, model) 稳定、不同 model 不同 key。
3. **Selector sticky**（`selector_test.go`，用 `store.NewMemoryStore()`）：
   - `SetSticky` 后 `GetSticky` 取回相同 candidate；不存在返回 nil；store 为 nil 时 Get→nil/Set→no-op。
   - `IsCandidateAlive`：有 active key 且不在 cooldown → true；group 无 active key → false；在 cooldown → false。
4. **Middleware 集成**（用 MemoryStore + seed alias/key）：
   - 首轮（无 assistant）不写锁；
   - 多轮第二次命中锁 → picked 与首次相同（即使 token 估算跨档也不换）；
   - 锁定 candidate 失效（清掉 active key）→ 回退正常选择并刷新。
5. 回归：现有 `selector_test.go` 因 `NewSelector` 签名变化按新签名补 store 实参（传 `store.NewMemoryStore()` 或 nil）。

## 验收标准

- `go build ./...`、`go test ./...` 通过。
- store 为 nil 或单轮请求时行为与现状完全一致（零回归）。
- 多轮 auto 对话：第二轮起即使估算 token 跨过 tier 阈值，仍复用首轮选中的 candidate（不换模型）。
- 锁定目标失效时自动回退选新目标，不报错。
- 多实例（Redis）下 sticky 跨实例共享。

## 影响面

- 改 `internal/router_engine/selector.go`（store 字段 + 三个 sticky 方法 + NewSelector 签名）、`internal/router_engine/middleware.go`（parseSession + 集成）、`internal/container/container.go`（注册补传 store）、`internal/router_engine/*_test.go`（签名 + 新测试）。
- 无 DB schema、无 API、无前端改动。
- 默认开启但零回归（单轮/无会话标识时不介入）。
