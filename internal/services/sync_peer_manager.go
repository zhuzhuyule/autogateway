package services

import (
	"context"
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

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// SyncPeerManager manages outgoing connections to other nodes in the mesh.
type SyncPeerManager struct {
	db              *gorm.DB
	syncService     *SyncService
	settingsManager *config.SystemSettingsManager

	peersMu       sync.Mutex
	activePeers   map[string]*websocket.Conn // key: Peer ID
	notifyChan    chan struct{}
	lastPushTime  time.Time
}

func NewSyncPeerManager(db *gorm.DB, syncService *SyncService, settingsManager *config.SystemSettingsManager) *SyncPeerManager {
	return &SyncPeerManager{
		db:              db,
		syncService:     syncService,
		settingsManager: settingsManager,
		activePeers:     make(map[string]*websocket.Conn),
		notifyChan:      make(chan struct{}, 1),
		lastPushTime:    time.Now().Add(-24 * time.Hour), // Ensure initial push if needed
	}
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

	// Try to connect
	wsURL := convertToWSUrl(peer.URL) + "/api/sync/ws"
	
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		logrus.Debugf("failed to connect to peer %s at %s: %v", peer.Name, wsURL, err)
		return
	}

	logrus.Infof("connected to peer %s (WS)", peer.Name)
	m.peersMu.Lock()
	m.activePeers[peer.ID] = conn
	m.peersMu.Unlock()

	// Update status
	m.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Update("status", "connected")

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
	// We export changes since lastPushTime
	payload, err := m.syncService.ExportPayload(ctx, &m.lastPushTime, settings.SyncAPIKeys)
	if err != nil {
		logrus.Errorf("failed to export payload for push: %v", err)
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
		return
	}

	msg, _ := json.Marshal(map[string]string{
		"ciphertext": ciphertext,
	})

	m.peersMu.Lock()
	defer m.peersMu.Unlock()

	pushedToAny := false
	for peerID, conn := range m.activePeers {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			logrus.Warnf("failed to push to peer %s: %v", peerID, err)
			conn.Close()
			delete(m.activePeers, peerID)
		} else {
			pushedToAny = true
			// Update last synced at
			now := time.Now()
			m.db.Model(&models.SyncPeer{}).Where("id = ?", peerID).Update("last_synced_at", now)
		}
	}

	if pushedToAny {
		m.lastPushTime = time.Now()
		logrus.Infof("successfully pushed local changes to connected peers")
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
		if settings.SyncAPIKeys && peer.SyncAPIKeys {
			if strings.Contains(pullURL, "?") {
				pullURL += "&sync_api_keys=true"
			} else {
				pullURL += "?sync_api_keys=true"
			}
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
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var response struct {
			Ciphertext string `json:"ciphertext"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if response.Ciphertext == "" {
			continue // No new data
		}

		payload, err := m.syncService.DecryptPayload(response.Ciphertext, settings.SyncKey)
		if err != nil {
			logrus.Errorf("failed to decrypt pulled payload from peer %s: %v", peer.Name, err)
			continue
		}

		if err := m.syncService.ProcessPayload(ctx, payload); err != nil {
			logrus.Errorf("failed to process pulled payload from peer %s: %v", peer.Name, err)
			continue
		}

		now := time.Now()
		m.db.Model(&models.SyncPeer{}).Where("id = ?", peer.ID).Update("last_synced_at", now)
		logrus.Infof("successfully pulled and merged changes from peer %s", peer.Name)
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
