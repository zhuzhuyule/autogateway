# 视频异步任务队列 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Playground 视频生成从前端阻塞 POST 改造成后端持久化任务队列 + 后台 worker,前端发起后秒回、30s 轮询回填、并提供轻量队列管理面板。

**Architecture:** 新增 `video_tasks` 表 + `VideoTaskService`(CRUD/状态机/DB 条件 UPDATE 租约)+ `VideoTaskWorker`(后台 goroutine 池,claim 任务后复用 `ChannelProxy` 发 agnes 请求)+ REST API。前端 `sendVideo` 改为入队,顶层 30s 定时器扫聊天消息批量回填,新增队列抽屉面板。

**Tech Stack:** Go 1.25 + Gin + GORM + go.uber.org/dig;Vue3 + TS + Vite。

参考 spec:`docs/superpowers/specs/2026-06-23-video-task-queue-design.md`

---

## File Structure

后端:
- `internal/models/types.go` — 追加 `VideoTask` struct(与现有 model 同文件,遵循现状)
- `internal/app/app.go` — `AutoMigrate` 列表追加 `&models.VideoTask{}`;启动/停止 worker
- `internal/services/video_task_service.go`(新)— 队列 CRUD + 状态机 + 租约 claim
- `internal/services/video_task_service_test.go`(新)
- `internal/services/video_task_worker.go`(新)— 后台轮询 worker
- `internal/services/video_task_worker_test.go`(新)
- `internal/handler/video_task_handler.go`(新)— REST handler
- `internal/handler/video_task_handler_test.go`(新)
- `internal/container/container.go` — Provide service/worker/handler
- `internal/router/router.go` — 注册 `/api/video-tasks` 路由 + NewRouter 参数

前端:
- `web/src/api/videoTasks.ts`(新)— API client + 类型
- `web/src/views/Playground.vue` — `ChatMessage.videoTaskId`、`sendVideo` 改入队、顶层轮询回填、队列面板入口
- `web/src/components/playground/VideoQueueDrawer.vue`(新)— 队列面板
- `web/src/utils/videoTaskReconcile.ts`(新)— 回填纯函数(便于推理/将来测试)
- `web/src/locales/{zh-CN,en-US,ja-JP}.ts` — 新增 i18n key

---

## Task 1: VideoTask 数据模型 + 迁移

**Files:**
- Modify: `internal/models/types.go`(文件末尾追加)
- Modify: `internal/app/app.go:120-132`(AutoMigrate 列表)

- [ ] **Step 1: 追加 VideoTask model**

在 `internal/models/types.go` 末尾追加(`time` 已在该包导入;若未导入则补 `import "time"`):

```go
// VideoTask 表示一个后端托管的异步视频生成任务。
// 状态机: pending → running → completed / failed / canceled。
// lease_owner/lease_expires 用于 P9 mesh 多实例下的任务级租约,
// 保证同一任务只被一个实例执行(避免重复 POST 重复扣 key 额度)。
type VideoTask struct {
	ID             string         `gorm:"type:varchar(64);primaryKey" json:"id"`
	GroupName      string         `gorm:"type:varchar(255);not null;index" json:"group_name"`
	Model          string         `gorm:"type:varchar(255);not null" json:"model"`
	Prompt         string         `gorm:"type:text;not null" json:"prompt"`
	Params         string         `gorm:"type:text" json:"params"` // JSON: num_frames/frame_rate 等
	Status         string         `gorm:"type:varchar(20);not null;index;default:'pending'" json:"status"`
	UpstreamTaskID string         `gorm:"type:varchar(255)" json:"upstream_task_id"`
	VideoURL       string         `gorm:"type:text" json:"video_url"`
	Progress       int            `gorm:"default:0" json:"progress"`
	Error          string         `gorm:"type:text" json:"error"`
	LeaseOwner     string         `gorm:"type:varchar(64);index" json:"-"`
	LeaseExpires   *time.Time     `json:"-"`
	StartedAt      *time.Time     `json:"started_at"`
	CompletedAt    *time.Time     `json:"completed_at"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
}

