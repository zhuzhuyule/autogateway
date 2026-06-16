# 密钥手动停用/启用 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** 给每个 key 加手动「停用/启用」开关:停用=独立 `disabled` 状态(移出 active_keys,不被请求选中,不被 CronChecker/熔断自动恢复),启用=恢复 active。

**Spec:** `docs/superpowers/specs/2026-06-16-key-manual-disable-design.md`

---

## Task 1: 后端 disabled 状态 + SetKeyEnabled + 端点

**Files:** `internal/models/types.go`、`internal/keypool/provider.go`、`internal/services/`(KeyService)、`internal/handler/key_handler.go`、`internal/router/router.go`、`internal/keypool/provider_test.go`

- [ ] **Step 1: 读现状** Read `keypool/provider.go` 的 `RestoreKeys`/`RestoreMultipleKeys`(DB+store 双写 + executeTransactionWithRetry 模式,SetKeyEnabled 照它)、`SyncGroupKeysFromDB`(invalid 分支,加 disabled 同款)、`SelectKey`(确认 status!=active 跳过)。Read `key_handler.go` 的 `RestoreMultipleKeys` handler + KeyService 调用分层。Read `models/types.go` 状态常量。

- [ ] **Step 2: 加状态常量** `models/types.go`:`KeyStatusDisabled = "disabled"`(紧跟 KeyStatusInvalid)。

- [ ] **Step 3: 写 keypool 失败测试** `provider_test.go` 加 `TestSetKeyEnabled`(MemoryStore + 最小 KeyProvider,参考现有 keypool 测试构造):
  - seed 1 个 active key(在 active_keys);`SetKeyEnabled(id, false)` → DB+store status=disabled、不在 active_keys;`SelectKey(group, Limits{})` 跳过它(返回 ErrNoActiveKeys 或别的 key)。
  - `SetKeyEnabled(id, true)` → status=active、在 active_keys、failure_count=0。

- [ ] **Step 4: 实现 SetKeyEnabled** `keypool/provider.go`:
```go
func (p *KeyProvider) SetKeyEnabled(keyID uint, enabled bool) error {
	targetStatus := models.KeyStatusDisabled
	if enabled {
		targetStatus = models.KeyStatusActive
	}
	keyHashKey := fmt.Sprintf("key:%d", keyID)
	err := p.executeTransactionWithRetry(func(tx *gorm.DB) error {
		var key models.APIKey
		if err := tx.Set("gorm:query_option", "FOR UPDATE").First(&key, keyID).Error; err != nil {
			return err // gorm.ErrRecordNotFound 上层转 404
		}
		updates := map[string]any{"status": targetStatus}
		if enabled {
			updates["failure_count"] = 0
		}
		if err := tx.Model(&key).Updates(updates).Error; err != nil {
			return err
		}
		activeListKey := fmt.Sprintf("group:%d:active_keys", key.GroupID)
		if enabled {
			_ = p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusActive, "failure_count": 0})
			_ = p.store.LRem(activeListKey, 0, keyID) // 防重复
			_ = p.store.LPush(activeListKey, keyID)
		} else {
			_ = p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusDisabled})
			_ = p.store.LRem(activeListKey, 0, keyID)
		}
		return nil
	})
	return err
}
```
(按 RestoreKeys 实际的 store 调用风格微调;key.GroupID 取自查到的 key。)

- [ ] **Step 5: SyncGroupKeysFromDB 加 disabled 分支** 在 `if k.Status == models.KeyStatusInvalid {...}` 之后加:
```go
if k.Status == models.KeyStatusDisabled {
    _ = p.store.LRem(activeListKey, 0, k.ID)
    _ = p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusDisabled})
    continue
}
```

- [ ] **Step 6: KeyService 转调** 看 KeyService 怎么包 keypool(RestoreKeys 那层),加 `SetKeyEnabled(keyID, enabled) error` 透传到 `keypoolProvider.SetKeyEnabled`。若 handler 直接调 keypool 则跳过此层。

