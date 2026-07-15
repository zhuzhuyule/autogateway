package services

import (
	"testing"
	"time"

	"autogateway/internal/models"
)

// newInviteTestService 在 newTestSyncService 基础上补 AutoMigrate(&models.InviteToken{}) ——
// newSyncTestDB 的迁移列表里没有 InviteToken(Task 1 只在生产迁移里加了它), 内存库测试需要自己补。
func newInviteTestService(t *testing.T) *SyncService {
	s, db := newTestSyncService(t)
	if err := db.AutoMigrate(&models.InviteToken{}); err != nil {
		t.Fatalf("failed to auto migrate InviteToken: %v", err)
	}
	return s
}

func TestInvite_ConsumeOnce(t *testing.T) {
	s := newInviteTestService(t)
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
	s := newInviteTestService(t)
	code, _ := s.GenerateInviteToken(-time.Minute) // 已过期
	if err := s.ConsumeInviteToken(code, "fp"); err == nil {
		t.Fatal("过期 token 应拒绝")
	}
}

func TestInvite_Unknown(t *testing.T) {
	s := newInviteTestService(t)
	if err := s.ConsumeInviteToken("nope", "fp"); err == nil {
		t.Fatal("未知 token 应拒绝")
	}
}
