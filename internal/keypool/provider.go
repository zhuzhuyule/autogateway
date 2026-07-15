package keypool

import (
	"errors"
	"fmt"
	"autogateway/internal/config"
	"autogateway/internal/encryption"
	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"
	"autogateway/internal/ratelimit"
	"autogateway/internal/store"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type KeyProvider struct {
	db              *gorm.DB
	store           store.Store
	settingsManager *config.SystemSettingsManager
	encryptionSvc   encryption.Service
	ledger          *ratelimit.Ledger
}

// NewProvider 创建一个新的 KeyProvider 实例。
func NewProvider(db *gorm.DB, store store.Store, settingsManager *config.SystemSettingsManager, encryptionSvc encryption.Service, ledger *ratelimit.Ledger) *KeyProvider {
	return &KeyProvider{
		db:              db,
		store:           store,
		settingsManager: settingsManager,
		encryptionSvc:   encryptionSvc,
		ledger:          ledger,
	}
}

// SelectKey 为指定的分组原子性地选择并轮换一个可用的 APIKey。
func (p *KeyProvider) SelectKey(groupID uint, limits ratelimit.Limits) (*models.APIKey, error) {
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", groupID)
	// 防御性最大跳过次数. 正常路径一发命中, 这是 store/db desync 兜底.
	// 设 16 而不是无限循环, 避免上层 bug 让 SelectKey 卡死.
	const maxSkip = 16

	for attempt := 0; attempt < maxSkip; attempt++ {
		// 1. Atomically rotate the key ID from the list
		keyIDStr, err := p.store.Rotate(activeKeysListKey)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, app_errors.ErrNoActiveKeys
			}
			return nil, fmt.Errorf("failed to rotate key from store: %w", err)
		}

		keyID, err := strconv.ParseUint(keyIDStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse key ID '%s': %w", keyIDStr, err)
		}

		// 2. Get key details from HASH
		keyHashKey := fmt.Sprintf("key:%d", keyID)
		keyDetails, err := p.store.HGetAll(keyHashKey)
		if err != nil {
			return nil, fmt.Errorf("failed to get key details for key ID %d: %w", keyID, err)
		}

		// 防御性 check: hash 不存在 (被 SyncGroupKeysFromDB Delete 但 active_keys
		// LRem 没及时) 或 status 非 active (被对端 sync 标 invalid 但 active_keys
		// 没清干净) → 跳过这把 key + 从 active_keys LRem 出去, 让下次 Rotate
		// 直接拿下一把. 这是 store/db desync 的最后一道闸, 兜住直接 SQL 改 db
		// 或未来新路径绕过 store 同步的场景.
		if len(keyDetails) == 0 || keyDetails["status"] != models.KeyStatusActive {
			reason := "status_not_active"
			if len(keyDetails) == 0 {
				reason = "hash_missing"
			}
			logrus.WithFields(logrus.Fields{
				"groupID": groupID,
				"keyID":   keyID,
				"reason":  reason,
				"status":  keyDetails["status"],
			}).Warn("SelectKey: stale entry in active_keys, evicting and re-rotating")
			_ = p.store.LRem(activeKeysListKey, 0, uint(keyID))
			if len(keyDetails) > 0 {
				_ = p.store.Delete(keyHashKey)
			}
			continue
		}

		// 速率账本准入：当前窗口已达上限 → 跳过选下一个（不 LRem，额度会恢复）
		if p.ledger != nil && !limits.IsZero() {
			ok, err := p.ledger.Allow(groupID, uint(keyID), limits)
			if err != nil {
				logrus.WithError(err).Warn("ratelimit Allow failed, fail-open")
			} else if !ok {
				continue
			}
		}

		// 3. Manually unmarshal the map into an APIKey struct
		failureCount, _ := strconv.ParseInt(keyDetails["failure_count"], 10, 64)
		createdAt, _ := strconv.ParseInt(keyDetails["created_at"], 10, 64)

		// Decrypt the key value for use by channels
		encryptedKeyValue := keyDetails["key_string"]
		decryptedKeyValue, err := p.encryptionSvc.Decrypt(encryptedKeyValue)
		if err != nil {
			// If decryption fails, try to use the value as-is (backward compatibility for unencrypted keys)
			logrus.WithFields(logrus.Fields{
				"keyID": keyID,
				"error": err,
			}).Debug("Failed to decrypt key value, using as-is for backward compatibility")
			decryptedKeyValue = encryptedKeyValue
		}

		if p.ledger != nil && !limits.IsZero() {
			if err := p.ledger.Record(groupID, uint(keyID), limits); err != nil {
				logrus.WithError(err).Warn("ratelimit Record failed")
			}
		}

		return &models.APIKey{
			ID:           uint(keyID),
			KeyValue:     decryptedKeyValue,
			Status:       keyDetails["status"],
			FailureCount: failureCount,
			GroupID:      groupID,
			CreatedAt:    time.Unix(createdAt, 0),
		}, nil
	}

	// 跳过 maxSkip 次后还没找到有效 key, 当作整组没活 key 处理.
	return nil, app_errors.ErrNoActiveKeys
}

