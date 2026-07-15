package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"autogateway/internal/config"
	"autogateway/internal/models"
	"autogateway/internal/response"
	"autogateway/internal/services"
	"autogateway/internal/version"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var upgrader = websocket.Upgrader{
	// Origin 校验交给业务层 (peer.SyncKey + settings.SyncKey 二段校验), WS 协议层放行.
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// SyncHandler handles the P2P sync endpoints (WebSocket and HTTP pull).
type SyncHandler struct {
	syncService     *services.SyncService
	settingsManager *config.SystemSettingsManager
	keypair         *services.NodeKeypairService
	db              *gorm.DB
	peerManager     *services.SyncPeerManager // for manual trigger pull/push

	// Active WebSocket connections
	clientsMu sync.Mutex
	clients   map[*websocket.Conn]bool
}

// NewSyncHandler creates a new SyncHandler
func NewSyncHandler(syncService *services.SyncService, settingsManager *config.SystemSettingsManager, keypair *services.NodeKeypairService, db *gorm.DB, peerManager *services.SyncPeerManager) *SyncHandler {
	return &SyncHandler{
		syncService:     syncService,
		settingsManager: settingsManager,
		keypair:         keypair,
		db:              db,
		peerManager:     peerManager,
		clients:         make(map[*websocket.Conn]bool),
	}
}

// TriggerPull manually pulls from the given peer (POST /api/sync/peers/:id/pull).
// Returns 200 on success, 4xx on bad request, 5xx on pull error (timeout / decrypt).
func (h *SyncHandler) TriggerPull(c *gin.Context) {
	peerID := c.Param("id")
	if peerID == "" {
		c.JSON(400, gin.H{"code": 400, "message": "missing peer id"})
		return
	}
	if err := h.peerManager.PullPeer(c.Request.Context(), peerID); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "ok"})
}

// PreviewPush returns a non-destructive summary of what TriggerPush would send.
// GET /api/sync/peers/:id/preview-push. Used by the UI to show a confirm modal
// before actually pushing.
func (h *SyncHandler) PreviewPush(c *gin.Context) {
	peerID := c.Param("id")
	if peerID == "" {
		c.JSON(400, gin.H{"code": 400, "message": "missing peer id"})
		return
	}
	preview, err := h.peerManager.PreviewPushPayload(c.Request.Context(), peerID)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": preview})
}

// TriggerPush manually pushes to the given peer (POST /api/sync/peers/:id/push).
// Requires an active WS connection to the peer.
func (h *SyncHandler) TriggerPush(c *gin.Context) {
	peerID := c.Param("id")
	if peerID == "" {
		c.JSON(400, gin.H{"code": 400, "message": "missing peer id"})
		return
	}
	if err := h.peerManager.PushPeer(c.Request.Context(), peerID); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "ok"})
}

// Broadcast 向所有已连接的 client (作为 ws server 端持有) 推送密文消息.
// SyncPeerManager 在本地变更触发 push 时也调用此方法, 让 server 端持有的 conn 也能
// 收到推送, 实现双向 mesh (Gap 3 修复).
func (h *SyncHandler) Broadcast(msg []byte) {
	h.clientsMu.Lock()
	conns := make([]*websocket.Conn, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c)
	}
	h.clientsMu.Unlock()

	logrus.Infof("ws broadcast: pushing %d bytes to %d connected ws clients", len(msg), len(conns))
	sent := 0
	for _, c := range conns {
		if err := c.WriteMessage(websocket.TextMessage, msg); err != nil {
			logrus.Warnf("ws broadcast write failed: %v", err)
			h.clientsMu.Lock()
			delete(h.clients, c)
			h.clientsMu.Unlock()
			_ = c.Close()
		} else {
			sent++
		}
	}
	if len(conns) > 0 {
		logrus.Infof("ws broadcast: sent OK to %d/%d clients", sent, len(conns))
	}
}

