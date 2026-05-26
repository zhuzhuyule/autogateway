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

	for {
		messageType, msg, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logrus.Errorf("ws read error: %v", err)
			}
			break
		}

		if messageType == websocket.TextMessage {
			// Expecting a JSON object with ciphertext and sync_api_keys flag
			var request struct {
				Ciphertext string `json:"ciphertext"`
			}
			if err := json.Unmarshal(msg, &request); err != nil {
				logrus.Warnf("failed to unmarshal sync message: %v", err)
				h.writeLog("", "push", "error", fmt.Sprintf("unmarshal: %v", err), "ws")
				continue
			}

			payload, err := h.syncService.DecryptPayload(request.Ciphertext, settings.SyncKey)
			if err != nil {
				logrus.Errorf("failed to decrypt sync payload: %v", err)
				h.writeLog("", "push", "error", fmt.Sprintf("decrypt: %v", err), "ws")
				continue
			}

			if err := h.syncService.ProcessPayload(context.Background(), payload); err != nil {
				logrus.Errorf("failed to process sync payload: %v", err)
				h.writeLog(payload.SourcePeerID, "push", "error", fmt.Sprintf("merge: %v", err), "ws")
				continue
			}

			h.writeLog(payload.SourcePeerID, "push", "success", "", "ws")
			logrus.Infof("successfully processed sync payload from peer %s (ws)", payload.SourcePeerID)
		}
	}
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

	// Do they want API keys?
	syncAPIKeys := c.Query("sync_api_keys") == "true" && settings.SyncAPIKeys

	payload, err := h.syncService.ExportPayload(c.Request.Context(), since, syncAPIKeys)
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
	peer.SyncAPIKeys = payload.SyncAPIKeys

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