// UpdateStatus 异步地提交一个 Key 状态更新任务。
func (p *KeyProvider) UpdateStatus(apiKey *models.APIKey, group *models.Group, isSuccess bool, errorMessage string) {
	go func() {
		keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
		activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)

		if isSuccess {
			if err := p.handleSuccess(apiKey.ID, keyHashKey, activeKeysListKey); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "error": err}).Error("Failed to handle key success")
			}
		} else {
			if app_errors.IsUnCounted(errorMessage) {
				logrus.WithFields(logrus.Fields{
					"keyID": apiKey.ID,
					"error": errorMessage,
				}).Debug("Uncounted error, skipping failure handling")
			} else {
				if err := p.handleFailure(apiKey, group, keyHashKey, activeKeysListKey); err != nil {
					logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "error": err}).Error("Failed to handle key failure")
				}
			}
		}
	}()
}

// executeTransactionWithRetry wraps a database transaction with a retry mechanism.
func (p *KeyProvider) executeTransactionWithRetry(operation func(tx *gorm.DB) error) error {
	const maxRetries = 3
	const baseDelay = 50 * time.Millisecond
	const maxJitter = 150 * time.Millisecond
	var err error

	for i := range maxRetries {
		err = p.db.Transaction(operation)
		if err == nil {
			return nil
		}

		if strings.Contains(err.Error(), "database is locked") {
			jitter := time.Duration(rand.Intn(int(maxJitter)))
			totalDelay := baseDelay + jitter
			logrus.Debugf("Database is locked, retrying in %v... (attempt %d/%d)", totalDelay, i+1, maxRetries)
			time.Sleep(totalDelay)
			continue
		}

		break
	}

	return err
}

func (p *KeyProvider) handleSuccess(keyID uint, keyHashKey, activeKeysListKey string) error {
	keyDetails, err := p.store.HGetAll(keyHashKey)
	if err != nil {
		return fmt.Errorf("failed to get key details from store: %w", err)
	}

	if keyDetails["status"] == models.KeyStatusDisabled {
		return nil // 手动停用的 key 不因 in-flight 成功被自动恢复
	}

	failureCount, _ := strconv.ParseInt(keyDetails["failure_count"], 10, 64)
	isActive := keyDetails["status"] == models.KeyStatusActive

	if failureCount == 0 && isActive {
		return nil
	}

	return p.executeTransactionWithRetry(func(tx *gorm.DB) error {
		var key models.APIKey
		if err := tx.Set("gorm:query_option", "FOR UPDATE").First(&key, keyID).Error; err != nil {
			return fmt.Errorf("failed to lock key %d for update: %w", keyID, err)
		}

		updates := map[string]any{"failure_count": 0}
		if !isActive {
			updates["status"] = models.KeyStatusActive
		}

		if err := tx.Model(&key).Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to update key in DB: %w", err)
		}

		if err := p.store.HSet(keyHashKey, updates); err != nil {
			return fmt.Errorf("failed to update key details in store: %w", err)
		}

		if !isActive {
			logrus.WithField("keyID", keyID).Debug("Key has recovered and is being restored to active pool.")
			if err := p.store.LRem(activeKeysListKey, 0, keyID); err != nil {
				return fmt.Errorf("failed to LRem key before LPush on recovery: %w", err)
			}
			if err := p.store.LPush(activeKeysListKey, keyID); err != nil {
				return fmt.Errorf("failed to LPush key back to active list: %w", err)
			}
		}

		return nil
	})
}

