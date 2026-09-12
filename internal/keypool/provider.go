package keypool

import (
	"autogateway/internal/config"
	"autogateway/internal/encryption"
	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"
	"autogateway/internal/ratelimit"
	"autogateway/internal/store"
	"context"
	"errors"
	"fmt"
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
	triager         ErrorTriager // 可选:LLM 错误归因兜底,经 SetTriager 后置注入
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
	// 最大跳过次数随池大小自适应: 至少能把整个活跃池扫一遍(+8 desync buffer),
	// 这样限流/冷却中的 key 较多时不会被固定小上限提前挡住、误报"无可用 key";
	// 上限 512 兜住失控. 下限 16 保持对小池的旧行为.
	maxSkip := 16
	if n, err := p.store.LLen(activeKeysListKey); err == nil {
		if want := int(n) + 8; want > maxSkip {
			maxSkip = want
		}
	}
	if maxSkip > 512 {
		maxSkip = 512
	}

	// cooledKeyID/cooledDetails 记录本轮"仅因冷却被跳过"的第一把 key。冷却是软的:
	// 有非冷却可用 key 就用它(避开刚故障的); 若整池都在冷却, 退回用这把 fallback,
	// 让请求仍能打到上游拿到真实错误, 而不是误报"无可用 key"。
	var cooledKeyID uint64
	var cooledDetails map[string]string

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
		// LRem 没及时) / status 非 active (被对端 sync 标 invalid 但 active_keys
		// 没清干净) / 缺凭据 (key_string 空, hash 被只写 status 的路径建出来) →
		// 跳过这把 key + 从 active_keys LRem 出去, 让下次 Rotate 直接拿下一把.
		// 这是 store/db desync 的最后一道闸, 兜住直接 SQL 改 db
		// 或未来新路径绕过 store 同步的场景.
		// 缺凭据**绝不能**放行: 空 key 打上游只会拿到一个跟真实原因无关的 4xx
		// (Gitee 对 "Bearer " 返回 400), 掩盖掉真正的问题. 宁可这里报 no active keys.
		missingCredential := len(keyDetails) > 0 && keyDetails["key_string"] == ""
		if len(keyDetails) == 0 || keyDetails["status"] != models.KeyStatusActive || missingCredential {
			reason := "status_not_active"
			switch {
			case len(keyDetails) == 0:
				reason = "hash_missing"
			case missingCredential:
				reason = "missing_credential"
			}
			logrus.WithFields(logrus.Fields{
				"groupID": groupID,
				"keyID":   keyID,
				"reason":  reason,
				"status":  keyDetails["status"],
			}).Warn("SelectKey: stale entry in active_keys, evicting and re-rotating")
			_ = p.store.LRem(activeKeysListKey, 0, uint(keyID))
			// 缺凭据时保留 hash: status/failure_count 依然有效, 下次完整重载
			// (LoadKeysFromDB / SyncGroupKeysFromDB) 能原地补齐, 不必从 DB 重建.
			if len(keyDetails) > 0 && !missingCredential {
				_ = p.store.Delete(keyHashKey)
			}
			continue
		}

		// key 级冷却(软跳过): 被上游限流(429)/瞬态故障(5xx)的 key 打了 TTL 冷却标记,
		// 冷却期内先跳过它去找非冷却的(不 LRem, 到期自动恢复), 但记住它作 fallback。
		// 根治标准分组里坏 key 一直参与确定性轮转导致的"同一 API 时通时不通"。
		if cooled, err := p.store.Exists(fmt.Sprintf("key:%d:cooldown", keyID)); err == nil && cooled {
			if cooledKeyID == 0 {
				cooledKeyID = keyID
				cooledDetails = keyDetails
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

		return p.buildSelectedKey(groupID, keyID, keyDetails, limits), nil
	}

	// 没有非冷却可用 key: 若有冷却 fallback, 退回用它(整池冷却时让请求仍能打上游,
	// 拿到真实错误而非误报无 key)。否则当作整组没活 key。
	if cooledKeyID != 0 {
		return p.buildSelectedKey(groupID, cooledKeyID, cooledDetails, limits), nil
	}
	return nil, app_errors.ErrNoActiveKeys
}

// buildSelectedKey 把 store 里的 key hash 组装成可用的 *APIKey(解密 key 值 +
// 若配了限流则记一次账)。SelectKey 的正常命中与冷却 fallback 两条路径共用。
func (p *KeyProvider) buildSelectedKey(groupID uint, keyID uint64, keyDetails map[string]string, limits ratelimit.Limits) *models.APIKey {
	failureCount, _ := strconv.ParseInt(keyDetails["failure_count"], 10, 64)
	createdAt, _ := strconv.ParseInt(keyDetails["created_at"], 10, 64)

	encryptedKeyValue := keyDetails["key_string"]
	decryptedKeyValue, err := p.encryptionSvc.Decrypt(encryptedKeyValue)
	if err != nil {
		// 解密失败按原值用(兼容未加密的历史 key)。
		logrus.WithFields(logrus.Fields{"keyID": keyID, "error": err}).Debug("Failed to decrypt key value, using as-is for backward compatibility")
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
	}
}

// CoolDownKey 给一把 key 打一个 TTL 冷却标记, SelectKey 在冷却期内跳过它。
// 用于被上游限流(429)/瞬态故障(5xx)的 key —— 不拉黑、不失效, 只是暂时不选,
// TTL 到期自动恢复。直接 Set 覆盖(不严格取 max): 连续错误会重置冷却窗口,
// 影响可忽略。dur<=0 时不做任何事。
func (p *KeyProvider) CoolDownKey(keyID uint, dur time.Duration) {
	if dur <= 0 {
		return
	}
	cooldownKey := fmt.Sprintf("key:%d:cooldown", keyID)
	if err := p.store.Set(cooldownKey, []byte("1"), dur); err != nil {
		logrus.WithFields(logrus.Fields{"keyID": keyID, "error": err}).Debug("Failed to set key cooldown")
	}
}

// UpdateStatus 异步地提交一个 Key 状态更新任务。statusCode 是上游 HTTP 状态码
// (0 表示传输错误 / 无响应),供错误归因分类器判断该失败是否该计到 key 头上。
func (p *KeyProvider) UpdateStatus(apiKey *models.APIKey, group *models.Group, isSuccess bool, statusCode int, errorMessage string) {
	go func() {
		keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
		activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)

		if isSuccess {
			if err := p.handleSuccess(apiKey.ID, keyHashKey, activeKeysListKey); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "error": err}).Error("Failed to handle key success")
			}
			return
		}

		if !p.shouldCountFailure(group, statusCode, errorMessage) {
			logrus.WithFields(logrus.Fields{
				"keyID":  apiKey.ID,
				"status": statusCode,
				"error":  errorMessage,
			}).Debug("Uncounted error (not the key's fault), skipping failure handling")
			return
		}
		if err := p.handleFailure(apiKey, group, keyHashKey, activeKeysListKey); err != nil {
			logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "error": err}).Error("Failed to handle key failure")
		}
	}()
}

