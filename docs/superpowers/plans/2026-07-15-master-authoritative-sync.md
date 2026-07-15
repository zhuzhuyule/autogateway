# 主从同步架构（Master-Authoritative Sync）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 mesh 同步从"对称双向 P2P + LWW"改为"单 master 权威 + follower 全量快照镜像 + master 集中排除清单 + follower 手动上行迁移",根治反复出现的分裂态不一致,并顺带止血已污染数据。

**Architecture:** master(HK, `IS_MASTER=true`)是唯一权威源;follower 定期 pull master 的**全量快照**,按 master 下发的 `sync_policy`(排除清单)做**镜像替换**(多退少补,不比时间戳)。follower 本地改动默认不外传,仅手动迁移 push 给 master。master 端保留 `ProcessPayload` 的 upsert 语义接收迁移。

**Tech Stack:** Go 1.25 + GORM + glebarez/sqlite;既有 WS push / HTTP pull / nacl-box 加密 / SyncPeer 基础设施复用。

参考 spec:`docs/superpowers/specs/2026-07-15-master-authoritative-sync-design.md`

---

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/services/sync_policy.go` | 新建 | `SyncPolicy` 结构、默认排除清单、字段/类别裁剪 helper、从 payload 读取 policy |
| `internal/services/sync_snapshot.go` | 新建 | `ApplySnapshot`(follower 镜像替换) + 其 remap/删除辅助 |
| `internal/services/sync_service.go` | 改 | `SyncPayload` 加 `Policy` 字段;`ExportSnapshot` 薄封装 `ExportPayload(nil)`+policy;抽 `preserveExcludedFields` |
| `internal/services/sync_peer_manager.go` | 改 | `pullOnePeer` 按角色分支;follower 关闭自动 push |
| `internal/handler/sync_handler.go` | 改 | `PullEndpoint` master 返回全量快照+policy;policy CRUD |
| `internal/app/app.go` | 改 | 移除 `V2_5_25_CleanupOrphanKeys` 注册 |
| `web/src/components/v3/PeerSyncPanel.vue` | 改 | master 排除清单 UI + follower"本地改动需迁移"提示 |

测试文件:`internal/services/sync_snapshot_test.go`、`internal/services/sync_policy_test.go`。

---

## 阶段 A — 止血 + 镜像核心

### Task 1: 移除有害的 V2_5_25 迁移

**Files:**
- Modify: `internal/app/app.go`（移除 V2_5_25 调用块）
- Modify: `internal/db/migrations/v2_5_25_CleanupOrphanKeys.go`（改成 no-op 保留函数签名，避免其它引用编译失败；正文清空）

- [ ] **Step 1: 把迁移正文改成 no-op**

`internal/db/migrations/v2_5_25_CleanupOrphanKeys.go` 整个函数体替换为：

```go
// V2_5_25_CleanupOrphanKeys 已废弃 — 曾用 deleted_at=now() 软删孤儿 key, 但该"当前时刻
// 墓碑"经 (group_name,key_hash) 业务键同步会 LWW 覆盖其它节点的合法 key(见 2026-07-15
// spec)。孤儿由 keypool LoadKeysFromDB(C) 兜底不加载即可, 无需主动删。保留空函数避免
// app.go 历史引用编译失败;新部署不再调用。
func V2_5_25_CleanupOrphanKeys(db *gorm.DB) error {
	_ = db
	return nil
}
```

- [ ] **Step 2: 从 app.go 移除调用**

删除 `internal/app/app.go` 中这一块（约 156-159 行）：

```go
		// P9: 清理指向已删/不存在分组的孤儿活 key(存量脏数据); 合并侧 D'+加载侧 C 已堵新增.
		if err := db.V2_5_25_CleanupOrphanKeys(a.db); err != nil {
			return fmt.Errorf("V2_5_25 cleanup orphan api keys failed: %w", err)
		}
```

- [ ] **Step 3: 编译验证**

Run: `go build ./...`
Expected: 无错误。

- [ ] **Step 4: Commit**

```bash
git add internal/app/app.go internal/db/migrations/v2_5_25_CleanupOrphanKeys.go
git commit -m "🐛 fix(sync): 废弃 V2_5_25 孤儿清理迁移(now 墓碑污染同步)"
```

---

### Task 2: SyncPolicy 结构 + 默认排除清单

**Files:**
- Create: `internal/services/sync_policy.go`
- Test: `internal/services/sync_policy_test.go`
- Modify: `internal/services/sync_service.go`（`SyncPayload` 加 `Policy *SyncPolicy`）

- [ ] **Step 1: 写失败测试**

`internal/services/sync_policy_test.go`：

```go
package services

import "testing"

func TestDefaultSyncPolicy_ExcludesLocalFields(t *testing.T) {
	p := DefaultSyncPolicy()
	if !p.IsFieldExcluded("group", "proxy_url") {
		t.Fatal("group.proxy_url 应默认排除")
	}
	if !p.IsFieldExcluded("setting", "app_url") {
		t.Fatal("setting.app_url 应默认排除")
	}
	// 未列入的字段默认同步(不排除)
	if p.IsFieldExcluded("group", "channel_type") {
		t.Fatal("group.channel_type 不应排除")
	}
}

