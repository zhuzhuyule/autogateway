package db

import (
	"autogateway/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V2_10_1_GroupModelsSource 确保 groups 有 models_source 列。
//
// Why 显式 migration: 与 V2_7_1 同理 —— AutoMigrate 对"已存在表新增列"在部分
// 环境不生效, 且 Slave 节点不跑 AutoMigrate, 但同样要读这个列判断清单来源。
//
// 幂等: 列已存在则直接返回。
func V2_10_1_GroupModelsSource(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.Group{}) {
		return nil
	}
	if db.Migrator().HasColumn(&models.Group{}, "ModelsSource") {
		return nil
	}
	if err := db.Migrator().AddColumn(&models.Group{}, "ModelsSource"); err != nil {
		return err
	}
	logrus.Info("V2_10_1: added models_source column to groups")
	return nil
}
