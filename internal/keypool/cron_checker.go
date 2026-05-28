package keypool

import (
	"context"
	"autogateway/internal/config"
	"autogateway/internal/encryption"
	"autogateway/internal/models"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// keyBackoffState 记录一把 invalid key 探活的退避状态. 内存 map, 不持久化 —
// 重启时全部清空, 等效首次探活. 这是有意的简化, 避免加 db 字段 + migration.
type keyBackoffState struct {
	lastAttemptAt       time.Time
	consecutiveFailures int
}

// NewCronChecker is responsible for periodically validating invalid keys.
type CronChecker struct {
	DB              *gorm.DB
	SettingsManager *config.SystemSettingsManager
	Validator       *KeyValidator
	EncryptionSvc   encryption.Service
	stopChan        chan struct{}
	wg              sync.WaitGroup

	// backoff 状态: key.ID → 上次探活时刻 + 连续失败次数. 已知失效但慢的
	// key (上游 quota 超 / 账号封 / 服务挂) 每 group 级 interval (默认 1h) 全
	// 打一遍上游浪费配额. 语义: 前 3 次失败给机会 (临时抖动可恢复), 第 4 次起
	// 进退避 1h → 2h → 4h → 8h → 16h → 24h.
	backoffMu    sync.Mutex
	backoffState map[uint]*keyBackoffState
}

// NewCronChecker creates a new CronChecker.
func NewCronChecker(
	db *gorm.DB,
	settingsManager *config.SystemSettingsManager,
	validator *KeyValidator,
	encryptionSvc encryption.Service,
) *CronChecker {
	return &CronChecker{
		DB:              db,
		SettingsManager: settingsManager,
		Validator:       validator,
		EncryptionSvc:   encryptionSvc,
		stopChan:        make(chan struct{}),
		backoffState:    make(map[uint]*keyBackoffState),
	}
}

// shouldSkipByBackoff 判断当前 invalid key 是否还在退避窗口内 (true 表示跳过本次).
//
// 语义: 前 3 次失败是网络/上游临时抖动, 给机会按 group 级 KeyValidationInterval
// 正常探活; 第 4 次起进退避, 1h → 2h → 4h → 8h → 16h → 24h (上限).
// 区分"真死 key" vs "暂时不通", 避免临时抖动被过早惩罚.
func (s *CronChecker) shouldSkipByBackoff(keyID uint) bool {
	s.backoffMu.Lock()
	defer s.backoffMu.Unlock()
	st, ok := s.backoffState[keyID]
	if !ok {
		return false
	}
	const graceCount = 3
	if st.consecutiveFailures < graceCount {
		return false
	}
	exp := st.consecutiveFailures - graceCount
	if exp > 4 { // 2^4 = 16, 16h; >4 就直接 24h 上限
		exp = 4
	}
	delay := time.Hour * time.Duration(1<<exp)
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return time.Since(st.lastAttemptAt) < delay
}

// recordBackoffResult 探活后更新退避状态. success=true 清空记录, false 累加失败.
func (s *CronChecker) recordBackoffResult(keyID uint, success bool) {
	s.backoffMu.Lock()
	defer s.backoffMu.Unlock()
	if success {
		delete(s.backoffState, keyID)
		return
	}
	st, ok := s.backoffState[keyID]
	if !ok {
		st = &keyBackoffState{}
		s.backoffState[keyID] = st
	}
	st.lastAttemptAt = time.Now()
	st.consecutiveFailures++
}

// Start begins the cron job execution.
func (s *CronChecker) Start() {
	logrus.Debug("Starting CronChecker...")
	s.wg.Add(1)
	go s.runLoop()
}

// Stop stops the cron job, respecting the context for shutdown timeout.
func (s *CronChecker) Stop(ctx context.Context) {
	close(s.stopChan)

	// Wait for the goroutine to finish, or for the shutdown to time out.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logrus.Info("CronChecker stopped gracefully.")
	case <-ctx.Done():
		logrus.Warn("CronChecker stop timed out.")
	}
}

func (s *CronChecker) runLoop() {
	defer s.wg.Done()

	s.submitValidationJobs()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			logrus.Debug("CronChecker: Running as Master, submitting validation jobs.")
			s.submitValidationJobs()
		case <-s.stopChan:
			return
		}
	}
}

