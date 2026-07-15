package services

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"autogateway/internal/models"
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