- [ ] **Step 7: handler + 路由** `key_handler.go`:
```go
func (s *Server) DisableKey(c *gin.Context) { s.setKeyEnabled(c, false) }
func (s *Server) EnableKey(c *gin.Context)  { s.setKeyEnabled(c, true) }
// setKeyEnabled: parse :id → KeyService/keypool.SetKeyEnabled → gorm.ErrRecordNotFound 转 404 → response.Success
```
`router.go` keys 组加 `keys.POST("/:id/disable", ...)` + `keys.POST("/:id/enable", ...)`。

- [ ] **Step 8: 验证** `go build ./... && go test ./internal/keypool/ -run TestSetKeyEnabled -v` + `go test ./...` 无回归。

- [ ] **Step 9: Commit** `git add internal/ && git commit -m "✨ feat(keypool): 密钥手动停用/启用 (独立 disabled 状态 + SetKeyEnabled + 端点)"`

---

## Task 2: 前端停用/启用按钮

**Files:** `web/src/components/keys/KeyTable.vue`、`web/src/api/keys.ts`、3 locales

- [ ] **Step 1: 读现状** Read `KeyTable.vue` 行操作区(editKey/restoreKey/deleteKey 按钮渲染处)+ status 展示(active/invalid chip)+ KeyRow 类型(status 字段)。Read `api/keys.ts` 的 restoreKeys/updateKey 写法。

- [ ] **Step 2: api** `keys.ts` 加:
```ts
async disableKey(keyId: number): Promise<void> { await http.post(`/keys/${keyId}/disable`, {}); },
async enableKey(keyId: number): Promise<void> { await http.post(`/keys/${keyId}/enable`, {}); },
```
(路径前缀对齐现有 keys api。)

- [ ] **Step 3: 按钮 + 逻辑** 行操作区加「停用/启用」按钮:
  - `key.status === 'active'` → 显示"停用"(t keys.disableKey),点击 `disableKey(id)` → 成功本地 `key.status='disabled'` + message。
  - `key.status === 'disabled'` → 显示"启用"(t keys.enableKey),点击 `enableKey(id)` → `key.status='active'` + message。
  - `key.status === 'invalid'` → 不显示此开关(走现有 restore)。
  - loading 态防重复点击。

- [ ] **Step 4: status chip** 状态展示加 disabled 分支(灰色"已停用" t keys.statusDisabled),区别于 active/invalid。找现有 status chip 渲染处加。

- [ ] **Step 5: i18n** 三语加 `keys.disableKey`/`keys.enableKey`/`keys.statusDisabled`/`keys.keyDisabled`/`keys.keyEnabled`。

- [ ] **Step 6: 验证 + Commit** `cd web && npm run type-check`;`git add web/src/ && git commit -m "✨ feat(web): 密钥行手动停用/启用按钮 + disabled 状态展示"`

---

## Task 3: 收尾

- [ ] `go build ./... && go test ./... && cd web && npm run type-check` 全绿。
- [ ] 对照 spec 验收:停用→不被选中+CronChecker不恢复;启用→恢复;sync 保持 disabled;现有流程零回归。
- [ ] finishing-a-development-branch(分支 `feat/key-manual-disable`)。

---

## Self-Review
- **Spec coverage**: disabled 状态+SetKeyEnabled+Sync分支+端点→Task1;前端按钮+chip+api+i18n→Task2。✅
- **Placeholder**: SetKeyEnabled 给了完整代码;KeyService 分层/handler/前端按现有 RestoreKeys 模式现场对齐。
- **Type consistency**: `SetKeyEnabled(keyID uint, enabled bool) error`、`/keys/:id/disable`+`/enable`、`disableKey/enableKey`、`KeyStatusDisabled="disabled"` 各处一致。✅
- **关键**: disabled 独立于 invalid(CronChecker 只查 invalid 不碰)、SelectKey 已对非active跳过、Sync 加 disabled 分支防 mesh 当 active 恢复。
