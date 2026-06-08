# 粘性会话（Sticky Session）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 多轮对话内锁定首次选中的 candidate 30min，避免跨轮换模型/换分组（auto 路由的 tier 漂移、alias/exact 的 SWRR 轮换）。

**Architecture:** `Selector` 持有 `store.Store` 并新增 `GetSticky`/`SetSticky`/`IsCandidateAlive`；`Middleware` 解析会话标识（X-Session-Id 优先，回退首条 user 消息 SHA1）+ 多轮判断，命中且存活则复用锁定 candidate、否则正常选择并刷新锁。单轮/无会话标识/store 为 nil 时零回归。

**Tech Stack:** Go 1.25、gin、`crypto/sha1`、`encoding/json`、store（Redis/Memory）。

**Spec:** `docs/superpowers/specs/2026-06-08-sticky-session-design.md`

---

## File Structure

- **Modify** `internal/router_engine/selector.go` — `Selector` 加 `store` 字段；`NewSelector` 加 `store.Store` 参数；新增 `StickyTTL` 常量 + `GetSticky`/`SetSticky`/`IsCandidateAlive`。
- **Modify** `internal/container/container.go:178` — `NewSelector(db)` → `NewSelector(db, store)`。
- **Modify** `internal/router_engine/middleware.go` — `parseSession` + `stickyStoreKey` + 集成进 `Middleware`。
- **Modify/Create** `internal/router_engine/selector_test.go`、`middleware_test.go` — 签名更新 + 新测试。

---

## Task 1: Selector 持有 store + sticky 方法

**Files:**
- Modify: `internal/router_engine/selector.go`（`Selector` struct ~83-90、`NewSelector` ~93-101）
- Modify: `internal/container/container.go`（~178）
- Test: `internal/router_engine/selector_test.go`

- [ ] **Step 1: 写失败测试**

向 `internal/router_engine/selector_test.go` 追加（用 MemoryStore）：

```go
func TestStickyGetSet(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		store:     st,
	}
	if got := s.GetSticky("k"); got != nil {
		t.Fatal("absent sticky should be nil")
	}
	c := &Candidate{GroupID: 3, RealModel: "m", Alias: "a", Weight: 2}
	s.SetSticky("k", c)
	got := s.GetSticky("k")
	if got == nil || got.GroupID != 3 || got.RealModel != "m" {
		t.Fatalf("GetSticky = %+v, want GroupID=3 RealModel=m", got)
	}
}

func TestStickyNilStoreNoop(t *testing.T) {
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		store:     nil,
	}
	s.SetSticky("k", &Candidate{GroupID: 1}) // 不 panic
	if got := s.GetSticky("k"); got != nil {
		t.Fatal("nil store GetSticky should be nil")
	}
}
```

确认 `selector_test.go` import 含 `"autogateway/internal/store"`（缺则加）。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/router_engine/ -run TestSticky -v`
Expected: 编译失败（`Selector` 无 `store` 字段 / `GetSticky`/`SetSticky` 未定义）。

- [ ] **Step 3: Selector 加 store 字段 + NewSelector 签名**

在 `selector.go` import 块加 `"autogateway/internal/store"`、`"context"`（若未引入）、`"crypto/sha1"`+`"encoding/hex"`+`"encoding/json"`（GetSticky/SetSticky 用 json；sha1 在 middleware 用，这里只需 json）。

`Selector` struct（~83-90）追加 `store store.Store` 字段（放 policy 之后）：

```go
type Selector struct {
	db        *gorm.DB
	cooldown  *cooldownStore
	swrrState *swrrStateMap
	settings  Settings
	policy    failover.CooldownPolicy
	store     store.Store
	mu        sync.RWMutex
}
```

`NewSelector`（~93-101）签名加 `st store.Store`，赋值：

```go
func NewSelector(db *gorm.DB, st store.Store) *Selector {
	s := &Selector{
		db:        db,
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		store:     st,
	}
	s.loadSettingsFromDB()
	return s
}
```

- [ ] **Step 4: 加 StickyTTL + GetSticky + SetSticky**

在 `selector.go` 末尾（或合适位置）新增：

```go
// StickyTTL 粘性会话锁定时长。
const StickyTTL = 30 * time.Minute

