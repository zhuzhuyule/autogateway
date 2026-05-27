package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"autogateway/internal/config"
	"autogateway/internal/models"
	"autogateway/internal/response"
	"autogateway/internal/services"
	"autogateway/internal/version"

	"github.com/gin-gonic/gin"
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

	// Active WebSocket connections
	clientsMu sync.Mutex
	clients   map[*websocket.Conn]bool
}

// NewSyncHandler creates a new SyncHandler
func NewSyncHandler(syncService *services.SyncService, settingsManager *config.SystemSettingsManager, keypair *services.NodeKeypairService, db *gorm.DB) *SyncHandler {
	return &SyncHandler{
		syncService:     syncService,
		settingsManager: settingsManager,
		keypair:         keypair,
		db:              db,
		clients:         make(map[*websocket.Conn]bool),
	}
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

	// 启用同步即同步全部 — 不再区分 sync_api_keys
	payload, err := h.syncService.ExportPayload(c.Request.Context(), since)
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
	SyncEnabled bool   `json:"sync_enabled"`
	SyncKey     string `json:"sync_key"`
}

// GetConfig 返回当前节点的同步开关 + Sync Secret.
func (h *SyncHandler) GetConfig(c *gin.Context) {
	s := h.settingsManager.GetSettings()
	logrus.Debugf("GetConfig: sync_enabled=%v sync_key_set=%v", s.SyncEnabled, s.SyncKey != "")
	response.Success(c, syncConfigBody{
		SyncEnabled: s.SyncEnabled,
		SyncKey:     s.SyncKey,
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
