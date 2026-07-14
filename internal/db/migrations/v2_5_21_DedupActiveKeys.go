package db

import (
	"fmt"
	"time"

	"autogateway/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V2_5_21_DedupActiveKeys 收敛同一 (group_id, key_hash) 的重复"活"记录, 并建立部分唯一
// 索引 uni_api_keys_group_hash_active, 保证每个 (group_id, key_hash) 至多一条活 key。
//
// Why: 旧的 mesh 同步合并用 tx.Unscoped().First() 按 key_hash 匹配, 命中最小 id 的记录 —
// 往往是墓碑。于是 incoming 活 key 的更新落到墓碑上: 真正的活 key 没被同步、墓碑 updated_at
// 被抬成 now → 每轮增量导出重命中 → 永不收敛的 churn, 并留下同 key_hash 多条活记录的脏数据。
// 合并逻辑已改为"活记录唯一匹配 + 墓碑作删除信号"(方案A), 这里清理历史脏数据 + 加 DB 层防护。
//
// 清理规则: 每个 (group_id, key_hash) 有多条活记录 → 保留 updated_at 最新的一条, 其余软删。
// 软删而非硬删: P9 mesh 用 max(UpdatedAt, DeletedAt) 做 LWW, 硬删会丢墓碑破坏同步。
//
// 跨方言: sqlite/postgres 建部分唯一索引; mysql 不支持部分索引, 靠合并逻辑 + 应用层保证。
// 幂等: 索引已存在则跳过。MUST 在 AutoMigrate 之后调用。
func V2_5_21_DedupActiveKeys(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.APIKey{}) {
		return nil
	}

	// 1) 清理重复活记录 (建唯一索引前必须, 否则 CREATE UNIQUE INDEX 会失败)
	if err := dedupActiveAPIKeys(db); err != nil {
		return fmt.Errorf("dedup active api keys: %w", err)
	}

	// 2) 建部分唯一索引
	dialect := db.Dialector.Name()
	const idx = "uni_api_keys_group_hash_active"
	switch dialect {
	case "sqlite", "postgres":
		exists, err := indexExists(db, dialect, "api_keys", idx)
		if err != nil {
			return fmt.Errorf("check index existence: %w", err)
		}
		if exists {
			return nil
		}
		// key_hash 为空的老数据不纳入唯一约束 (它们靠 key_value 匹配)
		sql := fmt.Sprintf(
			"CREATE UNIQUE INDEX %s ON api_keys(group_id, key_hash) WHERE deleted_at IS NULL AND key_hash != ''",
			idx,
		)
		if err := db.Exec(sql).Error; err != nil {
			return fmt.Errorf("create partial unique index: %w", err)
		}
		logrus.Infof("V2_5_21: created partial unique index %s on api_keys(group_id,key_hash)", idx)
	case "mysql":
		logrus.Warn("V2_5_21: MySQL 不支持部分唯一索引 — api_keys 活记录唯一性靠合并逻辑 + 应用层保证")
	default:
		logrus.Warnf("V2_5_21: unknown dialect %q, skip partial unique index", dialect)
	}
	return nil
}

// dedupActiveAPIKeys 每个 (group_id, key_hash) 保留 updated_at 最新的活记录, 其余软删。
func dedupActiveAPIKeys(db *gorm.DB) error {
	type dupKey struct {
		GroupID uint
		KeyHash string
	}
	var dups []dupKey
	if err := db.Model(&models.APIKey{}).
		Select("group_id, key_hash").
		Where("deleted_at IS NULL AND key_hash != ''").
		Group("group_id, key_hash").
		Having("COUNT(*) > 1").
		Scan(&dups).Error; err != nil {
		return err
	}
	if len(dups) == 0 {
		return nil
	}

	now := time.Now()
	softDeleted := 0
	for _, d := range dups {
		var rows []models.APIKey
		if err := db.Where("group_id = ? AND key_hash = ? AND deleted_at IS NULL", d.GroupID, d.KeyHash).
			Order("updated_at DESC, id DESC").
			Find(&rows).Error; err != nil {
			return err
		}
		// rows[0] = updated_at 最新的, 保留; 其余软删
		for i := 1; i < len(rows); i++ {
			if err := db.Model(&models.APIKey{}).
				Where("id = ?", rows[i].ID).
				Update("deleted_at", now).Error; err != nil {
				return err
			}
			softDeleted++
		}
	}
	logrus.Infof("V2_5_21: 清理了 %d 条重复的活 api_key (每个 group+hash 仅保留最新一条)", softDeleted)
	return nil
}