func TestSyncPolicy_CategoryExcluded(t *testing.T) {
	p := &SyncPolicy{ExcludedCategories: []string{"setting"}}
	if !p.IsCategoryExcluded("setting") {
		t.Fatal("setting 类别应排除")
	}
	if p.IsCategoryExcluded("key") {
		t.Fatal("key 类别不应排除")
	}
}

func TestSyncPolicy_NilSafe(t *testing.T) {
	var p *SyncPolicy // nil = 全同步
	if p.IsCategoryExcluded("group") || p.IsFieldExcluded("group", "proxy_url") {
		t.Fatal("nil policy 应视为全同步(不排除任何东西)")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/services/ -run TestSyncPolicy -run TestDefaultSyncPolicy`
Expected: FAIL（`SyncPolicy` / `DefaultSyncPolicy` 未定义）。

- [ ] **Step 3: 实现 sync_policy.go**

```go
package services

// SyncPolicy 是 master 集中定义的"哪些不同步"规则。默认空 = 全同步; master 新增字段
// 因不在清单里, 默认自动纳入同步。随快照下发给 follower, 由 follower 在 ApplySnapshot
// 时执行(排除的类别整体跳过, 排除的字段保留本地值)。
//
// 类别取值: group / subgroup / key / alias / setting。
type SyncPolicy struct {
	ExcludedCategories []string            `json:"excludedCategories"`
	ExcludedFields     map[string][]string `json:"excludedFields"`
}

// DefaultSyncPolicy 预置"本机专属字段"排除 — 每台机器不同, 必须本地自治。
func DefaultSyncPolicy() *SyncPolicy {
	return &SyncPolicy{
		ExcludedCategories: []string{},
		ExcludedFields: map[string][]string{
			// Group.Config 里的 proxy_url 是本机代理地址
			"group": {"proxy_url"},
			// SystemSettings 里的本机地址 / 代理 / 同步自身配置
			"setting": {"app_url", "proxy_url", "sync_enabled", "sync_key"},
		},
	}
}

func (p *SyncPolicy) IsCategoryExcluded(category string) bool {
	if p == nil {
		return false
	}
	for _, c := range p.ExcludedCategories {
		if c == category {
			return true
		}
	}
	return false
}

func (p *SyncPolicy) IsFieldExcluded(category, field string) bool {
	if p == nil || p.ExcludedFields == nil {
		return false
	}
	for _, f := range p.ExcludedFields[category] {
		if f == field {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: SyncPayload 加 Policy 字段**

`internal/services/sync_service.go` 的 `SyncPayload` struct（约 45-52 行）末尾加一行：

```go
	// Policy 由 master 在导出快照时带上, follower 据此裁剪镜像(nil = 全同步)。
	Policy *SyncPolicy `json:"policy,omitempty"`
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/services/ -run 'TestSyncPolicy|TestDefaultSyncPolicy'`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/services/sync_policy.go internal/services/sync_policy_test.go internal/services/sync_service.go
git commit -m "✨ feat(sync): SyncPolicy 排除清单结构 + 默认本机字段"
```

---

### Task 3: preserveExcludedFields 字段裁剪 helper

**Files:**
- Modify: `internal/services/sync_service.go`（把 `preserveLocalProxyURL` 泛化为按 policy 保留字段）
- Test: `internal/services/sync_policy_test.go`（追加）

- [ ] **Step 1: 写失败测试（追加到 sync_policy_test.go）**

```go
func TestPreserveExcludedGroupFields(t *testing.T) {
	policy := DefaultSyncPolicy()
	incoming := &models.Group{Name: "g", Config: models.JSONMap{"proxy_url": "http://master-proxy", "channel_type": "openai"}}
	existing := &models.Group{Name: "g", Config: models.JSONMap{"proxy_url": "http://local-proxy"}}

	preserveExcludedGroupFields(incoming, existing, policy)

	if incoming.Config["proxy_url"] != "http://local-proxy" {
		t.Fatalf("proxy_url 应保留本地, got %v", incoming.Config["proxy_url"])
	}
	if incoming.Config["channel_type"] != "openai" {
		t.Fatal("非排除字段应跟随 master")
	}
}

func TestPreserveExcludedGroupFields_LocalMissing(t *testing.T) {
	policy := DefaultSyncPolicy()
	incoming := &models.Group{Name: "g", Config: models.JSONMap{"proxy_url": "http://master-proxy"}}
	existing := &models.Group{Name: "g", Config: models.JSONMap{}} // 本地没配 proxy

	preserveExcludedGroupFields(incoming, existing, policy)

	// 本地没值 → 删掉 incoming 的, 不从 master 继承本机字段
	if _, ok := incoming.Config["proxy_url"]; ok {
		t.Fatal("本地无 proxy_url 时应删除 incoming 的, 不继承 master")
	}
}
```

需要确认 `models` 已在 test 文件 import；若无则加 `"autogateway/internal/models"`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/services/ -run TestPreserveExcludedGroupFields`
Expected: FAIL（`preserveExcludedGroupFields` 未定义）。

- [ ] **Step 3: 实现 —— 在 sync_service.go 现有 preserveLocalProxyURL 下方新增**

```go
// preserveExcludedGroupFields 按 policy 让 group.Config 里被排除的字段保留本机值 —
// preserveLocalProxyURL 的通用化: 对 policy.ExcludedFields["group"] 里的每个字段, 用
// existing(本机现有)的值覆盖 incoming(master)的值; 本机没有该值则从 incoming 删掉,
// 不继承 master 的本机专属配置。existing 为 nil(本地全新记录)时全部删掉。
func preserveExcludedGroupFields(incoming *models.Group, existing *models.Group, policy *SyncPolicy) {
	if policy == nil {
		return
	}
	for _, field := range policy.ExcludedFields["group"] {
		var local any
		hasLocal := false
		if existing != nil && existing.Config != nil {
			local, hasLocal = existing.Config[field]
		}
		if incoming.Config == nil {
			if !hasLocal {
				continue
			}
			incoming.Config = models.JSONMap{}
		}
		if hasLocal {
			incoming.Config[field] = local
		} else {
			delete(incoming.Config, field)
		}
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/services/ -run TestPreserveExcludedGroupFields`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/services/sync_service.go internal/services/sync_policy_test.go
git commit -m "✨ feat(sync): preserveExcludedGroupFields — 按 policy 保留本机字段"
```

---

### Task 4: ApplySnapshot 镜像替换核心

**Files:**
- Create: `internal/services/sync_snapshot.go`
- Test: `internal/services/sync_snapshot_test.go`

镜像语义（对每个未被 `ExcludedCategories` 排除的类别）：master 有→upsert(排除字段保留本地)、master 无→本地活记录软删。跨端 `group_id` 用 name 建 remap 表。复用现有 `syncMergeSave`、`affectedKeyGroups`→keypool invalidate 模式；不比较时间戳（master 权威）。

- [ ] **Step 1: 写失败测试（三情形 + 分裂态收敛）**

`internal/services/sync_snapshot_test.go`：

```go
package services

import (
	"context"
	"testing"

	"autogateway/internal/models"
)

// helper: 建一个内存 sqlite + SyncService(见 sync_service_test.go 的 setup 风格)
// 复用 newTestSyncService(t) — 若不存在, 参照 sync_service_test.go 抽一个。

func TestApplySnapshot_CreatesMissing(t *testing.T) {
	s, db := newTestSyncService(t)
	// master 快照有一个 group + 一个 key, 本地空
	snap := &SyncPayload{
		Groups:  []models.Group{{Name: "g1", ChannelType: "openai"}},
		APIKeys: []models.APIKey{{GroupID: 1, KeyValue: "k1", KeyHash: "h1", Status: models.KeyStatusActive}},
	}
	if err := s.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var gc, kc int64
	db.Model(&models.Group{}).Where("deleted_at IS NULL").Count(&gc)
	db.Model(&models.APIKey{}).Where("deleted_at IS NULL").Count(&kc)
	if gc != 1 || kc != 1 {
		t.Fatalf("期望镜像出 1 group / 1 key, got %d/%d", gc, kc)
	}
}

func TestApplySnapshot_DeletesExtra(t *testing.T) {
	s, db := newTestSyncService(t)
	// 本地先有 g1(k1) 和 g1(k2), master 快照只有 g1(k1) → k2 应被镜像软删
	db.Create(&models.Group{Name: "g1", ChannelType: "openai"})
	db.Create(&models.APIKey{GroupID: 1, KeyValue: "k1", KeyHash: "h1", Status: models.KeyStatusActive})
	db.Create(&models.APIKey{GroupID: 1, KeyValue: "k2", KeyHash: "h2", Status: models.KeyStatusActive})

	snap := &SyncPayload{
		Groups:  []models.Group{{Name: "g1", ChannelType: "openai"}},
		APIKeys: []models.APIKey{{GroupID: 1, KeyValue: "k1", KeyHash: "h1", Status: models.KeyStatusActive}},
	}
	if err := s.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var alive int64
	db.Model(&models.APIKey{}).Where("deleted_at IS NULL").Count(&alive)
	if alive != 1 {
		t.Fatalf("多余的 k2 应被镜像软删, 剩 %d 活 key", alive)
	}
}

func TestApplySnapshot_SplitBrainConverges(t *testing.T) {
	s, db := newTestSyncService(t)
	// 分裂态: 本地 h1 是"活"的, master 快照里 h1 不存在(master 认为已删)
	db.Create(&models.Group{Name: "agnes", ChannelType: "openai"})
	db.Create(&models.APIKey{GroupID: 1, KeyValue: "k1", KeyHash: "h1", Status: models.KeyStatusActive})
	// master 快照: group 在, 但没有任何 key(等价 master 侧该 key 是墓碑/不存在)
	snap := &SyncPayload{Groups: []models.Group{{Name: "agnes", ChannelType: "openai"}}}
	if err := s.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var alive int64
	db.Model(&models.APIKey{}).Where("group_id=1 AND deleted_at IS NULL").Count(&alive)
	if alive != 0 {
		t.Fatal("镜像后应随 master 删除本地多余活 key(分裂态收敛)")
	}
}

func TestApplySnapshot_ExcludedCategorySkipped(t *testing.T) {
	s, db := newTestSyncService(t)
	db.Create(&models.SystemSetting{SettingKey: "app_url", SettingValue: "http://local"})
	snap := &SyncPayload{
		Policy:   &SyncPolicy{ExcludedCategories: []string{"setting"}},
		Settings: []models.SystemSetting{{SettingKey: "app_url", SettingValue: "http://master"}},
	}
	if err := s.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var v models.SystemSetting
	db.Where("setting_key = ?", "app_url").First(&v)
	if v.SettingValue != "http://local" {
		t.Fatal("setting 类别被排除, 不应被 master 覆盖")
	}
}
```

> 注:若 `newTestSyncService(t) (*SyncService, *gorm.DB)` 尚不存在,先从 `sync_service_test.go` 现有 setup 抽取一个返回 `(service, db)` 的 helper（同一步 commit）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/services/ -run TestApplySnapshot`
Expected: FAIL（`ApplySnapshot` 未定义）。

- [ ] **Step 3: 实现 ApplySnapshot（sync_snapshot.go）**

```go
package services

import (
	"context"
	"errors"
	"fmt"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

// ApplySnapshot 是 follower 的镜像替换: 把本地"在同步范围内的类别"对齐到 master 的全量
// 快照 —— master 有则 upsert(排除字段保留本地), master 无则本地活记录软删。不比较任何
// 时间戳(master 即真值), 因此不可能出现 LWW 分裂态。
//
// 与 ProcessPayload(LWW 合并, master 侧接收迁移用)刻意分开: 这里是单向权威, 语义清晰,
// 便于独立测试。跨端 group_id 用 name 建 remap 表, 与 ProcessPayload 一致。
func (s *SyncService) ApplySnapshot(ctx context.Context, snap *SyncPayload) error {
	if snap == nil {
		return nil
	}
	ctx = context.WithValue(ctx, syncMergeKey{}, true)
	policy := snap.Policy
	affectedKeyGroups := make(map[uint]bool)

	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// group name → 本端 group_id 的映射, 供 subgroup/key remap。
		groupIDByName := map[string]uint{}

		// ---- 1. Groups(除非被排除) ----
		if !policy.IsCategoryExcluded("group") {
			masterNames := map[string]bool{}
			for i := range snap.Groups {
				incoming := snap.Groups[i]
				masterNames[incoming.Name] = true
				var existing models.Group
				err := tx.Unscoped().Where("name = ?", incoming.Name).First(&existing).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					// 新建: master 的本机字段不继承
					preserveExcludedGroupFields(&incoming, nil, policy)
					incoming.ID = 0
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("snapshot create group %s: %w", incoming.Name, err)
					}
					groupIDByName[incoming.Name] = incoming.ID
				} else {
					// 覆盖(master 权威), 但保留本机字段 + 复用本端 id
					preserveExcludedGroupFields(&incoming, &existing, policy)
					incoming.ID = existing.ID
					if err := syncMergeSave(tx, &incoming); err != nil {
						return fmt.Errorf("snapshot update group %s: %w", incoming.Name, err)
					}
					groupIDByName[incoming.Name] = existing.ID
				}
			}
			// master 没有的本地活 group → 镜像软删(连带其 key)
			var localGroups []models.Group
			if err := tx.Where("deleted_at IS NULL").Find(&localGroups).Error; err != nil {
				return err
			}
			for _, lg := range localGroups {
				if masterNames[lg.Name] {
					continue
				}
				if err := tx.Delete(&models.Group{}, lg.ID).Error; err != nil {
					return fmt.Errorf("snapshot delete extra group %s: %w", lg.Name, err)
				}
				res := tx.Where("group_id = ? AND deleted_at IS NULL", lg.ID).Delete(&models.APIKey{})
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected > 0 {
					affectedKeyGroups[lg.ID] = true
				}
			}
		} else {
			// group 类别被排除: 用本地现有 name→id 填 remap 表, 供 key/subgroup 用
			var localGroups []models.Group
			if err := tx.Where("deleted_at IS NULL").Find(&localGroups).Error; err != nil {
				return err
			}
			for _, lg := range localGroups {
				groupIDByName[lg.Name] = lg.ID
			}
		}

		// ---- 2. APIKeys(除非被排除) ----
		if !policy.IsCategoryExcluded("key") {
			// master 快照里 key 的 group 归属: 用 snap.Groups 的 (原 group_id → name),
			// 再 name→本端 id 映射。构造 master 侧 groupID→name。
			masterGroupName := map[uint]string{}
			for _, g := range snap.Groups {
				masterGroupName[g.ID] = g.Name
			}
			// 本端 group_id → master 该 group 应有的 key_hash 集合
			type keyKey struct {
				GroupID uint
				Hash    string
			}
			masterKeys := map[keyKey]bool{}
			for i := range snap.APIKeys {
				incoming := snap.APIKeys[i]
				name := masterGroupName[incoming.GroupID]
				localGID, ok := groupIDByName[name]
				if !ok {
					continue // 对应 group 不在同步范围/不存在, 跳过该 key
				}
				incoming.GroupID = localGID
				masterKeys[keyKey{localGID, incoming.KeyHash}] = true

				var existing models.APIKey
				q := tx.Unscoped().Where("group_id = ? AND key_hash = ?", localGID, incoming.KeyHash)
				err := q.First(&existing).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					incoming.ID = 0
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("snapshot create key: %w", err)
					}
				} else {
					incoming.ID = existing.ID
					if err := syncMergeSave(tx, &incoming); err != nil {
						return fmt.Errorf("snapshot update key: %w", err)
					}
				}
				affectedKeyGroups[localGID] = true
			}
			// master 无、本地有的活 key → 镜像软删(仅限同步范围内的 group)
			for name, localGID := range groupIDByName {
				_ = name
				var localKeys []models.APIKey
				if err := tx.Where("group_id = ? AND deleted_at IS NULL", localGID).Find(&localKeys).Error; err != nil {
					return err
				}
				for _, lk := range localKeys {
					if masterKeys[keyKey{localGID, lk.KeyHash}] {
						continue
					}
					if err := tx.Delete(&models.APIKey{}, lk.ID).Error; err != nil {
						return err
					}
					affectedKeyGroups[localGID] = true
				}
			}
		}

		// ---- 3. Settings(除非被排除) ----
		if !policy.IsCategoryExcluded("setting") {
			for _, incoming := range snap.Settings {
				if policy.IsFieldExcluded("setting", incoming.SettingKey) {
					continue // 排除字段: 保留本地(SystemSetting 是 kv, 字段=SettingKey)
				}
				var existing models.SystemSetting
				err := tx.Unscoped().Where("setting_key = ?", incoming.SettingKey).First(&existing).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					incoming.ID = 0
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("snapshot create setting %s: %w", incoming.SettingKey, err)
					}
				} else {
					incoming.ID = existing.ID
					if err := syncMergeSave(tx, &incoming); err != nil {
						return fmt.Errorf("snapshot update setting %s: %w", incoming.SettingKey, err)
					}
				}
			}
			// setting 一般不做镜像删除(避免误删本机自治项); 只 upsert master 有的。
		}

		// ---- 4. ModelAliases / SubGroups: 同 group 镜像思路, 见下方 helper ----
		if err := s.applySnapshotAliases(tx, snap, policy); err != nil {
			return err
		}
		if err := s.applySnapshotSubGroups(tx, snap, policy, groupIDByName); err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}

	// 事务外: 让 keypool 跟 db 重新对齐(软删的 key 从 active_keys 摘除)。
	for gid := range affectedKeyGroups {
		if err := s.keypoolInvalidator.SyncGroupKeysFromDB(gid); err != nil {
			// 不上抛: db 已是真值, 下次启动 LoadKeysFromDB 兜底。
			continue
		}
	}
	return nil
}
```

- [ ] **Step 4: 实现 alias/subgroup helper（同 sync_snapshot.go 追加）**

```go
// applySnapshotAliases 镜像 model_aliases: 按 (alias, real_model) 业务键 upsert +
// master 无则软删。alias 不引用 group_id, 无需 remap。
func (s *SyncService) applySnapshotAliases(tx *gorm.DB, snap *SyncPayload, policy *SyncPolicy) error {
	if policy.IsCategoryExcluded("alias") {
		return nil
	}
	type ak struct{ Alias, Real string }
	master := map[ak]bool{}
	for i := range snap.ModelAliases {
		in := snap.ModelAliases[i]
		master[ak{in.Alias, in.RealModel}] = true
		var existing models.ModelAlias
		err := tx.Unscoped().Where("alias = ? AND real_model = ?", in.Alias, in.RealModel).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			in.ID = 0
			if err := tx.Create(&in).Error; err != nil {
				return fmt.Errorf("snapshot create alias %s: %w", in.Alias, err)
			}
		} else {
			in.ID = existing.ID
			if err := syncMergeSave(tx, &in); err != nil {
				return fmt.Errorf("snapshot update alias %s: %w", in.Alias, err)
			}
		}
	}
	var local []models.ModelAlias
	if err := tx.Where("deleted_at IS NULL").Find(&local).Error; err != nil {
		return err
	}
	for _, la := range local {
		if master[ak{la.Alias, la.RealModel}] {
			continue
		}
		if err := tx.Delete(&models.ModelAlias{}, la.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// applySnapshotSubGroups 镜像聚合关系: 按 (group name, subgroup name) 业务键。用
// groupIDByName 把 master 的 group_id/sub_group_id remap 到本端。
func (s *SyncService) applySnapshotSubGroups(tx *gorm.DB, snap *SyncPayload, policy *SyncPolicy, groupIDByName map[string]uint) error {
	if policy.IsCategoryExcluded("subgroup") {
		return nil
	}
	// master group_id → name
	nameByMasterID := map[uint]string{}
	for _, g := range snap.Groups {
		nameByMasterID[g.ID] = g.Name
	}
	type pair struct{ G, S uint }
	master := map[pair]bool{}
	for i := range snap.SubGroups {
		in := snap.SubGroups[i]
		gName, sName := nameByMasterID[in.GroupID], nameByMasterID[in.SubGroupID]
		gID, gok := groupIDByName[gName]
		sID, sok := groupIDByName[sName]
		if !gok || !sok {
			continue
		}
		in.GroupID, in.SubGroupID = gID, sID
		master[pair{gID, sID}] = true
		var existing models.GroupSubGroup
		err := tx.Unscoped().Where("group_id = ? AND sub_group_id = ?", gID, sID).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			in.ID = 0
			if err := tx.Create(&in).Error; err != nil {
				return err
			}
		} else {
			in.ID = existing.ID
			if err := syncMergeSave(tx, &in); err != nil {
				return err
			}
		}
	}
	// 镜像删除本地多余聚合关系(仅同步范围内 group)
	var local []models.GroupSubGroup
	if err := tx.Where("deleted_at IS NULL").Find(&local).Error; err != nil {
		return err
	}
	for _, ls := range local {
		if master[pair{ls.GroupID, ls.SubGroupID}] {
			continue
		}
		if err := tx.Delete(&models.GroupSubGroup{}, ls.ID).Error; err != nil {
			return err
		}
	}
	return nil
}
```

> 实现注意:确认 `models.ModelAlias` 有 `RealModel` 字段、`models.GroupSubGroup` 有 `GroupID`/`SubGroupID` 字段(查 `internal/models/types.go` 校准命名);若命名不同按实际改。`syncMergeSave` 已在 sync_service.go 定义。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/services/ -run TestApplySnapshot -v`
Expected: 四个用例 PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/services/sync_snapshot.go internal/services/sync_snapshot_test.go
git commit -m "✨ feat(sync): ApplySnapshot — follower 全量快照镜像替换(废弃 LWW)"
```

---

### Task 5: pullOnePeer 按角色分支

**Files:**
- Modify: `internal/services/sync_peer_manager.go`（`pullOnePeer` 的 merge 段）

- [ ] **Step 1: 改 pullOnePeer 的 ProcessPayload 调用点（约 556 行）**

把：

```go
	if err := m.syncService.ProcessPayload(ctx, payload); err != nil {
```

改为按角色分支：

```go
	// 主从: follower(非 master) 用镜像替换, 恒等于 master; master 不 pull follower 的
	// 自动同步(见 doPull 里对 master 的短路), 这里保留 ProcessPayload 仅作过渡兜底。
	var mergeErr error
	if !m.syncService.configManager.IsMaster() {
		mergeErr = m.syncService.ApplySnapshot(ctx, payload)
	} else {
		mergeErr = m.syncService.ProcessPayload(ctx, payload)
	}
	if mergeErr != nil {
```

并把该 if 块内后续 `err` 引用改为 `mergeErr`（错误日志、writeLog、return）。

- [ ] **Step 2: master 不自动 pull follower —— 在 doPull 顶部短路（约 459 行）**

`doPull` 开头 `if !settings.SyncEnabled { return }` 之后加：

```go
	// 主从: master 是权威源, 不自动 pull follower(避免把 follower 本地态吸回来)。
	// follower→master 只走用户手动迁移(PushPeer)。
	if m.syncService.configManager.IsMaster() {
		return
	}
```

- [ ] **Step 3: 编译 + 全量测试**

Run: `go build ./... && go test ./internal/services/...`
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add internal/services/sync_peer_manager.go
git commit -m "✨ feat(sync): pull 按角色分支 — follower 镜像 / master 不自动 pull"
```

---

### Task 6: PullEndpoint 返回全量快照 + policy

**Files:**
- Modify: `internal/handler/sync_handler.go`（`PullEndpoint`）
- Modify: `internal/services/sync_service.go`（新增 `ExportSnapshot`）

- [ ] **Step 1: 新增 ExportSnapshot（sync_service.go，ExportPayload 下方）**

```go
// ExportSnapshot 是 master 给 follower 的全量快照: 忽略 since(始终全量) + 附带 sync_policy。
// follower 用 ApplySnapshot 镜像它。数据量小(几十条), 全量无压力, 且是根治一致性的关键
// (增量无法表达"本地多出来的该删")。
func (s *SyncService) ExportSnapshot(ctx context.Context) (*SyncPayload, error) {
	payload, err := s.ExportPayload(ctx, nil) // 全量
	if err != nil {
		return nil, err
	}
	payload.Policy = s.LoadSyncPolicy(ctx)
	return payload, nil
}
```

`LoadSyncPolicy` 在 Task 7 实现;本 task 先临时返回 `DefaultSyncPolicy()` 占位:

```go
// 临时: Task 7 换成从 sync_policy setting 读。
func (s *SyncService) LoadSyncPolicy(ctx context.Context) *SyncPolicy {
	_ = ctx
	return DefaultSyncPolicy()
}
```

- [ ] **Step 2: PullEndpoint 用 ExportSnapshot（约 410-411 行）**

把：

```go
	// 启用同步即同步全部 — 不再区分 sync_api_keys
	payload, err := h.syncService.ExportPayload(c.Request.Context(), since)
```

改为：

```go
	// 主从: master 给 follower 的是"全量快照 + policy"(忽略 since)。
	_ = since // 主从模式不用增量 since
	payload, err := h.syncService.ExportSnapshot(c.Request.Context())
```

- [ ] **Step 3: 编译验证**

Run: `go build ./...`
Expected: 无错误。

- [ ] **Step 4: Commit**

```bash
git add internal/services/sync_service.go internal/handler/sync_handler.go
git commit -m "✨ feat(sync): PullEndpoint 返回全量快照 + policy(ExportSnapshot)"
```

---

## 阶段 B — 排除清单持久化与管理

### Task 7: SyncPolicy 持久化(SystemSetting)

**Files:**
- Modify: `internal/services/sync_policy.go`（`LoadSyncPolicy` / `SaveSyncPolicy`）
- Modify: `internal/services/sync_service.go`（移除 Task 6 的临时 `LoadSyncPolicy`）
- Test: `internal/services/sync_policy_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestSaveLoadSyncPolicy_RoundTrip(t *testing.T) {
	s, _ := newTestSyncService(t)
	ctx := context.Background()
	p := &SyncPolicy{ExcludedCategories: []string{"alias"}, ExcludedFields: map[string][]string{"group": {"proxy_url"}}}
	if err := s.SaveSyncPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	got := s.LoadSyncPolicy(ctx)
	if !got.IsCategoryExcluded("alias") || !got.IsFieldExcluded("group", "proxy_url") {
		t.Fatal("round-trip 丢失 policy")
	}
}

func TestLoadSyncPolicy_DefaultWhenAbsent(t *testing.T) {
	s, _ := newTestSyncService(t)
	got := s.LoadSyncPolicy(context.Background())
	if !got.IsFieldExcluded("group", "proxy_url") {
		t.Fatal("无存储时应回退默认 policy")
	}
}
```

- [ ] **Step 2: 跑确认失败**

Run: `go test ./internal/services/ -run 'TestSaveLoadSyncPolicy|TestLoadSyncPolicy'`
Expected: FAIL。

- [ ] **Step 3: 实现（sync_policy.go 追加），并删除 sync_service.go 里 Task 6 的临时版**

```go
import (
	"context"
	"encoding/json"

	"autogateway/internal/models"
)

const syncPolicySettingKey = "sync_policy"

// LoadSyncPolicy 从 sync_policy setting 读; 缺失/解析失败 → DefaultSyncPolicy。
func (s *SyncService) LoadSyncPolicy(ctx context.Context) *SyncPolicy {
	var row models.SystemSetting
	err := s.db.WithContext(ctx).Where("setting_key = ?", syncPolicySettingKey).First(&row).Error
	if err != nil {
		return DefaultSyncPolicy()
	}
	var p SyncPolicy
	if json.Unmarshal([]byte(row.SettingValue), &p) != nil {
		return DefaultSyncPolicy()
	}
	return &p
}

// SaveSyncPolicy 落库到 sync_policy setting(upsert by setting_key)。
func (s *SyncService) SaveSyncPolicy(ctx context.Context, p *SyncPolicy) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var row models.SystemSetting
	err = s.db.WithContext(ctx).Where("setting_key = ?", syncPolicySettingKey).First(&row).Error
	if err != nil {
		return s.db.WithContext(ctx).Create(&models.SystemSetting{
			SettingKey: syncPolicySettingKey, SettingValue: string(b),
		}).Error
	}
	return s.db.WithContext(ctx).Model(&row).Update("setting_value", string(b)).Error
}
```

> 注:`sync_policy` 这个 setting 本身应加入 `ExcludedFields["setting"]` 默认清单(它是 master 专属规则, 不该被 follower 反向覆盖),回到 Task 2 的 `DefaultSyncPolicy` 补上 `"sync_policy"`。

- [ ] **Step 4: 跑确认通过 + 全量回归**

Run: `go test ./internal/services/...`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/services/sync_policy.go internal/services/sync_service.go internal/services/sync_policy_test.go
git commit -m "✨ feat(sync): sync_policy 持久化到 SystemSetting"
```

---

### Task 8: policy CRUD endpoint(master)

**Files:**
- Modify: `internal/handler/sync_handler.go`（GET/PUT `/api/sync/policy`）
- Modify: 路由注册处（`internal/router` 或 app 装配 sync 路由的地方 — grep `sync/pull` 找到注册块）

- [ ] **Step 1: 加 handler 方法（sync_handler.go）**

```go
// GetSyncPolicy 返回当前 sync_policy(master 用于 UI 展示/编辑)。
func (h *SyncHandler) GetSyncPolicy(c *gin.Context) {
	c.JSON(http.StatusOK, h.syncService.LoadSyncPolicy(c.Request.Context()))
}

// UpdateSyncPolicy 保存 sync_policy(仅 master 有意义)。
func (h *SyncHandler) UpdateSyncPolicy(c *gin.Context) {
	var p services.SyncPolicy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.syncService.SaveSyncPolicy(c.Request.Context(), &p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
```

> 确认 `SyncHandler` 能引用 `services.SyncPolicy`(import path);若 handler 不便直接依赖 services 类型, 用一个 handler 本地 DTO 再转换。

- [ ] **Step 2: 注册路由**

在 sync 路由组(与 `pull`/`ws`/`peers` 同组)加：

```go
		sync.GET("/policy", syncHandler.GetSyncPolicy)
		sync.PUT("/policy", syncHandler.UpdateSyncPolicy)
```

- [ ] **Step 3: 编译 + 手测**

Run: `go build ./...`
Expected: 无错误。

- [ ] **Step 4: Commit**

```bash
git add internal/handler/sync_handler.go internal/router/*.go
git commit -m "✨ feat(sync): sync_policy CRUD endpoint"
```

---

### Task 9: 前端 master 排除清单 UI

**Files:**
- Modify: `web/src/components/v3/PeerSyncPanel.vue`

- [ ] **Step 1: 加 policy 编辑区(仅 master 展示)**

在 PeerSyncPanel 里,当本节点 `is_master` 时,展示一个"同步排除清单"卡片:
- 类别开关(group/subgroup/key/alias/setting)对应 `excludedCategories`;
- 每类别下一个 tags 输入框编辑 `excludedFields[category]`;
- 载入:`GET /api/sync/policy`;保存:`PUT /api/sync/policy`。

用 naive-ui `n-switch` + `n-dynamic-tags`。默认值来自后端(已含本机字段)。保存后 toast 成功。

（具体 Vue 代码依 PeerSyncPanel 现有结构编写;保持与现有 `useApi`/请求封装一致。）

- [ ] **Step 2: 构建验证**

Run: `npm --prefix web run build`
Expected: 构建成功。

- [ ] **Step 3: Commit**

```bash
git add web/src/components/v3/PeerSyncPanel.vue
git commit -m "✨ feat(sync): master 端同步排除清单 UI"
```

---

## 阶段 C — 上行迁移与提示

### Task 10: follower 关闭自动 push

**Files:**
- Modify: `internal/services/sync_peer_manager.go`（`pushToPeers` 顶部 or `pushLoop`）

- [ ] **Step 1: pushToPeers 顶部按角色短路（约 345 行）**

`pushToPeers` 开头加：

```go
	// 主从: follower 本地改动默认不自动外传(避免污染 master/其它 follower)。
	// follower→master 只走用户手动迁移 PushPeer(它显式调用, 不经此自动路径的 gate)。
	// 注: PushPeer 复用 pushToPeers, 需用一个 forced 标志区分; 见 Step 2。
	if !m.syncService.configManager.IsMaster() && !forced {
		return
	}
```

- [ ] **Step 2: 给 pushToPeers 加 forced 参数**

签名改 `func (m *SyncPeerManager) pushToPeers(ctx context.Context, settings types.SystemSettings, forced bool)`;
- `pushLoop` 里的自动调用传 `false`;
- `PushPeer`(手动迁移)里的调用传 `true`。

- [ ] **Step 3: 编译 + 测试**

Run: `go build ./... && go test ./internal/services/...`
Expected: PASS。

- [ ] **Step 4: Commit**

```bash
git add internal/services/sync_peer_manager.go
git commit -m "✨ feat(sync): follower 关闭自动 push, 仅手动迁移"
```

---

### Task 11: 前端 follower 迁移提示

**Files:**
- Modify: `web/src/components/v3/PeerSyncPanel.vue`

- [ ] **Step 1: follower 视图加提示 + 迁移入口**

当本节点非 master 时:
- 顶部 `n-alert` 提示:"本节点为 follower,配置以 master 为准。本地改动不会自动同步,需点『迁移到主站』手动上推,否则下轮镜像会被 master 覆盖。"
- 复用现有 preview-push 面板作"迁移到主站"入口(选择记录 → PushPeer 到 master peer)。

- [ ] **Step 2: 构建验证**

Run: `npm --prefix web run build`
Expected: 成功。

- [ ] **Step 3: Commit**

```bash
git add web/src/components/v3/PeerSyncPanel.vue
git commit -m "✨ feat(sync): follower 迁移提示 + 手动迁移入口"
```

---

### Task 12: 集成验证 + 部署止血 runbook

**Files:**
- Create: `docs/superpowers/plans/2026-07-15-master-sync-deploy-runbook.md`（部署手册）

- [ ] **Step 1: 全量测试 + 前端构建**

Run: `go test ./... && npm --prefix web run build`
Expected: 全绿。

- [ ] **Step 2: 版本 bump + 发版**

- `internal/version/version.go` 与 `web/package.json` bump(按 project_version_policy, patch+1 → v2.5.26)。
- commit + tag + push,等 CI 镜像。

- [ ] **Step 3: 部署顺序(止血合一)**

1. **HK(master)**:`.env` 加 `IS_MASTER=true`;`docker compose pull && up -d`。
2. 核对 HK 快照正确性(agnes/xfyun 活 key 数);若本地更全,从本地做一次手动迁移到 HK。
3. **本地 + mini(follower)**:确保 `IS_MASTER` 未设/false;各配一个指向 HK 的 peer;`pull && up -d`。
4. 等 follower 首轮 pull → 镜像 HK。

- [ ] **Step 4: 验证三节点一致**

在三节点查(同 2026-07-15 排查用的 SQL):
```sql
SELECT count(*) FROM api_keys WHERE deleted_at IS NULL;                 -- 三节点应相等
SELECT g.name,count(*) FROM api_keys k JOIN `groups` g ON g.id=k.group_id
  WHERE k.deleted_at IS NULL GROUP BY g.name;                          -- per-group 应一致
```
Expected: 三节点活 key 总数与 per-group 分布完全一致;agnes/xfyun 恢复;无孤儿。

- [ ] **Step 5: Commit runbook**

```bash
git add docs/superpowers/plans/2026-07-15-master-sync-deploy-runbook.md
git commit -m "📝 docs(sync): 主从同步部署止血 runbook"
```

---

## Self-Review 检查项(实现者开工前过一遍)

- **Spec 覆盖**:§3 角色→Task 5/6/10;§4.1 镜像→Task 4/5;§4.2 迁移→Task 10/11;§5 policy→Task 2/3/7/8/9;§7 止血→Task 12;移除 V2_5_25→Task 1。全覆盖。
- **类型一致**:`ApplySnapshot`/`ExportSnapshot`/`LoadSyncPolicy`/`SaveSyncPolicy`/`SyncPolicy`/`preserveExcludedGroupFields` 命名在各 task 间一致。
- **待核对的现有命名**(实现时校准,不匹配按实际改):`models.ModelAlias.RealModel`、`models.GroupSubGroup.GroupID/SubGroupID`、`models.Group.Config`(JSONMap)、`models.APIKey.KeyHash/GroupID`、`syncMergeSave`、`newTestSyncService` helper。
- **占位符**:Task 9/11 前端为结构描述(依 PeerSyncPanel 现状写);其余均含完整代码。
```
