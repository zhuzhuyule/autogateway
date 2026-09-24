package db

import (
	"autogateway/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// requestLogUsageColumns 是 ①成本可观测性 / ②错误归因 加在 request_logs 上的列,
// 与 models.RequestLog 的字段一一对应。
var requestLogUsageColumns = []string{
	"PromptTokens",
	"CompletionTokens",
	"TotalTokens",
	"CostUSD",
	"CachedPromptTokens",
	"ErrorCategory",
}

// V2_8_2_RequestLogUsageColumns 确保 request_logs 有 token 用量 / 成本 / 错误归因这几列。
//
// Why 显式 migration(而不是靠 AutoMigrate):
//
//	models.RequestLog 里这几列的注释写着"AutoMigrate 自动加这些数值列, 无需手写
//	迁移" —— 这话只对 Master 成立。AutoMigrate 在 app.go 里包在
//	`if configManager.IsMaster()` 分支内, **Slave 节点不跑**。
//	于是 IS_SLAVE=true 的部署永远拿不到这几列, 而 logRequest 每次 INSERT 都会带上
//	它们, 写入直接报 "table request_logs has no column named prompt_tokens"。
//	后果是请求日志**静默地全部不落库**(实测: 某库 412 条日志全部停在 2026-07,
//	之后零新增), 连带 top-models / model-timings / usage-summary / 别名建议 一起变空。
//
//	所以这里显式补列, 并在**主从两个分支都注册** —— 与 V2_5_30 / V2_7_1 同一套路。
//
// 幂等: 逐列判断, 缺哪列补哪列。不用"任一列存在就整体跳过", 那样中途只补过部分
// 列的老库会被漏掉。加列一律 NOT NULL DEFAULT 0, 对已有行安全。
func V2_8_2_RequestLogUsageColumns(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.RequestLog{}) {
		return nil
	}
	var added []string
	for _, col := range requestLogUsageColumns {
		if db.Migrator().HasColumn(&models.RequestLog{}, col) {
			continue
		}
		if err := db.Migrator().AddColumn(&models.RequestLog{}, col); err != nil {
			return err
		}
		added = append(added, col)
	}
	if len(added) > 0 {
		logrus.Infof("V2_8_2: added request_logs columns %v", added)
	}
	return nil
}
