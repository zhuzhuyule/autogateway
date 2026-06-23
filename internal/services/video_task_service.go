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