// GetSticky 读取并反序列化粘性 candidate；不存在/解析失败/store 为 nil → nil。
func (s *Selector) GetSticky(storeKey string) *Candidate {
	if s.store == nil {
		return nil
	}
	b, err := s.store.Get(storeKey)
	if err != nil || len(b) == 0 {
		return nil
	}
	var c Candidate
	if err := json.Unmarshal(b, &c); err != nil {
		return nil
	}
	return &c
}

// SetSticky 序列化并写入粘性 candidate，TTL StickyTTL；store 为 nil 或序列化失败 → no-op。
func (s *Selector) SetSticky(storeKey string, c *Candidate) {
	if s.store == nil || c == nil {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := s.store.Set(storeKey, b, StickyTTL); err != nil {
		logrus.WithError(err).Debug("sticky SetSticky failed")
	}
}
```

> 注意 `store.Get` 在 key 不存在时的返回：若返回 `store.ErrNotFound`，上面的 `err != nil` 已覆盖（返回 nil）。确认 MemoryStore.Get/RedisStore.Get 对 absent key 的行为（Read 一眼），保证 absent → GetSticky 返回 nil 而非 panic。

- [ ] **Step 5: 加 IsCandidateAlive**

复用现有私有过滤 helper（`filterByActiveKeys`/`filterCooldown`/`filterByExposed` 接收 `[]Candidate` 返回过滤后切片）：

```go
// IsCandidateAlive 校验 candidate 当前是否仍可用：有 active key、不在 cooldown、
// 未被 exposed/blocked 拦。
func (s *Selector) IsCandidateAlive(ctx context.Context, c *Candidate) bool {
	if c == nil {
		return false
	}
	cands := []Candidate{*c}
	if cands = s.filterByActiveKeys(ctx, cands); len(cands) == 0 {
		return false
	}
	if cands = s.filterByExposed(ctx, cands); len(cands) == 0 {
		return false
	}
	if cands = s.filterCooldown(cands); len(cands) == 0 {
		return false
	}
	return true
}
```

> Read `selector.go` 确认这三个 helper 的真实签名（是否都接收 `(ctx, []Candidate)` 或部分只接收 `[]Candidate`）。`filterCooldown` 在现有代码里签名是 `filterCooldown(cands []Candidate) []Candidate`（无 ctx），`filterByActiveKeys`/`filterByExposed` 是 `(ctx, cands)`。按实际签名调用。

- [ ] **Step 6: container 注册补传 store**

`internal/container/container.go:178` 把 `return router_engine.NewSelector(db)` 改为 `return router_engine.NewSelector(db, st)`，其中 `st` 是该 provider 函数能拿到的 `store.Store`（Read 该 provider 函数签名/上下文：若闭包参数没有 store，给该 provider 函数加 `st store.Store` 入参，dig 会注入；参考同文件 `NewLedger`/`NewProvider` 怎么拿 store）。

- [ ] **Step 7: 构建 + 测试**

Run: `go build ./... && go test ./internal/router_engine/ -run TestSticky -v`
Expected: 通过。注意 `go build ./...` 会因其他地方调用旧 `NewSelector(db)` 报错——全仓库 `grep -rn "NewSelector(" internal/`，把所有非测试调用补 store 实参；测试里的 `NewSelector(db)` 调用补 `nil` 或 `store.NewMemoryStore()`。

- [ ] **Step 8: 加 IsCandidateAlive 测试 + 跑全包**

向 selector_test.go 追加（seed 一个 group + active key 的最小用例，参考同包/keypool 测试的 MemoryStore seed 方式；若 IsCandidateAlive 依赖 db 查询难构造，至少测「nil candidate → false」+「无 active key → false」）：

```go
func TestIsCandidateAliveNil(t *testing.T) {
	s := &Selector{cooldown: newCooldownStore(), swrrState: newSWRRStateMap(), settings: DefaultSettings(), policy: failover.DefaultCooldownPolicy(), store: store.NewMemoryStore()}
	if s.IsCandidateAlive(context.Background(), nil) {
		t.Fatal("nil candidate should not be alive")
	}
}
```

Run: `go test ./internal/router_engine/ -v`
Expected: 全 PASS。

- [ ] **Step 9: Commit**

```bash
git add internal/router_engine/selector.go internal/router_engine/selector_test.go internal/container/container.go
git commit -m "✨ feat(router_engine): Selector 持有 store + GetSticky/SetSticky/IsCandidateAlive"
```

---

## Task 2: Middleware 会话解析 + 集成

**Files:**
- Modify: `internal/router_engine/middleware.go`（`Middleware` ~39-158）
- Test: `internal/router_engine/middleware_test.go`（无则建）

- [ ] **Step 1: 写失败测试**

创建/追加 `internal/router_engine/middleware_test.go`（`package router_engine`）：

```go
func TestParseSessionHeaderPriority(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Id", "abc")
	body := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`)
	key, multi := parseSession(body, h)
	if key != "hdr:abc" {
		t.Fatalf("sessionKey = %q, want hdr:abc", key)
	}
	if !multi {
		t.Fatal("should be multi-turn (has assistant)")
	}
}

func TestParseSessionFallbackSHA1Stable(t *testing.T) {
	body1 := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	body2 := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`)
	k1, m1 := parseSession(body1, http.Header{})
	k2, m2 := parseSession(body2, http.Header{})
	if k1 != k2 {
		t.Fatalf("first-user-msg SHA1 should be stable across turns: %q vs %q", k1, k2)
	}
	if m1 {
		t.Fatal("single user msg → not multi-turn")
	}
	if !m2 {
		t.Fatal("has assistant → multi-turn")
	}
	if k1 == "" {
		t.Fatal("sessionKey should not be empty")
	}
}

func TestParseSessionArrayContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	k, _ := parseSession(body, http.Header{})
	// 与 string "hello" 的 hash 应一致（flatten 文本）
	kStr, _ := parseSession([]byte(`{"messages":[{"role":"user","content":"hello"}]}`), http.Header{})
	if k != kStr {
		t.Fatalf("array-of-blocks content should hash same as string: %q vs %q", k, kStr)
	}
}

func TestStickyStoreKeyVariesByModel(t *testing.T) {
	a := stickyStoreKey("msg:x", "auto")
	b := stickyStoreKey("msg:x", "gpt-4o")
	if a == b {
		t.Fatal("different model should yield different sticky key")
	}
	if a != stickyStoreKey("msg:x", "auto") {
		t.Fatal("same inputs should be stable")
	}
}
```

确认 import 含 `"net/http"`、`"testing"`。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/router_engine/ -run "TestParseSession|TestStickyStoreKey" -v`
Expected: 编译失败（`parseSession`/`stickyStoreKey` 未定义）。

- [ ] **Step 3: 实现 parseSession + stickyStoreKey**

在 `middleware.go` 加（import 补 `"crypto/sha1"`、`"encoding/hex"`、`"net/http"`）：

```go
// parseSession 提取会话标识与是否多轮。sessionKey 优先 X-Session-Id 头，
// 否则取首条 user 消息内容的 SHA1。isMultiTurn = messages 含 assistant。
func parseSession(bodyBytes []byte, header http.Header) (sessionKey string, isMultiTurn bool) {
	if v := strings.TrimSpace(header.Get("X-Session-Id")); v != "" {
		sessionKey = "hdr:" + v
	}
	var probe struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &probe); err != nil {
		return sessionKey, false
	}
	firstUser := ""
	for _, m := range probe.Messages {
		if m.Role == "assistant" {
			isMultiTurn = true
		}
		if firstUser == "" && m.Role == "user" {
			firstUser = flattenContent(m.Content)
		}
	}
	if sessionKey == "" && firstUser != "" {
		sum := sha1.Sum([]byte(firstUser))
		sessionKey = "msg:" + hex.EncodeToString(sum[:])
	}
	return sessionKey, isMultiTurn
}

// flattenContent 把 message content（string 或 array-of-blocks）拼成纯文本。
func flattenContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, part := range v {
			if pm, ok := part.(map[string]any); ok {
				if t, _ := pm["type"].(string); t == "text" {
					if s, _ := pm["text"].(string); s != "" {
						sb.WriteString(s)
					}
				}
			}
		}
		return sb.String()
	}
	return ""
}

// stickyStoreKey 由 sessionKey + requestedModel 派生稳定的 store key。
func stickyStoreKey(sessionKey, requestedModel string) string {
	sum := sha1.Sum([]byte(sessionKey + "|" + requestedModel))
	return "sticky:" + hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/router_engine/ -run "TestParseSession|TestStickyStoreKey" -v`
