package services

import (
	"context"
	"sync"
	"time"

	"autogateway/internal/channel"
	"autogateway/internal/keypool"
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

	sem      chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewVideoTaskWorker(svc *VideoTaskService, upstream videoUpstream) *VideoTaskWorker {
	w := &VideoTaskWorker{
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
	// I2: sem 只初始化一次, 容量绑定 concurrency。Start() 不再重建,
	// 避免"Start 替换 sem 引用导致旧 goroutine 释放到旧 channel"的隐患。
	w.sem = make(chan struct{}, w.concurrency)
	return w
}

// NewVideoTaskWorkerWithChannel 是生产构造:用 channelUpstream 作为上游实现。
func NewVideoTaskWorkerWithChannel(
	svc *VideoTaskService,
	gm *GroupManager,
	cf *channel.Factory,
	kp *keypool.KeyProvider,
) *VideoTaskWorker {
	return NewVideoTaskWorker(svc, newChannelUpstream(gm, cf, kp))
}

// Start 启动后台扫描循环。
func (w *VideoTaskWorker) Start() {
	w.wg.Add(1)
	go w.runLoop()
}

// Stop 停止扫描并等待在途任务退出。可安全重复调用(I3: sync.Once 防止
// close 已关闭 channel 的 panic)。
func (w *VideoTaskWorker) Stop(ctx context.Context) {
	w.stopOnce.Do(func() { close(w.stopCh) })
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
// I1: 先用非阻塞 select 抢一个并发槽, 抢不到立即返回(余下任务留给下一轮),
// 绝不在 runLoop 里阻塞等待槽位 —— 否则 Stop 无法及时响应, 且会 claim 下
// 无法立即执行的任务。槽位抢到后再 Claim, Claim 失败则归还槽位。
func (w *VideoTaskWorker) processOnce(ctx context.Context) {
	ids, err := w.svc.FindClaimable(w.concurrency)
	if err != nil {
		logrus.WithError(err).Warn("video worker: find claimable failed")
		return
	}
	for _, id := range ids {
		select {
		case w.sem <- struct{}{}:
		default:
			return // 本轮已无空闲槽, 余下任务下一轮再处理
		}
		ok, err := w.svc.Claim(id, w.owner, w.leaseTTL)
		if err != nil || !ok {
			<-w.sem // 归还槽位:被别的实例抢走或出错
			continue
		}
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
