package services

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"autogateway/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var videoTaskTestDBSeq atomic.Int64

func newVideoTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 每个测试独立的 in-memory DB(唯一的 cache 名),避免共享 cache 跨测试泄漏数据。
	dsn := fmt.Sprintf("file:videotask_%d?mode=memory&cache=shared", videoTaskTestDBSeq.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.VideoTask{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestVideoTaskService_CreateAndGet(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)

	task, err := svc.Create("agnes", "agnes-video-v2.0", "a cat", `{"num_frames":121}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected generated id")
	}
	if task.Status != models.VideoTaskPending {
		t.Fatalf("expected pending, got %s", task.Status)
	}

	got, err := svc.Get(task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Prompt != "a cat" {
		t.Fatalf("expected prompt round-trip, got %q", got.Prompt)
	}
}

func TestVideoTaskService_ListByIDs(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	a, _ := svc.Create("g", "m", "p1", "")
	b, _ := svc.Create("g", "m", "p2", "")
	_, _ = svc.Create("g", "m", "p3", "")

	got, err := svc.ListByIDs([]string{a.ID, b.ID})
	if err != nil {
		t.Fatalf("list by ids: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

func TestVideoTaskService_ListPaged(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	for i := 0; i < 5; i++ {
		_, _ = svc.Create("g", "m", "p", "")
	}
	tasks, total, err := svc.List("", 1, 3)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 5 {
		t.Fatalf("expected total 5, got %d", total)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected page size 3, got %d", len(tasks))
	}
}

func TestVideoTaskService_ClaimExclusive(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")

	ok1, err := svc.Claim(task.ID, "owner-A", 10*time.Minute)
	if err != nil {
		t.Fatalf("claim1: %v", err)
	}
	ok2, err := svc.Claim(task.ID, "owner-B", 10*time.Minute)
	if err != nil {
		t.Fatalf("claim2: %v", err)
	}
	if !ok1 || ok2 {
		t.Fatalf("expected only first claim to succeed, got ok1=%v ok2=%v", ok1, ok2)
	}
	got, _ := svc.Get(task.ID)
	if got.Status != models.VideoTaskRunning || got.LeaseOwner != "owner-A" {
		t.Fatalf("expected running/owner-A, got %s/%s", got.Status, got.LeaseOwner)
	}
}

func TestVideoTaskService_ClaimExpiredTakeover(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")

	// owner-A 用一个已过期的租约抢占(负时长 → lease_expires 在过去)
	ok, _ := svc.Claim(task.ID, "owner-A", -1*time.Minute)
	if !ok {
		t.Fatal("expected first claim ok")
	}
	// owner-B 应能接管(A 的租约已过期)
	ok, err := svc.Claim(task.ID, "owner-B", 10*time.Minute)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if !ok {
		t.Fatal("expected owner-B to take over expired lease")
	}
}

func TestVideoTaskService_FindClaimable(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	_, _ = svc.Create("g", "m", "p1", "")
	_, _ = svc.Create("g", "m", "p2", "")
	ids, err := svc.FindClaimable(10)
	if err != nil {
		t.Fatalf("find claimable: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 claimable, got %d", len(ids))
	}
}

func TestVideoTaskService_Lifecycle(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")
	_, _ = svc.Claim(task.ID, "A", 10*time.Minute)

	if err := svc.UpdateProgress(task.ID, 50, "up-123"); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := svc.MarkCompleted(task.ID, "https://x/v.mp4"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, _ := svc.Get(task.ID)
	if got.Status != models.VideoTaskCompleted || got.VideoURL != "https://x/v.mp4" {
		t.Fatalf("bad terminal state: %+v", got)
	}
	if got.CompletedAt == nil {
		t.Fatal("expected completed_at set")
	}
}

func TestVideoTaskService_RenewLease(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")
	_, _ = svc.Claim(task.ID, "A", 1*time.Minute)
	before, _ := svc.Get(task.ID)
	if err := svc.RenewLease(task.ID, "A", 30*time.Minute); err != nil {
		t.Fatalf("renew: %v", err)
	}
	after, _ := svc.Get(task.ID)
	if !after.LeaseExpires.After(*before.LeaseExpires) {
		t.Fatal("expected lease extended")
	}
}

func TestVideoTaskService_CancelAndRetryAndDelete(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", `{"num_frames":121}`)

	if err := svc.Cancel(task.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, _ := svc.Get(task.ID)
	if got.Status != models.VideoTaskCanceled {
		t.Fatalf("expected canceled, got %s", got.Status)
	}

	_ = svc.MarkFailed(task.ID, "boom") // 置为 failed 以便重试
	retry, err := svc.Retry(task.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retry.ID == task.ID || retry.Status != models.VideoTaskPending || retry.Params != `{"num_frames":121}` {
		t.Fatalf("retry should clone into a fresh pending task: %+v", retry)
	}

	if err := svc.Delete(task.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(task.ID); err == nil {
		t.Fatal("expected deleted task to be gone")
	}
}

// I1: 用户取消后, worker 迟到的 MarkCompleted/MarkFailed/UpdateProgress 不得
// 把已取消任务"复活"成 completed/failed, 也不得覆盖进度。
func TestVideoTaskService_TerminalWritesDoNotResurrectCanceled(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")
	_, _ = svc.Claim(task.ID, "A", 10*time.Minute) // running
	_ = svc.Cancel(task.ID)                         // 用户取消

	// worker 迟到的写入都应 no-op(任务非 running)
	if err := svc.UpdateProgress(task.ID, 80, "up-1"); err != nil {
		t.Fatalf("update progress: %v", err)
	}
	if err := svc.MarkCompleted(task.ID, "https://x/v.mp4"); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	if err := svc.MarkFailed(task.ID, "boom"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	got, _ := svc.Get(task.ID)
	if got.Status != models.VideoTaskCanceled {
		t.Fatalf("canceled task must stay canceled, got %s", got.Status)
	}
	if got.VideoURL != "" || got.Progress != 0 {
		t.Fatalf("canceled task must not be mutated, got url=%q progress=%d", got.VideoURL, got.Progress)
	}
}
