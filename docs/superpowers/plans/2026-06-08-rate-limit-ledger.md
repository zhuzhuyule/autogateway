# 四维速率账本（Rate-Limit Ledger）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `keypool.SelectKey` 挑 key 时按 `(group,key)` 维度的 RPM/RPD 固定窗口计数做主动准入，超限的 key 跳过、选下一个；上限可经 group 配置 + freeProviders 预填，默认 0=不限流零回归。

**Architecture:** 新增 store 滑窗原语 `IncrWithTTL`/`GetInt`（Redis Lua + Memory）→ 新包 `internal/ratelimit` 封装账本 `Allow`/`Record`（固定窗口 key 含时间戳）→ `SelectKey` 注入 ledger 集成准入 → 照 `BlacklistThreshold` 模式加 group 配置字段（tag 驱动前端）→ freeProviders 预填。

**Tech Stack:** Go 1.25、go-redis v9、gin、GORM；前端 Vue 3 + TS（仅 i18n 文案 + 创建分组预填）。

**Spec:** `docs/superpowers/specs/2026-06-08-rate-limit-ledger-design.md`

---

## File Structure

- **Modify** `internal/store/store.go` — `Store` 接口加 `IncrWithTTL`、`GetInt`。
- **Modify** `internal/store/redis.go` — Lua 实现 `IncrWithTTL` + `GetInt`。
- **Modify** `internal/store/memory.go` — 进程内实现 `IncrWithTTL` + `GetInt`（带过期）。
- **Create** `internal/ratelimit/ledger.go` + `ledger_test.go` — `Limits`、`Ledger.Allow`、`Ledger.Record`。
- **Modify** `internal/keypool/provider.go` — `KeyProvider` 加 `ledger` 字段；`SelectKey` 加 `limits` 参数 + 准入/记账。
- **Modify** `internal/proxy/server.go` + `internal/keypool/validator.go` — `SelectKey` 调用方传 limits。
- **Modify** `internal/types/types.go` + `internal/models/types.go` + EffectiveConfig 合并处 — 加 `RPMLimit`/`RPDLimit`。
- **Modify** `web/src/locales/{zh-CN,en-US,ja-JP}.ts` — 配置项 i18n 文案。
- **Modify** `web/src/data/freeProviders.ts` + 创建分组组件 — 预填上限。

---

## Task 1: store 滑窗原语

**Files:**
- Modify: `internal/store/store.go`（`Store` 接口，约 24-69）
- Modify: `internal/store/redis.go`
- Modify: `internal/store/memory.go`
- Test: `internal/store/memory_test.go`（若不存在则创建）

- [ ] **Step 1: 接口加方法**

在 `internal/store/store.go` 的 `Store` 接口里（`HIncrBy` 附近）新增：

```go
	// IncrWithTTL 原子自增 key 的整数值并返回结果；仅当本次新建（结果==1）时设 ttl。
	IncrWithTTL(key string, ttl time.Duration) (int64, error)

	// GetInt 读取 key 的整数计数；key 不存在返回 (0, nil)。
	GetInt(key string) (int64, error)
```

- [ ] **Step 2: 写 Memory 失败测试**

在 `internal/store/memory_test.go`（无则建，`package store`）追加：

```go
func TestMemoryIncrWithTTLAndGetInt(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	if n, _ := s.GetInt("c"); n != 0 {
		t.Fatalf("absent key GetInt = %d, want 0", n)
	}
	v1, err := s.IncrWithTTL("c", time.Minute)
	if err != nil || v1 != 1 {
		t.Fatalf("first IncrWithTTL = (%d,%v), want (1,nil)", v1, err)
	}
	v2, _ := s.IncrWithTTL("c", time.Minute)
	if v2 != 2 {
		t.Fatalf("second IncrWithTTL = %d, want 2", v2)
	}
	if n, _ := s.GetInt("c"); n != 2 {
		t.Fatalf("GetInt = %d, want 2", n)
	}
}

func TestMemoryIncrWithTTLExpiry(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()
	if _, err := s.IncrWithTTL("c", 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if n, _ := s.GetInt("c"); n != 0 {
		t.Fatalf("after expiry GetInt = %d, want 0", n)
	}
	v, _ := s.IncrWithTTL("c", time.Minute)
	if v != 1 {
		t.Fatalf("post-expiry IncrWithTTL = %d, want 1 (reset)", v)
	}
}
```

确认 import 含 `"testing"`、`"time"`。

