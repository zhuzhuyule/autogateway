package services

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"autogateway/internal/models"

	"github.com/google/uuid"
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

// ConsumeInviteToken 校验并消费一次性 token(存在 && 未用 && 未过期)。
// 单条原子条件 UPDATE: WHERE 已含 used=false AND expires_at>now, 靠 RowsAffected==0 判失败 ——
// 没有 SELECT-then-UPDATE 的 TOCTOU 窗口, Postgres/MySQL 下并发双用被 DB 行锁串行化,
// 第二个 UPDATE 因 used 已 true 匹配 0 行返回错误(mirror video_task_service.go 的 Claim())。
func (s *SyncService) ConsumeInviteToken(code, usedByFingerprint string) error {
	now := time.Now()
	res := s.db.Model(&models.InviteToken{}).
		Where("code = ? AND used = ? AND expires_at > ?", code, false, now).
		Updates(map[string]any{"used": true, "used_by": usedByFingerprint, "used_at": &now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("invite token invalid / used / expired")
	}
	return nil
}

// PurgeExpiredInvites 清理过期/已用 token(避免无限增长)。返回删除条数。
func (s *SyncService) PurgeExpiredInvites() int64 {
	res := s.db.Where("expires_at < ? OR used = ?", time.Now(), true).Delete(&models.InviteToken{})
	return res.RowsAffected
}

// GenerateSyncKey 导出包装: handler 包(sync_handler.go 的 JoinEndpoint)拿不到私有的
// randSyncKey, 但落子 peer 前需要分配一把 per-peer sync_key —— 让这一步留在 service 层,
// handler 只管调用, 不重复实现随机数生成逻辑。
func (s *SyncService) GenerateSyncKey() (string, error) {
	return randSyncKey()
}

// randSyncKey 生成一把 per-peer sync_key(join 时父分配给子)。
func randSyncKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gen sync key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// JoinParent 子侧: 用邀请 token 加入 inviterURL 指向的父节点。
// POST 父的 /api/sync/join(Task 3), 解密父分配的 per-peer key, 把父落库为本机
// 唯一的 is_master peer(换父先清掉旧的 is_master), 最后本机切 follower。
//
// 请求体对齐 Task 3 的 joinRequest: 不发 my_fingerprint —— 父侧一律从 my_public_key
// 自己推导指纹(JoinEndpoint 的 FingerprintOf), 子端自报的指纹不可信。
func (s *SyncService) JoinParent(ctx context.Context, inviterURL, token string) error {
	reqBody, err := json.Marshal(map[string]string{
		"token":         token,
		"my_url":        s.selfAppURL(),
		"my_public_key": s.keypair.PublicKeyBase64(),
		"my_name":       s.selfAppURL(),
	})
	if err != nil {
		return fmt.Errorf("marshal join request: %w", err)
	}

	joinURL := strings.TrimRight(inviterURL, "/") + "/api/sync/join"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("build join request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(httpReq)
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
		return fmt.Errorf("decode join response: %w", err)
	}

	// 解密父分配的 per-peer sync key(nacl/box, 父公钥 + 本机私钥)。
	parentPub, err := DecodePublicKeyBase64(out.Parent.PublicKey)
	if err != nil {
		return fmt.Errorf("bad parent public key: %w", err)
	}
	keyBytes, err := s.keypair.DecryptFrom(out.SyncKeyEnc, parentPub)
	if err != nil {
		return fmt.Errorf("decrypt sync key: %w", err)
	}

	// 一个事务里换父: 先清掉旧的 is_master(本机至多一个 master peer), 再按
	// pinned_fingerprint 显式两步落新父(First→Create/Updates, 不用 FirstOrCreate —
	// 跟 JoinEndpoint 的落子 peer 写法保持一致), 最后本机切 follower。
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.SyncPeer{}).Where("is_master = ?", true).
			Update("is_master", false).Error; err != nil {
			return fmt.Errorf("clear old master peer: %w", err)
		}

		var existing models.SyncPeer
		err := tx.Where("pinned_fingerprint = ?", out.Parent.Fingerprint).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			parent := models.SyncPeer{
				ID:                uuid.NewString(),
				Name:              out.Parent.Name,
				URL:               out.Parent.URL,
				SyncKey:           string(keyBytes),
				Role:              "client",
				Status:            "disconnected",
				IsMaster:          true,
				PublicKeyX25519:   out.Parent.PublicKey,
				PinnedFingerprint: out.Parent.Fingerprint,
			}
			if err := tx.Create(&parent).Error; err != nil {
				return fmt.Errorf("create parent peer: %w", err)
			}
		case err != nil:
			return err
		default:
			if err := tx.Model(&existing).Updates(map[string]any{
				"url":               out.Parent.URL,
				"sync_key":          string(keyBytes),
				"public_key_x25519": out.Parent.PublicKey,
				"is_master":         true,
			}).Error; err != nil {
				return fmt.Errorf("update parent peer: %w", err)
			}
		}

		return s.setNodeRoleTx(tx, "true")
	})
}

// selfAppURL 读本机对外地址(join 请求里告诉父怎么描述本机)。取不到就留空 ——
// 父侧落子 peer 的 URL 用途是展示 + 后续 follower pull 用的连接地址, 缺失不阻断加入流程。
func (s *SyncService) selfAppURL() string {
	var row models.SystemSetting
	if err := s.db.Where("setting_key = ?", "app_url").First(&row).Error; err == nil {
		return row.SettingValue
	}
	return ""
}

// setNodeRoleTx 事务内版 SetNodeRole(JoinParent 换父和切 follower 要在同一事务原子生效,
// 不能等事务提交后再单独开一次连接调 SetNodeRole)。
func (s *SyncService) setNodeRoleTx(tx *gorm.DB, isSlaveVal string) error {
	var row models.SystemSetting
	err := tx.Where("setting_key = ?", nodeIsSlaveSettingKey).First(&row).Error
	if err != nil {
		return tx.Create(&models.SystemSetting{SettingKey: nodeIsSlaveSettingKey, SettingValue: isSlaveVal}).Error
	}
	return tx.Model(&row).Update("setting_value", isSlaveVal).Error
}