func (p *KeyProvider) handleFailure(apiKey *models.APIKey, group *models.Group, keyHashKey, activeKeysListKey string) error {
	keyDetails, err := p.store.HGetAll(keyHashKey)
	if err != nil {
		return fmt.Errorf("failed to get key details from store: %w", err)
	}

	if keyDetails["status"] == models.KeyStatusInvalid || keyDetails["status"] == models.KeyStatusDisabled {
		return nil
	}

	failureCount, _ := strconv.ParseInt(keyDetails["failure_count"], 10, 64)

	// 获取该分组的有效配置
	blacklistThreshold := group.EffectiveConfig.BlacklistThreshold

	return p.executeTransactionWithRetry(func(tx *gorm.DB) error {
		var key models.APIKey
		if err := tx.Set("gorm:query_option", "FOR UPDATE").First(&key, apiKey.ID).Error; err != nil {
			return fmt.Errorf("failed to lock key %d for update: %w", apiKey.ID, err)
		}

		newFailureCount := failureCount + 1

		updates := map[string]any{"failure_count": newFailureCount}
		shouldBlacklist := blacklistThreshold > 0 && newFailureCount >= int64(blacklistThreshold)
		if shouldBlacklist {
			updates["status"] = models.KeyStatusInvalid
		}

		if err := tx.Model(&key).Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to update key stats in DB: %w", err)
		}

		if _, err := p.store.HIncrBy(keyHashKey, "failure_count", 1); err != nil {
			return fmt.Errorf("failed to increment failure count in store: %w", err)
		}

		if shouldBlacklist {
			logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "threshold": blacklistThreshold}).Warn("Key has reached blacklist threshold, disabling.")
			if err := p.store.LRem(activeKeysListKey, 0, apiKey.ID); err != nil {
				return fmt.Errorf("failed to LRem key from active list: %w", err)
			}
			if err := p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusInvalid}); err != nil {
				return fmt.Errorf("failed to update key status to invalid in store: %w", err)
			}
		}

		return nil
	})
}

// LoadKeysFromDB 从数据库加载所有分组和密钥，并填充到 Store 中。
func (p *KeyProvider) LoadKeysFromDB() error {
	logrus.Debug("First time startup, loading keys from DB...")

	// 1. 分批从数据库加载并使用 Pipeline 写入 Redis
	allActiveKeyIDs := make(map[uint][]any)
	batchSize := 10000
	var batchKeys []*models.APIKey

	// C: 只加载"属于有效 group(存在且未删)"的 key — 孤儿 key(指向已删/不存在的 group)不进
	// keypool, 既不会被 SelectKey 选中(匹配不到 provider 必失败), 也不占 active_keys 列表。
	// 这也自愈"group 尚未同步到本端"的暂时孤儿: group 一到, 下次重载即纳入, 不误删任何数据。
	validGroups := p.db.Model(&models.Group{}).Select("id").Where("deleted_at IS NULL")
	err := p.db.Model(&models.APIKey{}).Where("group_id IN (?)", validGroups).
		FindInBatches(&batchKeys, batchSize, func(tx *gorm.DB, batch int) error {
		logrus.Debugf("Processing batch %d with %d keys...", batch, len(batchKeys))

		var pipeline store.Pipeliner
		if redisStore, ok := p.store.(store.RedisPipeliner); ok {
			pipeline = redisStore.Pipeline()
		}

		for _, key := range batchKeys {
			keyHashKey := fmt.Sprintf("key:%d", key.ID)
			keyDetails := p.apiKeyToMap(key)

			if pipeline != nil {
				pipeline.HSet(keyHashKey, keyDetails)
			} else {
				if err := p.store.HSet(keyHashKey, keyDetails); err != nil {
					logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to HSet key details")
				}
			}

			if key.Status == models.KeyStatusActive {
				allActiveKeyIDs[key.GroupID] = append(allActiveKeyIDs[key.GroupID], key.ID)
			}
		}

		if pipeline != nil {
			if err := pipeline.Exec(); err != nil {
				return fmt.Errorf("failed to execute pipeline for batch %d: %w", batch, err)
			}
		}
		return nil
	}).Error

	if err != nil {
		return fmt.Errorf("failed during batch processing of keys: %w", err)
	}

	// 2. 更新所有分组的 active_keys 列表
	logrus.Info("Updating active key lists for all groups...")
	for groupID, activeIDs := range allActiveKeyIDs {
		if len(activeIDs) > 0 {
			activeKeysListKey := fmt.Sprintf("group:%d:active_keys", groupID)
			p.store.Delete(activeKeysListKey)
			if err := p.store.LPush(activeKeysListKey, activeIDs...); err != nil {
				logrus.WithFields(logrus.Fields{"groupID": groupID, "error": err}).Error("Failed to LPush active keys for group")
			}
		}
	}

	return nil
}