- [ ] **Step 3: 运行确认失败**

Run: `go test ./internal/store/ -run TestMemoryIncrWithTTL -v`
Expected: 编译失败（`IncrWithTTL`/`GetInt` undefined on MemoryStore）。

- [ ] **Step 4: Memory 实现**

先 Read `internal/store/memory.go` 看清现有内部存储结构（是否已有带过期的 map、锁字段名）。MemoryStore 通常已有一个 `sync.RWMutex` 和若干 map。新增一个专用计数 map 与实现（按实际字段名适配；若已有通用 entry+expireAt 结构则复用）：

```go
// 计数器（带过期）——若 MemoryStore 已有通用过期存储可复用之，否则新增：
type counterEntry struct {
	val      int64
	expireAt time.Time // 零值 = 不过期
}
// 在 MemoryStore struct 加： counters map[string]*counterEntry
// 在 NewMemoryStore 初始化： counters: make(map[string]*counterEntry)

func (m *MemoryStore) IncrWithTTL(key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	e := m.counters[key]
	if e == nil || (!e.expireAt.IsZero() && now.After(e.expireAt)) {
		e = &counterEntry{val: 0}
		if ttl > 0 {
			e.expireAt = now.Add(ttl)
		}
		m.counters[key] = e
	}
	e.val++
	return e.val, nil
}

func (m *MemoryStore) GetInt(key string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.counters[key]
	if e == nil || (!e.expireAt.IsZero() && time.Now().After(e.expireAt)) {
		return 0, nil
	}
	return e.val, nil
}
```

> 注意：锁字段名以现有代码为准（可能叫 `mu`/`lock`）。若 MemoryStore 用单一 `data map[string]any` 存所有类型，则在该 map 上存 `*counterEntry` 并做类型断言，保持与现有风格一致。`Clear()` 若清空所有 map，记得也清 `counters`。

- [ ] **Step 5: 运行确认 Memory 通过**

Run: `go test ./internal/store/ -run TestMemoryIncrWithTTL -v`
Expected: PASS。

- [ ] **Step 6: Redis 实现**

在 `internal/store/redis.go`（`HIncrBy` 附近）新增。用 Lua 保证 INCR+PEXPIRE 原子：

```go
// incrTTLScript：INCR 后若为首次（==1）则设过期。原子。
var incrTTLScript = redis.NewScript(`
local v = redis.call('INCR', KEYS[1])
if v == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return v
`)

func (s *RedisStore) IncrWithTTL(key string, ttl time.Duration) (int64, error) {
	return incrTTLScript.Run(context.Background(), s.client, []string{s.prefixKey(key)}, ttl.Milliseconds()).Int64()
}

func (s *RedisStore) GetInt(key string) (int64, error) {
	v, err := s.client.Get(context.Background(), s.prefixKey(key)).Int64()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}
```

确认 `redis.go` 已 import `redis "github.com/redis/go-redis/v9"`（现有 `redis.Nil` 用法证明已 import）。

- [ ] **Step 7: 全包构建 + 测试**

Run: `go build ./internal/store/ && go test ./internal/store/ -v`
Expected: 编译通过；Memory 测试 PASS。（Redis 实现无 miniredis 时不强制单测，靠 Task 3 集成验证。）

- [ ] **Step 8: Commit**

```bash
git add internal/store/
git commit -m "✨ feat(store): IncrWithTTL/GetInt 固定窗口计数原语 (Redis Lua + Memory)"
```

---

## Task 2: ratelimit 账本包

**Files:**
- Create: `internal/ratelimit/ledger.go`
- Test: `internal/ratelimit/ledger_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/ratelimit/ledger_test.go`（`package ratelimit`），用 `store.NewMemoryStore()` + 注入固定时钟：