// writeLog 写一条 SyncLog. 失败时只 warn, 不阻断同步主路径.
func (h *SyncHandler) writeLog(peerID, action, status, errMsg, details string) {
	log := models.SyncLog{
		PeerID:       peerID,
		Action:       action,
		Status:       status,
		ErrorMessage: errMsg,
		Details:      details,
		Timestamp:    time.Now(),
	}
	if err := h.db.Create(&log).Error; err != nil {
		logrus.Warnf("failed to write sync log: %v", err)
	}
}

// WsEndpoint is the WebSocket server endpoint for incoming peer connections.
//
// 鉴权策略 (P9.0 Gap 2/6):
//   - X-Sync-Key header 必须匹配某个 SyncPeer.SyncKey (per-peer 入门 token)
//   - settings.SyncKey 是全局对称加密密钥 (AES-GCM payload), 不参与握手鉴权
//   - 没配置任何 peer 时拒绝任何 WS 连接, 防"开启 sync 但忘配 peer"敞口
func (h *SyncHandler) WsEndpoint(c *gin.Context) {
	settings := h.settingsManager.GetSettings()
	if !settings.SyncEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "Sync is not enabled on this node"})
		return
	}
	if settings.SyncKey == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "SyncKey (global encryption key) is not configured"})
		return
	}

	// Per-peer 鉴权: 找一个 SyncKey 匹配的 peer
	reqKey := c.GetHeader("X-Sync-Key")
	if reqKey == "" {
		// 兼容某些 WS 客户端不发 header 的情况, 也接受 query 参数
		reqKey = c.Query("sync_key")
	}
	if reqKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Missing X-Sync-Key"})
		return
	}
	var peer models.SyncPeer
	if err := h.db.Where("sync_key = ?", reqKey).First(&peer).Error; err != nil {
		h.writeLog("", "push", "error", "ws auth failed: unknown sync_key", "ws")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid X-Sync-Key"})
		return
	}

	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logrus.Errorf("failed to upgrade to websocket: %v", err)
		return
	}
	defer ws.Close()

	h.clientsMu.Lock()
	h.clients[ws] = true
	h.clientsMu.Unlock()

	defer func() {
		h.clientsMu.Lock()
		delete(h.clients, ws)
		h.clientsMu.Unlock()
	}()

	logrus.Infof("peer %s connected to WS sync endpoint: %s", peer.Name, ws.RemoteAddr())

	// P9.1 hello/welcome 握手: 客户端必须先发 hello{version, schema_hash}.
	// 服务端做兼容性闸门 → welcome / warning / reject.
	if !h.performHandshake(ws, &peer) {
		return // reject 时 performHandshake 已经写完拒绝帧
	}

	for {
		messageType, msg, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logrus.Errorf("ws read error: %v", err)
			}
			break
		}

		if messageType == websocket.TextMessage {
			var msgIn services.WSMessage
			if err := json.Unmarshal(msg, &msgIn); err != nil {
				// 兼容旧客户端: 把整个消息当作裸 ciphertext 包尝试一次
				var legacy struct {
					Ciphertext string `json:"ciphertext"`
				}
				if err2 := json.Unmarshal(msg, &legacy); err2 != nil {
					logrus.Warnf("failed to unmarshal sync message: %v", err)
					h.writeLog(peer.ID, "push", "error", fmt.Sprintf("unmarshal: %v", err), "ws")
					continue
				}
				msgIn.Type = "sync"
				msgIn.Ciphertext = legacy.Ciphertext
			}

			if msgIn.Type != "sync" || msgIn.Ciphertext == "" {
				continue // 心跳/未知类型暂时忽略
			}

			// P9.x 优先用非对称密钥解 (sender 是握手时记录的 peer.PublicKeyX25519);
			// 失败再回退到 legacy AES-GCM. 必须双试: 对端的 broadcaster.Broadcast
			// 路径用 legacy 加密 (因为不知道每个 conn 对应哪个 peer 公钥), 这是
			// mini → 本机 实时同步的关键路径; 没 fallback 这条路径就死.
			var payload *services.SyncPayload
			var err error
			if peer.PublicKeyX25519 != "" {
				payload, err = h.syncService.DecryptPayloadFrom(msgIn.Ciphertext, peer.PublicKeyX25519)
				if err != nil && settings.SyncKey != "" {
					payload, err = h.syncService.DecryptPayload(msgIn.Ciphertext, settings.SyncKey)
				}
			} else {
				payload, err = h.syncService.DecryptPayload(msgIn.Ciphertext, settings.SyncKey)
			}
			if err != nil {
				logrus.Errorf("failed to decrypt sync payload: %v", err)
				h.writeLog(peer.ID, "push", "error", fmt.Sprintf("decrypt: %v", err), "ws")
				continue
			}

			// 主从: 只有 master 接收 push(follower 的手动迁移上行); follower 不接收任何
			// push, 一致性只靠 pull 镜像, 避免增量 LWW 破坏镜像。
			if !h.syncService.IsMaster() {
				continue
			}
			if err := h.syncService.ProcessPayload(context.Background(), payload); err != nil {
				logrus.Errorf("failed to process sync payload: %v", err)
				h.writeLog(peer.ID, "push", "error", fmt.Sprintf("merge: %v", err), "ws")
				continue
			}

			h.writeLog(peer.ID, "push", "success", "", "ws")
			logrus.Infof("successfully processed sync payload from peer %s (ws)", payload.SourcePeerID)
		}
	}
}