// SyncGroupKeysFromDB 在 mesh sync 把 db 改了之后, 把该 group 的 redis store
// 跟 db 的真值对齐. 保留运行时累计的 failure_count (active 状态的 key 不动 hash),
// 只处理: 软删除 → 清 hash + 从 active_keys LRem; 状态变 invalid → LRem; 新增/
// 状态恢复 active → 加回 active_keys + HSet hash.
//
// 不能用 LoadKeysFromDB (会刷掉所有 group 的 failure_count counter, 导致正常
// 失败累计被回退), 也不能用 RemoveKeysFromStore (会 Delete 整个 active_keys
// 列表). 这是一个精确的 per-group 同步, 只动有变化的 key.
func (p *KeyProvider) SyncGroupKeysFromDB(groupID uint) error {
	var allKeys []models.APIKey
	if err := p.db.Unscoped().Where("group_id = ?", groupID).Find(&allKeys).Error; err != nil {
		return fmt.Errorf("sync group %d: query keys: %w", groupID, err)
	}

	activeListKey := fmt.Sprintf("group:%d:active_keys", groupID)

	for i := range allKeys {
		k := &allKeys[i]
		keyHashKey := fmt.Sprintf("key:%d", k.ID)

		if k.DeletedAt.Valid {
			// 软删除: 把 hash 删掉 + 从 active list 摘出去
			_ = p.store.LRem(activeListKey, 0, k.ID)
			_ = p.store.Delete(keyHashKey)
			continue
		}

		if k.Status == models.KeyStatusInvalid {
			// 失效: 从 active list 摘出 + 把 hash 的 status 字段刷成 invalid
			_ = p.store.LRem(activeListKey, 0, k.ID)
			_ = p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusInvalid})
			continue
		}

		if k.Status == models.KeyStatusDisabled {
			// 手动停用: 从 active list 摘出 + 把 hash 的 status 字段刷成 disabled
			_ = p.store.LRem(activeListKey, 0, k.ID)
			_ = p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusDisabled})
			continue
		}

		// active: 确保 hash 存在 + 在 active list 里. 已存在的 hash 不动 (保护 failure_count).
		existingHash, err := p.store.HGetAll(keyHashKey)
		if err != nil || len(existingHash) == 0 {
			// hash 不存在 → 新 key 同步过来, 建 hash + 加 active list
			_ = p.store.HSet(keyHashKey, p.apiKeyToMap(k))
			_ = p.store.LPush(activeListKey, k.ID)
			continue
		}
		if existingHash["status"] != models.KeyStatusActive {
			// hash 存在但 status 异常 (可能从 invalid 被对端 RestoreKeys 改回了): 修正
			_ = p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusActive})
			// LRem 防重复后再 LPush (避免出现 active list 同 keyID 多条)
			_ = p.store.LRem(activeListKey, 0, k.ID)
			_ = p.store.LPush(activeListKey, k.ID)
		}
	}

	return nil
}

// AddKeys 批量添加新的 Key 到池和数据库中。
func (p *KeyProvider) AddKeys(groupID uint, keys []models.APIKey) error {
	if len(keys) == 0 {
		return nil
	}

	err := p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&keys).Error; err != nil {
			return err
		}

		// 使用批量方法添加到缓存
		return p.addKeysToCacheBatch(groupID, keys)
	})

	return err
}

// ReloadKey 从 DB 重新读取单条 key 并覆盖 cache. 用于 in-place 编辑 key
// (UpdateKey) 后同步 cache - key_value/key_hash/status 都可能变, 需要让 SWRR
// 拿到最新数据. addKeyToStore 内部 HSet 用 key:<id> 覆盖, LRem+LPush 处理活跃
// 列表, 所以同 id 重写是安全幂等的.
func (p *KeyProvider) ReloadKey(keyID uint) error {
	var key models.APIKey
	if err := p.db.First(&key, keyID).Error; err != nil {
		return err
	}
	// Non-active 状态先从活跃列表 LRem; addKeyToStore 只在 Active 时 LPush.
	if key.Status != models.KeyStatusActive {
		activeKeysListKey := fmt.Sprintf("group:%d:active_keys", key.GroupID)
		_ = p.store.LRem(activeKeysListKey, 0, key.ID)
	}
	return p.addKeyToStore(&key)
}