```go
package ratelimit

import (
	"testing"
	"time"

	"autogateway/internal/store"
)

func newAt(s store.Store, t time.Time) *Ledger {
	l := NewLedger(s)
	l.now = func() time.Time { return t }
	return l
}

func TestLedgerZeroLimitsAllowsAndSkipsRecord(t *testing.T) {
	s := store.NewMemoryStore()
	l := newAt(s, time.Unix(1_700_000_000, 0))
	ok, err := l.Allow(1, 1, Limits{})
	if err != nil || !ok {
		t.Fatalf("zero limits Allow = (%v,%v), want (true,nil)", ok, err)
	}
	// Record 不应写任何东西
	if err := l.Record(1, 1, Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerRPMAdmitsThenBlocks(t *testing.T) {
	s := store.NewMemoryStore()
	l := newAt(s, time.Unix(1_700_000_000, 0))
	lim := Limits{RPM: 2}

	for i := 0; i < 2; i++ {
		ok, _ := l.Allow(1, 7, lim)
		if !ok {
			t.Fatalf("request %d should be allowed", i)
		}
		if err := l.Record(1, 7, lim); err != nil {
			t.Fatal(err)
		}
	}
	ok, _ := l.Allow(1, 7, lim)
	if ok {
		t.Fatal("3rd request should be blocked (RPM=2)")
	}
}

func TestLedgerRPMResetsNextMinute(t *testing.T) {
	s := store.NewMemoryStore()
	base := time.Unix(1_700_000_000, 0)
	l := newAt(s, base)
	lim := Limits{RPM: 1}
	l.Record(1, 7, lim)
	if ok, _ := l.Allow(1, 7, lim); ok {
		t.Fatal("should block within same minute")
	}
	// 跳到下一分钟
	l.now = func() time.Time { return base.Add(time.Minute) }
	if ok, _ := l.Allow(1, 7, lim); !ok {
		t.Fatal("should allow in next minute window")
	}
}

func TestLedgerRPDBlocks(t *testing.T) {
	s := store.NewMemoryStore()
	l := newAt(s, time.Unix(1_700_000_000, 0))
	lim := Limits{RPD: 1}
	l.Record(1, 7, lim)
	if ok, _ := l.Allow(1, 7, lim); ok {
		t.Fatal("should block when RPD reached")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/ratelimit/ -v`
Expected: 编译失败（包/类型未定义）。

- [ ] **Step 3: 实现 ledger.go**

创建 `internal/ratelimit/ledger.go`：

```go
// Package ratelimit 实现 (group,key) 维度的固定窗口 RPM/RPD 准入账本。
package ratelimit

import (
	"fmt"
	"time"

	"autogateway/internal/store"
)

// Limits 是一个 (group,key) 维度的上限快照（<=0 = 该维不限）。
type Limits struct {
	RPM int
	RPD int
}

// IsZero 报告是否完全不限流（两维都 <=0）。
func (l Limits) IsZero() bool { return l.RPM <= 0 && l.RPD <= 0 }

// Ledger 基于 store 的固定窗口计数器做准入与记账。
type Ledger struct {
	store store.Store
	now   func() time.Time
}

func NewLedger(s store.Store) *Ledger {
	return &Ledger{store: s, now: time.Now}
}

func (l *Ledger) rpmKey(groupID, keyID uint) string {
	return fmt.Sprintf("rl:rpm:%d:%d:%d", groupID, keyID, l.now().Unix()/60)
}

func (l *Ledger) rpdKey(groupID, keyID uint) string {
	return fmt.Sprintf("rl:rpd:%d:%d:%s", groupID, keyID, l.now().UTC().Format("20060102"))
}

// Allow 检查当前 RPM/RPD 窗口是否仍有余量。limits.IsZero() 直接放行。
func (l *Ledger) Allow(groupID, keyID uint, limits Limits) (bool, error) {
	if limits.IsZero() {
		return true, nil
	}
	if limits.RPM > 0 {
		n, err := l.store.GetInt(l.rpmKey(groupID, keyID))
		if err != nil {
			return false, err
		}
		if int(n) >= limits.RPM {
			return false, nil
		}
	}
	if limits.RPD > 0 {
		n, err := l.store.GetInt(l.rpdKey(groupID, keyID))
		if err != nil {
			return false, err
		}
		if int(n) >= limits.RPD {
			return false, nil
		}
	}
	return true, nil
}

// Record 对启用的维度各 +1（预占）。limits.IsZero() 时 no-op。
func (l *Ledger) Record(groupID, keyID uint, limits Limits) error {
	if limits.IsZero() {
		return nil
	}
	if limits.RPM > 0 {
		if _, err := l.store.IncrWithTTL(l.rpmKey(groupID, keyID), 120*time.Second); err != nil {
			return err
		}
	}
	if limits.RPD > 0 {
		if _, err := l.store.IncrWithTTL(l.rpdKey(groupID, keyID), 26*time.Hour); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/ratelimit/ -v`