// submitValidationJobs finds groups whose keys need validation and validates them concurrently.
func (s *CronChecker) submitValidationJobs() {
	var groups []models.Group
	if err := s.DB.Where("group_type != ? OR group_type IS NULL", "aggregate").Find(&groups).Error; err != nil {
		logrus.Errorf("CronChecker: Failed to get groups: %v", err)
		return
	}

	validationStartTime := time.Now()
	var wg sync.WaitGroup

	for i := range groups {
		group := &groups[i]
		group.EffectiveConfig = s.SettingsManager.GetEffectiveConfig(group.Config)
		interval := time.Duration(group.EffectiveConfig.KeyValidationIntervalMinutes) * time.Minute

		if group.LastValidatedAt == nil || validationStartTime.Sub(*group.LastValidatedAt) > interval {
			wg.Add(1)
			g := group
			go func() {
				defer wg.Done()
				s.validateGroupKeys(g)
			}()
		}
	}

	wg.Wait()
}

// validateGroupKeys validates all invalid keys for a single group concurrently.
func (s *CronChecker) validateGroupKeys(group *models.Group) {
	groupProcessStart := time.Now()

	var invalidKeys []models.APIKey
	err := s.DB.Where("group_id = ? AND status = ?", group.ID, models.KeyStatusInvalid).Find(&invalidKeys).Error
	if err != nil {
		logrus.Errorf("CronChecker: Failed to get invalid keys for group %s: %v", group.Name, err)
		return
	}

	if len(invalidKeys) == 0 {
		if err := s.DB.Model(group).Update("last_validated_at", time.Now()).Error; err != nil {
			logrus.Errorf("CronChecker: Failed to update last_validated_at for group %s: %v", group.Name, err)
		}
		logrus.Infof("CronChecker: Group '%s' has no invalid keys to check.", group.Name)
		return
	}

	// 退避过滤: 跳过仍在退避窗口内的 key, 避免对已知失效 key 反复打上游.
	candidateKeys := make([]models.APIKey, 0, len(invalidKeys))
	var skippedByBackoff int
	for i := range invalidKeys {
		if s.shouldSkipByBackoff(invalidKeys[i].ID) {
			skippedByBackoff++
			continue
		}
		candidateKeys = append(candidateKeys, invalidKeys[i])
	}
	if skippedByBackoff > 0 {
		logrus.Debugf("CronChecker: Group '%s' skipped %d invalid key(s) still in backoff window.", group.Name, skippedByBackoff)
	}
	if len(candidateKeys) == 0 {
		if err := s.DB.Model(group).Update("last_validated_at", time.Now()).Error; err != nil {
			logrus.Errorf("CronChecker: Failed to update last_validated_at for group %s: %v", group.Name, err)
		}
		return
	}

	var becameValidCount int32
	var keyWg sync.WaitGroup
	jobs := make(chan *models.APIKey, len(candidateKeys))

	concurrency := group.EffectiveConfig.KeyValidationConcurrency
	for range concurrency {
		keyWg.Add(1)
		go func() {
			defer keyWg.Done()
			for {
				select {
				case key, ok := <-jobs:
					if !ok {
						return
					}

					// Decrypt the key before validation
					decryptedKey, err := s.EncryptionSvc.Decrypt(key.KeyValue)
					if err != nil {
						logrus.WithError(err).WithField("key_id", key.ID).Error("CronChecker: Failed to decrypt key for validation, skipping")
						continue
					}

					// Create a copy with decrypted value for validation
					keyForValidation := *key
					keyForValidation.KeyValue = decryptedKey

					isValid, _ := s.Validator.ValidateSingleKey(&keyForValidation, group)
					s.recordBackoffResult(key.ID, isValid)
					if isValid {
						atomic.AddInt32(&becameValidCount, 1)
					}
				case <-s.stopChan:
					return
				}
			}
		}()
	}

DistributeLoop:
	for i := range candidateKeys {
		select {
		case jobs <- &candidateKeys[i]:
		case <-s.stopChan:
			break DistributeLoop
		}
	}
	close(jobs)

	keyWg.Wait()

	if err := s.DB.Model(group).Update("last_validated_at", time.Now()).Error; err != nil {
		logrus.Errorf("CronChecker: Failed to update last_validated_at for group %s: %v", group.Name, err)
	}

	duration := time.Since(groupProcessStart)
	logrus.Infof(
		"CronChecker: Group '%s' validation finished. Total checked: %d, became valid: %d. Duration: %s.",
		group.Name,
		len(invalidKeys),
		becameValidCount,
		duration.String(),
	)
}