// performHandshake 在 ws 连接建立后做一次 hello/welcome 兼容性握手.
// 返回 true 表示握手通过, 同步可以继续; false 表示已发 reject 并应关闭连接.
//
// 闸门规则:
//   - major 版本不一致 → reject
//   - schema_hash 不一致 → reject (LWW 会把对端缺失字段抹空, 风险太高)
//   - 都一致 → welcome
//   - 仅 minor/patch 不一致 → welcome + warning (向后兼容)
//
// 5s 内未收到 hello → reject.
func (h *SyncHandler) performHandshake(ws *websocket.Conn, peer *models.SyncPeer) bool {
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer ws.SetReadDeadline(time.Time{}) // 复位

	_, raw, err := ws.ReadMessage()
	if err != nil {
		h.writeLog(peer.ID, "push", "error", "handshake timeout: "+err.Error(), "ws")
		return false
	}
	var hello services.WSMessage
	if err := json.Unmarshal(raw, &hello); err != nil || hello.Type != "hello" {
		_ = ws.WriteJSON(services.WSMessage{Type: "reject", Reason: "expected hello frame"})
		h.writeLog(peer.ID, "push", "error", "bad hello frame", "ws")
		return false
	}

	myVer := version.Version
	mySchema := services.ComputeSchemaHash()
	myPub := h.keypair.PublicKeyBase64()

	if services.ExtractMajor(hello.Version) != services.ExtractMajor(myVer) {
		_ = ws.WriteJSON(services.WSMessage{
			Type:        "reject",
			Reason:      "major_version_mismatch",
			PeerVersion: hello.Version, MyVersion: myVer,
		})
		h.writeLog(peer.ID, "push", "error",
			fmt.Sprintf("major version mismatch peer=%s mine=%s", hello.Version, myVer), "ws")
		return false
	}
	if hello.SchemaHash != mySchema {
		_ = ws.WriteJSON(services.WSMessage{
			Type:       "reject",
			Reason:     "schema_mismatch",
			PeerSchema: hello.SchemaHash, MySchema: mySchema,
		})
		h.writeLog(peer.ID, "push", "error",
			fmt.Sprintf("schema mismatch peer=%s mine=%s", hello.SchemaHash, mySchema), "ws")
		return false
	}

	// P9.x: 指纹钉扎 — 若 SyncPeer.PinnedFingerprint 存在, 对端 hello.PublicKey 算出的
	// 指纹必须匹配, 否则拒绝 (防止冒名顶替 / MITM).
	if peer.PinnedFingerprint != "" && hello.PublicKey != "" {
		actualFp := services.FingerprintOf(hello.PublicKey)
		if actualFp != peer.PinnedFingerprint {
			_ = ws.WriteJSON(services.WSMessage{
				Type:   "reject",
				Reason: "fingerprint_mismatch",
			})
			h.writeLog(peer.ID, "push", "error",
				fmt.Sprintf("fingerprint mismatch pinned=%s actual=%s", peer.PinnedFingerprint, actualFp), "ws")
			return false
		}
	}

	// 握手通过 — 把对端公钥 + 版本/schema/状态落库.
	// status="connected" 反映 "数据通畅" 而非 "我们主动 dial 对方". 即使本机
	// 因为容器网络无法 dial 对端, 但对端连进来 + 推数据成功, UI 就该绿色显示.
	now := time.Now()
	updates := map[string]any{
		"status":           "connected",
		"peer_version":     hello.Version,
		"peer_schema_hash": hello.SchemaHash,
		"last_synced_at":   now,
	}
	if hello.PublicKey != "" {
		updates["public_key_x25519"] = hello.PublicKey
		peer.PublicKeyX25519 = hello.PublicKey
	}
	h.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Updates(updates)

	// 若 minor/patch 不一致只发 warning, 不阻断
	resp := services.WSMessage{
		Type:        "welcome",
		MyVersion:   myVer,
		MySchema:    mySchema,
		MyPublicKey: myPub,
	}
	if hello.Version != myVer {
		resp.Type = "warning"
		resp.Reason = "minor_version_diff"
		resp.PeerVersion = hello.Version
		h.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Update("status", "warning:minor_version_diff")
	} else {
		// version 一致时清掉旧的 warning, 否则 UI 一直挂着不实际的"过期"状态
		h.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Update("status", "active")
	}
	_ = ws.WriteJSON(resp)
	return true
}