Expected: 全 PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/ratelimit/
git commit -m "✨ feat(ratelimit): (group,key) 固定窗口 RPM/RPD 账本 Allow/Record"
```

---

## Task 3: SelectKey 集成准入

**Files:**
- Modify: `internal/keypool/provider.go`（`KeyProvider` struct ~20-35、`NewProvider` ~28、`SelectKey` ~38-117）
- Test: `internal/keypool/provider_test.go`（无则建）

- [ ] **Step 1: 写失败测试**

在 `internal/keypool/provider_test.go`（`package keypool`）追加。先 Read `provider.go` 确认 `NewProvider` 现有签名与依赖，测试用 MemoryStore 构造一个最小 KeyProvider（按实际依赖适配；encryption 可用现有测试 helper 或 nil-安全路径）：

```go
func TestSelectKeySkipsRateLimitedKey(t *testing.T) {
	// 构造：group 1 有两把 active key (id 100,101)，ledger RPM=1。
	// 先把 key 100 记满 RPM，SelectKey 应跳过 100 选到 101。
	// 具体构造按 provider.go 的依赖注入方式写（store.NewMemoryStore + NewLedger）。
	// 断言：第一次 SelectKey(1, Limits{RPM:1}) 返回某 key 并记账；
	//      把返回的那把记满后再 SelectKey 应返回另一把；
	//      两把都满后返回 app_errors.ErrNoActiveKeys。
}
```

> 实现者：根据 `provider.go` 真实构造方式补全此测试主体（这是集成测试，需 seed active_keys 列表 + key hash）。参考同包/`subgroup` 测试里 MemoryStore 的 seed 方式。若 KeyProvider 构造依赖过重难以单测，改为针对「ledger 为 nil 时 SelectKey 行为不变」+「注入 ledger 后超限跳过」两条最小断言。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/keypool/ -run TestSelectKey -v`
Expected: 编译失败（`SelectKey` 参数不匹配 / 无 ledger 字段）。

- [ ] **Step 3: KeyProvider 加 ledger + NewProvider 注入**

在 `internal/keypool/provider.go`：import 加 `"autogateway/internal/ratelimit"`。`KeyProvider` struct 加字段 `ledger *ratelimit.Ledger`。`NewProvider` 加参数 `ledger *ratelimit.Ledger` 并赋值（放在参数列表末尾）。

> 注意：`NewProvider` 的调用方（容器/wire，通常 `internal/container` 或 `internal/app`）需补传 ledger。用 `grep -rn "keypool.NewProvider(" internal/` 找到调用点，在那里 `ratelimit.NewLedger(store)` 构造并传入（store 与 keypool 用的是同一个）。

- [ ] **Step 4: SelectKey 加 limits 参数 + 准入/记账**

`SelectKey` 签名改为 `func (p *KeyProvider) SelectKey(groupID uint, limits ratelimit.Limits) (*models.APIKey, error)`。在 `for attempt < maxSkip` 循环里、status 检查通过之后、解密返回之前插入：

```go
		// 速率账本准入：当前窗口已达上限 → 跳过选下一个（不 LRem，额度会恢复）
		if p.ledger != nil && !limits.IsZero() {
			ok, err := p.ledger.Allow(groupID, uint(keyID), limits)
			if err != nil {
				logrus.WithError(err).Warn("ratelimit Allow failed, fail-open")
			} else if !ok {
				continue
			}
		}
```

在 `return &models.APIKey{...}` 之前插入预占记账：

```go
		if p.ledger != nil && !limits.IsZero() {
			if err := p.ledger.Record(groupID, uint(keyID), limits); err != nil {
				logrus.WithError(err).Warn("ratelimit Record failed")
			}
		}
```

- [ ] **Step 5: 改两个调用方**

- `internal/proxy/server.go:296`：`ps.keyProvider.SelectKey(group.ID)` → `ps.keyProvider.SelectKey(group.ID, ratelimit.Limits{RPM: group.EffectiveConfig.RPMLimit, RPD: group.EffectiveConfig.RPDLimit})`。import 加 `"autogateway/internal/ratelimit"`。（`RPMLimit`/`RPDLimit` 字段由 Task 4 加；本 Task 先用字面量 `ratelimit.Limits{}` 占位编译，Task 4 完成后回填——或调整任务顺序，见下方说明。）
- `internal/keypool/validator.go:168`：探活不受限流 → `s.keypoolProvider.SelectKey(group.ID, ratelimit.Limits{})`。import 加 ratelimit。

