# 阶段 B：四维速率账本（Rate-Limit Ledger）设计

> FreeLLMAPI 能力集成路线第 2 阶段。借鉴点 #4（选 key 前的主动配额准入）。
> 本阶段范围：**先做 RPM/RPD（请求维）**，TPM/TPD（token 维）留作后续增量。
> 前置：阶段 A 冷却分级已合并。整体路线见记忆 `project_freellmapi_integration_roadmap`。

## 背景与现状

当前对免费 provider 额度是**被动反应**：请求打到上游被 429 拒 → 才知道该 key 这分钟满了 → 触发冷却（阶段 A）。问题是那一次请求白白消耗、响应变慢。

**阶段 B 改为主动准入**：在 `keypool.SelectKey` 挑 key 的那一刻，先查这个 key 当前窗口的请求计数，已达上限就跳过、选下一个 key；全部超限才报无可用 key。配合阶段 A 形成「预防（账本）+ 兜底（冷却）」双层。

### 关键现状（已探明）

- `store.Store` 接口**无任何滑窗/计数原语**（只有 `Set/Get/HIncrBy/SetNX/LPush/Rotate` 等）。Redis 与 Memory 两个实现都需扩展。
- `keypool.SelectKey(groupID uint)` 用 `store.Rotate` 从 `group:{id}:active_keys` 轮转取 key，已有 `maxSkip=16` 防御性跳过循环（status 非 active 的 key 会被跳过）。准入逻辑可复用这个循环。
- `SelectKey` 两个调用方：`proxy/server.go:296`、`keypool/validator.go:168`。前者作用域有 `group *models.Group`（含 `group.EffectiveConfig`）。
- 配置是 **tag 驱动**：`types.SystemSettings`（系统默认 + `name/category/desc/validate/default` tag，前端 Settings 表单自动渲染）+ `models.GroupConfig`（`*int` 指针，group 级覆盖，存 group 表 JSON 列，**无需 DB migration**）+ 运行时合并出 `group.EffectiveConfig`。照 `BlacklistThreshold` 模式加新字段即可，前端自动出表单，只需补 i18n。
- `web/src/data/freeProviders.ts` 的 `FreeProvider` 结构可加免费档默认上限，创建分组时预填。

## 目标

1. 选 key 前按 `(group, key)` 维度的 RPM/RPD 计数做准入，超限的 key 跳过。
2. 准入通过即记一次（预占），多实例间通过 Redis 共享计数。
3. 上限可配：group 级配置字段（admin 可改）+ freeProviders 预填免费档默认值；`0`/留空 = 不限流。
4. 无 Redis（单机 Memory store）时降级为进程内近似计数，不报错。

## 非目标（YAGNI）

- **TPM/TPD（token 维）**：需请求前估算 + 请求后回填，本阶段不做，留接口余量。
- **模型级 `(group, model, key)` 维度**：先账号级 `(group, key)`，覆盖多数 provider。
- **精确滑动窗口**：用固定窗口计数器（见下），不引入 sorted-set。
- 跨实例计数的强一致：固定窗口 + Redis INCR 已足够「保守不超限」，不追求精确到个位。

## 设计

### 1. store 滑窗原语（固定窗口）

在 `store.Store` 接口新增一个方法：

```go
// IncrWithTTL 原子自增 key 的整数值并返回自增后的结果；当且仅当 key 是本次
// 新建（自增后值 == 1）时设置 ttl 过期。用于固定窗口限流计数器。
IncrWithTTL(key string, ttl time.Duration) (int64, error)
```

并新增一个只读查询（准入用，不自增）：

```go
// GetInt 读取 key 的整数计数；key 不存在返回 (0, nil)。
GetInt(key string) (int64, error)
```

**Redis 实现**（`redis.go`）：`IncrWithTTL` 用 Lua 脚本或 pipeline 保证原子——`INCR` 后若返回 1 则 `EXPIRE key ttl`。`GetInt` 用 `GET` + 解析（nil → 0）。注意走 `prefixKey`。

**Memory 实现**（`memory.go`）：用现有锁结构存 `map[string]struct{ val int64; expireAt time.Time }`，`IncrWithTTL` 加锁自增、过期则重置、首次设 expireAt；`GetInt` 读值（过期视为 0）。单机/无 Redis 时即进程内近似计数。

### 2. 账本服务 `internal/ratelimit/ledger.go`（新包）

纯逻辑封装 key 设计与准入/记账，依赖 `store.Store`。