// PullEndpoint allows peers to fetch the latest state via HTTP (cold-start / recovery).
//
// 鉴权 & 加密 跟 WsEndpoint 一致 (P9.x 非对称):
//   - X-Sync-Key 头匹配 peers 表的某行 (per-peer token, 不再是全局 settings.SyncKey)
//   - 若该 peer 有 PublicKeyX25519 (握手时落库) → 用 nacl/box 非对称加密响应
//   - 否则回退到全局 SyncKey 路径 (兼容老 peer)
func (h *SyncHandler) PullEndpoint(c *gin.Context) {
	settings := h.settingsManager.GetSettings()
	if !settings.SyncEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "Sync is not enabled on this node"})
		return
	}
	if settings.SyncKey == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "SyncKey is not configured"})
		return
	}

	// Per-peer 鉴权 — 跟 WsEndpoint 一致
	reqKey := c.GetHeader("X-Sync-Key")
	if reqKey == "" {
		reqKey = c.Query("sync_key")
	}
	if reqKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Missing X-Sync-Key"})
		return
	}
	var peer models.SyncPeer
	if err := h.db.Where("sync_key = ?", reqKey).First(&peer).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid X-Sync-Key"})
		return
	}

	// Optional since timestamp
	var since *time.Time
	sinceStr := c.Query("since")
	if sinceStr != "" {
		t, err := time.Parse(time.RFC3339Nano, sinceStr)
		if err == nil {
			since = &t
		}
	}

	// 主从: master 给 follower 的是"全量快照 + policy"(忽略 since)。
	_ = since // 主从模式不用增量 since
	payload, err := h.syncService.ExportSnapshot(c.Request.Context())
	if err != nil {
		logrus.Errorf("failed to export payload: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to export data"})
		return
	}

	// 加密公钥选择优先级 (修 mini 端 peer.PublicKeyX25519 错存自己公钥的 stale bug):
	//   1. 调用方在 query 里显式带的 my_public_key (最权威, 是 caller 的 fresh 公钥)
	//   2. 我们 db 里 peer.PublicKeyX25519 (握手时落库)
	//   3. legacy 全局 SyncKey AES-GCM (最后回退)
	// 同时, 如果 query 公钥跟 db 不一致, 顺手更新 db 让下次直接走快路径.
	var ciphertext string
	callerPubKey := c.Query("my_public_key")
	if callerPubKey != "" && callerPubKey != peer.PublicKeyX25519 {
		h.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Update("public_key_x25519", callerPubKey)
		logrus.Infof("PullEndpoint: peer %s public_key_x25519 corrected from %.16s... to %.16s...",
			peer.ID, peer.PublicKeyX25519, callerPubKey)
	}
	encryptKey := callerPubKey
	if encryptKey == "" {
		encryptKey = peer.PublicKeyX25519
	}
	if encryptKey != "" {
		ciphertext, err = h.syncService.EncryptPayloadFor(payload, encryptKey)
	} else {
		ciphertext, err = h.syncService.EncryptPayload(payload, settings.SyncKey)
	}
	if err != nil {
		logrus.Errorf("failed to encrypt payload: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt data"})
		return
	}

	// 对端来 pull 也算"数据通畅", 更新本端的 peer.status / last_synced_at,
	// UI 上让用户看到这个 peer 是活的 (即使本端没主动连出去).
	h.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Updates(map[string]any{
		"status":         "connected",
		"last_synced_at": time.Now(),
	})

	c.JSON(http.StatusOK, gin.H{
		"ciphertext": ciphertext,
	})
}

