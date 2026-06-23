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
