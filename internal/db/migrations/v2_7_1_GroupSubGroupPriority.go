package db

import (
	"autogateway/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V2_7_1_GroupSubGroupPriority 确保 group_sub_groups 有 priority 列。
//
// Why 显式 migration: 与 V2_5_30 同理 —— AutoMigrate 对"已存在表新增列"在部分
// 环境不生效。而 Slave 节点**不跑 AutoMigrate**(schema 由 Master 负责), 但同样
// 要读 group_sub_groups 做选路, 所以这个迁移在主从两个分支都要跑。
//
// 幂等: 列已存在则直接返回。
func V2_7_1_GroupSubGroupPriority(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.GroupSubGroup{}) {
		return nil
	}
	if db.Migrator().HasColumn(&models.GroupSubGroup{}, "Priority") {
		return nil
	}
	if err := db.Migrator().AddColumn(&models.GroupSubGroup{}, "Priority"); err != nil {
		return err
	}
	logrus.Info("V2_7_1: added priority column to group_sub_groups")
	return nil
}
