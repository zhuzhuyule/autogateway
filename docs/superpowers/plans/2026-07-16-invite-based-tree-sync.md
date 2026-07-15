# 邀请制加入 + 树形级联同步 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让节点用一次性邀请链接一键加入,自动把邀请者设为父节点(镜像源),形成"一父多子"的树形同步网。

**Architecture:** 父签发一次性 token → 子 POST `/api/sync/join` 交换 URL/公钥 + 父用子公钥加密下发 per-peer key → 子把父落库为 `is_master` peer(唯一, 换父即清旧)并切 follower。级联复用现有"follower 被下级 pull 照常供全量快照"。每对(子,父)独立 key、加密交换,无全网 gossip。

**Tech Stack:** Go 1.25 + GORM + glebarez/sqlite;复用 `NodeKeypairService`(nacl/box `EncryptFor`/`DecryptFrom`)、`SyncPeer` 表、现有 pull/ApplySnapshot。Vue3 + naive-ui + vue-router。

参考 spec: `docs/superpowers/specs/2026-07-15-invite-based-mesh-join-design.md`

---

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/models/types.go` | 改 | 新增 `InviteToken` |
| `internal/db/migrations/v2_7_0_InviteTokens.go` | 新建 | 建 invite_tokens 表(幂等) |
| `internal/app/app.go` | 改 | **Master + Slave 两个分支**都注册 V2_7_0 migration |
| `internal/services/sync_invite.go` | 新建 | `SyncService` 的 invite 方法: GenerateInviteToken / ConsumeInviteToken / PurgeExpiredInvites / JoinParent |
| `internal/handler/sync_handler.go` | 改 | `GenerateInvite`(签发链接) + `JoinEndpoint`(父侧收加入) + `TriggerJoinParent`(子侧触发) |
| `internal/router/router.go` | 改 | 路由 `/sync/invite`(POST) + `/sync/join`(POST) + `/sync/join-parent`(POST) |
| `web/src/api/sync.ts` | 改 | `generateInvite()` + `joinParent()` |
| `web/src/components/v3/PeerSyncPanel.vue` | 改 | 「生成邀请链接」按钮 + 「用链接加入」入口 |
| `web/src/router/index.ts` + `web/src/views/Join.vue` | 改/新建 | `/join` 路由 + 加入确认页 |

invite 逻辑作为 `SyncService` 的方法放独立文件 `sync_invite.go`(SyncService 已持有 `db` + `keypair`, 免新 dig 依赖), 不塞进已很大的 sync_service.go。

---

## 阶段 A — 后端 token + join

### Task 1: InviteToken 模型 + 迁移

**Files:**
- Modify: `internal/models/types.go`
- Create: `internal/db/migrations/v2_7_0_InviteTokens.go`
- Modify: `internal/app/app.go`（Master 分支 + Slave 分支各注册一次）

- [ ] **Step 1: 加 InviteToken 模型**

`internal/models/types.go` 末尾加:
```go
// InviteToken 一次性邀请凭证(本机, 不同步)。父签发, 子 join 时消费。
type InviteToken struct {
	Code      string     `gorm:"primaryKey;type:varchar(64)" json:"code"`
	ExpiresAt time.Time  `gorm:"index" json:"expires_at"`
	Used      bool       `gorm:"not null;default:false" json:"used"`
	UsedBy    string     `gorm:"type:varchar(64)" json:"used_by"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
}
```

- [ ] **Step 2: 建迁移**

`internal/db/migrations/v2_7_0_InviteTokens.go`:
```go
package db

import (
	"autogateway/internal/models"

	"gorm.io/gorm"
)

// V2_7_0_InviteTokens 建 invite_tokens 表。Master 和 Slave 分支都要调 —— Slave 分支不跑
// AutoMigrate(见 2026-07-15 主从踩坑), 邀请功能两种角色都要用, 故显式 AutoMigrate 该表。幂等。
func V2_7_0_InviteTokens(db *gorm.DB) error {
	return db.AutoMigrate(&models.InviteToken{})
}
```

- [ ] **Step 3: app.go 两个分支都注册**

Master 分支(在 `V2_5_30_SyncPeerIsMaster` 调用之后)加:
```go
		if err := db.V2_7_0_InviteTokens(a.db); err != nil {
			return fmt.Errorf("V2_7_0 invite_tokens failed: %w", err)
		}
```
Slave 分支(在 `V2_5_30_SyncPeerIsMaster` 调用之后)也加同样三行。

- [ ] **Step 4: 编译**

Run: `go build ./...`
Expected: 无错误。

- [ ] **Step 5: Commit**

```bash
git add internal/models/types.go internal/db/migrations/v2_7_0_InviteTokens.go internal/app/app.go
git commit -m "✨ feat(invite): InviteToken 模型 + 迁移(Master+Slave 两分支建表)"
```

---

### Task 2: 生成 + 消费邀请 token(一次性、并发安全)

**Files:**
- Create: `internal/services/sync_invite.go`
- Test: `internal/services/sync_invite_test.go`

- [ ] **Step 1: 写失败测试**

`internal/services/sync_invite_test.go`:
```go
package services

import (
	"testing"
	"time"
)

func TestInvite_ConsumeOnce(t *testing.T) {
	s, _ := newTestSyncService(t)
	code, err := s.GenerateInviteToken(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeInviteToken(code, "fp-a"); err != nil {
		t.Fatalf("首次消费应成功: %v", err)
	}
	if err := s.ConsumeInviteToken(code, "fp-b"); err == nil {
		t.Fatal("二次消费应失败(一次性)")
	}
}

func TestInvite_Expired(t *testing.T) {
	s, _ := newTestSyncService(t)
	code, _ := s.GenerateInviteToken(-time.Minute) // 已过期
	if err := s.ConsumeInviteToken(code, "fp"); err == nil {
		t.Fatal("过期 token 应拒绝")
	}
}

func TestInvite_Unknown(t *testing.T) {
	s, _ := newTestSyncService(t)
	if err := s.ConsumeInviteToken("nope", "fp"); err == nil {
		t.Fatal("未知 token 应拒绝")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/services/ -run TestInvite`
Expected: FAIL(`GenerateInviteToken`/`ConsumeInviteToken` 未定义)。

- [ ] **Step 3: 实现 sync_invite.go**

```go
package services

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

// GenerateInviteToken 签发一次性邀请 token, ttl 后过期。返回 URL-safe code。
func (s *SyncService) GenerateInviteToken(ttl time.Duration) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(buf)
	tok := models.InviteToken{Code: code, ExpiresAt: time.Now().Add(ttl)}
	if err := s.db.Create(&tok).Error; err != nil {
		return "", err
	}
	return code, nil
}

// ConsumeInviteToken 校验并消费一次性 token(存在 && 未用 && 未过期), 事务内置 used 防并发双用。
func (s *SyncService) ConsumeInviteToken(code, usedByFingerprint string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		var tok models.InviteToken
		err := tx.Where("code = ? AND used = ? AND expires_at > ?", code, false, now).First(&tok).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("invite token invalid / used / expired")
		}
		if err != nil {
			return err
		}
		return tx.Model(&models.InviteToken{}).Where("code = ?", code).
			Updates(map[string]any{"used": true, "used_by": usedByFingerprint, "used_at": &now}).Error
	})
}

// PurgeExpiredInvites 清理过期/已用 token(避免无限增长)。返回删除条数。
func (s *SyncService) PurgeExpiredInvites() int64 {
	res := s.db.Where("expires_at < ? OR used = ?", time.Now(), true).Delete(&models.InviteToken{})
	return res.RowsAffected
}

// randSyncKey 生成一把 per-peer sync_key(join 时父分配给子)。
func randSyncKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gen sync key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/services/ -run TestInvite`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/services/sync_invite.go internal/services/sync_invite_test.go
git commit -m "✨ feat(invite): 一次性邀请 token 生成/消费(并发安全)"
```

---

### Task 3: JoinEndpoint(父侧收加入)

**Files:**
- Modify: `internal/handler/sync_handler.go`
- Modify: `internal/router/router.go`

父侧: 校验 token → 生成 per-peer key K → 把子落库为 peer(is_master=false) → 用子公钥加密 K → 返回 K + 父自己的信息。

- [ ] **Step 1: 加 JoinEndpoint(sync_handler.go, 放 ListPeers 前)**

```go
// joinRequest 是子 POST /api/sync/join 的请求体。
type joinRequest struct {
	Token         string `json:"token"`
	MyURL         string `json:"my_url"`
	MyPublicKey   string `json:"my_public_key"`
	MyFingerprint string `json:"my_fingerprint"`
	MyName        string `json:"my_name"`
}

// JoinEndpoint 父侧: 被邀方带 token 来加入。校验 token → 分配 per-peer key → 落子 peer →
// 用子公钥加密 key 回传 + 父自己的连接信息(子据此把父设为镜像源)。
func (h *SyncHandler) JoinEndpoint(c *gin.Context) {
	var req joinRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Token == "" || req.MyURL == "" || req.MyPublicKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token/my_url/my_public_key"})
		return
	}
	// 校验并消费 token(一次性)
	if err := h.syncService.ConsumeInviteToken(req.Token, req.MyFingerprint); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	// 分配 per-peer key
	key, err := randSyncKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 落子 peer(去重: 同指纹已存在则更新)
	peer := models.SyncPeer{
		ID:                genPeerID(),
		Name:              req.MyName,
		URL:               req.MyURL,
		SyncKey:           key,
		Role:              "client",
		Status:            "disconnected",
		IsMaster:          false, // 子不是父的镜像源
		PublicKeyX25519:   req.MyPublicKey,
		PinnedFingerprint: req.MyFingerprint,
	}
	if err := h.db.Where("pinned_fingerprint = ?", req.MyFingerprint).
		Assign(peer).FirstOrCreate(&models.SyncPeer{}, models.SyncPeer{PinnedFingerprint: req.MyFingerprint}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 用子公钥加密 key
	childPub, err := services.DecodePublicKeyBase64(req.MyPublicKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad public key"})
		return
	}
	encKey, err := h.keypair.EncryptFor([]byte(key), childPub)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	settings := h.settingsManager.GetSettings()
	c.JSON(http.StatusOK, gin.H{
		"sync_key_enc": encKey,
		"parent": gin.H{
			"url":         settings.AppUrl,
			"public_key":  h.keypair.PublicKeyBase64(),
			"fingerprint": h.keypair.Fingerprint(),
			"name":        settings.AppUrl, // 展示名, 无独立字段就用 app_url
		},
	})
}

// genPeerID 生成 peer 主键(复用现有生成方式;若已有 helper 用现成的)。
func genPeerID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
```
> 注: `genPeerID`/`rand`/`base64` 若 handler 已有等价工具则复用, 别重复定义;`services.DecodePublicKeyBase64` 是包级函数。确认 `SyncHandler` 已注入 `keypair *services.NodeKeypairService`(现有)。

- [ ] **Step 2: 注册路由(router.go, 与 /sync/pull 同组)**

```go
	api.POST("/sync/join", syncHandler.JoinEndpoint)
```
放在 `api.GET("/sync/pull", ...)` 附近。注意: join 是被邀方无凭证访问(token 即凭证), 不要加 X-Sync-Key 中间件。

- [ ] **Step 3: 编译**

Run: `go build ./...`
Expected: 无错误(若 FirstOrCreate 写法报错, 改用先 `First` 判存在再 `Create`/`Updates` 的显式两步)。

- [ ] **Step 4: Commit**

```bash
git add internal/handler/sync_handler.go internal/router/router.go
git commit -m "✨ feat(invite): JoinEndpoint 父侧收加入(消费token+分配key+落子peer+加密回传)"
```

---

### Task 4: JoinParent(子侧触发加入)

**Files:**
- Modify: `internal/services/sync_invite.go`
- Modify: `internal/handler/sync_handler.go`
- Modify: `internal/router/router.go`

子侧: 调父的 `/api/sync/join` → 解密 key → 把父落库为 `is_master` peer(换父: 清旧) → 本机切 follower。

- [ ] **Step 1: 实现 JoinParent(sync_invite.go 追加)**

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// JoinParent 子侧: 用邀请 token 加入 inviterURL 指向的父节点。成功后父成为本机唯一镜像源。
func (s *SyncService) JoinParent(ctx context.Context, inviterURL, token string) error {
	body, _ := json.Marshal(map[string]string{
		"token":          token,
		"my_url":         s.selfAppURL(),
		"my_public_key":  s.keypair.PublicKeyBase64(),
		"my_fingerprint": s.keypair.Fingerprint(),
		"my_name":        s.selfAppURL(),
	})
	url := strings.TrimRight(inviterURL, "/") + "/api/sync/join"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("join request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("join rejected: http %d", resp.StatusCode)
	}
	var out struct {
		SyncKeyEnc string `json:"sync_key_enc"`
		Parent     struct {
			URL         string `json:"url"`
			PublicKey   string `json:"public_key"`
			Fingerprint string `json:"fingerprint"`
			Name        string `json:"name"`
		} `json:"parent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	// 解密父分配的 per-peer key
	parentPub, err := DecodePublicKeyBase64(out.Parent.PublicKey)
	if err != nil {
		return fmt.Errorf("bad parent public key: %w", err)
	}
	keyBytes, err := s.keypair.DecryptFrom(out.SyncKeyEnc, parentPub)
	if err != nil {
		return fmt.Errorf("decrypt sync key: %w", err)
	}
	// 换父: 清掉旧的 is_master, 落新父
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.SyncPeer{}).Where("is_master = ?", true).Update("is_master", false).Error; err != nil {
			return err
		}
		parent := models.SyncPeer{
			ID:                genInvitePeerID(),
			Name:              out.Parent.Name,
			URL:               out.Parent.URL,
			SyncKey:           string(keyBytes),
			Role:              "client",
			Status:            "disconnected",
			IsMaster:          true, // 父 = 本机镜像源
			PublicKeyX25519:   out.Parent.PublicKey,
			PinnedFingerprint: out.Parent.Fingerprint,
		}
		// 同父已存在(按指纹)则更新, 否则新建
		var existing models.SyncPeer
		e := tx.Where("pinned_fingerprint = ?", out.Parent.Fingerprint).First(&existing).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			if err := tx.Create(&parent).Error; err != nil {
				return err
			}
		} else if e == nil {
			parent.ID = existing.ID
			if err := tx.Model(&existing).Updates(parent).Error; err != nil {
				return err
			}
		} else {
			return e
		}
		// 本机切 follower(有了父就是子站)
		return s.setNodeRoleTx(tx, "true")
	})
}