// PushEndpoint allows peers to push data via HTTP
func (h *SyncHandler) PushEndpoint(c *gin.Context) {
	settings := h.settingsManager.GetSettings()
	if !settings.SyncEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "Sync is not enabled on this node"})
		return
	}
	if settings.SyncKey == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "SyncKey is not configured"})
		return
	}

	reqKey := c.GetHeader("X-Sync-Key")
	if reqKey != settings.SyncKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid SyncKey"})
		return
	}

	var request struct {
		Ciphertext string `json:"ciphertext"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	payload, err := h.syncService.DecryptPayload(request.Ciphertext, settings.SyncKey)
	if err != nil {
		logrus.Errorf("failed to decrypt push payload: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to decrypt data"})
		return
	}

	// 主从: follower 不接收 push(一致性只靠 pull 镜像); 只有 master 接收上行迁移。
	if !h.syncService.IsMaster() {
		c.JSON(http.StatusOK, gin.H{"status": "ignored: follower does not accept push"})
		return
	}
	if err := h.syncService.ProcessPayload(c.Request.Context(), payload); err != nil {
		logrus.Errorf("failed to process push payload: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process data"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "success"})
}

// syncConfigBody 是 /api/sync/config 的 GET 响应与 PUT 请求体.
//
// 前端 PeerSyncPanel 用此端点统一管理同步开关与密钥, 不再走通用的 /api/settings 路径,
// 也不再出现在 Settings 页 (字段在 SystemSettings 上加了 hidden:"true").
type syncConfigBody struct {
	SyncEnabled bool `json:"sync_enabled"`
	SyncKey     string `json:"sync_key"`
	// IsMaster 只读(由 IS_MASTER 环境变量决定, 不经 API 改), 供前端区分展示
	// master 排除清单编辑 / follower 迁移提示。PUT 时忽略。
	IsMaster bool `json:"is_master"`
}

// GetConfig 返回当前节点的同步开关 + Sync Secret.
func (h *SyncHandler) GetConfig(c *gin.Context) {
	s := h.settingsManager.GetSettings()
	logrus.Debugf("GetConfig: sync_enabled=%v sync_key_set=%v", s.SyncEnabled, s.SyncKey != "")
	response.Success(c, syncConfigBody{
		SyncEnabled: s.SyncEnabled,
		SyncKey:     s.SyncKey,
		IsMaster:    h.syncService.IsMaster(),
	})
}

// UpdateConfig 更新同步开关 + Sync Secret. 通过 settingsManager 复用原有的 system_settings 表.
func (h *SyncHandler) UpdateConfig(c *gin.Context) {
	var body syncConfigBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_body", "message": err.Error()})
		return
	}
	logrus.Infof("UpdateConfig: sync_enabled=%v sync_key_len=%d", body.SyncEnabled, len(body.SyncKey))
	if err := h.settingsManager.UpdateSettings(map[string]any{
		"sync_enabled": body.SyncEnabled,
		"sync_key":     body.SyncKey,
	}); err != nil {
		logrus.Errorf("UpdateConfig failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": "update_failed", "message": err.Error()})
		return
	}
	// 立即验证读回, 防 syncer 异步 reload 没追上
	post := h.settingsManager.GetSettings()
	logrus.Infof("UpdateConfig OK, readback: sync_enabled=%v sync_key_len=%d", post.SyncEnabled, len(post.SyncKey))
	response.Success(c, syncConfigBody{
		SyncEnabled: post.SyncEnabled,
		SyncKey:     post.SyncKey,
	})
}

// ListPeers returns all configured sync peers
// GetSyncPolicy 返回当前 sync_policy(master 用于 UI 展示/编辑)。
func (h *SyncHandler) GetSyncPolicy(c *gin.Context) {
	c.JSON(http.StatusOK, h.syncService.LoadSyncPolicy(c.Request.Context()))
}

// UpdateSyncPolicy 保存 sync_policy(仅 master 有意义, follower 端保存无实际作用)。
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

// SetRole 热切本机主从角色(is_slave=true→follower, false→master), 立即生效不重启。
// 联动: 切成 master 时清掉所有 peer 的主站标记(master 是权威源, 不镜像任何 peer)。
func (h *SyncHandler) SetRole(c *gin.Context) {
	var body struct {
		IsSlave bool `json:"is_slave"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	val := "false"
	if body.IsSlave {
		val = "true"
	}
	if err := h.syncService.SetNodeRole(c.Request.Context(), val); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !body.IsSlave {
		// 切成 master → 清所有 peer 主站标记(本站自己是权威, 不镜像别人)
		if err := h.db.Model(&models.SyncPeer{}).Where("is_master = ?", true).Update("is_master", false).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "is_master": !body.IsSlave})
}

