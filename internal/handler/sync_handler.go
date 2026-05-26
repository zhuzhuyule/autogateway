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
	db              *gorm.DB

	// Active WebSocket connections
	clientsMu sync.Mutex
	clients   map[*websocket.Conn]bool
}

// NewSyncHandler creates a new SyncHandler
func NewSyncHandler(syncService *services.SyncService, settingsManager *config.SystemSettingsManager, db *gorm.DB) *SyncHandler {
	return &SyncHandler{
		syncService:     syncService,
		settingsManager: settingsManager,
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

	for _, c := range conns {
		if err := c.WriteMessage(websocket.TextMessage, msg); err != nil {
			logrus.Warnf("ws broadcast failed: %v", err)
			h.clientsMu.Lock()
			delete(h.clients, c)
			h.clientsMu.Unlock()
			_ = c.Close()
		}
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

			payload, err := h.syncService.DecryptPayload(msgIn.Ciphertext, settings.SyncKey)
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

	// 通过 — 若 minor/patch 不一致只发 welcome + reason="minor_diff", 不阻断
	resp := services.WSMessage{Type: "welcome", MyVersion: myVer, MySchema: mySchema}
	if hello.Version != myVer {
		resp.Type = "warning"
		resp.Reason = "minor_version_diff"
		resp.PeerVersion = hello.Version
	}
	_ = ws.WriteJSON(resp)
	return true
}

// PullEndpoint allows peers to fetch the latest state via HTTP (cold-start / recovery)
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

	// Verify auth (simple pre-shared SyncKey check via header)
	reqKey := c.GetHeader("X-Sync-Key")
	if reqKey != settings.SyncKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid SyncKey"})
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

	ciphertext, err := h.syncService.EncryptPayload(payload, settings.SyncKey)
	if err != nil {
		logrus.Errorf("failed to encrypt payload: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encrypt data"})
		return
	}

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
	c.JSON(http.StatusOK, syncConfigBody{
		SyncEnabled: s.SyncEnabled,
		SyncKey:     s.SyncKey,
	})
}

// UpdateConfig 更新同步开关 + Sync Secret. 通过 settingsManager 复用原有的 system_settings 表.
func (h *SyncHandler) UpdateConfig(c *gin.Context) {
	var body syncConfigBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if err := h.settingsManager.UpdateSettings(map[string]any{
		"sync_enabled": body.SyncEnabled,
		"sync_key":     body.SyncKey,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// ListPeers returns all configured sync peers
func (h *SyncHandler) ListPeers(c *gin.Context) {
	var peers []models.SyncPeer
	if err := h.db.Find(&peers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch peers"})
		return
	}
	c.JSON(http.StatusOK, peers)
}

// CreatePeer adds a new sync peer
func (h *SyncHandler) CreatePeer(c *gin.Context) {
	var peer models.SyncPeer
	if err := c.ShouldBindJSON(&peer); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload"})
		return
	}
	if err := h.db.Create(&peer).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create peer"})
		return
	}
	c.JSON(http.StatusOK, peer)
}

// UpdatePeer updates an existing sync peer
func (h *SyncHandler) UpdatePeer(c *gin.Context) {
	id := c.Param("id")
	var peer models.SyncPeer
	if err := h.db.First(&peer, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Peer not found"})
		return
	}

	var payload models.SyncPeer
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload"})
		return
	}

	peer.Name = payload.Name
	peer.URL = payload.URL
	peer.SyncKey = payload.SyncKey
	peer.Role = payload.Role

	if err := h.db.Save(&peer).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update peer"})
		return
	}
	c.JSON(http.StatusOK, peer)
}

// DeletePeer removes a sync peer
func (h *SyncHandler) DeletePeer(c *gin.Context) {
	id := c.Param("id")
	if err := h.db.Delete(&models.SyncPeer{}, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete peer"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success"})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch logs"})
		return
	}
	c.JSON(http.StatusOK, logs)
}
