package services

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// modelRefreshInterval 后台刷新 available_models 缓存的间隔.
// provider 上新模型是天级事件, 12h 在新鲜度与上游请求开销之间平衡.
const modelRefreshInterval = 12 * time.Hour

// modelRefreshInitialDelay 进程启动后首刷的延迟, 错开启动高峰.
const modelRefreshInitialDelay = 2 * time.Minute

// modelRefreshConcurrency 并发刷新上限, 避免同时向多个上游打 /v1/models.
const modelRefreshConcurrency = 5

// ModelRefreshService 周期性刷新所有 standard 分组的 available_models 缓存,
// 保证聚合分组 /v1/models 并集列表的新鲜度. 单个分组刷新失败仅告警, 保留旧缓存.
type ModelRefreshService struct {
	groupManager *GroupManager
	groupService *GroupService
	stopCh       chan struct{}
	wg           sync.WaitGroup
}

// NewModelRefreshService 构造后台模型刷新服务.
func NewModelRefreshService(groupManager *GroupManager, groupService *GroupService) *ModelRefreshService {
	return &ModelRefreshService{
		groupManager: groupManager,
		groupService: groupService,
		stopCh:       make(chan struct{}),
	}
}

// Start 启动后台刷新循环.
func (s *ModelRefreshService) Start() {
	s.wg.Add(1)
	go s.run()
	logrus.Debug("Model refresh service started")
}

// Stop 优雅停止.
func (s *ModelRefreshService) Stop(ctx context.Context) {
	close(s.stopCh)

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logrus.Info("ModelRefreshService stopped gracefully.")
	case <-ctx.Done():
		logrus.Warn("ModelRefreshService stop timed out.")
	}
}

func (s *ModelRefreshService) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(modelRefreshInterval)
	defer ticker.Stop()

	// 启动后延迟一会儿再首刷, 错开进程启动高峰 (拉上游 /v1/models 有网络开销).
	initial := time.NewTimer(modelRefreshInitialDelay)
	defer initial.Stop()

	for {
		select {
		case <-initial.C:
			s.refreshAll()
		case <-ticker.C:
			s.refreshAll()
		case <-s.stopCh:
			return
		}
	}
}

// refreshAll 并发刷新所有 standard 分组的 available_models. 单个失败仅告警、保留旧缓存.
// RefreshAvailableModels 内部会 Invalidate 分组缓存, 从而触发 SubGroupManager 重建
// 路由集合, 聚合路由与 /v1/models 并集自动用上新数据.
func (s *ModelRefreshService) refreshAll() {
	groups := s.groupManager.GetAllGroups()
	if len(groups) == 0 {
		return
	}
	ctx := context.Background()

	sem := make(chan struct{}, modelRefreshConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	refreshed, failed := 0, 0

	for _, g := range groups {
		if g.GroupType != "standard" {
			continue
		}
		// 提前响应停止信号, 不再派发新的刷新.
		select {
		case <-s.stopCh:
			wg.Wait()
			return
		default:
		}

		gid := g.ID
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if _, err := s.groupService.RefreshAvailableModels(ctx, gid); err != nil {
				logrus.WithError(err).WithField("group_id", gid).
					Debug("periodic model refresh failed, keeping old cache")
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			mu.Lock()
			refreshed++
			mu.Unlock()
		}()
	}

	wg.Wait()
	if refreshed > 0 || failed > 0 {
		logrus.WithFields(logrus.Fields{
			"refreshed": refreshed,
			"failed":    failed,
		}).Info("periodic available_models refresh completed")
	}
}