// SetPeerMaster 切换指定 peer 的主站标记(toggle):
//   - 该 peer 当前不是主站 → 设为唯一主站(其余取消) + 本站自动切 follower(镜像它=当子站);
//   - 该 peer 当前已是主站 → 取消(撤销), 本站不再镜像任何 peer, 等重新指定。
// follower 只镜像 is_master=true 的 peer。
func (h *SyncHandler) SetPeerMaster(c *gin.Context) {
	id := c.Param("id")
	var peer models.SyncPeer
	if err := h.db.First(&peer, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "peer not found"})
		return
	}
	if peer.IsMaster {
		// 撤销主站: 本站不再镜像它
		if err := h.db.Model(&models.SyncPeer{}).Where("id = ?", id).Update("is_master", false).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "peer_is_master": false})
		return
	}
	// 设为唯一主站
	if err := h.db.Model(&models.SyncPeer{}).Where("is_master = ?", true).Update("is_master", false).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Model(&models.SyncPeer{}).Where("id = ?", id).Update("is_master", true).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 联动: 指定了要镜像的 master, 本站自然是 follower(子站)
	if err := h.syncService.SetNodeRole(c.Request.Context(), "true"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "peer_is_master": true})
}

// joinRequest 是子 POST /api/sync/join 的请求体。
// 注意: 不收 my_fingerprint —— 指纹一律由服务端从 my_public_key 推导(见 JoinEndpoint),
// 防止 client 伪造指纹去覆盖(幂等 Updates)别人已有的 peer 行。
type joinRequest struct {
	Token       string `json:"token"`
	MyURL       string `json:"my_url"`
	MyPublicKey string `json:"my_public_key"`
	MyName      string `json:"my_name"`
}

