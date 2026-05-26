package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"autogateway/internal/config"
	"autogateway/internal/models"
	"autogateway/internal/types"
	"autogateway/internal/version"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Broadcaster 抽象了 "向所有 ws server 端持有的 client conn 广播一条消息" 的能力.
// 用接口而不是直接依赖 handler 包以避免循环依赖.
type Broadcaster interface {
	Broadcast(msg []byte)
}

// SyncPeerManager manages outgoing connections to other nodes in the mesh.
type SyncPeerManager struct {
	db              *gorm.DB
	syncService     *SyncService
	settingsManager *config.SystemSettingsManager

	peersMu     sync.Mutex
	activePeers map[string]*websocket.Conn // key: Peer ID
	notifyChan  chan struct{}

	// broadcaster 由 app.go 在启动时注入 (setter 注入避免与 dig 注册顺序冲突).
	// 用于 Gap 3 双向化: pushToPeers 既推 client 持有的 conn, 也通过 broadcaster
	// 推 server 持有的 conn.
	broadcaster Broadcaster
}

// SetBroadcaster 注入 ws server 端的广播器.
func (m *SyncPeerManager) SetBroadcaster(b Broadcaster) {
	m.broadcaster = b
}

func NewSyncPeerManager(db *gorm.DB, syncService *SyncService, settingsManager *config.SystemSettingsManager) *SyncPeerManager {
	return &SyncPeerManager{
		db:              db,
		syncService:     syncService,
		settingsManager: settingsManager,
		activePeers:     make(map[string]*websocket.Conn),
		notifyChan:      make(chan struct{}, 1),
	}
}

// computeSinceFromPeers 取所有 peer 中最小的 last_synced_at, 作为 ExportPayload 的 since.
// 含义: 最落后那个 peer 决定了"需要回放的下限". 这样保证重启后不会丢任何变更.
// 如果没有任何已同步过的 peer (全是 null), 返回 nil → ExportPayload 会带全量.
func (m *SyncPeerManager) computeSinceFromPeers() *time.Time {
	var minTime *time.Time
	rows, err := m.db.Model(&models.SyncPeer{}).
		Select("MIN(last_synced_at) as min_t").
		Where("last_synced_at IS NOT NULL").
		Rows()
	if err != nil {
		return nil
	}
	defer rows.Close()
	if rows.Next() {
		var t sql.NullTime
		if err := rows.Scan(&t); err == nil && t.Valid {
			minTime = &t.Time
		}
	}
	return minTime
}

// Start begins the background peer connection loop and push loop
func (m *SyncPeerManager) Start(ctx context.Context) {
	go m.connectionLoop(ctx)
	go m.pushLoop(ctx)
	go m.pullLoop(ctx)
}

// NotifyChange can be called by handlers or GORM hooks to trigger a push
func (m *SyncPeerManager) NotifyChange() {
	select {
	case m.notifyChan <- struct{}{}:
	default:
		// already queued
	}
}

// connectionLoop maintains WS connections to all configured peers
func (m *SyncPeerManager) connectionLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second) // Check peer connections periodically
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.disconnectAll()
			return
		case <-ticker.C:
			settings := m.settingsManager.GetSettings()
			if !settings.SyncEnabled {
				m.disconnectAll()
				continue
			}

			var peers []models.SyncPeer
			if err := m.db.Find(&peers).Error; err != nil {
				logrus.Errorf("failed to load sync peers: %v", err)
				continue
			}

			for _, peer := range peers {
				m.ensureConnection(peer)
			}
		}
	}
}

