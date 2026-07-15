package db

import "gorm.io/gorm"

// V2_5_25_CleanupOrphanKeys 已废弃 — 曾用 deleted_at=now() 软删"孤儿 key"(指向已删/不存在
// 分组的活 key), 但该"当前时刻墓碑"经 (group_name, key_hash) 业务键同步会 LWW 覆盖其它
// 节点上指向有效同名 group 的合法 key(2026-07-15 定位: agnes/xfyun 在 mini 被全灭)。
//
// 根因是"多主 + LWW + now 墓碑"这套机制, 已改为主从架构(master-authoritative)根治,
// 见 docs/superpowers/specs/2026-07-15-master-authoritative-sync-design.md。孤儿由
// keypool LoadKeysFromDB(只载有效 group 的 key)兜底不加载即可, 无需主动软删。
//
// 保留空函数仅为避免历史引用编译失败;新部署不再调用(app.go 已摘除注册)。
func V2_5_25_CleanupOrphanKeys(db *gorm.DB) error {
	_ = db
	return nil
}