> **任务顺序说明**：Task 4 加 `EffectiveConfig.RPMLimit/RPDLimit` 字段。为避免编译循环，**先做 Task 4 再做本 Task 的 Step 5 的 proxy 行**，或本 Step 先在 proxy 传 `ratelimit.Limits{}`、Task 4 完成后改成读 EffectiveConfig。实现者择一，最终 proxy 必须读 EffectiveConfig。

- [ ] **Step 6: 构建 + 测试**

Run: `go build ./... && go test ./internal/keypool/ -v`
Expected: 通过。

- [ ] **Step 7: Commit**

```bash
git add internal/keypool/ internal/proxy/server.go
git commit -m "✨ feat(keypool): SelectKey 集成速率账本准入"
```

---

## Task 4: group 配置字段（RPMLimit/RPDLimit）

**Files:**
- Modify: `internal/types/types.go`（`SystemSettings` 密钥配置区，`BlacklistThreshold` 附近）
- Modify: `internal/models/types.go`（`GroupConfig`，`BlacklistThreshold` 附近）
- Modify: EffectiveConfig 合并处（用 grep 定位）
- Modify: `web/src/locales/{zh-CN,en-US,ja-JP}.ts`

- [ ] **Step 1: SystemSettings 加字段**

在 `internal/types/types.go` 的 `SystemSettings`「密钥配置」区（`BlacklistThreshold` 行附近）加：

```go
	RPMLimit int `json:"rpm_limit" default:"0" name:"config.rpm_limit" category:"config.category.key" desc:"config.rpm_limit_desc" validate:"min=0"`
	RPDLimit int `json:"rpd_limit" default:"0" name:"config.rpd_limit" category:"config.category.key" desc:"config.rpd_limit_desc" validate:"min=0"`
```

- [ ] **Step 2: GroupConfig 加字段**

在 `internal/models/types.go` 的 `GroupConfig`（`BlacklistThreshold *int` 附近）加：

```go
	RPMLimit *int `json:"rpm_limit,omitempty"`
	RPDLimit *int `json:"rpd_limit,omitempty"`
```

- [ ] **Step 3: EffectiveConfig 合并补字段**

用 `grep -rn "BlacklistThreshold" internal/ | grep -v _test` 定位 GroupConfig→EffectiveConfig 的合并函数（把 `*int` group override 覆盖到 system 默认的那处）。照 `BlacklistThreshold` 的合并写法，为 `RPMLimit`/`RPDLimit` 各加一行同样的「group 指针非 nil 则覆盖」逻辑。

> 若合并是反射/通用实现（按 json tag 自动合并），则可能无需手写——Read 该函数确认。手写则照样式补两行。

- [ ] **Step 4: i18n 文案**

在 `web/src/locales/zh-CN.ts`、`en-US.ts`、`ja-JP.ts` 各自的 config 区加四个 key（找到 `blacklist_threshold` 文案旁边加）：

zh-CN：
```ts
  "config.rpm_limit": "每分钟请求上限 (RPM)",
  "config.rpm_limit_desc": "单个密钥每分钟最大请求数，0 表示不限流",
  "config.rpd_limit": "每天请求上限 (RPD)",
  "config.rpd_limit_desc": "单个密钥每天最大请求数，0 表示不限流",
```
en-US：
```ts
  "config.rpm_limit": "Requests Per Minute (RPM)",
  "config.rpm_limit_desc": "Max requests per minute per key; 0 means unlimited",
  "config.rpd_limit": "Requests Per Day (RPD)",
  "config.rpd_limit_desc": "Max requests per day per key; 0 means unlimited",
```
ja-JP：
```ts
  "config.rpm_limit": "1分あたりのリクエスト上限 (RPM)",
  "config.rpm_limit_desc": "キーごとの1分あたり最大リクエスト数。0 は無制限",
  "config.rpd_limit": "1日あたりのリクエスト上限 (RPD)",
  "config.rpd_limit_desc": "キーごとの1日あたり最大リクエスト数。0 は無制限",
```
> key 的确切前缀/写法以各文件现有 `config.blacklist_threshold` 条目为准（对象嵌套 vs 扁平字符串 key）。照现有条目格式插入。

- [ ] **Step 5: 回填 proxy 调用 + 构建测试**

把 Task 3 Step 5 中 proxy 的 `ratelimit.Limits{}`（若当时用了占位）改为 `ratelimit.Limits{RPM: group.EffectiveConfig.RPMLimit, RPD: group.EffectiveConfig.RPDLimit}`。

Run: `go build ./... && go test ./...`
Expected: 通过（services 等无关包不回归）。前端：`cd web && npm run build`（或现有前端构建命令）确认 i18n 无语法错。