// RemoveKeys 批量从池和数据库中移除 Key。
func (p *KeyProvider) RemoveKeys(groupID uint, keyValues []string) (int64, error) {
	if len(keyValues) == 0 {
		return 0, nil
	}

	var keysToDelete []models.APIKey
	var deletedCount int64

	err := p.db.Transaction(func(tx *gorm.DB) error {
		var keyHashes []string
		for _, keyValue := range keyValues {
			keyHash := p.encryptionSvc.Hash(keyValue)
			if keyHash != "" {
				keyHashes = append(keyHashes, keyHash)
			}
		}

		if len(keyHashes) == 0 {
			return nil
		}

		if err := tx.Where("group_id = ? AND key_hash IN ?", groupID, keyHashes).Find(&keysToDelete).Error; err != nil {
			return err
		}

		if len(keysToDelete) == 0 {
			return nil
		}

		keyIDsToDelete := pluckIDs(keysToDelete)

		result := tx.Where("id IN ?", keyIDsToDelete).Delete(&models.APIKey{})
		if result.Error != nil {
			return result.Error
		}
		deletedCount = result.RowsAffected

		for _, key := range keysToDelete {
			if err := p.removeKeyFromStore(key.ID, key.GroupID); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to remove key from store after DB deletion, rolling back transaction")
				return err
			}
		}

		return nil
	})

	return deletedCount, err
}

// SetKeyEnabled 手动停用 (enabled=false) 或启用 (enabled=true) 指定 key。
// 停用 → status=disabled, 从 active_keys 移除 (CronChecker 不会自动恢复它);
// 启用 → status=active, failure_count=0, 重新加入 active_keys 轮转池。
func (p *KeyProvider) SetKeyEnabled(keyID uint, enabled bool) error {
	targetStatus := models.KeyStatusDisabled
	if enabled {
		targetStatus = models.KeyStatusActive
	}
	keyHashKey := fmt.Sprintf("key:%d", keyID)

	return p.executeTransactionWithRetry(func(tx *gorm.DB) error {
		var key models.APIKey
		if err := tx.Set("gorm:query_option", "FOR UPDATE").First(&key, keyID).Error; err != nil {
			return err
		}

		updates := map[string]any{"status": targetStatus}
		if enabled {
			updates["failure_count"] = 0
		}
		if err := tx.Model(&key).Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to update key %d in DB: %w", keyID, err)
		}

		activeListKey := fmt.Sprintf("group:%d:active_keys", key.GroupID)
		if enabled {
			if err := p.store.HSet(keyHashKey, map[string]any{
				"status":        models.KeyStatusActive,
				"failure_count": 0,
			}); err != nil {
				return fmt.Errorf("failed to HSet key %d status=active: %w", keyID, err)
			}
			_ = p.store.LRem(activeListKey, 0, keyID)
			if err := p.store.LPush(activeListKey, keyID); err != nil {
				return fmt.Errorf("failed to LPush key %d to active list: %w", keyID, err)
			}
		} else {
			if err := p.store.HSet(keyHashKey, map[string]any{
				"status": models.KeyStatusDisabled,
			}); err != nil {
				return fmt.Errorf("failed to HSet key %d status=disabled: %w", keyID, err)
			}
			_ = p.store.LRem(activeListKey, 0, keyID)
		}
		return nil
	})
}

// RestoreKeys 恢复组内所有无效的 Key。
func (p *KeyProvider) RestoreKeys(groupID uint) (int64, error) {
	var invalidKeys []models.APIKey
	var restoredCount int64

	err := p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ? AND status = ?", groupID, models.KeyStatusInvalid).Find(&invalidKeys).Error; err != nil {
			return err
		}

		if len(invalidKeys) == 0 {
			return nil
		}

		updates := map[string]any{
			"status":        models.KeyStatusActive,
			"failure_count": 0,
		}
		result := tx.Model(&models.APIKey{}).Where("group_id = ? AND status = ?", groupID, models.KeyStatusInvalid).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		restoredCount = result.RowsAffected

		for _, key := range invalidKeys {
			key.Status = models.KeyStatusActive
			key.FailureCount = 0
			if err := p.addKeyToStore(&key); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to restore key in store after DB update, rolling back transaction")
				return err
			}
		}
		return nil
	})

	return restoredCount, err
}