// JoinEndpoint 父侧: 被邀方带 token 来加入。校验并消费 token(一次性) → 分配一把
// per-peer sync_key → 把子落库为普通 peer(is_master=false, 子不是父的镜像源) →
// 用子公钥加密该 key → 返回加密的 key + 父自己的连接信息(子据此把父设为镜像源, Task 4)。
//
// 指纹信任: PinnedFingerprint 一律由服务端从 my_public_key 推导(FingerprintOf),
// 绝不信任 client 自报的指纹 —— 否则 client 可伪造一个别人的指纹, 借幂等 Updates 覆盖
// 别人的 peer 行。server-derived 指纹也和后续 WS 握手时 FingerprintOf(peer.PublicKeyX25519)
// 的校验口径一致。
//
// 鉴权: 公开路由, 无 X-Sync-Key 中间件 —— 被邀方此时还没有任何凭证, token 本身就是
// 唯一凭证, 在 ConsumeInviteToken 里一次性校验消费(见 router.go 注册位置)。
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
	// 先校验公钥并解析(顺带拿到 childPub 加密用) —— 放在消费 token 之前, 公钥非法就直接
	// 400 而不烧掉一次性 token。
	childPub, err := services.DecodePublicKeyBase64(req.MyPublicKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad public key"})
		return
	}
	// 服务端从公钥推导指纹(唯一可信来源)。公钥已解析成功, 这里必非空。
	fp := services.FingerprintOf(req.MyPublicKey)

	// 校验并消费 token(一次性, DB 原子条件 UPDATE, 见 ConsumeInviteToken 注释)。
	// used_by 审计字段也记 server-derived 指纹, 不记 client 自报值。
	if err := h.syncService.ConsumeInviteToken(req.Token, fp); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	// 分配 per-peer key(生成逻辑留在 service 层, 见 GenerateSyncKey 注释)
	key, err := h.syncService.GenerateSyncKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 用子公钥加密 key —— 提前做, 避免落库后再失败导致 peer 悬空
	encKey, err := h.keypair.EncryptFor([]byte(key), childPub)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 落子 peer, 幂等: 按 pinned_fingerprint 显式两步(先 First 判存在, 无则 Create, 有则 Updates)。
	// 子重复 join(比如 token 过期重发)应该更新同一行, 而不是每次插一条新 peer。
	// 去重键用 server-derived fp, client 无法伪造去命中别人的行。
	var existing models.SyncPeer
	err = h.db.Where("pinned_fingerprint = ?", fp).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		peer := models.SyncPeer{
			ID:                uuid.NewString(),
			Name:              req.MyName,
			URL:               req.MyURL,
			SyncKey:           key,
			Role:              "client",
			Status:            "disconnected",
			IsMaster:          false, // 子不是父的镜像源, 父不镜像子
			PublicKeyX25519:   req.MyPublicKey,
			PinnedFingerprint: fp,
		}
		if err := h.db.Create(&peer).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	default:
		if err := h.db.Model(&existing).Updates(map[string]any{
			"name":              req.MyName,
			"url":               req.MyURL,
			"sync_key":          key,
			"public_key_x25519": req.MyPublicKey,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"sync_key_enc": encKey,
		"parent": gin.H{
			"url":         h.settingsManager.GetAppUrl(),
			"public_key":  h.keypair.PublicKeyBase64(),
			"fingerprint": h.keypair.Fingerprint(),
			"name":        h.settingsManager.GetAppUrl(), // 展示名, 无独立字段就用 app_url
		},
	})
}

// TriggerJoinParent 子侧: 本机(登录用户在自己 UI 点"加入")触发后端去加入 inviter_url
// 指向的父节点。这是本机管理操作(需要鉴权), 跟无凭证的 JoinEndpoint(父侧收加入)
// 不是一回事 —— 挂在 protected 路由组, 见 router.go 注册位置。
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