// shouldCountFailure 决定一次上游失败是否该计入 key 的 failure_count。
//
// Tier 1(规则,永远开):app_errors.Classify 按状态码+错误文本归因。请求错/
// 限流/上游故障一律不计——只有 KeyError 和判不出的 Unknown 才可能计入。
//
// Tier 2(LLM 兜底,opt-in):仅当规则落到 Unknown 且分组开启时,才请 LLM
// 二次判定;判定失败/未配置一律回退到「计入」(保守=保持历史行为,绝不因归因
// 服务不可用就放过本该熔断的坏 key)。
func (p *KeyProvider) shouldCountFailure(group *models.Group, statusCode int, errorMessage string) bool {
	cat := app_errors.Classify(statusCode, errorMessage)
	if !cat.CountsAgainstKey() {
		return false
	}
	// 到这里 cat 是 KeyError(确定计入,不劳烦 LLM)或 Unknown。
	if cat == app_errors.CategoryUnknown && p.triager != nil && group.EffectiveConfig.EnableLLMErrorTriage {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if count, ok := p.triager.ShouldCountAgainstKey(ctx, group, statusCode, errorMessage); ok {
			return count
		}
	}
	return true
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

		// hash 必须带 key_string —— 只写 status/failure_count 会留下一个没有凭据的
		// 残缺 hash, SelectKey 选出来就是空串, 请求会带着 "Authorization: Bearer "
		// 打到上游(实测 Gitee 返回一个与真实原因无关的 400 Bad Request)。
		// 这条路径可能是 hash 的**唯一**创建者: Slave 节点不跑 LoadKeysFromDB,
		// 校验通过时 handleSuccess 就是第一个碰 store 的人。用刚锁定的 DB 行补齐
		// 完整字段(含 KeyValue), 再把 updates 覆盖上去保证语义不变。
		fullHash := p.apiKeyToMap(&key)
		for k, v := range updates {
			fullHash[k] = v
		}
		if err := p.store.HSet(keyHashKey, fullHash); err != nil {
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
		// 缺凭据的残缺 hash (被只写 status 的路径建出来) 不能只修 status —— 那样
		// 它会带着空 key 进轮转. 用 DB 真值整体重建, 顺带把 failure_count 归位.
		if existingHash["key_string"] == "" {
			_ = p.store.HSet(keyHashKey, p.apiKeyToMap(k))
			_ = p.store.LRem(activeListKey, 0, k.ID)
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

// HydrateStoreFromDB 把 DB 里"store 中还没有"的 key 补进 key 池。
//
// 与 LoadKeysFromDB 的区别: LoadKeysFromDB 是 Master 的"以 DB 为准"全量初始化
// —— 它会 Delete 并重建每个 group 的 active_keys 列表, 并用 DB 值覆盖 hash。
// Slave 不能这么做: 多实例共享同一个 store 时, 一个 Slave 重启就会把 Master
// 运行期维护的 active_keys 顺序冲掉。
//
// 但 Slave 又**必须**在启动时补一次。原因是 store 不是持久化的, 而 mesh sync
// 只在**变更**时触发 (sync_service / sync_snapshot 合并收尾才调
// SyncGroupKeysFromDB) —— 启动前就已经 active 的 key 永远不会被重新加回来。
// 结果是: Slave 每次重启后 store 都是空的, 所有代理请求 503 NO_KEYS_AVAILABLE,
// 必须手动 validate-group 才能恢复 (实测踩过, 而且那一步还引出了"空凭据打上游")。
//
// 所以这里复用 SyncGroupKeysFromDB 的**非破坏性**语义逐个 group 补齐:
// 只补缺失的 hash / 把该 active 的 key 加回 active_keys, 已存在的 hash 一律
// 不动 (保护运行期累计的 failure_count)。
func (p *KeyProvider) HydrateStoreFromDB() error {
	var groupIDs []uint
	if err := p.db.Model(&models.Group{}).
		Where("deleted_at IS NULL").
		Pluck("id", &groupIDs).Error; err != nil {
		return fmt.Errorf("hydrate store: list groups: %w", err)
	}

	for _, groupID := range groupIDs {
		if err := p.SyncGroupKeysFromDB(groupID); err != nil {
			return fmt.Errorf("hydrate store: %w", err)
		}
	}

	logrus.WithField("groups", len(groupIDs)).Debug("Key pool hydrated from DB (non-destructive).")
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