// RestoreMultipleKeys 恢复指定的 Key。
func (p *KeyProvider) RestoreMultipleKeys(groupID uint, keyValues []string) (int64, error) {
	if len(keyValues) == 0 {
		return 0, nil
	}

	var keysToRestore []models.APIKey
	var restoredCount int64

	err := p.db.Transaction(func(tx *gorm.DB) error {
		var keyHashes []string
		for _, keyValue := range keyValues {
			keyHash := p.encryptionSvc.Hash(keyValue)
			if keyHash != "" {
				keyHashes = append(keyHashes, keyHash)
			}
		}

		if len(keyHashes) == 0 {
			return nil
		}

		if err := tx.Where("group_id = ? AND key_hash IN ? AND status = ?", groupID, keyHashes, models.KeyStatusInvalid).Find(&keysToRestore).Error; err != nil {
			return err
		}

		if len(keysToRestore) == 0 {
			return nil
		}

		keyIDsToRestore := pluckIDs(keysToRestore)

		updates := map[string]any{
			"status":        models.KeyStatusActive,
			"failure_count": 0,
		}
		result := tx.Model(&models.APIKey{}).Where("id IN ?", keyIDsToRestore).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		restoredCount = result.RowsAffected

		for _, key := range keysToRestore {
			key.Status = models.KeyStatusActive
			key.FailureCount = 0
			if err := p.addKeyToStore(&key); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to restore key in store after DB update")
				return err
			}
		}

		return nil
	})

	return restoredCount, err
}

// RemoveInvalidKeys 移除组内所有无效的 Key。
func (p *KeyProvider) RemoveInvalidKeys(groupID uint) (int64, error) {
	return p.removeKeysByStatus(groupID, models.KeyStatusInvalid)
}

// RemoveAllKeys 移除组内所有的 Key。
func (p *KeyProvider) RemoveAllKeys(groupID uint) (int64, error) {
	return p.removeKeysByStatus(groupID)
}

// removeKeysByStatus is a generic function to remove keys by status.
// If no status is provided, it removes all keys in the group.
func (p *KeyProvider) removeKeysByStatus(groupID uint, status ...string) (int64, error) {
	var keysToRemove []models.APIKey
	var removedCount int64

	err := p.db.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("group_id = ?", groupID)
		if len(status) > 0 {
			query = query.Where("status IN ?", status)
		}

		if err := query.Find(&keysToRemove).Error; err != nil {
			return err
		}

		if len(keysToRemove) == 0 {
			return nil
		}

		deleteQuery := tx.Where("group_id = ?", groupID)
		if len(status) > 0 {
			deleteQuery = deleteQuery.Where("status IN ?", status)
		}
		result := deleteQuery.Delete(&models.APIKey{})
		if result.Error != nil {
			return result.Error
		}
		removedCount = result.RowsAffected

		for _, key := range keysToRemove {
			if err := p.removeKeyFromStore(key.ID, key.GroupID); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to remove key from store after DB deletion, rolling back transaction")
				return err
			}
		}
		return nil
	})

	return removedCount, err
}

// RemoveKeysFromStore 直接从内存存储中移除指定的键，不涉及数据库操作
// 这个方法适用于数据库已经删除但需要清理内存存储的场景
func (p *KeyProvider) RemoveKeysFromStore(groupID uint, keyIDs []uint) error {
	if len(keyIDs) == 0 {
		return nil
	}

	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", groupID)

	// 第一步：直接删除整个 active_keys 列表
	if err := p.store.Delete(activeKeysListKey); err != nil {
		logrus.WithFields(logrus.Fields{
			"groupID": groupID,
			"error":   err,
		}).Error("Failed to delete active keys list")
		return err
	}

	// 第二步：批量删除所有相关的key hash
	for _, keyID := range keyIDs {
		keyHashKey := fmt.Sprintf("key:%d", keyID)
		if err := p.store.Delete(keyHashKey); err != nil {
			logrus.WithFields(logrus.Fields{
				"keyID": keyID,
				"error": err,
			}).Error("Failed to delete key hash")
		}
	}

	logrus.WithFields(logrus.Fields{
		"groupID":  groupID,
		"keyCount": len(keyIDs),
	}).Info("Successfully cleaned up group keys from store")

	return nil
}