// VideoTask 状态常量
const (
	VideoTaskPending   = "pending"
	VideoTaskRunning   = "running"
	VideoTaskCompleted = "completed"
	VideoTaskFailed    = "failed"
	VideoTaskCanceled  = "canceled"
)
```

- [ ] **Step 2: 加入 AutoMigrate**

`internal/app/app.go` 的 `a.db.AutoMigrate(...)` 调用列表(约 120-130 行)追加一行:

```go
		&models.SyncLog{},
		&models.VideoTask{},
	); err != nil {
```

- [ ] **Step 3: 编译验证**

Run: `go build ./...`
Expected: 编译通过,无错误。

- [ ] **Step 4: Commit**

```bash
git add internal/models/types.go internal/app/app.go
git commit -m "✨ feat(video): VideoTask model + AutoMigrate"
```

---

## Task 2: VideoTaskService — 队列 CRUD + 状态机 + 租约 claim

**Files:**
- Create: `internal/services/video_task_service.go`
- Test: `internal/services/video_task_service_test.go`

服务只碰 DB(`*gorm.DB`),不含任何 HTTP/agnes 逻辑(那是 worker 的事),便于独立测试。

- [ ] **Step 1: 写失败测试 — Create + Get**

`internal/services/video_task_service_test.go`:

```go
package services

import (
	"testing"
	"time"

	"autogateway/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newVideoTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/services/ -run TestVideoTaskService_CreateAndGet`
Expected: FAIL —— `undefined: NewVideoTaskService`。

- [ ] **Step 3: 写最小实现 — Create/Get**

`internal/services/video_task_service.go`:

```go
package services

import (
	"errors"
	"time"

	"autogateway/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// VideoTaskService 管理 video_tasks 表的生命周期(CRUD + 状态机 + 租约)。
// 只与 DB 交互,不含 HTTP/上游逻辑(由 VideoTaskWorker 负责)。
type VideoTaskService struct {
	db *gorm.DB
}

func NewVideoTaskService(db *gorm.DB) *VideoTaskService {
	return &VideoTaskService{db: db}
}

// Create 入队一个新任务(status=pending)。
func (s *VideoTaskService) Create(groupName, model, prompt, params string) (*models.VideoTask, error) {
	task := &models.VideoTask{
		ID:        uuid.NewString(),
		GroupName: groupName,
		Model:     model,
		Prompt:    prompt,
		Params:    params,
		Status:    models.VideoTaskPending,
	}
	if err := s.db.Create(task).Error; err != nil {
		return nil, err
	}
	return task, nil
}

// Get 按 id 取单个任务。
func (s *VideoTaskService) Get(id string) (*models.VideoTask, error) {
	var task models.VideoTask
	if err := s.db.First(&task, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &task, nil
}
```

> 注:`github.com/google/uuid` 已是本仓库依赖(keypool/sync 用过)。若 `go build` 报缺失,运行 `go get github.com/google/uuid`。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/services/ -run TestVideoTaskService_CreateAndGet`
Expected: PASS。

- [ ] **Step 5: 写失败测试 — ListByIDs + List(分页/过滤)**

追加到 `video_task_service_test.go`:

```go
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
```

- [ ] **Step 6: 运行确认失败**

Run: `go test ./internal/services/ -run 'TestVideoTaskService_List'`
Expected: FAIL —— `undefined: ListByIDs` / `List`。

- [ ] **Step 7: 实现 ListByIDs + List**

追加到 `video_task_service.go`:

```go
// ListByIDs 批量取任务(前端 30s 轮询用)。空入参返回空切片。
func (s *VideoTaskService) ListByIDs(ids []string) ([]models.VideoTask, error) {
	if len(ids) == 0 {
		return []models.VideoTask{}, nil
	}
	var tasks []models.VideoTask
	if err := s.db.Where("id IN ?", ids).Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

// List 分页列出任务(队列面板用)。status 为空则不过滤。page 从 1 起。
func (s *VideoTaskService) List(status string, page, pageSize int) ([]models.VideoTask, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	q := s.db.Model(&models.VideoTask{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var tasks []models.VideoTask
	if err := q.Order("created_at DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&tasks).Error; err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}
```

- [ ] **Step 8: 运行确认通过**

Run: `go test ./internal/services/ -run 'TestVideoTaskService_List'`
Expected: PASS。

- [ ] **Step 9: 写失败测试 — Claim 租约(并发只成一个 + 过期可接管)**

追加:

```go
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
```

- [ ] **Step 10: 运行确认失败**

Run: `go test ./internal/services/ -run 'TestVideoTaskService_Claim|TestVideoTaskService_FindClaimable'`
Expected: FAIL —— `undefined: Claim` / `FindClaimable`。

- [ ] **Step 11: 实现 Claim + FindClaimable(DB 条件 UPDATE,原子)**

追加到 `video_task_service.go`:

```go
// FindClaimable 返回可被 claim 的任务 id:pending,或 running 但租约已过期(实例崩溃)。
func (s *VideoTaskService) FindClaimable(limit int) ([]string, error) {
	now := time.Now()
	var tasks []models.VideoTask
	err := s.db.Select("id").
		Where("status = ?", models.VideoTaskPending).
		Or("status = ? AND lease_expires < ?", models.VideoTaskRunning, now).
		Limit(limit).Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	return ids, nil
}

// Claim 用一次原子条件 UPDATE 抢占任务,成功返回 true。
// 仅当任务为 pending,或 running 但租约已过期时才能抢占 —— 受影响行数==1 即成功。
// 这是 P9 mesh 多实例下保证"同一任务只被一个实例执行"的核心。
func (s *VideoTaskService) Claim(id, owner string, lease time.Duration) (bool, error) {
	now := time.Now()
	expires := now.Add(lease)
	res := s.db.Model(&models.VideoTask{}).
		Where("id = ?", id).
		Where("status = ? OR (status = ? AND lease_expires < ?)",
			models.VideoTaskPending, models.VideoTaskRunning, now).
		Updates(map[string]any{
			"status":        models.VideoTaskRunning,
			"lease_owner":   owner,
			"lease_expires": expires,
			"started_at":    now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
```

- [ ] **Step 12: 运行确认通过**

Run: `go test ./internal/services/ -run 'TestVideoTaskService_Claim|TestVideoTaskService_FindClaimable'`
Expected: PASS。

- [ ] **Step 13: 写失败测试 — 终态/进度/续约/取消/重试/删除**

追加:

```go
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
```

- [ ] **Step 14: 运行确认失败**

Run: `go test ./internal/services/ -run 'TestVideoTaskService_Lifecycle|TestVideoTaskService_RenewLease|TestVideoTaskService_CancelAndRetryAndDelete'`
Expected: FAIL —— 相关方法未定义。

- [ ] **Step 15: 实现剩余状态机方法**

追加到 `video_task_service.go`:

```go
// UpdateProgress 更新进度与上游任务 id(异步轮询阶段调用)。
func (s *VideoTaskService) UpdateProgress(id string, progress int, upstreamTaskID string) error {
	updates := map[string]any{"progress": progress}
	if upstreamTaskID != "" {
		updates["upstream_task_id"] = upstreamTaskID
	}
	return s.db.Model(&models.VideoTask{}).Where("id = ?", id).Updates(updates).Error
}

// MarkCompleted 置任务为完成态并写入视频 URL。
func (s *VideoTaskService) MarkCompleted(id, videoURL string) error {
	now := time.Now()
	return s.db.Model(&models.VideoTask{}).Where("id = ?", id).Updates(map[string]any{
		"status":       models.VideoTaskCompleted,
		"video_url":    videoURL,
		"progress":     100,
		"completed_at": now,
	}).Error
}

// MarkFailed 置任务为失败态并记录错误。
func (s *VideoTaskService) MarkFailed(id, errMsg string) error {
	now := time.Now()
	return s.db.Model(&models.VideoTask{}).Where("id = ?", id).Updates(map[string]any{
		"status":       models.VideoTaskFailed,
		"error":        errMsg,
		"completed_at": now,
	}).Error
}

// RenewLease 续约,防止长任务执行中租约到期被别的实例抢走。
func (s *VideoTaskService) RenewLease(id, owner string, lease time.Duration) error {
	return s.db.Model(&models.VideoTask{}).
		Where("id = ? AND lease_owner = ?", id, owner).
		Update("lease_expires", time.Now().Add(lease)).Error
}

// Cancel 取消任务(worker 执行循环会检测到 canceled 并停止回填)。
func (s *VideoTaskService) Cancel(id string) error {
	now := time.Now()
	return s.db.Model(&models.VideoTask{}).Where("id = ?", id).Updates(map[string]any{
		"status":       models.VideoTaskCanceled,
		"completed_at": now,
	}).Error
}

// IsCanceled 供 worker 在轮询循环中检测取消。
func (s *VideoTaskService) IsCanceled(id string) bool {
	var task models.VideoTask
	if err := s.db.Select("status").First(&task, "id = ?", id).Error; err != nil {
		return false
	}
	return task.Status == models.VideoTaskCanceled
}

// Retry 基于失败任务克隆出一条新的 pending 任务(原记录保留)。
func (s *VideoTaskService) Retry(id string) (*models.VideoTask, error) {
	src, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if src.Status != models.VideoTaskFailed && src.Status != models.VideoTaskCanceled {
		return nil, errors.New("only failed or canceled tasks can be retried")
	}
	return s.Create(src.GroupName, src.Model, src.Prompt, src.Params)
}

// Delete 删除任务记录(软删除,DeletedAt)。
func (s *VideoTaskService) Delete(id string) error {
	return s.db.Where("id = ?", id).Delete(&models.VideoTask{}).Error
}
```

- [ ] **Step 16: 运行全部 service 测试确认通过**

Run: `go test ./internal/services/ -run 'TestVideoTaskService'`
Expected: PASS（全部）。

- [ ] **Step 17: Commit**

```bash
git add internal/services/video_task_service.go internal/services/video_task_service_test.go
git commit -m "✨ feat(video): VideoTaskService 队列CRUD+状态机+任务级租约claim"
```

---

## Task 3: VideoTaskWorker — 后台轮询 worker

**Files:**
- Create: `internal/services/video_task_worker.go`
- Test: `internal/services/video_task_worker_test.go`

worker 通过一个最小接口 `videoUpstream` 调上游,测试用 fake 注入(不依赖真实 channel/agnes)。生产实现由 Task 4 用 `ChannelProxy` 接线。

- [ ] **Step 1: 写失败测试 — 阻塞返 completed 路径**

`internal/services/video_task_worker_test.go`:

```go
package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"autogateway/internal/models"
)

// fakeUpstream 模拟 agnes:create 返回一个预设响应,poll 按队列返回。
type fakeUpstream struct {
	mu        sync.Mutex
	createOut videoUpstreamResult
	createErr error
	polls     []videoUpstreamResult
	pollIdx   int
}

func (f *fakeUpstream) Create(ctx context.Context, t *models.VideoTask) (videoUpstreamResult, error) {
	if f.createErr != nil {
		return videoUpstreamResult{}, f.createErr
	}
	return f.createOut, nil
}

func (f *fakeUpstream) Poll(ctx context.Context, t *models.VideoTask) (videoUpstreamResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pollIdx >= len(f.polls) {
		return f.polls[len(f.polls)-1], nil
	}
	r := f.polls[f.pollIdx]
	f.pollIdx++
	return r, nil
}

func waitForStatus(t *testing.T, svc *VideoTaskService, id, want string) *models.VideoTask {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := svc.Get(id)
		if got != nil && got.Status == want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := svc.Get(id)
	t.Fatalf("timeout waiting for status %s, last=%+v", want, got)
	return nil
}

func newTestWorker(svc *VideoTaskService, up videoUpstream) *VideoTaskWorker {
	w := NewVideoTaskWorker(svc, up)
	w.pollInterval = 5 * time.Millisecond
	w.scanInterval = 5 * time.Millisecond
	w.maxDuration = 1 * time.Second
	w.owner = "test-owner"
	return w
}

func TestVideoTaskWorker_BlockingCompleted(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")

	up := &fakeUpstream{createOut: videoUpstreamResult{
		Status: models.VideoTaskCompleted, VideoURL: "https://x/v.mp4",
	}}
	w := newTestWorker(svc, up)
	w.processOnce(context.Background()) // 单轮:claim + 执行

	got := waitForStatus(t, svc, task.ID, models.VideoTaskCompleted)
	if got.VideoURL != "https://x/v.mp4" {
		t.Fatalf("expected video url, got %q", got.VideoURL)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/services/ -run TestVideoTaskWorker_BlockingCompleted`
Expected: FAIL —— `undefined: videoUpstream` / `NewVideoTaskWorker` 等。

- [ ] **Step 3: 实现 worker(接口 + 核心执行)**

`internal/services/video_task_worker.go`:

```go
package services

import (
	"context"
	"sync"
	"time"

	"autogateway/internal/models"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// videoUpstreamResult 是上游调用的归一化结果。
type videoUpstreamResult struct {
	Status         string // VideoTaskCompleted / VideoTaskFailed / 其它(=进行中)
	VideoURL       string
	UpstreamTaskID string
	Progress       int
	Err            string
}

// videoUpstream 抽象 agnes 上游调用,便于测试注入 fake。
type videoUpstream interface {
	// Create 发起视频生成。agnes 的 POST 可能阻塞到完成 → 返回 completed+url;
	// 也可能返回 queued → 带 UpstreamTaskID,进入 Poll 轮询。
	Create(ctx context.Context, task *models.VideoTask) (videoUpstreamResult, error)
	// Poll 查询上游任务状态。
	Poll(ctx context.Context, task *models.VideoTask) (videoUpstreamResult, error)
}

// VideoTaskWorker 后台轮询:claim pending 任务 → 调上游 → 回填终态。
type VideoTaskWorker struct {
	svc      *VideoTaskService
	upstream videoUpstream

	owner        string
	concurrency  int
	scanInterval time.Duration
	pollInterval time.Duration
	leaseTTL     time.Duration
	maxDuration  time.Duration

	sem    chan struct{}
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func NewVideoTaskWorker(svc *VideoTaskService, upstream videoUpstream) *VideoTaskWorker {
	return &VideoTaskWorker{
		svc:          svc,
		upstream:     upstream,
		owner:        uuid.NewString(),
		concurrency:  4,
		scanInterval: 5 * time.Second,
		pollInterval: 5 * time.Second,
		leaseTTL:     20 * time.Minute,
		maxDuration:  15 * time.Minute,
		stopCh:       make(chan struct{}),
	}
}

// Start 启动后台扫描循环。
func (w *VideoTaskWorker) Start() {
	w.sem = make(chan struct{}, w.concurrency)
	w.wg.Add(1)
	go w.runLoop()
}

// Stop 停止扫描并等待在途任务退出。
func (w *VideoTaskWorker) Stop(ctx context.Context) {
	close(w.stopCh)
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (w *VideoTaskWorker) runLoop() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.processOnce(context.Background())
		}
	}
}

// processOnce 扫描一批可 claim 的任务,逐个尝试 claim 并并发执行。
func (w *VideoTaskWorker) processOnce(ctx context.Context) {
	ids, err := w.svc.FindClaimable(w.concurrency * 2)
	if err != nil {
		logrus.WithError(err).Warn("video worker: find claimable failed")
		return
	}
	for _, id := range ids {
		ok, err := w.svc.Claim(id, w.owner, w.leaseTTL)
		if err != nil || !ok {
			continue // 被别的实例抢走或出错,跳过
		}
		w.sem <- struct{}{}
		w.wg.Add(1)
		go func(taskID string) {
			defer w.wg.Done()
			defer func() { <-w.sem }()
			w.execute(ctx, taskID)
		}(id)
	}
}

// execute 执行单个已 claim 的任务:Create → (completed/failed 直接终态 | 否则轮询)。
func (w *VideoTaskWorker) execute(ctx context.Context, id string) {
	task, err := w.svc.Get(id)
	if err != nil {
		return
	}
	res, err := w.upstream.Create(ctx, task)
	if err != nil {
		_ = w.svc.MarkFailed(id, err.Error())
		return
	}
	if res.Status == models.VideoTaskCompleted && res.VideoURL != "" {
		_ = w.svc.MarkCompleted(id, res.VideoURL)
		return
	}
	if res.Status == models.VideoTaskFailed {
		_ = w.svc.MarkFailed(id, res.Err)
		return
	}
	// 异步路径:记录上游 task id,进入轮询
	if res.UpstreamTaskID != "" {
		_ = w.svc.UpdateProgress(id, res.Progress, res.UpstreamTaskID)
		task.UpstreamTaskID = res.UpstreamTaskID
	}
	w.pollUntilDone(ctx, task)
}

// pollUntilDone 周期轮询上游直到完成/失败/超时/被取消。
func (w *VideoTaskWorker) pollUntilDone(ctx context.Context, task *models.VideoTask) {
	deadline := time.Now().Add(w.maxDuration)
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return // 进程关闭,留待下次启动重新 claim(租约过期)
		case <-ticker.C:
		}
		if w.svc.IsCanceled(task.ID) {
			return // 用户取消,停止回填
		}
		if time.Now().After(deadline) {
			_ = w.svc.MarkFailed(task.ID, "timeout")
			return
		}
		_ = w.svc.RenewLease(task.ID, w.owner, w.leaseTTL)
		res, err := w.upstream.Poll(ctx, task)
		if err != nil {
			continue // 单次抖动,下轮重试
		}
		switch {
		case res.Status == models.VideoTaskCompleted && res.VideoURL != "":
			_ = w.svc.MarkCompleted(task.ID, res.VideoURL)
			return
		case res.Status == models.VideoTaskFailed:
			_ = w.svc.MarkFailed(task.ID, res.Err)
			return
		default:
			_ = w.svc.UpdateProgress(task.ID, res.Progress, "")
		}
	}
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/services/ -run TestVideoTaskWorker_BlockingCompleted`
Expected: PASS。

- [ ] **Step 5: 写失败测试 — 异步轮询路径 + 失败 + 取消**

追加到 `video_task_worker_test.go`:

```go
func TestVideoTaskWorker_AsyncPollThenComplete(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")

	up := &fakeUpstream{
		createOut: videoUpstreamResult{Status: "queued", UpstreamTaskID: "up-1", Progress: 0},
		polls: []videoUpstreamResult{
			{Status: "in_progress", Progress: 40},
			{Status: models.VideoTaskCompleted, VideoURL: "https://x/done.mp4", Progress: 100},
		},
	}
	w := newTestWorker(svc, up)
	w.processOnce(context.Background())

	got := waitForStatus(t, svc, task.ID, models.VideoTaskCompleted)
	if got.VideoURL != "https://x/done.mp4" || got.UpstreamTaskID != "up-1" {
		t.Fatalf("bad result: %+v", got)
	}
}

func TestVideoTaskWorker_CreateError(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")

	up := &fakeUpstream{createErr: errors.New("network down")}
	w := newTestWorker(svc, up)
	w.processOnce(context.Background())

	got := waitForStatus(t, svc, task.ID, models.VideoTaskFailed)
	if got.Error != "network down" {
		t.Fatalf("expected error recorded, got %q", got.Error)
	}
}

func TestVideoTaskWorker_CanceledStopsPolling(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")
	// 上游一直返回进行中,worker 应在检测到取消后退出且不置 completed
	up := &fakeUpstream{
		createOut: videoUpstreamResult{Status: "queued", UpstreamTaskID: "up-1"},
		polls:     []videoUpstreamResult{{Status: "in_progress", Progress: 10}},
	}
	w := newTestWorker(svc, up)
	_ = svc.Cancel(task.ID) // 先取消
	w.processOnce(context.Background())

	// claim 不会命中已 canceled 的任务(FindClaimable 只找 pending/过期running)
	got, _ := svc.Get(task.ID)
	if got.Status != models.VideoTaskCanceled {
		t.Fatalf("expected stays canceled, got %s", got.Status)
	}
}
```

- [ ] **Step 6: 运行确认通过**

Run: `go test ./internal/services/ -run TestVideoTaskWorker`
Expected: PASS（全部 worker 测试）。

> 说明:`TestVideoTaskWorker_CanceledStopsPolling` 验证 canceled 任务不会被 claim;运行中任务的取消由 `pollUntilDone` 里的 `IsCanceled` 检测,已在 `execute`/`pollUntilDone` 实现。

- [ ] **Step 7: Commit**

```bash
git add internal/services/video_task_worker.go internal/services/video_task_worker_test.go
git commit -m "✨ feat(video): VideoTaskWorker 后台并发轮询(阻塞/异步/失败/取消/超时)"
```

---

## Task 4: agnes 上游适配 + HTTP handler + 接线(DI/router/启动)

**Files:**
- Create: `internal/services/video_task_upstream.go`(channel 实现 videoUpstream)
- Create: `internal/handler/video_task_handler.go`
- Test: `internal/handler/video_task_handler_test.go`
- Modify: `internal/container/container.go`、`internal/router/router.go`、`internal/app/app.go`

- [ ] **Step 1: 实现 channelUpstream(复用 ChannelProxy 发 agnes 请求)**

`internal/services/video_task_upstream.go`:

```go
package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"autogateway/internal/channel"
	"autogateway/internal/keypool"
	"autogateway/internal/models"
	"autogateway/internal/ratelimit"

	"github.com/sirupsen/logrus"
)

// channelUpstream 用现有 ChannelProxy(BuildUpstreamURL/ModifyRequest/GetHTTPClient)
// 向 agnes 发请求,完全复用代理的 URL 构造与鉴权头注入逻辑。
type channelUpstream struct {
	groupManager   *GroupManager
	channelFactory *channel.Factory
	keypool        *keypool.KeyProvider
}

func newChannelUpstream(gm *GroupManager, cf *channel.Factory, kp *keypool.KeyProvider) *channelUpstream {
	return &channelUpstream{groupManager: gm, channelFactory: cf, keypool: kp}
}

// agnesResponse 同时覆盖 POST(create)与 GET(poll)的响应字段。
type agnesResponse struct {
	TaskID              string `json:"task_id"`
	VideoID             string `json:"video_id"`
	Status              string `json:"status"`
	Progress            int    `json:"progress"`
	RemixedFromVideoID  string `json:"remixed_from_video_id"`
	Error               any    `json:"error"`
}

func (r agnesResponse) toResult() videoUpstreamResult {
	out := videoUpstreamResult{Status: r.Status, Progress: r.Progress, VideoURL: r.RemixedFromVideoID}
	if r.TaskID != "" {
		out.UpstreamTaskID = r.TaskID
	} else if r.VideoID != "" {
		out.UpstreamTaskID = r.VideoID
	}
	if r.Error != nil {
		out.Err = fmt.Sprintf("%v", r.Error)
	}
	return out
}

func (u *channelUpstream) prepare(group string) (*models.Group, channel.ChannelProxy, *models.APIKey, error) {
	g, err := u.groupManager.GetGroupByName(group)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("group %q not found: %w", group, err)
	}
	ch, err := u.channelFactory.GetChannel(g)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get channel: %w", err)
	}
	key, err := u.keypool.SelectKey(g.ID, ratelimit.Limits{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("select key: %w", err)
	}
	return g, ch, key, nil
}

// doRequest 构造并发送一个上游请求。proxyPath 形如 "/proxy/<group>/v1/videos"。
func (u *channelUpstream) doRequest(ctx context.Context, group string, method, proxyPath string, body []byte) (*agnesResponse, error) {
	g, ch, key, err := u.prepare(group)
	if err != nil {
		return nil, err
	}
	originalURL := &url.URL{Path: proxyPath}
	target, err := ch.BuildUpstreamURL(originalURL, group)
	if err != nil {
		return nil, fmt.Errorf("build upstream url: %w", err)
	}
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	ch.ModifyRequest(req, key, g)

	resp, err := ch.GetHTTPClient().Do(req)
	if err != nil {
		u.keypool.UpdateStatus(key, g, false, err.Error())
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		u.keypool.UpdateStatus(key, g, false, fmt.Sprintf("status %d", resp.StatusCode))
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(raw))
	}
	u.keypool.UpdateStatus(key, g, true, "")
	var out agnesResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode upstream response: %w", err)
	}
	return &out, nil
}

func (u *channelUpstream) Create(ctx context.Context, task *models.VideoTask) (videoUpstreamResult, error) {
	// 组装 body:model + prompt + params(若有)
	payload := map[string]any{"model": task.Model, "prompt": task.Prompt}
	if task.Params != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(task.Params), &extra); err == nil {
			for k, v := range extra {
				payload[k] = v
			}
		}
	}
	body, _ := json.Marshal(payload)
	// agnes 的 POST 可能阻塞数分钟,用一个宽松超时的 context
	cctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	resp, err := u.doRequest(cctx, task.GroupName, http.MethodPost,
		"/proxy/"+task.GroupName+"/v1/videos", body)
	if err != nil {
		return videoUpstreamResult{}, err
	}
	logrus.WithField("task", task.ID).Debug("video create upstream returned")
	return resp.toResult(), nil
}

func (u *channelUpstream) Poll(ctx context.Context, task *models.VideoTask) (videoUpstreamResult, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := u.doRequest(cctx, task.GroupName, http.MethodGet,
		"/proxy/"+task.GroupName+"/v1/videos/"+task.UpstreamTaskID, nil)
	if err != nil {
		return videoUpstreamResult{}, err
	}
	return resp.toResult(), nil
}
```

> 执行者核对点:`ChannelProxy.BuildUpstreamURL(originalURL, groupName)` 如何从 path 剥离 `/proxy/<group>` 前缀 —— 见 `internal/channel/base_channel.go` 的实现。若它期望的不是带 `/proxy/<group>` 前缀的 path,按其实际契约调整 `proxyPath`(目标是让上游 URL 落在 `<agnes-base>/v1/videos[/<id>]`)。

- [ ] **Step 2: 编译验证 upstream**

Run: `go build ./internal/services/`
Expected: 通过。

- [ ] **Step 3: 加 worker 的生产构造函数(注入 channelUpstream)**

追加到 `internal/services/video_task_worker.go`:

```go
// NewVideoTaskWorkerWithChannel 是生产构造:用 channelUpstream 作为上游实现。
func NewVideoTaskWorkerWithChannel(
	svc *VideoTaskService,
	gm *GroupManager,
	cf *channel.Factory,
	kp *keypool.KeyProvider,
) *VideoTaskWorker {
	return NewVideoTaskWorker(svc, newChannelUpstream(gm, cf, kp))
}
```

并在该文件 import 补 `"autogateway/internal/channel"` 与 `"autogateway/internal/keypool"`。

Run: `go build ./internal/services/`
Expected: 通过。

- [ ] **Step 4: 写失败测试 — handler Create/Get/List/操作**

`internal/handler/video_task_handler_test.go`:

```go
package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"autogateway/internal/models"
	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newHandlerTestRouter(t *testing.T) (*gin.Engine, *services.VideoTaskService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&models.VideoTask{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	svc := services.NewVideoTaskService(db)
	h := NewVideoTaskHandler(svc)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/video-tasks")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.POST("/:id/cancel", h.Cancel)
	g.POST("/:id/retry", h.Retry)
	g.DELETE("/:id", h.Delete)
	return r, svc
}

func TestVideoTaskHandler_CreateAndGet(t *testing.T) {
	r, _ := newHandlerTestRouter(t)

	body, _ := json.Marshal(map[string]any{
		"group": "agnes", "model": "agnes-video-v2.0", "prompt": "a cat",
		"params": map[string]any{"num_frames": 121},
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/video-tasks", bytes.NewReader(body))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created models.VideoTask
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.ID == "" || created.Status != models.VideoTaskPending {
		t.Fatalf("bad created: %+v", created)
	}

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "/api/video-tasks/"+created.ID, nil)
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("get status=%d", w2.Code)
	}
}

func TestVideoTaskHandler_ListByIDs(t *testing.T) {
	r, svc := newHandlerTestRouter(t)
	a, _ := svc.Create("g", "m", "p", "")
	b, _ := svc.Create("g", "m", "p", "")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/video-tasks?ids="+a.ID+","+b.ID, nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d", w.Code)
	}
	var resp struct {
		Tasks []models.VideoTask `json:"tasks"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(resp.Tasks))
	}
}
```

- [ ] **Step 5: 运行确认失败**

Run: `go test ./internal/handler/ -run TestVideoTaskHandler`
Expected: FAIL —— `undefined: NewVideoTaskHandler`。

- [ ] **Step 6: 实现 handler**

`internal/handler/video_task_handler.go`:

```go
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"autogateway/internal/services"

	"github.com/gin-gonic/gin"
)

type VideoTaskHandler struct {
	svc *services.VideoTaskService
}

func NewVideoTaskHandler(svc *services.VideoTaskService) *VideoTaskHandler {
	return &VideoTaskHandler{svc: svc}
}

type createVideoTaskRequest struct {
	Group  string         `json:"group"`
	Model  string         `json:"model"`
	Prompt string         `json:"prompt"`
	Params map[string]any `json:"params"`
}

func (h *VideoTaskHandler) Create(c *gin.Context) {
	var req createVideoTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Group == "" || req.Model == "" || strings.TrimSpace(req.Prompt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group, model, prompt are required"})
		return
	}
	params := ""
	if req.Params != nil {
		if b, err := json.Marshal(req.Params); err == nil {
			params = string(b)
		}
	}
	task, err := h.svc.Create(req.Group, req.Model, req.Prompt, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, task)
}

func (h *VideoTaskHandler) Get(c *gin.Context) {
	task, err := h.svc.Get(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, task)
}

// List: ?ids=a,b,c 批量查;否则 ?status=&page=&page_size= 分页。
func (h *VideoTaskHandler) List(c *gin.Context) {
	if idsParam := c.Query("ids"); idsParam != "" {
		ids := strings.Split(idsParam, ",")
		tasks, err := h.svc.ListByIDs(ids)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"tasks": tasks})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	tasks, total, err := h.svc.List(c.Query("status"), page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": tasks, "total": total})
}

func (h *VideoTaskHandler) Cancel(c *gin.Context) {
	if err := h.svc.Cancel(c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *VideoTaskHandler) Retry(c *gin.Context) {
	task, err := h.svc.Retry(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, task)
}

func (h *VideoTaskHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
```

- [ ] **Step 7: 运行确认通过**

Run: `go test ./internal/handler/ -run TestVideoTaskHandler`
Expected: PASS。

- [ ] **Step 8: DI 容器注册**

`internal/container/container.go` 在 service/handler 注册区追加(紧邻其它 `container.Provide(...)`):

```go
	if err := container.Provide(func(db *gorm.DB) *services.VideoTaskService {
		return services.NewVideoTaskService(db)
	}); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewVideoTaskWorkerWithChannel); err != nil {
		return nil, err
	}
	if err := container.Provide(handler.NewVideoTaskHandler); err != nil {
		return nil, err
	}
```

> 核对:容器里 `*gorm.DB` 的提供方名称(`db.NewDB` 返回类型)。若现有 service 是直接拿 `*gorm.DB`,沿用同款。`services.NewVideoTaskWorkerWithChannel` 的入参(VideoTaskService/GroupManager/channel.Factory/keypool.KeyProvider)都已在容器中,dig 自动注入。

- [ ] **Step 9: router 注册路由**

`internal/router/router.go`:
1. `NewRouter` 形参追加 `videoTaskHandler *handler.VideoTaskHandler`。
2. `registerAPIRoutes(...)` 调用与函数签名追加该参数,继续透传到 `registerProtectedAPIRoutes`。
3. 在 `registerProtectedAPIRoutes` 内追加:

```go
	videoTasks := api.Group("/video-tasks")
	{
		videoTasks.POST("", videoTaskHandler.Create)
		videoTasks.GET("", videoTaskHandler.List)
		videoTasks.GET("/:id", videoTaskHandler.Get)
		videoTasks.POST("/:id/cancel", videoTaskHandler.Cancel)
		videoTasks.POST("/:id/retry", videoTaskHandler.Retry)
		videoTasks.DELETE("/:id", videoTaskHandler.Delete)
	}
```

> 注:`NewRouter` 经 dig `Invoke` 构造,新增形参会被自动注入,无需手改调用点(确认 `container.go` 里以 `container.Invoke(... NewRouter ...)` 或 `Provide(NewRouter)` 方式装配)。

- [ ] **Step 10: 启动/停止 worker**

在 `internal/app/app.go` 的应用启动处(其它后台服务如 CronChecker `.Start()` 附近)注入并启动 worker;在 shutdown 处 `Stop`。App 结构体若以 dig 注入字段持有各服务,追加 `videoTaskWorker *services.VideoTaskWorker` 字段并在构造 App 的 Provide/Invoke 中带上;启动:

```go
	a.videoTaskWorker.Start()
```

停止(graceful shutdown,与 CronChecker.Stop 同处):

```go
	a.videoTaskWorker.Stop(ctx)
```

> 核对:App 如何持有后台服务(看 CronChecker 的注入与 Start/Stop 接线),照搬同款。

- [ ] **Step 11: 全量编译 + 测试**

Run: `go build ./... && go test ./internal/services/ ./internal/handler/ -run Video`
Expected: 编译通过,Video 相关测试全 PASS。

- [ ] **Step 12: Commit**

```bash
git add internal/services/video_task_upstream.go internal/services/video_task_worker.go internal/handler/video_task_handler.go internal/handler/video_task_handler_test.go internal/container/container.go internal/router/router.go internal/app/app.go
git commit -m "✨ feat(video): agnes上游适配 + REST handler + DI/router/worker接线"
```

---

## Task 5: 前端 API client

**Files:**
- Create: `web/src/api/videoTasks.ts`

- [ ] **Step 1: 写 API client**

参考 `web/src/api/` 下现有模块的 http 封装(如 `request`/axios 实例)。若现有 api 用统一 `http` 封装,沿用;此处给出基于 `fetch` + authKey 的独立实现(与 Playground 现状一致):

```ts
// web/src/api/videoTasks.ts
export interface VideoTask {
  id: string;
  group_name: string;
  model: string;
  prompt: string;
  status: "pending" | "running" | "completed" | "failed" | "canceled";
  upstream_task_id: string;
  video_url: string;
  progress: number;
  error: string;
  started_at: string | null;
  completed_at: string | null;
  created_at: string;
}

function authHeaders(authKey: string): HeadersInit {
  return { Authorization: `Bearer ${authKey}`, "Content-Type": "application/json" };
}

export async function createVideoTask(
  authKey: string,
  body: { group: string; model: string; prompt: string; params?: Record<string, unknown> },
): Promise<VideoTask> {
  const resp = await fetch("/api/video-tasks", {
    method: "POST",
    headers: authHeaders(authKey),
    body: JSON.stringify(body),
  });
  if (!resp.ok) throw new Error(`create video task failed: ${resp.status}`);
  return resp.json();
}

export async function getVideoTasksByIds(authKey: string, ids: string[]): Promise<VideoTask[]> {
  if (ids.length === 0) return [];
  const resp = await fetch(`/api/video-tasks?ids=${encodeURIComponent(ids.join(","))}`, {
    headers: authHeaders(authKey),
  });
  if (!resp.ok) throw new Error(`list video tasks failed: ${resp.status}`);
  const data = await resp.json();
  return data.tasks ?? [];
}

export async function listVideoTasks(
  authKey: string,
  opts: { status?: string; page?: number; pageSize?: number } = {},
): Promise<{ tasks: VideoTask[]; total: number }> {
  const p = new URLSearchParams();
  if (opts.status) p.set("status", opts.status);
  p.set("page", String(opts.page ?? 1));
  p.set("page_size", String(opts.pageSize ?? 20));
  const resp = await fetch(`/api/video-tasks?${p.toString()}`, { headers: authHeaders(authKey) });
  if (!resp.ok) throw new Error(`list video tasks failed: ${resp.status}`);
  return resp.json();
}

export async function cancelVideoTask(authKey: string, id: string): Promise<void> {
  await fetch(`/api/video-tasks/${id}/cancel`, { method: "POST", headers: authHeaders(authKey) });
}
export async function retryVideoTask(authKey: string, id: string): Promise<VideoTask> {
  const resp = await fetch(`/api/video-tasks/${id}/retry`, { method: "POST", headers: authHeaders(authKey) });
  if (!resp.ok) throw new Error(`retry failed: ${resp.status}`);
  return resp.json();
}
export async function deleteVideoTask(authKey: string, id: string): Promise<void> {
  await fetch(`/api/video-tasks/${id}`, { method: "DELETE", headers: authHeaders(authKey) });
}
```

- [ ] **Step 2: 类型检查**

Run: `cd web && npm run type-check`
Expected: 通过(无 videoTasks.ts 报错)。

- [ ] **Step 3: Commit**

```bash
git add web/src/api/videoTasks.ts
git commit -m "✨ feat(web): video-tasks API client"
```

---

## Task 6: 回填纯函数 + ChatMessage.videoTaskId + sendVideo 改入队

**Files:**
- Create: `web/src/utils/videoTaskReconcile.ts`
- Modify: `web/src/views/Playground.vue`(`ChatMessage` interface ~83;`sendVideo` ~985-1113)

- [ ] **Step 1: 写回填纯函数**

`web/src/utils/videoTaskReconcile.ts`:

```ts
import type { VideoTask } from "@/api/videoTasks";

export interface ReconcilableMessage {
  videoTaskId?: string;
  phase?: "thinking" | "streaming" | "done";
  content: string;
  error?: boolean;
}

// reconcileMessage 根据后端任务状态原地更新一条消息。返回是否发生变化。
// 文案插值交给调用方(i18n);这里只负责状态机映射。
export function reconcileMessage(
  msg: ReconcilableMessage,
  task: VideoTask,
  texts: { generating: (p: number) => string; failed: string; timeout: string },
): boolean {
  if (msg.phase === "done") return false;
  switch (task.status) {
    case "completed":
      if (task.video_url) {
        msg.content = `![](${task.video_url})`;
        msg.phase = "done";
        return true;
      }
      return false;
    case "failed":
      msg.content = task.error ? `${texts.failed} (${task.error})` : texts.failed;
      msg.error = true;
      msg.phase = "done";
      return true;
    case "canceled":
      msg.content = texts.failed;
      msg.error = true;
      msg.phase = "done";
      return true;
    case "pending":
    case "running":
    default:
      msg.content = texts.generating(task.progress ?? 0);
      return true;
  }
}

// collectPendingTaskIds 从所有会话消息里收集仍需轮询的 videoTaskId。
export function collectPendingTaskIds(
  sessions: { messages: ReconcilableMessage[] }[],
): string[] {
  const ids: string[] = [];
  for (const s of sessions) {
    for (const m of s.messages) {
      if (m.videoTaskId && m.phase !== "done") ids.push(m.videoTaskId);
    }
  }
  return ids;
}
```

- [ ] **Step 2: 给 ChatMessage 加字段**

`Playground.vue` 的 `interface ChatMessage` 内追加(放在 `usage?` 附近):

```ts
  // 后端视频任务 id — 关联后端异步队列;随 localStorage 持久化, 刷新后仍可回填
  videoTaskId?: string;
```

- [ ] **Step 3: 改写 sendVideo 为入队**

把 `sendVideo`(约 985-1113)函数体替换为入队逻辑(去掉前端阻塞 POST + 轮询):

```ts
async function sendVideo(groupName: string, modelName: string, prompt: string) {
  const s = active.value;
  if (!s) return;
  const now = Date.now();
  s.messages.push({ role: "user", content: prompt, sentAt: now });
  s.messages.push({
    role: "assistant",
    content: t("playground.videoSubmitting"),
    phase: "generating" as unknown as "thinking", // 复用 generating 文案;phase 用 thinking 显示 spinner
    sentAt: now,
  });
  const asst = s.messages[s.messages.length - 1];
  asst.phase = "thinking";
  input.value = "";
  pendingAttachments.value = [];
  sending.value = true;
  s.updatedAt = Date.now();
  if (s.title === t("playground.defaultTitle")) s.title = prompt.slice(0, 24);
  await nextTick();
  scrollToBottom();

  try {
    const task = await createVideoTask(authKey.value || "", {
      group: groupName,
      model: modelName,
      prompt,
      params: { num_frames: 121, frame_rate: 24 },
    });
    asst.videoTaskId = task.id;
    asst.content = t("playground.videoQueued");
  } catch (e) {
    asst.content = `[network error] ${(e as Error).message}`;
    asst.error = true;
    asst.phase = "done";
    asst.doneAt = Date.now();
  } finally {
    sending.value = false;
  }
}
```

并在文件顶部 import:

```ts
import { createVideoTask } from "@/api/videoTasks";
```

> 删除原 `sendVideo` 内已不再使用的 `VIDEO_POLL_INTERVAL_MS` / `VIDEO_POLL_MAX` 常量(若无其它引用)。`videoSubmitting` i18n 已存在;新增 `videoQueued`(Task 8 一并补三语)。

- [ ] **Step 4: 类型检查**

Run: `cd web && npm run type-check`
Expected: 通过(`videoQueued` 在 Task 8 补,若此步报缺 key 可先在三语各加一行占位中文,Task 8 完善)。

- [ ] **Step 5: Commit**

```bash
git add web/src/utils/videoTaskReconcile.ts web/src/views/Playground.vue
git commit -m "✨ feat(web): sendVideo 改为入队 + ChatMessage.videoTaskId + 回填纯函数"
```

---

## Task 7: 顶层 30s 轮询回填

**Files:**
- Modify: `web/src/views/Playground.vue`

- [ ] **Step 1: 接入轮询定时器**

在 `<script setup>` 中加入(`onMounted`/`onBeforeUnmount` 已 import;`sessions` 为现有响应式会话数组,名称以实际为准):

```ts
import { getVideoTasksByIds } from "@/api/videoTasks";
import { reconcileMessage, collectPendingTaskIds } from "@/utils/videoTaskReconcile";

const VIDEO_RECONCILE_MS = 30000;
let videoReconcileTimer: number | undefined;

async function reconcileVideoTasks() {
  if (document.hidden) return; // 后台标签页跳过本轮
  const ids = collectPendingTaskIds(sessions.value);
  if (ids.length === 0) return;
  let tasks;
  try {
    tasks = await getVideoTasksByIds(authKey.value || "", ids);
  } catch {
    return; // 单次失败下轮重试
  }
  const byId = new Map(tasks.map(tk => [tk.id, tk]));
  const texts = {
    generating: (p: number) => t("playground.videoGenerating", { p }),
    failed: t("playground.requestFailed"),
    timeout: t("playground.videoTimeout"),
  };
  for (const s of sessions.value) {
    for (const m of s.messages) {
      if (!m.videoTaskId || m.phase === "done") continue;
      const tk = byId.get(m.videoTaskId);
      if (tk) reconcileMessage(m, tk, texts);
    }
  }
}

onMounted(() => {
  videoReconcileTimer = window.setInterval(reconcileVideoTasks, VIDEO_RECONCILE_MS);
  void reconcileVideoTasks(); // 进入页面立即对一次(刷新后快速恢复)
});
onBeforeUnmount(() => {
  if (videoReconcileTimer) window.clearInterval(videoReconcileTimer);
});
```

> 核对:`sessions` 的实际响应式变量名(本文件用 `sessions.value`);`reconcileMessage` 的 `ReconcilableMessage` 与 `ChatMessage` 字段兼容(content/phase/error/videoTaskId 均已存在)。`phase` 取值 `reconcileMessage` 内写的是 `"done"`,与 ChatMessage 的 `phase` 联合类型一致。

- [ ] **Step 2: 类型检查**

Run: `cd web && npm run type-check`
Expected: 通过。

- [ ] **Step 3: 手动验证(dev)**

启动后端 + 前端,在 Playground 选视频模型发起一次,确认:消息显示"排队中"→ 后端 worker 跑完后 30s 内(或进页面立即对一次)消息自动变成 `<video>` 播放器。

- [ ] **Step 4: Commit**

```bash
git add web/src/views/Playground.vue
git commit -m "✨ feat(web): Playground 顶层 30s 轮询回填视频任务(可见性降频)"
```

---

## Task 8: 队列面板 + i18n

**Files:**
- Create: `web/src/components/playground/VideoQueueDrawer.vue`
- Modify: `web/src/views/Playground.vue`(挂载抽屉 + 入口按钮)
- Modify: `web/src/locales/{zh-CN,en-US,ja-JP}.ts`

- [ ] **Step 1: 补 i18n(三语)**

在每个 locale 的 `playground` 命名空间下 `videoSubmitting` 附近追加(zh-CN 示例,en/ja 同位翻译):

```ts
    videoQueued: "已加入队列, 后台生成中…",
    videoQueueTitle: "视频任务队列",
    videoQueueEmpty: "暂无任务",
    videoQueueCancel: "取消",
    videoQueueRetry: "重试",
    videoQueueDelete: "删除",
    videoQueueOpen: "队列",
    videoQueueStatusPending: "排队中",
    videoQueueStatusRunning: "生成中",
    videoQueueStatusCompleted: "已完成",
    videoQueueStatusFailed: "失败",
    videoQueueStatusCanceled: "已取消",
```

en-US:

```ts
    videoQueued: "Queued, generating in background…",
    videoQueueTitle: "Video task queue",
    videoQueueEmpty: "No tasks",
    videoQueueCancel: "Cancel",
    videoQueueRetry: "Retry",
    videoQueueDelete: "Delete",
    videoQueueOpen: "Queue",
    videoQueueStatusPending: "Queued",
    videoQueueStatusRunning: "Generating",
    videoQueueStatusCompleted: "Completed",
    videoQueueStatusFailed: "Failed",
    videoQueueStatusCanceled: "Canceled",
```

ja-JP:

```ts
    videoQueued: "キューに追加しました。バックグラウンドで生成中…",
    videoQueueTitle: "動画タスクキュー",
    videoQueueEmpty: "タスクなし",
    videoQueueCancel: "キャンセル",
    videoQueueRetry: "再試行",
    videoQueueDelete: "削除",
    videoQueueOpen: "キュー",
    videoQueueStatusPending: "待機中",
    videoQueueStatusRunning: "生成中",
    videoQueueStatusCompleted: "完了",
    videoQueueStatusFailed: "失敗",
    videoQueueStatusCanceled: "キャンセル済み",
```

- [ ] **Step 2: 写队列面板组件**

`web/src/components/playground/VideoQueueDrawer.vue`(遵循项目现有组件风格;若项目用 Element Plus/Naive 等 UI 库,用其 Drawer/Table 组件 —— 核对现有组件 import):

```vue
<script setup lang="ts">
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  listVideoTasks, cancelVideoTask, retryVideoTask, deleteVideoTask,
  type VideoTask,
} from "@/api/videoTasks";

const props = defineProps<{ open: boolean; authKey: string }>();
const emit = defineEmits<{ (e: "update:open", v: boolean): void }>();
const { t } = useI18n();

const tasks = ref<VideoTask[]>([]);
const loading = ref(false);

async function refresh() {
  loading.value = true;
  try {
    const r = await listVideoTasks(props.authKey, { page: 1, pageSize: 50 });
    tasks.value = r.tasks;
  } finally {
    loading.value = false;
  }
}

watch(() => props.open, v => { if (v) void refresh(); });

function statusText(s: string): string {
  const map: Record<string, string> = {
    pending: t("playground.videoQueueStatusPending"),
    running: t("playground.videoQueueStatusRunning"),
    completed: t("playground.videoQueueStatusCompleted"),
    failed: t("playground.videoQueueStatusFailed"),
    canceled: t("playground.videoQueueStatusCanceled"),
  };
  return map[s] ?? s;
}

async function onCancel(id: string) { await cancelVideoTask(props.authKey, id); await refresh(); }
async function onRetry(id: string) { await retryVideoTask(props.authKey, id); await refresh(); }
async function onDelete(id: string) { await deleteVideoTask(props.authKey, id); await refresh(); }
</script>

<template>
  <div v-if="open" class="video-queue-mask" @click.self="emit('update:open', false)">
    <aside class="video-queue-drawer">
      <header class="vq-head">
        <span>{{ t("playground.videoQueueTitle") }}</span>
        <button @click="refresh">⟳</button>
        <button @click="emit('update:open', false)">✕</button>
      </header>
      <p v-if="!tasks.length" class="vq-empty">{{ t("playground.videoQueueEmpty") }}</p>
      <ul v-else class="vq-list">
        <li v-for="tk in tasks" :key="tk.id" class="vq-item">
          <div class="vq-main">
            <span class="vq-status" :data-status="tk.status">{{ statusText(tk.status) }}</span>
            <span class="vq-prompt">{{ tk.prompt }}</span>
            <span v-if="tk.status === 'running'" class="vq-progress">{{ tk.progress }}%</span>
          </div>
          <div class="vq-actions">
            <button v-if="tk.status === 'running' || tk.status === 'pending'" @click="onCancel(tk.id)">
              {{ t("playground.videoQueueCancel") }}
            </button>
            <button v-if="tk.status === 'failed' || tk.status === 'canceled'" @click="onRetry(tk.id)">
              {{ t("playground.videoQueueRetry") }}
            </button>
            <button @click="onDelete(tk.id)">{{ t("playground.videoQueueDelete") }}</button>
          </div>
        </li>
      </ul>
    </aside>
  </div>
</template>

<style scoped>
.video-queue-mask { position: fixed; inset: 0; background: rgba(0,0,0,.3); z-index: 50; }
.video-queue-drawer { position: absolute; right: 0; top: 0; bottom: 0; width: 380px; max-width: 90vw;
  background: var(--bg, #fff); box-shadow: -2px 0 12px rgba(0,0,0,.15); display: flex; flex-direction: column; }
.vq-head { display: flex; align-items: center; gap: 8px; padding: 12px 16px; border-bottom: 1px solid #eee; }
.vq-head span { flex: 1; font-weight: 600; }
.vq-empty { padding: 24px; text-align: center; color: #999; }
.vq-list { list-style: none; margin: 0; padding: 8px; overflow: auto; }
.vq-item { padding: 10px; border-bottom: 1px solid #f0f0f0; }
.vq-main { display: flex; align-items: center; gap: 8px; }
.vq-prompt { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.vq-actions { display: flex; gap: 6px; margin-top: 6px; }
</style>
```

> 核对:项目是否已有统一 UI 库与抽屉组件 —— 若有,优先用库组件替换上面的手写 mask/drawer,保持视觉一致。

- [ ] **Step 3: 在 Playground 挂载抽屉 + 入口按钮**

`Playground.vue`:

```ts
import VideoQueueDrawer from "@/components/playground/VideoQueueDrawer.vue";
const videoQueueOpen = ref(false);
```

模板工具栏区加按钮(放在合适的工具栏位置):

```vue
<button class="toolbar-btn" @click="videoQueueOpen = true">{{ t("playground.videoQueueOpen") }}</button>
...
<VideoQueueDrawer v-model:open="videoQueueOpen" :auth-key="authKey || ''" />
```

- [ ] **Step 4: 类型检查 + 构建**

Run: `cd web && npm run type-check && npm run build`
Expected: 通过。

- [ ] **Step 5: 手动验证**

打开队列面板:能看到任务列表与状态;对 running 任务点取消 → 状态变 canceled;对 failed 点重试 → 出现新 pending;删除 → 行消失。

- [ ] **Step 6: Commit**

```bash
git add web/src/components/playground/VideoQueueDrawer.vue web/src/views/Playground.vue web/src/locales/zh-CN.ts web/src/locales/en-US.ts web/src/locales/ja-JP.ts
git commit -m "✨ feat(web): 视频任务队列面板(取消/重试/删除) + 三语i18n"
```

---

## Self-Review(写计划后自检结果)

**Spec coverage:**
- 后端持久化队列 + worker → Task 1-4 ✓
- 直接存 agnes URL 不下载 → `VideoTask.VideoURL` 存 url,无下载逻辑 ✓
- 聊天内就地刷新 → Task 7 ✓
- 轻量队列面板 → Task 8 ✓
- 取消/重试/删除 → service(Task 2)+ handler(Task 4)+ 面板(Task 8)✓
- 30s 轮询(仅有进行中任务、可见性降频)→ Task 7 ✓
- 任务级租约(多实例)→ `Claim` 条件 UPDATE + 并发测试(Task 2)✓
- 超时 15min → worker `maxDuration`(Task 3)✓
- 错误处理矩阵 → MarkFailed/Cancel/IsCanceled/Poll 抖动 continue(Task 3)✓
- 测试:状态机/租约并发/worker 各路径/handler → Task 2-4 ✓

**Placeholder scan:** 无 TBD/TODO;每个代码步骤含完整代码。少数"核对点"是对现有代码契约的指认(BuildUpstreamURL 前缀、App 持有后台服务方式、sessions 变量名、UI 库),非占位 —— 均给出默认实现可直接跑,核对仅为对齐既有风格。

**Type consistency:** service 方法名(Create/Get/ListByIDs/List/FindClaimable/Claim/RenewLease/UpdateProgress/MarkCompleted/MarkFailed/Cancel/IsCanceled/Retry/Delete)在 worker/handler/测试中引用一致;`videoUpstreamResult`/`videoUpstream` 接口在 worker 与 upstream 实现间一致;前端 `VideoTask` 类型字段与后端 JSON tag 一致;`reconcileMessage`/`collectPendingTaskIds` 签名与 Task 7 调用一致。

**已知风险/执行者注意:**
1. `ChannelProxy.BuildUpstreamURL` 的 path 契约需对照实现(Task 4 Step 1 核对点)。
2. agnes 的 POST 阻塞最长可达 ~6-12min,`channelUpstream.Create` 用 12min context;同时后端 group `request_timeout`(当前 600s=10min)是另一道闸 —— 若实测视频常 >10min,需在 group 配置调大 `request_timeout`,否则 channel 的 HTTPClient 会先超时。这是配置项,不在代码内写死。
3. worker `owner` 每进程一个 uuid;P9 mesh 下多实例各自扫描,靠 `Claim` 的条件 UPDATE 去重(已测)。
