package keypool

import (
	"context"
	"fmt"
	"autogateway/internal/channel"
	"autogateway/internal/config"
	"autogateway/internal/encryption"
	"autogateway/internal/models"
	"autogateway/internal/utils"
	"time"

	"github.com/sirupsen/logrus"
	"go.uber.org/dig"
	"gorm.io/gorm"
)

// RequestLogRecorder 是 keypool 写探活日志所需的最小接口,
// 用接口而非直接依赖 services.RequestLogService 是为了打破
// keypool ↔ services 的导入循环 (services/key_manual_validation_service
// 已经反向 import 了 keypool).
type RequestLogRecorder interface {
	Record(log *models.RequestLog) error
}

// KeyTestResult holds the validation result for a single key.
type KeyTestResult struct {
	KeyValue string `json:"key_value"`
	IsValid  bool   `json:"is_valid"`
	Error    string `json:"error,omitempty"`
}

// KeyValidator provides methods to validate API keys.
type KeyValidator struct {
	DB                *gorm.DB
	channelFactory    *channel.Factory
	SettingsManager   *config.SystemSettingsManager
	keypoolProvider   *KeyProvider
	encryptionSvc     encryption.Service
	requestLogService RequestLogRecorder
}

type KeyValidatorParams struct {
	dig.In
	DB                *gorm.DB
	ChannelFactory    *channel.Factory
	SettingsManager   *config.SystemSettingsManager
	KeypoolProvider   *KeyProvider
	EncryptionSvc     encryption.Service
	RequestLogService RequestLogRecorder
}

// NewKeyValidator creates a new KeyValidator.
func NewKeyValidator(params KeyValidatorParams) *KeyValidator {
	return &KeyValidator{
		DB:                params.DB,
		channelFactory:    params.ChannelFactory,
		SettingsManager:   params.SettingsManager,
		keypoolProvider:   params.KeypoolProvider,
		encryptionSvc:     params.EncryptionSvc,
		requestLogService: params.RequestLogService,
	}
}

// ValidateSingleKey performs a validation check on a single API key.
func (s *KeyValidator) ValidateSingleKey(key *models.APIKey, group *models.Group) (bool, error) {
	if group.EffectiveConfig.AppUrl == "" {
		group.EffectiveConfig = s.SettingsManager.GetEffectiveConfig(group.Config)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(group.EffectiveConfig.KeyValidationTimeoutSeconds)*time.Second)
	defer cancel()

	ch, err := s.channelFactory.GetChannel(group)
	if err != nil {
		return false, fmt.Errorf("failed to get channel for group %s: %w", group.Name, err)
	}

	startedAt := time.Now()
	result := ch.ValidateKey(ctx, key, group)
	duration := time.Since(startedAt).Milliseconds()

	var errorMsg string
	if !result.IsValid && result.Err != nil {
		errorMsg = result.Err.Error()
	}
	s.keypoolProvider.UpdateStatus(key, group, result.IsValid, errorMsg)

	s.recordTestLog(key, group, result, duration, errorMsg)

	if !result.IsValid {
		logrus.WithFields(logrus.Fields{
			"error":    result.Err,
			"key_id":   key.ID,
			"group_id": group.ID,
			"url":      result.URL,
			"status":   result.StatusCode,
		}).Debug("Key validation failed")
		return false, result.Err
	}

	logrus.WithFields(logrus.Fields{
		"key_id":   key.ID,
		"is_valid": result.IsValid,
	}).Debug("Key validation successful")

	return true, nil
}

// recordTestLog 写入一行 RequestType=test 的 request_log, 让 ValidateKey
// 探活在前端 Logs.vue live tail 中可见 (errorContains 模糊搜定位 404/401 等
// upstream 错误). hourly stats 聚合时会跳过 test 类型, 所以不会污染
// success/failure 统计.
func (s *KeyValidator) recordTestLog(key *models.APIKey, group *models.Group, result channel.ValidateResult, duration int64, errorMsg string) {
	if s.requestLogService == nil {
		return
	}

	logEntry := &models.RequestLog{
		GroupID:      group.ID,
		GroupName:    group.Name,
		IsSuccess:    result.IsValid,
		StatusCode:   result.StatusCode,
		RequestPath:  utils.TruncateString(result.URL, 500),
		Duration:     duration,
		RequestType:  models.RequestTypeTest,
		Model:        group.TestModel,
		UpstreamAddr: utils.TruncateString(result.URL, 500),
		ErrorMessage: errorMsg,
	}

	if key != nil && key.KeyValue != "" {
		encryptedKeyValue, err := s.encryptionSvc.Encrypt(key.KeyValue)
		if err != nil {
			logrus.WithError(err).Error("Failed to encrypt key value for test log")
			logEntry.KeyValue = "failed-to-encryption"
		} else {
			logEntry.KeyValue = encryptedKeyValue
		}
		logEntry.KeyHash = s.encryptionSvc.Hash(key.KeyValue)
	}

	if err := s.requestLogService.Record(logEntry); err != nil {
		logrus.WithError(err).Error("Failed to record validate-key test log")
	}
}

