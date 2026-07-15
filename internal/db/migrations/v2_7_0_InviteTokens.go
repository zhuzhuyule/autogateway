package db

import (
	"autogateway/internal/models"

	"gorm.io/gorm"
)

// V2_7_0_InviteTokens 建 invite_tokens 表。Master 和 Slave 分支都要调 — Slave 分支不跑
// AutoMigrate, 邀请功能两种角色都要用, 故显式 AutoMigrate 该表。幂等。
func V2_7_0_InviteTokens(db *gorm.DB) error {
	return db.AutoMigrate(&models.InviteToken{})
}