// GenerateInvite 父侧: 登录用户在自己 UI 点"生成邀请链接", 签发一次性 token(24h 有效)
// 并拼出自包含本机地址的邀请链接, 供分享给子节点扫码/粘贴加入。
//
// 鉴权: 本机管理操作, 挂在 protected 路由组(跟 /sync/join-parent 一样), 见 router.go
// 注册位置 —— 跟公开无凭证的 /sync/join(父侧收子的加入请求)不是同一类端点。
func (h *SyncHandler) GenerateInvite(c *gin.Context) {
	code, err := h.syncService.GenerateInviteToken(24 * time.Hour)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	appURL := strings.TrimRight(h.settingsManager.GetAppUrl(), "/")
	link := fmt.Sprintf("%s/#/join?token=%s&inviter=%s", appURL, code, url.QueryEscape(appURL))
	c.JSON(http.StatusOK, gin.H{"code": code, "link": link, "inviter_url": appURL})
}

func (h *SyncHandler) ListPeers(c *gin.Context) {
	var peers []models.SyncPeer
	if err := h.db.Find(&peers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": "db_error", "message": err.Error()})
		return
	}
	response.Success(c, peers)
}

// CreatePeer adds a new sync peer
func (h *SyncHandler) CreatePeer(c *gin.Context) {
	var peer models.SyncPeer
	if err := c.ShouldBindJSON(&peer); err != nil {
		logrus.Warnf("CreatePeer bind failed: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"code": "bind_failed", "message": err.Error()})
		return
	}
	logrus.Infof("CreatePeer: id=%s name=%s url=%s key_set=%v pinned_fp=%s",
		peer.ID, peer.Name, peer.URL, peer.SyncKey != "", peer.PinnedFingerprint)
	if err := h.db.Create(&peer).Error; err != nil {
		logrus.Errorf("CreatePeer DB error: %v (peer=%+v)", err, peer)
		c.JSON(http.StatusInternalServerError, gin.H{"code": "db_create_failed", "message": err.Error()})
		return
	}
	response.Success(c, peer)
}

// UpdatePeer updates an existing sync peer
func (h *SyncHandler) UpdatePeer(c *gin.Context) {
	id := c.Param("id")
	var peer models.SyncPeer
	if err := h.db.First(&peer, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": "not_found", "message": "Peer not found"})
		return
	}

	var payload models.SyncPeer
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "bind_failed", "message": err.Error()})
		return
	}

	peer.Name = payload.Name
	peer.URL = payload.URL
	peer.SyncKey = payload.SyncKey
	peer.Role = payload.Role
	peer.PinnedFingerprint = payload.PinnedFingerprint

	if err := h.db.Save(&peer).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": "db_save_failed", "message": err.Error()})
		return
	}
	response.Success(c, peer)
}

// DeletePeer removes a sync peer
func (h *SyncHandler) DeletePeer(c *gin.Context) {
	id := c.Param("id")
	if err := h.db.Delete(&models.SyncPeer{}, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": "db_delete_failed", "message": err.Error()})
		return
	}
	response.Success(c, gin.H{"id": id})
}

// ListLogs returns recent sync logs. Supports filter by peer_id and action.
// Capped at 200 rows, ordered by timestamp desc, for the UI 历史抽屉.
func (h *SyncHandler) ListLogs(c *gin.Context) {
	peerID := c.Query("peer_id")
	action := c.Query("action")
	limitStr := c.DefaultQuery("limit", "50")
	limit := 50
	if n, err := fmt.Sscanf(limitStr, "%d", &limit); err != nil || n != 1 || limit <= 0 || limit > 200 {
		limit = 50
	}

	q := h.db.Model(&models.SyncLog{}).Order("timestamp desc").Limit(limit)
	if peerID != "" {
		q = q.Where("peer_id = ?", peerID)
	}
	if action != "" {
		q = q.Where("action = ?", action)
	}

	var logs []models.SyncLog
	if err := q.Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": "db_query_failed", "message": err.Error()})
		return
	}
	response.Success(c, logs)
}
