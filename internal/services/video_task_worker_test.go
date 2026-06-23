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

// in-flight 取消:任务已 claim 并进入轮询后才被用户取消 —— worker 应检测到
// 取消而退出, 且即便上游随后"完成"也不会把任务复活为 completed(service 守卫兜底)。
// 这是整个租约设计要防的核心竞态, 此前未被覆盖。
func TestVideoTaskWorker_InFlightCancel(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")
	// 上游永远返回进行中(polls 耗尽后重复最后一个), 让任务停在轮询中
	up := &fakeUpstream{
		createOut: videoUpstreamResult{Status: "queued", UpstreamTaskID: "up-1"},
		polls:     []videoUpstreamResult{{Status: "in_progress", Progress: 10}},
	}
	w := newTestWorker(svc, up)
	w.processOnce(context.Background())

	// 等任务被 claim 进入 running(确保已开始轮询, 而非 claim 前就取消)
	waitForStatus(t, svc, task.ID, models.VideoTaskRunning)
	// 轮询途中取消
	_ = svc.Cancel(task.ID)

	// worker 下一轮检测到取消应退出; 任务终态保持 canceled, 未被复活/写入 URL
	// (观察窗口 ~300ms = 数十个 5ms 轮询周期, 足够确认不会被复活成 completed)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		got, _ := svc.Get(task.ID)
		if got.Status == models.VideoTaskCompleted {
			t.Fatalf("canceled in-flight task must not be resurrected to completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := svc.Get(task.ID)
	if got.Status != models.VideoTaskCanceled || got.VideoURL != "" {
		t.Fatalf("expected stays canceled with no url, got status=%s url=%q", got.Status, got.VideoURL)
	}
}

// 超时:上游始终未完成, worker 应在 maxDuration 后置 failed("timeout")。
func TestVideoTaskWorker_Timeout(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	task, _ := svc.Create("g", "m", "p", "")
	up := &fakeUpstream{
		createOut: videoUpstreamResult{Status: "queued", UpstreamTaskID: "up-1"},
		polls:     []videoUpstreamResult{{Status: "in_progress", Progress: 10}},
	}
	w := newTestWorker(svc, up)
	w.maxDuration = 50 * time.Millisecond // 快速触发超时
	w.processOnce(context.Background())

	got := waitForStatus(t, svc, task.ID, models.VideoTaskFailed)
	if got.Error != "timeout" {
		t.Fatalf("expected timeout error, got %q", got.Error)
	}
}

// Start/Stop 生命周期:runLoop 能扫到任务并完成; Stop 优雅退出; 重复 Stop 不 panic(I3)。
func TestVideoTaskWorker_StartStopGraceful(t *testing.T) {
	db := newVideoTaskTestDB(t)
	svc := NewVideoTaskService(db)
	up := &fakeUpstream{createOut: videoUpstreamResult{
		Status: models.VideoTaskCompleted, VideoURL: "https://x/v.mp4",
	}}
	w := newTestWorker(svc, up)
	w.Start()
	task, _ := svc.Create("g", "m", "p", "")

	got := waitForStatus(t, svc, task.ID, models.VideoTaskCompleted) // runLoop 自行扫到
	if got.VideoURL != "https://x/v.mp4" {
		t.Fatalf("expected url via runLoop, got %q", got.VideoURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w.Stop(ctx)
	w.Stop(ctx) // 重复 Stop 必须安全(sync.Once)
}