// ModelTestResult describes a single per-model connectivity probe.
// IsValid 严格反映"该模型在该 group 下用一个活跃 key 能不能 200 通", 不会
// 影响所选 key 的 active/invalid 状态 — 模型不存在与 key 失效语义不同.
type ModelTestResult struct {
	IsValid    bool   `json:"is_valid"`
	StatusCode int    `json:"status_code"`
	URL        string `json:"url"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Model      string `json:"model"`
}

// TestModelConnectivity 尝试对 group 内的指定 model 发起一次最小载荷的探活.
// 调用方应已把 aggregate 解析到具体的 sub-group 再传进来; 本方法只负责
// "选 key + 用 modelName 替代 group.TestModel 重建一个 channel + 跑 ValidateKey".
// 所选 key 不会被标记 invalid (绕过 keypoolProvider.UpdateStatus).
func (s *KeyValidator) TestModelConnectivity(group *models.Group, modelName string) (*ModelTestResult, error) {
	if modelName == "" {
		return nil, fmt.Errorf("model name is required")
	}

	apiKey, err := s.keypoolProvider.SelectKey(group.ID)
	if err != nil {
		return nil, err
	}

	if group.EffectiveConfig.AppUrl == "" {
		group.EffectiveConfig = s.SettingsManager.GetEffectiveConfig(group.Config)
	}

	// 拷贝 group 并改写 TestModel, 不污染 GroupManager 缓存里供代理使用的对象.
	groupCopy := *group
	groupCopy.TestModel = modelName

	ch, err := s.channelFactory.BuildChannel(&groupCopy)
	if err != nil {
		return nil, fmt.Errorf("failed to build channel for group %s: %w", group.Name, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(group.EffectiveConfig.KeyValidationTimeoutSeconds)*time.Second)
	defer cancel()

	startedAt := time.Now()
	result := ch.ValidateKey(ctx, apiKey, &groupCopy)
	duration := time.Since(startedAt).Milliseconds()

	out := &ModelTestResult{
		IsValid:    result.IsValid,
		StatusCode: result.StatusCode,
		URL:        result.URL,
		DurationMs: duration,
		Model:      modelName,
	}
	if result.Err != nil {
		out.Error = result.Err.Error()
	}

	// 写入 RequestType=test 的日志, 让前端 Logs live tail 能搜到这次探活的 404/401 等.
	s.recordTestLog(apiKey, &groupCopy, result, duration, out.Error)

	return out, nil
}

// TestMultipleKeys performs a synchronous validation for a list of key values within a specific group.
func (s *KeyValidator) TestMultipleKeys(group *models.Group, keyValues []string) ([]KeyTestResult, error) {
	results := make([]KeyTestResult, len(keyValues))

	// Generate hashes for all key values
	var keyHashes []string
	for _, keyValue := range keyValues {
		keyHash := s.encryptionSvc.Hash(keyValue)
		if keyHash == "" {
			continue
		}
		keyHashes = append(keyHashes, keyHash)
	}

	// Find which of the provided keys actually exist in the database for this group
	var existingKeys []models.APIKey
	if len(keyHashes) > 0 {
		if err := s.DB.Where("group_id = ? AND key_hash IN ?", group.ID, keyHashes).Find(&existingKeys).Error; err != nil {
			return nil, fmt.Errorf("failed to query keys from DB: %w", err)
		}
	}

	// Create a map of key_hash to APIKey for quick lookup
	existingKeyMap := make(map[string]models.APIKey)
	for _, k := range existingKeys {
		existingKeyMap[k.KeyHash] = k
	}

	for i, kv := range keyValues {
		keyHash := s.encryptionSvc.Hash(kv)
		apiKey, exists := existingKeyMap[keyHash]
		if !exists {
			results[i] = KeyTestResult{
				KeyValue: kv,
				IsValid:  false,
				Error:    "Key does not exist in this group or has been removed.",
			}
			continue
		}

		apiKey.KeyValue = kv

		isValid, validationErr := s.ValidateSingleKey(&apiKey, group)

		results[i] = KeyTestResult{
			KeyValue: kv,
			IsValid:  isValid,
			Error:    "",
		}
		if validationErr != nil {
			results[i].Error = validationErr.Error()
		}
	}

	return results, nil
}