func (m *SyncPeerManager) ensureConnection(peer models.SyncPeer) {
	m.peersMu.Lock()
	_, connected := m.activePeers[peer.ID]
	m.peersMu.Unlock()

	if connected {
		return
	}

	// Try to connect with per-peer X-Sync-Key header for WS handshake auth (Gap 2/6).
	wsURL := convertToWSUrl(peer.URL) + "/api/sync/ws"

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	headers := http.Header{}
	headers.Set("X-Sync-Key", peer.SyncKey)
	conn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		logrus.Debugf("failed to connect to peer %s at %s: %v", peer.Name, wsURL, err)
		return
	}

	// P9.1 兼容性握手: 发送 hello{version, schema_hash}, 等待 welcome/warning/reject.
	hello := WSMessage{
		Type:       "hello",
		Version:    version.Version,
		SchemaHash: ComputeSchemaHash(),
	}
	if err := conn.WriteJSON(hello); err != nil {
		logrus.Warnf("failed to send hello to peer %s: %v", peer.Name, err)
		conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var resp WSMessage
	if err := conn.ReadJSON(&resp); err != nil {
		logrus.Warnf("failed to read welcome from peer %s: %v", peer.Name, err)
		conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch resp.Type {
	case "reject":
		logrus.Warnf("peer %s rejected handshake: %s (peer=%s mine=%s)",
			peer.Name, resp.Reason, resp.MyVersion+"/"+resp.MySchema, version.Version+"/"+ComputeSchemaHash())
		m.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Updates(map[string]any{
			"status":           "rejected:" + resp.Reason,
			"peer_version":     resp.MyVersion,
			"peer_schema_hash": resp.MySchema,
		})
		m.writeLog(peer.ID, "push", "error", "handshake rejected: "+resp.Reason, "")
		conn.Close()
		return
	case "warning":
		logrus.Warnf("peer %s warning: %s (peer=%s mine=%s)",
			peer.Name, resp.Reason, resp.PeerVersion, version.Version)
		m.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Updates(map[string]any{
			"status":           "warning:" + resp.Reason,
			"peer_version":     resp.MyVersion,
			"peer_schema_hash": resp.MySchema,
		})
	case "welcome":
		m.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Updates(map[string]any{
			"status":           "connected",
			"peer_version":     resp.MyVersion,
			"peer_schema_hash": resp.MySchema,
		})
	default:
		logrus.Warnf("peer %s sent unexpected handshake response: %s", peer.Name, resp.Type)
		conn.Close()
		return
	}

	logrus.Infof("connected to peer %s (WS)", peer.Name)
	m.peersMu.Lock()
	m.activePeers[peer.ID] = conn
	m.peersMu.Unlock()

	// Read loop for this connection (detect disconnects)
	go func(peerID string, c *websocket.Conn) {
		defer func() {
			c.Close()
			m.peersMu.Lock()
			delete(m.activePeers, peerID)
			m.peersMu.Unlock()
			m.db.Model(&models.SyncPeer{}).Where("id = ?", peerID).Update("status", "disconnected")
			logrus.Infof("disconnected from peer %s", peerID)
		}()

		for {
			_, _, err := c.ReadMessage()
			if err != nil {
				break
			}
			// Currently we don't expect messages on the client connection, 
			// the server connection handles incoming pushes.
		}
	}(peer.ID, conn)
}

func (m *SyncPeerManager) disconnectAll() {
	m.peersMu.Lock()
	defer m.peersMu.Unlock()
	for _, conn := range m.activePeers {
		conn.Close()
	}
	m.activePeers = make(map[string]*websocket.Conn)
}

// pushLoop listens for local changes and pushes over WS
func (m *SyncPeerManager) pushLoop(ctx context.Context) {
	// Debounce pushes
	timer := time.NewTimer(time.Hour)
	timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.notifyChan:
			timer.Reset(2 * time.Second) // Wait 2s to aggregate changes
		case <-timer.C:
			settings := m.settingsManager.GetSettings()
			if !settings.SyncEnabled {
				continue
			}
			m.pushToPeers(ctx, settings)
		}
	}
}

func (m *SyncPeerManager) pushToPeers(ctx context.Context, settings types.SystemSettings) {
	// since = 所有 peer 中最旧的 last_synced_at, 持久化在 SyncPeer 表里, 重启不丢.
	since := m.computeSinceFromPeers()
	payload, err := m.syncService.ExportPayload(ctx, since)
	if err != nil {
		logrus.Errorf("failed to export payload for push: %v", err)
		m.writeLog("", "push", "error", fmt.Sprintf("export failed: %v", err), "")
		return
	}

	// Optimization: check if there's actually anything to push
	if len(payload.Settings) == 0 && len(payload.Groups) == 0 && len(payload.SubGroups) == 0 &&
		len(payload.ModelAliases) == 0 && len(payload.APIKeys) == 0 {
		return // Nothing changed
	}

	ciphertext, err := m.syncService.EncryptPayload(payload, settings.SyncKey)
	if err != nil {
		logrus.Errorf("failed to encrypt payload for push: %v", err)
		m.writeLog("", "push", "error", fmt.Sprintf("encrypt failed: %v", err), payloadSummary(payload))
		return
	}

	msg, _ := json.Marshal(WSMessage{Type: "sync", Ciphertext: ciphertext})

	m.peersMu.Lock()
	defer m.peersMu.Unlock()

	summary := payloadSummary(payload)
	pushedToAny := false
	for peerID, conn := range m.activePeers {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			logrus.Warnf("failed to push to peer %s: %v", peerID, err)
			m.writeLog(peerID, "push", "error", err.Error(), summary)
			conn.Close()
			delete(m.activePeers, peerID)
		} else {
			pushedToAny = true
			now := time.Now()
			m.db.Model(&models.SyncPeer{}).Where("id = ?", peerID).Update("last_synced_at", now)
			m.writeLog(peerID, "push", "success", "", summary)
		}
	}

	// Gap 3: 同时让 ws server 端持有的 conn 也收到推送 (双向 mesh).
	// peersMu 锁内调用是安全的, broadcaster 自己管 clientsMu.
	if m.broadcaster != nil {
		m.broadcaster.Broadcast(msg)
	}

	if pushedToAny {
		// 每个 peer 的 last_synced_at 已在循环里更新了 (持久化), 这里不需要全局缓存.
		logrus.Infof("successfully pushed local changes to connected peers (%s)", summary)
	}
}