- [ ] **Step 6: Commit**

```bash
git add internal/types/types.go internal/models/types.go web/src/locales/ internal/proxy/server.go
git commit -m "✨ feat(config): RPM/RPD 上限配置字段 (group 级 + i18n, tag 驱动前端)"
```

---

## Task 5: freeProviders 预填

**Files:**
- Modify: `web/src/data/freeProviders.ts`（`FreeProvider` interface + 各 provider 条目）
- Modify: `web/src/components/v3/V3NewGroupFlow.vue` 和/或 `web/src/components/keys/GroupFormModal.vue`（创建分组预填处）

- [ ] **Step 1: FreeProvider 加字段**

在 `freeProviders.ts` 的 `FreeProvider` interface 加：

```ts
  /** 免费档每分钟请求上限，预填到分组配置；不确定则不填 */
  rpmLimit?: number;
  /** 免费档每天请求上限，预填到分组配置；不确定则不填 */
  rpdLimit?: number;
```

为有把握的 provider 条目补上官方免费档数值（无把握的留空，不臆测）。例如 Groq 之类可填其公开 RPM/RPD；不确定的 provider 不加。

- [ ] **Step 2: 创建分组预填**

先 Read `V3NewGroupFlow.vue` / `GroupFormModal.vue` 找到「选中 provider → 预填 base_url/channel_type/test_model」的那段逻辑。在同处把 `provider.rpmLimit` / `provider.rpdLimit`（存在时）写入将要提交的 group 配置对象对应字段（`rpm_limit`/`rpd_limit`）。

> 若创建分组表单的 config 是动态 tag 驱动的（与 Settings 同机制），预填只需把值塞进提交 payload 的 config 部分即可，无需新增表单控件。

- [ ] **Step 3: 前端构建验证**

Run: `cd web && npm run build`（或项目实际前端构建/类型检查命令，如 `npm run type-check`）
Expected: 无类型错误。

- [ ] **Step 4: Commit**

```bash
git add web/src/data/freeProviders.ts web/src/components/
git commit -m "✨ feat(web): 创建分组时按 provider 预填 RPM/RPD 免费档上限"
```

---

## Task 6: 收尾验证

- [ ] **Step 1: 全量构建 + 测试**

Run: `go vet ./... && go test ./...`
Expected: 无 vet 报错；测试全绿（services 的预存测试此前已修复）。

- [ ] **Step 2: 默认零回归确认**

确认未配置上限（limit=0）时 `Allow` 恒 true、`Record` no-op、`SelectKey` 行为与改造前一致（Task 2 的 `TestLedgerZeroLimits...` + Task 3 的 nil/zero 用例覆盖）。

- [ ] **Step 3: 验收标准对照**

对照 spec「验收标准」：默认零回归 ✓；设上限后同 key 达 RPM 上限被跳过、跨分钟恢复（Task 2/3 测试覆盖）；多实例 Redis 共享、单机 Memory 不报错（Task 1 实现 + 测试覆盖）。

- [ ] **Step 4: 合并/PR**

按 finishing-a-development-branch 决定（分支 `feat/rate-limit-ledger`）。

---

## Self-Review 记录

- **Spec coverage**：store 原语→Task1；账本 Allow/Record→Task2；SelectKey 准入→Task3；配置字段+i18n→Task4；freeProviders 预填→Task5；TPM/TPD 明确不做（YAGNI）。✅
- **Placeholder scan**：代码步骤均含完整代码；Task3 Step1 集成测试主体留给实现者补全（标注了依据与回退断言），因 KeyProvider 构造依赖需现场确认——非代码 placeholder 而是有界的实现指令。Task4 Step3 合并逻辑用 grep 定位（行号会漂移）。
- **Type consistency**：`IncrWithTTL(key, ttl)`/`GetInt(key)`、`Limits{RPM,RPD}`/`IsZero`、`Ledger.Allow/Record(groupID,keyID,limits)`、`SelectKey(groupID, ratelimit.Limits)`、`EffectiveConfig.RPMLimit/RPDLimit`、`GroupConfig.RPMLimit/RPDLimit *int` 各 Task 间一致。✅
- **任务顺序依赖**：Task4 的 EffectiveConfig 字段被 Task3 的 proxy 调用引用——已在 Task3 Step5 注明用占位或调序，Task4 Step5 回填。建议实现顺序 1→2→4→3→5→6（先字段后集成），避免占位往返。
