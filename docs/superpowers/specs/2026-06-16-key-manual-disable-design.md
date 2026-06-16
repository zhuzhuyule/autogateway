# 密钥手动停用/启用 设计

> 给每个 API key 加「手动停用/启用」开关:停用后暂时不参与请求(不删除、统计保留),手动启用恢复。

## 背景与现状

key 当前只有 `active`/`invalid` 两态。痛点:
- 想临时停用某个 key(如同步产生的重复 key、想暂停某个额度紧的 key)只能删除,无法"暂停-恢复"。
- 不能复用 `invalid`:CronChecker **只自动探活 `invalid`** key 并尝试恢复,所以用 invalid 做手动停用会被自动恢复,违背"停用直到手动启用"。

已探明的有利现状:
- `SelectKey`(`keypool/provider.go:74`)对**任何 `status != active`** 的 key 都跳过 + 从 active_keys LRem。所以新增的 `disabled` 状态**天然被跳过**,无需改 SelectKey。
- CronChecker `validateGroupKeys` 只 `WHERE status = invalid` —— 不碰 disabled,**天然不会自动恢复**手动停用的 key。✓
- `SyncGroupKeysFromDB` 现处理 deleted/invalid/active,disabled 需补一个分支(否则 mesh 同步把 disabled 当 active 加回)。

## 目标

1. 新增独立 `disabled` 状态(区别于 invalid 熔断)。
2. 手动停用:`status=disabled` + 移出 active_keys → 不参与请求;不被 CronChecker/熔断自动恢复。
3. 手动启用:`status=active` + 加回 active_keys + 清 failure_count。
4. 前端每行「停用/启用」开关 + 状态展示。

## 非目标(YAGNI)

- 不做批量停用(先 per-key;批量后续可加)。
- 不做定时自动恢复(disabled 就是手动直到手动启用)。
- 不改 SelectKey(它对非 active 已正确跳过)、不改 CronChecker(它只管 invalid)。

## 设计

### 1. 后端状态

`internal/models/types.go` 加:
```go
KeyStatusDisabled = "disabled"
```

### 2. keypool: SetKeyEnabled

`internal/keypool/provider.go` 加(参考现有 RestoreKeys/handleFailure 的 DB+store 双写 + 事务模式):
```go
// SetKeyEnabled 手动停用/启用单个 key。
//   enabled=false → status=disabled + 从 active_keys 移除 (SelectKey 自然跳过)
//   enabled=true  → status=active + failure_count=0 + 加回 active_keys
// disabled 独立于 invalid: CronChecker 只探活 invalid, 不会自动恢复 disabled。
func (p *KeyProvider) SetKeyEnabled(keyID uint, enabled bool) error
```
- 用 `executeTransactionWithRetry` 包 DB 更新(status + 必要时 failure_count=0)。
- store 同步:`HSet status` + active_keys `LRem`(停用)/`LRem`+`LPush`(启用,先 LRem 防重复)。
- 停用:DB status=disabled、store HSet disabled、LRem active_keys。
- 启用:DB status=active + failure_count=0、store HSet active、LRem+LPush active_keys。

### 3. SyncGroupKeysFromDB 加 disabled 分支

在现有 `if k.Status == KeyStatusInvalid {...}` 旁加(同样语义:移出 active list + HSet disabled,**不加回**):
```go
if k.Status == models.KeyStatusDisabled {
    _ = p.store.LRem(activeListKey, 0, k.ID)
    _ = p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusDisabled})
    continue
}
```
确保 mesh 同步过来的 disabled key 保持停用,不被当 active 加回轮转池。

### 4. handler + 路由

`key_handler.go` 加:
```go
// POST /api/keys/:id/disable  和  POST /api/keys/:id/enable
func (s *Server) DisableKey(c *gin.Context)  // 调 KeyService → KeyProvider.SetKeyEnabled(id, false)
func (s *Server) EnableKey(c *gin.Context)   // SetKeyEnabled(id, true)
```
(经 KeyService 转调 keypool,与现有 UpdateKey/RestoreKeys 的分层一致。)
`router.go` keys 组加:
```go
keys.POST("/:id/disable", serverHandler.DisableKey)
keys.POST("/:id/enable", serverHandler.EnableKey)
```
注意路由顺序:`/:id/disable`/`/:id/enable` 与现有 `/:id`(PUT)、`/:id/notes`(PUT) 同为参数路由,gin 能区分 method+子路径。

### 5. 前端

- `KeyTable.vue` 每行操作区加「停用/启用」按钮:`status==='active'` 显示"停用"(调 disable),`status==='disabled'` 显示"启用"(调 enable)。`invalid` 不显示此开关(用现有 restore)。
- 状态展示:disabled 用独立 chip(灰色"已停用"),区别于 active(绿)/invalid(红)。
- `api/keys.ts` 加 `disableKey(id)` / `enableKey(id)` 调对应端点。
- 调用成功后本地更新该行 status + 视情况 emit refresh。
- 三语 i18n:`keys.disableKey`/`keys.enableKey`/`keys.statusDisabled`/`keys.keyDisabled`/`keys.keyEnabled`。

## 错误处理

- SetKeyEnabled 对不存在的 key 返回 ErrRecordNotFound。
- store 操作失败记日志但 DB 为准(下次 sync 对齐)。
- 已是目标状态(停用已 disabled / 启用已 active)→ 幂等,不报错。

## 测试

- keypool `SetKeyEnabled` 单测(MemoryStore):停用 → status=disabled + 不在 active_keys;启用 → active + 在 active_keys + failure_count=0;SelectKey 跳过 disabled。
- SyncGroupKeysFromDB:DB 中 disabled key 同步后不在 active_keys。
- 回归:CronChecker 不恢复 disabled(只查 invalid);现有 active/invalid 流程不变。
- 前端 type-check。

## 验收标准

- `go build ./... && go test ./internal/keypool/` + 前端 type-check 通过。
- 点"停用":该 key status=disabled、不再被请求选中、CronChecker 不自动恢复。
- 点"启用":恢复 active、重新参与请求。
- 多实例:disabled 经 mesh 同步保持停用(SyncGroupKeysFromDB 分支)。
- 现有 active/invalid/熔断/探活流程零回归。

## 影响面

- 后端:`internal/models/types.go`(状态常量)、`internal/keypool/provider.go`(SetKeyEnabled + Sync 分支)、`internal/services`(KeyService 转调)、`internal/handler/key_handler.go`(2 handler)、`internal/router/router.go`(2 路由)。
- 前端:`KeyTable.vue`(按钮+状态chip)、`api/keys.ts`、3 locales。
- 无 DB schema migration(status 字段已存在,只是新增取值)。