// pullLoop pulls periodically from HTTP (cold-start / recovery)
func (m *SyncPeerManager) pullLoop(ctx context.Context) {
	// 5-minute poll
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Do an initial pull right away if sync is enabled
	time.Sleep(5 * time.Second)
	m.doPull(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.doPull(ctx)
		}
	}
}

func (m *SyncPeerManager) doPull(ctx context.Context) {
	settings := m.settingsManager.GetSettings()
	if !settings.SyncEnabled {
		return
	}

	var peers []models.SyncPeer
	if err := m.db.Find(&peers).Error; err != nil {
		return
	}

	for _, peer := range peers {
		pullURL := fmt.Sprintf("%s/api/sync/pull", strings.TrimRight(peer.URL, "/"))
		if peer.LastSyncedAt != nil {
			pullURL += fmt.Sprintf("?since=%s", url.QueryEscape(peer.LastSyncedAt.Format(time.RFC3339Nano)))
		}
		req, err := http.NewRequestWithContext(ctx, "GET", pullURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("X-Sync-Key", peer.SyncKey)

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			logrus.Debugf("failed to pull from peer %s: %v", peer.Name, err)
			m.writeLog(peer.ID, "pull", "error", fmt.Sprintf("http failed: %v", err), "")
			continue
		}

		if resp.StatusCode != http.StatusOK {
			m.writeLog(peer.ID, "pull", "error", fmt.Sprintf("http status %d", resp.StatusCode), "")
			resp.Body.Close()
			continue
		}

		var response struct {
			Ciphertext string `json:"ciphertext"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			m.writeLog(peer.ID, "pull", "error", fmt.Sprintf("decode response: %v", err), "")
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if response.Ciphertext == "" {
			continue // No new data, not an error - skip logging to avoid noise
		}

		payload, err := m.syncService.DecryptPayload(response.Ciphertext, settings.SyncKey)
		if err != nil {
			logrus.Errorf("failed to decrypt pulled payload from peer %s: %v", peer.Name, err)
			m.writeLog(peer.ID, "pull", "error", fmt.Sprintf("decrypt: %v", err), "")
			continue
		}

		if err := m.syncService.ProcessPayload(ctx, payload); err != nil {
			logrus.Errorf("failed to process pulled payload from peer %s: %v", peer.Name, err)
			m.writeLog(peer.ID, "pull", "error", fmt.Sprintf("merge: %v", err), payloadSummary(payload))
			continue
		}

		now := time.Now()
		m.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Update("last_synced_at", now)
		summary := payloadSummary(payload)
		m.writeLog(peer.ID, "pull", "success", "", summary)
		logrus.Infof("successfully pulled and merged changes from peer %s (%s)", peer.Name, summary)
	}
}

// writeLog 写一条 SyncLog 到数据库. 失败时只 warn, 不阻断同步主路径.
func (m *SyncPeerManager) writeLog(peerID, action, status, errMsg, details string) {
	log := models.SyncLog{
		PeerID:       peerID,
		Action:       action,
		Status:       status,
		ErrorMessage: errMsg,
		Details:      details,
		Timestamp:    time.Now(),
	}
	if err := m.db.Create(&log).Error; err != nil {
		logrus.Warnf("failed to write sync log: %v", err)
	}
}

// payloadSummary 把 SyncPayload 摘要成 "groups=2,aliases=5" 这样的字符串, 给 SyncLog.Details 用.
func payloadSummary(p *SyncPayload) string {
	parts := []string{}
	if n := len(p.Settings); n > 0 {
		parts = append(parts, fmt.Sprintf("settings=%d", n))
	}
	if n := len(p.Groups); n > 0 {
		parts = append(parts, fmt.Sprintf("groups=%d", n))
	}
	if n := len(p.SubGroups); n > 0 {
		parts = append(parts, fmt.Sprintf("sub_groups=%d", n))
	}
	if n := len(p.ModelAliases); n > 0 {
		parts = append(parts, fmt.Sprintf("aliases=%d", n))
	}
	if n := len(p.APIKeys); n > 0 {
		parts = append(parts, fmt.Sprintf("api_keys=%d", n))
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, ",")
}

// PurgeOldLogs 删除超过 daysToKeep 天的 sync_logs, 避免无限增长.
func (m *SyncPeerManager) PurgeOldLogs(daysToKeep int) {
	cutoff := time.Now().AddDate(0, 0, -daysToKeep)
	res := m.db.Where("timestamp < ?", cutoff).Delete(&models.SyncLog{})
	if res.Error != nil {
		logrus.Warnf("failed to purge old sync_logs: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		logrus.Infof("purged %d sync_logs older than %d days", res.RowsAffected, daysToKeep)
	}
}

func convertToWSUrl(httpURL string) string {
	u := strings.TrimRight(httpURL, "/")
	if strings.HasPrefix(u, "https://") {
		return "wss://" + u[8:]
	}
	if strings.HasPrefix(u, "http://") {
		return "ws://" + u[7:]
	}
	return "ws://" + u
}