```go
package ratelimit

// Limits 是一个 (group,key) 维度的上限快照（0 = 该维不限）。
type Limits struct {
	RPM int
	RPD int
}

// IsZero 报告是否完全不限流（两维都 <=0），调用方可据此短路跳过账本。
func (l Limits) IsZero() bool { return l.RPM <= 0 && l.RPD <= 0 }

type Ledger struct {
	store store.Store
	now   func() time.Time // 注入便于测试；生产用 time.Now
}

func NewLedger(s store.Store) *Ledger

// Allow 检查 (groupID,keyID) 在当前 RPM/RPD 窗口是否仍有余量。
// limits.IsZero() 直接放行。任一维计数 >= 上限 → 返回 false。
func (l *Ledger) Allow(groupID, keyID uint, limits Limits) (bool, error)

// Record 在准入通过、即将使用该 key 时调用，对 RPM/RPD 计数各 +1（预占）。
// limits.IsZero() 时 no-op（不记账，省 Redis 调用）。
func (l *Ledger) Record(groupID, keyID uint, limits Limits) error
```

**key 设计**（走 store 的 prefix 之外再加自己的命名空间）：
- RPM：`rl:rpm:{groupID}:{keyID}:{unixMinute}`，TTL `120s`（窗口 60s + 60s 宽限，确保边界后仍可被读到再自然过期）。准入读「当前分钟」格。
- RPD：`rl:rpd:{groupID}:{keyID}:{yyyymmdd}`（UTC 日期），TTL `26h`（24h + 宽限）。

`unixMinute = now.Unix() / 60`，`yyyymmdd = now.UTC().Format("20060102")`。

**Allow 逻辑**：`limits.IsZero()` → true。否则：若 `RPM>0` 读 RPM 格计数，`>= RPM` → false；若 `RPD>0` 读 RPD 格，`>= RPD` → false；都未超 → true。读用 `GetInt`（不自增）。

**Record 逻辑**：`limits.IsZero()` → nil。否则对 RPM 格、RPD 格各 `IncrWithTTL`。即使某维 limit==0 也可不记该维（省调用）——只记 limit>0 的维度。

> 固定窗口边界突刺：可接受。如需更保守，admin 把上限设比官方额度略低（如 28/30）即可，无需复杂算法。

### 3. SelectKey 集成准入

`KeyProvider` 新增 `ledger *ratelimit.Ledger` 字段（`NewProvider` 注入；为 nil 时全部跳过准入，保持向后兼容）。

`SelectKey` 签名扩展为接收上限快照：

```go
// 旧: SelectKey(groupID uint) (*models.APIKey, error)
func (p *KeyProvider) SelectKey(groupID uint, limits ratelimit.Limits) (*models.APIKey, error)
```

在现有 `for attempt < maxSkip` 循环里、status 检查通过之后、解密返回之前，插入准入：

```go
// 速率账本准入：该 key 当前窗口已达上限 → 跳过选下一个（不 LRem，额度会恢复）
if p.ledger != nil && !limits.IsZero() {
    ok, err := p.ledger.Allow(groupID, uint(keyID), limits)
    if err != nil {
        logrus.WithError(err).Warn("ratelimit Allow failed, fail-open")
        // fail-open：账本故障不应阻断流量
    } else if !ok {
        continue // 轮到下一个 key
    }
}
```

准入通过、即将返回该 key 前 `Record`（预占）：

```go
if p.ledger != nil && !limits.IsZero() {
    if err := p.ledger.Record(groupID, uint(keyID), limits); err != nil {
        logrus.WithError(err).Warn("ratelimit Record failed")
        // 记账失败不阻断返回
    }
}
return &models.APIKey{...}, nil
```

> 全部 key 超限时，`maxSkip` 循环耗尽 → 返回 `ErrNoActiveKeys`（语义合理：暂时无可用额度）。`maxSkip` 保持 16，不因此调大——配合 Rotate（超限 key 被轮到尾部），16 次足以在常规 key 池里找到有余量的；极端全满即应快速失败让上层 failover。

**调用方改造**：
- `proxy/server.go:296`：`ps.keyProvider.SelectKey(group.ID, ratelimit.LimitsFromConfig(group.EffectiveConfig))`
- `keypool/validator.go:168`：探活路径**不应受限流约束**（健康检查不该被额度挡），传 `ratelimit.Limits{}`（IsZero → 全放行）。