Expected: PASS。

- [ ] **Step 5: 集成进 Middleware**

在 `Middleware` 的 `model` 确定之后（约 line 72，`if model == "" { model = "auto" }` 之后）、`var picked *Candidate` 选择逻辑处改造。把现有的：

```go
	var picked *Candidate
	switch {
	case model == "auto":
		...
	default:
		...
	}
```

改为先查 sticky：

```go
	sessionKey, isMultiTurn := parseSession(bodyBytes, c.Request.Header)
	var stickyKey string
	if isMultiTurn && sessionKey != "" {
		stickyKey = stickyStoreKey(sessionKey, model)
		if cand := s.GetSticky(stickyKey); cand != nil && s.IsCandidateAlive(c.Request.Context(), cand) {
			picked = cand
		}
	}

	if picked == nil {
		switch {
		case model == "auto":
			est := estimateTokensFromBody(bodyBytes)
			picked, err = s.PickForAuto(c.Request.Context(), est)
		default:
			picked, err = s.PickByAlias(c.Request.Context(), model)
			if err != nil || picked == nil {
				picked, err = s.PickByExactName(c.Request.Context(), model)
			}
		}
	}
```

并在成功设定 candidate 之后（约 line 139 `c.Set("router_engine.candidate", picked)` 之后）刷新锁：

```go
	if stickyKey != "" {
		s.SetSticky(stickyKey, picked)
	}
```

> 注意 `picked`、`err` 变量已在外层声明（保持现有 `err` 处理路径不变：sticky 命中时 err 为 nil，picked 非 nil，跳过 line 86 的 err 分支自然成立）。

- [ ] **Step 6: 集成测试**

向 middleware_test.go 追加一个端到端用例（用 `store.NewMemoryStore()` 构造 Selector + seed 一个 alias→group→active key，或退一步只验证「命中 sticky 时不调用正常 Pick」）。最小可行：构造 Selector 带 store，手动 `SetSticky` 一个存活 candidate，跑 Middleware 第二轮（多轮 body），断言 `c.Get("router_engine.candidate")` 等于锁定的 candidate。若集成构造太重，BLOCKED 汇报，保留 Step 1 的纯函数测试即可。

Run: `go build ./... && go test ./internal/router_engine/ -v`
Expected: 通过。

- [ ] **Step 7: Commit**

```bash
git add internal/router_engine/middleware.go internal/router_engine/middleware_test.go
git commit -m "✨ feat(router_engine): Middleware 集成粘性会话 (X-Session-Id/首条消息 SHA1, 多轮锁定)"
```

---

## Task 3: 收尾验证

- [ ] **Step 1: 全量构建 + 测试**

Run: `go vet ./... && go test ./...`
Expected: 无 vet 报错；测试全绿。

- [ ] **Step 2: 零回归确认**

确认：store 为 nil 时 sticky 全 no-op；单轮请求（无 assistant）不读不写锁、走原选择逻辑；多轮命中失效自动回退。由 Task1 的 nil-store 测试 + Task2 的 parseSession/集成测试覆盖。

- [ ] **Step 3: 验收对照**

对照 spec 验收标准：多轮 auto 对话第二轮起复用首轮 candidate（不换模型）；锁定失效自动回退；store 为 nil/单轮零回归。

- [ ] **Step 4: 合并/PR**

按 finishing-a-development-branch（分支 `feat/sticky-session`）。

---

## Self-Review 记录

- **Spec coverage**：store 持有 + sticky 读写 + 存活校验→Task1；会话解析（X-Session-Id/SHA1/多轮/array-content flatten）+ stickyKey + Middleware 集成→Task2；零回归→Task1 nil-store + Task2 单轮用例。✅
- **Placeholder scan**：代码步骤均含完整代码；Task1 Step6 container 注册、Step5 helper 签名用 grep/Read 现场确认（行号/签名会漂移）；集成测试在构造过重时允许退到纯函数测试——有界实现指令，非 placeholder。
- **Type consistency**：`NewSelector(db, st)`、`Selector.store`、`GetSticky(key)→*Candidate`、`SetSticky(key, *Candidate)`、`IsCandidateAlive(ctx, *Candidate)→bool`、`parseSession(body, header)→(string,bool)`、`stickyStoreKey(session, model)→string`、`StickyTTL` 各 Task 间一致。✅