// selfAppURL 读本机对外地址(邀请/加入时告诉对端怎么回连本机)。
func (s *SyncService) selfAppURL() string {
	var row models.SystemSetting
	if err := s.db.Where("setting_key = ?", "app_url").First(&row).Error; err == nil {
		return row.SettingValue
	}
	return ""
}

// setNodeRoleTx 事务内版 SetNodeRole(复用逻辑, 避免事务外再开连接)。
func (s *SyncService) setNodeRoleTx(tx *gorm.DB, isSlaveVal string) error {
	var row models.SystemSetting
	err := tx.Where("setting_key = ?", nodeIsSlaveSettingKey).First(&row).Error
	if err != nil {
		return tx.Create(&models.SystemSetting{SettingKey: nodeIsSlaveSettingKey, SettingValue: isSlaveVal}).Error
	}
	return tx.Model(&row).Update("setting_value", isSlaveVal).Error
}

func genInvitePeerID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
```
> 注: 若 `genPeerID`(Task 3) 与 `genInvitePeerID` 重复, 合并成一个包级 helper。`nodeIsSlaveSettingKey` 已在 sync_service.go 定义。

- [ ] **Step 2: 加 TriggerJoinParent handler(sync_handler.go)**

```go
// TriggerJoinParent 子侧前端触发: 本机用 token 加入 inviter_url 指向的父。
func (h *SyncHandler) TriggerJoinParent(c *gin.Context) {
	var body struct {
		InviterURL string `json:"inviter_url"`
		Token      string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.syncService.JoinParent(c.Request.Context(), body.InviterURL, body.Token); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
```

- [ ] **Step 3: 注册路由(router.go)**

```go
	api.POST("/sync/join-parent", syncHandler.TriggerJoinParent)
```

- [ ] **Step 4: 编译 + services 测试**

Run: `go build ./... && go test ./internal/services/...`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/services/sync_invite.go internal/handler/sync_handler.go internal/router/router.go
git commit -m "✨ feat(invite): JoinParent 子侧加入(解密key+落父peer换父+切follower)"
```

---

### Task 5: GenerateInvite handler(签发链接)

**Files:**
- Modify: `internal/handler/sync_handler.go`
- Modify: `internal/router/router.go`

- [ ] **Step 1: 加 GenerateInvite handler**

```go
// GenerateInvite 签发一次性邀请链接(默认 24h 过期)。链接自包含本机 app_url + token。
func (h *SyncHandler) GenerateInvite(c *gin.Context) {
	code, err := h.syncService.GenerateInviteToken(24 * time.Hour)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	appURL := strings.TrimRight(h.settingsManager.GetSettings().AppUrl, "/")
	link := fmt.Sprintf("%s/#/join?token=%s&inviter=%s", appURL, code, url.QueryEscape(appURL))
	c.JSON(http.StatusOK, gin.H{"code": code, "link": link, "inviter_url": appURL})
}
```
> 确认 sync_handler.go 已 import `fmt` / `strings` / `net/url` / `time`;缺则补。

- [ ] **Step 2: 注册路由**

```go
	api.POST("/sync/invite", syncHandler.GenerateInvite)
```

- [ ] **Step 3: 编译**

Run: `go build ./...`
Expected: 无错误。

- [ ] **Step 4: Commit**

```bash
git add internal/handler/sync_handler.go internal/router/router.go
git commit -m "✨ feat(invite): GenerateInvite 签发一次性邀请链接"
```

---

## 阶段 B — 前端

### Task 6: sync.ts API

**Files:**
- Modify: `web/src/api/sync.ts`

- [ ] **Step 1: 加两个方法(syncApi 里)**

```ts
  /** 主/父: 签发一次性邀请链接 */
  async generateInvite(): Promise<{ code: string; link: string; inviter_url: string }> {
    const response = await http.post("/sync/invite", {});
    return response.data;
  },
  /** 子: 用邀请链接(inviter_url + token)加入父 */
  async joinParent(inviterUrl: string, token: string): Promise<void> {
    await http.post("/sync/join-parent", { inviter_url: inviterUrl, token });
  },
```

- [ ] **Step 2: 构建验证**

Run: `npm --prefix web run build`
Expected: 成功。

- [ ] **Step 3: Commit**

```bash
git add web/src/api/sync.ts
git commit -m "✨ feat(invite): 前端 generateInvite/joinParent API"
```

---

### Task 7: PeerSyncPanel 生成链接 + 加入入口

**Files:**
- Modify: `web/src/components/v3/PeerSyncPanel.vue`

- [ ] **Step 1: 加两块 UI + 逻辑**

在同步页加:
- 「生成邀请链接」按钮 → 调 `syncApi.generateInvite()` → 弹出/展示 link + 复制按钮(复用现有 `copyText`);
- 「用链接加入」输入框 → 粘贴链接 → 前端解析出 `inviter` 和 `token`(URL 的 query)→ 调 `syncApi.joinParent(inviter, token)` → 成功后 `loadConfig()+loadPeers()` 刷新角色与列表。

解析链接 helper(内联):
```ts
function parseInvite(link: string): { inviter: string; token: string } | null {
  try {
    const q = link.split("?")[1] || "";
    const p = new URLSearchParams(q);
    const token = p.get("token");
    const inviter = p.get("inviter");
    if (token && inviter) return { inviter, token };
  } catch { /* ignore */ }
  return null;
}
```
UI 用 naive-ui `n-input` + `n-button` + `n-modal`(展示生成的链接), 与现有面板风格一致。

- [ ] **Step 2: 构建验证**

Run: `npm --prefix web run build`
Expected: 成功。

- [ ] **Step 3: Commit**

```bash
git add web/src/components/v3/PeerSyncPanel.vue
git commit -m "✨ feat(invite): 生成邀请链接 + 用链接加入 UI"
```

---

### Task 8: /join 路由 + 确认页

**Files:**
- Create: `web/src/views/Join.vue`
- Modify: `web/src/router/index.ts`

- [ ] **Step 1: 加 /join 路由**

`web/src/router/index.ts` 的 `routes` 数组加:
```ts
  { path: "/join", name: "join", component: () => import("@/views/Join.vue") },
```

- [ ] **Step 2: Join.vue(解析 token → 确认 → 调 joinParent)**

`web/src/views/Join.vue`: 从 `route.query` 读 `token` + `inviter`, 展示"确认加入 {inviter} 为父节点?"卡片, 点确认调 `syncApi.joinParent(inviter, token)`, 成功后跳同步页并提示。若缺参数展示错误。用 naive-ui `n-card`/`n-button`/`useMessage`。

- [ ] **Step 3: 构建验证**

Run: `npm --prefix web run build`
Expected: 成功。

- [ ] **Step 4: Commit**

```bash
git add web/src/router/index.ts web/src/views/Join.vue
git commit -m "✨ feat(invite): /join 加入确认页"
```

---

## 阶段 C — 集成 + 发版

### Task 9: 集成验证 + 发版 v2.7.0

- [ ] **Step 1: 全量测试 + 前端构建**

Run: `go test ./... && npm --prefix web run build`
Expected: 全绿。

- [ ] **Step 2: 本地双实例冒烟(可选但推荐)**

用两个 data 目录 + 两个端口起两个实例, A 生成邀请链接 → B `/join` 加入 → 校验 B 的 sync_peers 有 A(is_master=true)、B 角色变 follower、B 开始镜像 A。

- [ ] **Step 3: 版本 bump + 发版**

- `internal/version/version.go` → `v2.7.0`;`web/package.json` → `2.7.0`(minor, 按 project_version_policy: fork 独有大特性自主走 minor)。
- commit + `git tag v2.7.0` + push → 等 CI 镜像。

- [ ] **Step 4: 三节点部署 v2.7.0**

HK/mini `docker compose pull && up -d`;本地 ac_test `go build -o /tmp/ac_test . && nohup` 重启。现有主从关系不变(邀请是叠加功能)。

- [ ] **Step 5: Commit release**

```bash
git commit -m "🔖 chore(release): v2.7.0 — 邀请制加入 + 树形级联同步"
```

---

## Self-Review 检查项

- **Spec 覆盖**: §3 加入流程→Task 3/4;§4 换父→Task 4(清旧 is_master);§5 鉴权/加密交换→Task 3(EncryptFor)+Task 4(DecryptFrom);§7 InviteToken→Task 1;§8 组件→全覆盖;一次性 token→Task 2。
- **类型一致**: `GenerateInviteToken`/`ConsumeInviteToken`/`JoinParent`/`InviteToken`/`randSyncKey` 命名跨 task 一致;peer id helper 需合并为一个(genPeerID)避免重复定义。
- **待核对**: `SyncHandler.keypair` 字段名、`services.DecodePublicKeyBase64` 包级导出、`nodeIsSlaveSettingKey` 已定义、handler 现有 import — 实现时校准。
- **占位符**: Task 7/8 前端为结构描述(依 PeerSyncPanel/naive-ui 现状写), 其余含完整代码。
- **网络前提**: 不在代码里强制, 靠 §6 文档说明 + 邀请链接填公网 app_url。