// addKeyToStore is a helper to add a single key to the cache.
func (p *KeyProvider) addKeyToStore(key *models.APIKey) error {
	// 1. Store key details in HASH
	keyHashKey := fmt.Sprintf("key:%d", key.ID)
	keyDetails := p.apiKeyToMap(key)
	if err := p.store.HSet(keyHashKey, keyDetails); err != nil {
		return fmt.Errorf("failed to HSet key details for key %d: %w", key.ID, err)
	}

	// 2. If active, add to the active LIST
	if key.Status == models.KeyStatusActive {
		activeKeysListKey := fmt.Sprintf("group:%d:active_keys", key.GroupID)
		if err := p.store.LRem(activeKeysListKey, 0, key.ID); err != nil {
			return fmt.Errorf("failed to LRem key %d before LPush for group %d: %w", key.ID, key.GroupID, err)
		}
		if err := p.store.LPush(activeKeysListKey, key.ID); err != nil {
			return fmt.Errorf("failed to LPush key %d to group %d: %w", key.ID, key.GroupID, err)
		}
	}
	return nil
}

// addKeysToCacheBatch 批量添加密钥到缓存（用于批量导入场景）
func (p *KeyProvider) addKeysToCacheBatch(groupID uint, keys []models.APIKey) error {
	if len(keys) == 0 {
		return nil
	}

	// 1. 批量 HSet 密钥详情
	if pipeliner, ok := p.store.(store.RedisPipeliner); ok {
		// Redis: 使用 Pipeline 批量操作
		pipe := pipeliner.Pipeline()
		for i := range keys {
			keyHashKey := fmt.Sprintf("key:%d", keys[i].ID)
			pipe.HSet(keyHashKey, p.apiKeyToMap(&keys[i]))
		}
		if err := pipe.Exec(); err != nil {
			return fmt.Errorf("failed to batch HSet keys: %w", err)
		}
	} else {
		// MemoryStore: 降级为逐个 HSet
		for i := range keys {
			keyHashKey := fmt.Sprintf("key:%d", keys[i].ID)
			if err := p.store.HSet(keyHashKey, p.apiKeyToMap(&keys[i])); err != nil {
				return fmt.Errorf("failed to HSet key %d: %w", keys[i].ID, err)
			}
		}
	}

	// 2. 收集所有密钥 ID
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", groupID)
	activeKeyIDs := make([]any, len(keys))
	for i := range keys {
		activeKeyIDs[i] = keys[i].ID
	}

	// 3. 批量 LPush 活跃密钥
	if err := p.store.LPush(activeKeysListKey, activeKeyIDs...); err != nil {
		return fmt.Errorf("failed to batch LPush keys to group %d: %w", groupID, err)
	}

	return nil
}

// removeKeyFromStore is a helper to remove a single key from the cache.
func (p *KeyProvider) removeKeyFromStore(keyID, groupID uint) error {
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", groupID)
	if err := p.store.LRem(activeKeysListKey, 0, keyID); err != nil {
		logrus.WithFields(logrus.Fields{"keyID": keyID, "groupID": groupID, "error": err}).Error("Failed to LRem key from active list")
	}

	keyHashKey := fmt.Sprintf("key:%d", keyID)
	if err := p.store.Delete(keyHashKey); err != nil {
		return fmt.Errorf("failed to delete key HASH for key %d: %w", keyID, err)
	}
	return nil
}

// apiKeyToMap converts an APIKey model to a map for HSET.
func (p *KeyProvider) apiKeyToMap(key *models.APIKey) map[string]any {
	return map[string]any{
		"id":            fmt.Sprint(key.ID),
		"key_string":    key.KeyValue,
		"status":        key.Status,
		"failure_count": key.FailureCount,
		"group_id":      key.GroupID,
		"created_at":    key.CreatedAt.Unix(),
	}
}

// pluckIDs extracts IDs from a slice of APIKey.
func pluckIDs(keys []models.APIKey) []uint {
	ids := make([]uint, len(keys))
	for i, key := range keys {
		ids[i] = key.ID
	}
	return ids
}
