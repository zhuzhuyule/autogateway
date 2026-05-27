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
	keypair         *NodeKeypairService

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

func NewSyncPeerManager(db *gorm.DB, syncService *SyncService, settingsManager *config.SystemSettingsManager, keypair *NodeKeypairService) *SyncPeerManager {
	return &SyncPeerManager{
		db:              db,
		syncService:     syncService,
		settingsManager: settingsManager,
		keypair:         keypair,
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

	// P9.1 兼容性握手 + P9.x 公钥交换: hello 携带本机 X25519 公钥.
	hello := WSMessage{
		Type:       "hello",
		Version:    version.Version,
		SchemaHash: ComputeSchemaHash(),
		PublicKey:  m.keypair.PublicKeyBase64(),
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
	// P9.x: 若用户在 SyncPeer.PinnedFingerprint 钉了对端指纹, 这里要校验对端
	// MyPublicKey 算出的指纹是否一致, 防 MITM.
	if peer.PinnedFingerprint != "" && resp.MyPublicKey != "" {
		actualFp := FingerprintOf(resp.MyPublicKey)
		if actualFp != peer.PinnedFingerprint {
			logrus.Warnf("peer %s fingerprint mismatch: pinned=%s actual=%s",
				peer.Name, peer.PinnedFingerprint, actualFp)
			m.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Update("status",
				"rejected:fingerprint_mismatch")
			m.writeLog(peer.ID, "push", "error",
				fmt.Sprintf("fingerprint mismatch pinned=%s actual=%s", peer.PinnedFingerprint, actualFp), "")
			conn.Close()
			return
		}
	}

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
			"status":            "warning:" + resp.Reason,
			"peer_version":      resp.MyVersion,
			"peer_schema_hash":  resp.MySchema,
			"public_key_x25519": resp.MyPublicKey,
		})
	case "welcome":
		m.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Updates(map[string]any{
			"status":            "connected",
			"peer_version":      resp.MyVersion,
			"peer_schema_hash":  resp.MySchema,
			"public_key_x25519": resp.MyPublicKey,
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
	// 当前 peer 用值拷贝是为了在 goroutine 里安全引用 (避免 closure 捕获 range var).
	peerCopy := peer
	go func(peerCopy models.SyncPeer, c *websocket.Conn) {
		peerID := peerCopy.ID
		defer func() {
			c.Close()
			m.peersMu.Lock()
			delete(m.activePeers, peerID)
			m.peersMu.Unlock()
			m.db.Model(&models.SyncPeer{}).Where("id = ?", peerID).Update("status", "disconnected")
			logrus.Infof("disconnected from peer %s", peerCopy.Name)
		}()

		// 关键 — 不能 silent drop, 否则对端 server 通过 broadcaster 推给本端的
		// sync 帧全部丢失, 这是 mini → 本机 (本机作 client) 实时同步的唯一通道.
		settings := m.settingsManager.GetSettings()
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				break
			}
			// 尝试解码 sync 帧 + ProcessPayload, 保证 server→client 推送也生效
			var msgIn WSMessage
			if err := json.Unmarshal(msg, &msgIn); err != nil {
				continue
			}
			if msgIn.Type != "sync" || msgIn.Ciphertext == "" {
				continue // 心跳 / 其他类型暂时忽略
			}
			// 优先非对称解 (用对端公钥), 失败 fallback legacy.
			var payload *SyncPayload
			var perr error
			if peerCopy.PublicKeyX25519 != "" {
				payload, perr = m.syncService.DecryptPayloadFrom(msgIn.Ciphertext, peerCopy.PublicKeyX25519)
				if perr != nil && settings.SyncKey != "" {
					payload, perr = m.syncService.DecryptPayload(msgIn.Ciphertext, settings.SyncKey)
				}
			} else if settings.SyncKey != "" {
				payload, perr = m.syncService.DecryptPayload(msgIn.Ciphertext, settings.SyncKey)
			} else {
				continue
			}
			if perr != nil {
				logrus.Warnf("client-side ws decrypt failed from peer %s: %v", peerCopy.Name, perr)
				m.writeLog(peerID, "push", "error", "client ws decrypt: "+perr.Error(), "")
				continue
			}
			if err := m.syncService.ProcessPayload(context.Background(), payload); err != nil {
				logrus.Warnf("client-side ws merge failed from peer %s: %v", peerCopy.Name, err)
				m.writeLog(peerID, "push", "error", "client ws merge: "+err.Error(), "")
				continue
			}
			m.writeLog(peerID, "push", "success", "", "client-ws")
			logrus.Infof("client-ws: received and merged %s from peer %s", payloadSummary(payload), peerCopy.Name)
		}
	}(peerCopy, conn)
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

	// P9.x: per-peer 单独加密. 把全部 peers 从 DB 拉一次, 找到每个 ID 对应的
	// PublicKeyX25519 (握手时落库), 用 nacl/box 单独加密一份给该 peer.
	// 这样单 peer 私钥泄漏只暴露发给它的密文, 其他 peer 安全.
	var allPeers []models.SyncPeer
	if err := m.db.Find(&allPeers).Error; err != nil {
		logrus.Errorf("failed to load peers for per-peer encryption: %v", err)
		m.writeLog("", "push", "error", fmt.Sprintf("load peers: %v", err), "")
		return
	}
	peerPubByID := make(map[string]string, len(allPeers))
	for _, p := range allPeers {
		peerPubByID[p.ID] = p.PublicKeyX25519
	}

	summary := payloadSummary(payload)

	m.peersMu.Lock()
	defer m.peersMu.Unlock()

	pushedToAny := false
	for peerID, conn := range m.activePeers {
		var msgBytes []byte
		peerPub := peerPubByID[peerID]
		if peerPub != "" {
			// 主路径: 用 peer 的 X25519 公钥加密
			ciphertext, err := m.syncService.EncryptPayloadFor(payload, peerPub)
			if err != nil {
				logrus.Warnf("failed to encrypt for peer %s (asymmetric): %v", peerID, err)
				m.writeLog(peerID, "push", "error", "asym encrypt: "+err.Error(), summary)
				continue
			}
			msgBytes, _ = json.Marshal(WSMessage{Type: "sync", Ciphertext: ciphertext})
		} else {
			// 回退: 老版本 peer 未升级, 走全局 SyncKey
			ciphertext, err := m.syncService.EncryptPayload(payload, settings.SyncKey)
			if err != nil {
				logrus.Warnf("failed to encrypt for peer %s (legacy): %v", peerID, err)
				m.writeLog(peerID, "push", "error", "legacy encrypt: "+err.Error(), summary)
				continue
			}
			msgBytes, _ = json.Marshal(WSMessage{Type: "sync", Ciphertext: ciphertext})
		}

		if err := conn.WriteMessage(websocket.TextMessage, msgBytes); err != nil {
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

	// Gap 3 双向 mesh: ws server 端持有的 conn 也要推. 它们不在 m.activePeers 里,
	// 只能用对应的 peer 公钥单独加密 (server 端不知道 conn 对应哪个 peer 公钥的话,
	// 退化到对每个 peer 加密一份, 让 broadcaster 按 conn 拣选投递).
	// 当前 broadcaster 是无差别广播一条 msg, 这一份用回退路径加密 (settings.SyncKey),
	// 保证 server 端 conn 仍能解; 后续可扩展 broadcaster 接口支持 per-peer 分发.
	if m.broadcaster != nil && settings.SyncKey != "" {
		legacyCt, err := m.syncService.EncryptPayload(payload, settings.SyncKey)
		if err == nil {
			legacyMsg, _ := json.Marshal(WSMessage{Type: "sync", Ciphertext: legacyCt})
			m.broadcaster.Broadcast(legacyMsg)
		}
	}

	if pushedToAny {
		// 每个 peer 的 last_synced_at 已在循环里更新了 (持久化), 这里不需要全局缓存.
		logrus.Infof("successfully pushed local changes to connected peers (%s)", summary)
	}
}

// pullLoop pulls periodically from HTTP (cold-start / recovery)
func (m *SyncPeerManager) pullLoop(ctx context.Context) {
	// 5-minute poll
	// 1 分钟 pull 兜底: ws push 在 LAN 内秒级触达, pull 兜底覆盖 ws 不通的场景
	// (例如对端 docker 容器网络隔离, 没法 dial 本端 ws server). 1min 间隔在
	// 实时性 (用户能接受的等待) 和 mini 端负载 (60次/小时 vs 12次/小时) 之间平衡.
	ticker := time.NewTicker(1 * time.Minute)
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

	myPubKey := m.keypair.PublicKeyBase64()

	for _, peer := range peers {
		pullURL := fmt.Sprintf("%s/api/sync/pull", strings.TrimRight(peer.URL, "/"))
		// 把本端公钥显式带在 query, 让对端用这个加密响应 (绕过对端 db 里可能陈旧的公钥记录).
		// 这是修 mini 端 "Max" peer 错存了自己公钥 → 加密给本端无法解 的 stale-record bug.
		params := []string{}
		// doPull 用专属的 LastPulledAt 作为 since 下限, 跟 LastSyncedAt (push 也会改) 解耦.
		// 必须用 UTC 序列化 — SQLite TEXT 比较是字典序, "+00:00" 时间戳跟
		// "+08:00" 时间戳字典序混乱 (15:46+00 < 23:45+08 字面上, 但实际 15:46 UTC > 15:45 UTC).
		if peer.LastPulledAt != nil {
			params = append(params, "since="+url.QueryEscape(peer.LastPulledAt.UTC().Format(time.RFC3339Nano)))
		}
		if myPubKey != "" {
			params = append(params, "my_public_key="+url.QueryEscape(myPubKey))
		}
		if len(params) > 0 {
			pullURL += "?" + strings.Join(params, "&")
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

		// 跟 ws 路径一致: 优先非对称 (用对端公钥), 失败再回退 legacy.
		var payload *SyncPayload
		if peer.PublicKeyX25519 != "" {
			payload, err = m.syncService.DecryptPayloadFrom(response.Ciphertext, peer.PublicKeyX25519)
			if err != nil && settings.SyncKey != "" {
				payload, err = m.syncService.DecryptPayload(response.Ciphertext, settings.SyncKey)
			}
		} else {
			payload, err = m.syncService.DecryptPayload(response.Ciphertext, settings.SyncKey)
		}
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
		// 同时更新 last_synced_at (展示用) 和 last_pulled_at (pull since 用)
		m.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Updates(map[string]any{
			"last_synced_at": now,
			"last_pulled_at": now,
		})
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
