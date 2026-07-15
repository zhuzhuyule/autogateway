package db

import (
	"autogateway/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V2_5_30_SyncPeerIsMaster 确保 sync_peers 有 is_master 列 —— 主从拓扑: follower 只镜像
// is_master=true 的 peer(避免镜像所有 peer 互相覆盖清空)。
//
// Why 显式 migration: AutoMigrate 对"已存在表新增 bool 列"在部分环境未生效(2026-07-15
// 部署时本地/mini 的 sync_peers 没被 AutoMigrate 加上 is_master, 而 HK 加上了), 导致
// doPull 查 is_master 报 "no such column"。用 Migrator.AddColumn 显式兜底, 幂等。
func V2_5_30_SyncPeerIsMaster(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.SyncPeer{}) {
		return nil
	}
	if db.Migrator().HasColumn(&models.SyncPeer{}, "IsMaster") {
		return nil
	}
	if err := db.Migrator().AddColumn(&models.SyncPeer{}, "IsMaster"); err != nil {
		return err
	}
	logrus.Info("V2_5_30: added is_master column to sync_peers")
	return nil
}
