package db

import (
	"time"

	"autogateway/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V2_5_25_CleanupOrphanKeys 软删所有"孤儿"活 key — 即 group_id 指向已删(soft-deleted)或
// 根本不存在的分组的活跃 api_key。
//
// Why: 历史上 mesh 同步合并 group 墓碑时没有级联删除其下的 key(本端 DeleteGroup 会级联, 但
// ProcessPayload 合并到的 group 墓碑不会), 于是"别处删掉的分组"在本端留下一堆指向死 group 的
// 活 key。它们:
//   - 匹配不到 provider → 被 SelectKey 选中必然请求失败;
//   - 前端没有对应分组入口 → 无法查看/清理, 成了"莫名其妙冒出来"的僵尸 key;
//   - 还会被增量同步反复导出, 污染其它节点。
//
// 根因已从两侧堵死: 合并侧(sync_service 方案 D')group 墓碑时级联软删其活 key; 加载侧(keypool
// LoadKeysFromDB 方案 C)只把"属于有效 group"的 key 载入池。本迁移(方案 B)清理存量脏数据。
//
// 软删而非硬删: P9 mesh 用 max(UpdatedAt, DeletedAt) 做 LWW; 软删留墓碑, 才能把"这批 key 已删"
// 的事实同步到其它还没清理的节点; 硬删会丢墓碑, 反被对端当"缺失"重新推回来。
// 幂等: 再次运行时已无孤儿, 命中 0 行直接返回。MUST 在 AutoMigrate 之后调用。
func V2_5_25_CleanupOrphanKeys(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.APIKey{}) || !db.Migrator().HasTable(&models.Group{}) {
		return nil
	}

	// 有效 group = 存在且未软删。孤儿活 key = group_id 不在此集合的活 key。
	validGroups := db.Model(&models.Group{}).Select("id").Where("deleted_at IS NULL")

	res := db.Model(&models.APIKey{}).
		Where("deleted_at IS NULL AND group_id NOT IN (?)", validGroups).
		Update("deleted_at", time.Now())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		logrus.Infof("V2_5_25: 软删了 %d 条孤儿 api_key(指向已删/不存在的分组)", res.RowsAffected)
	}
	return nil
}