新增 helper（在 ratelimit 包或 keypool，避免循环依赖——放 ratelimit 包，入参用基础类型或在 keypool 内联构造）：
```go
// 从 EffectiveConfig 取 RPM/RPD 上限。EffectiveConfig 字段见 §4。
func LimitsFromConfig(rpm, rpd int) Limits { return Limits{RPM: rpm, RPD: rpd} }
```
（实际调用方从 `group.EffectiveConfig.RPMLimit`/`.RPDLimit` 取值传入，避免 ratelimit 依赖 models。）

### 4. 配置字段（照 BlacklistThreshold 模式）

- **`types.SystemSettings`** 加（「密钥配置」区）：
  ```go
  RPMLimit int `json:"rpm_limit" default:"0" name:"config.rpm_limit" category:"config.category.key" desc:"config.rpm_limit_desc" validate:"min=0"`
  RPDLimit int `json:"rpd_limit" default:"0" name:"config.rpd_limit" category:"config.category.key" desc:"config.rpd_limit_desc" validate:"min=0"`
  ```
  `0` = 不限流（默认关闭，不改变现有行为）。
- **`models.GroupConfig`** 加：
  ```go
  RPMLimit *int `json:"rpm_limit,omitempty"`
  RPDLimit *int `json:"rpd_limit,omitempty"`
  ```
- **EffectiveConfig 合并**：照现有合并逻辑（group 指针非 nil 则覆盖系统默认）补这两个字段。找到现有合并函数（`BlacklistThreshold` 处）依样添加。
- **i18n**：`zh-CN.ts`/`en-US.ts`/`ja-JP.ts` 各加 `config.rpm_limit`/`config.rpm_limit_desc`/`config.rpd_limit`/`config.rpd_limit_desc` 文案。前端 Settings/分组配置表单 tag 驱动自动渲染，无需改组件。

### 5. freeProviders 预填

- `FreeProvider`（`freeProviders.ts`）加可选字段：`rpmLimit?: number; rpdLimit?: number;`，给已知免费档的 provider 填上官方额度（无把握的不填）。
- 创建分组流程（`V3NewGroupFlow.vue` / `GroupFormModal.vue`）：一键选中某 provider 预填时，把 `rpmLimit/rpdLimit` 写进 group 配置的对应字段。无值则留空（不限流）。

## 错误处理

- 账本任何 Redis/store 错误一律 **fail-open**（放行 + 记 warn），绝不因限流子系统故障阻断正常流量。
- Memory store 多实例下各自计数（不共享）→ 实际限流偏松，可接受（单机部署本就无多实例问题；多实例部署必用 Redis）。
- 探活路径（validator）传空 Limits，不受限流影响。

## 测试

1. **store 原语**（`redis` 用 miniredis 或现有测试设施，`memory` 直接）：`IncrWithTTL` 首次返回 1 且设了 TTL、再次返回 2、过期后重置为 1；`GetInt` 不存在返回 0、存在返回值。
2. **ledger**（`internal/ratelimit/ledger_test.go`，注入 mock store + 固定 `now`）：
   - `IsZero` 短路放行 + 不记账。
   - RPM 未达上限放行、达到上限拒绝；RPD 同理。
   - `Record` 后 `Allow` 计数推进；跨分钟窗口计数重置（改 `now`）。
   - 只 limit>0 的维度被记账。
3. **SelectKey 集成**（`keypool` 测试，用 MemoryStore + ledger）：某 key 记满 RPM 后 `SelectKey` 跳过它选下一个；全满返回 `ErrNoActiveKeys`；ledger 为 nil 时行为同旧。
4. 回归：proxy / validator 调用方签名更新后全包测试绿。

## 验收标准

- `go build ./...` 通过；`go test ./...` 通过。
- 默认（limit=0）行为与现状完全一致（不限流，零回归）。
- 设上限后：同一 key 连续请求达 RPM 上限 → 后续请求在选 key 阶段跳过该 key；跨分钟后恢复。
- 多实例（Redis）共享计数；单机（Memory）进程内计数不报错。

## 影响面

- 新增包 `internal/ratelimit/`。
- 改 `internal/store/`（接口 + redis + memory）、`internal/keypool/provider.go`（SelectKey）、`internal/proxy/server.go` + `internal/keypool/validator.go`（调用方）、`internal/types/types.go` + `internal/models/types.go` + EffectiveConfig 合并、3 个 i18n 文件、`freeProviders.ts` + 创建分组组件。
- 无 DB schema migration（GroupConfig 走 JSON 列，SystemSettings 走 kv 表）。
- 默认不限流，灰度安全。
