package services

import (
	"context"
	"errors"
	"fmt"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

// ApplySnapshot 是 follower 的镜像替换: 把本地"在同步范围内的类别"对齐到 master 的全量
// 快照 —— master 有则 upsert(排除字段保留本地), master 无则本地活记录软删。不比较任何
// 时间戳(master 即真值), 因此不可能出现 LWW 分裂态。
//
// 与 ProcessPayload(LWW 合并, 仅 master 侧接收 follower 手动迁移时用)刻意分开: 这里是
// 单向权威, 语义清晰、便于独立测试。跨端 group_id 用 name 建 remap 表, 与 ProcessPayload
// 一致。软删镜像用的 deleted_at=now 只在 follower 本地, follower 不外推, 不污染他人。
func (s *SyncService) ApplySnapshot(ctx context.Context, snap *SyncPayload) error {
	if snap == nil {
		return nil
	}
	ctx = context.WithValue(ctx, syncMergeKey{}, true)
	policy := snap.Policy
	affectedKeyGroups := make(map[uint]bool)

	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// group name → 本端 group_id 的映射, 供 subgroup/key remap。
		groupIDByName := map[string]uint{}

		// ---- 1. Groups(除非被排除) ----
		if !policy.IsCategoryExcluded("group") {
			masterNames := map[string]bool{}
			for i := range snap.Groups {
				incoming := snap.Groups[i]
				masterNames[incoming.Name] = true
				// 只匹配"活"group: 历史重复 group(墓碑+活同名)时, Unscoped().First() 会命中
				// 墓碑(id 最小), syncMergeSave 复活它 → 与活记录撞 partial-unique → 整个镜像
				// 事务回滚。只认活记录: 有则更新, 无则新建(新建不与墓碑冲突, partial-unique 只管活)。
				var existing models.Group
				err := tx.Where("name = ?", incoming.Name).First(&existing).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					// 新建: master 的本机字段不继承
					preserveExcludedGroupFields(&incoming, nil, policy)
					incoming.ID = 0
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("snapshot create group %s: %w", incoming.Name, err)
					}
					groupIDByName[incoming.Name] = incoming.ID
				} else {
					// 覆盖(master 权威), 但保留本机字段 + 复用本端 id
					preserveExcludedGroupFields(&incoming, &existing, policy)
					incoming.ID = existing.ID
					if err := syncMergeSave(tx, &incoming); err != nil {
						return fmt.Errorf("snapshot update group %s: %w", incoming.Name, err)
					}
					groupIDByName[incoming.Name] = existing.ID
				}
			}
			// master 没有的本地活 group → 镜像软删(连带其 key)
			var localGroups []models.Group
			if err := tx.Where("deleted_at IS NULL").Find(&localGroups).Error; err != nil {
				return err
			}
			for _, lg := range localGroups {
				if masterNames[lg.Name] {
					continue
				}
				if err := tx.Delete(&models.Group{}, lg.ID).Error; err != nil {
					return fmt.Errorf("snapshot delete extra group %s: %w", lg.Name, err)
				}
				res := tx.Where("group_id = ? AND deleted_at IS NULL", lg.ID).Delete(&models.APIKey{})
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected > 0 {
					affectedKeyGroups[lg.ID] = true
				}
			}
		} else {
			// group 类别被排除: 用本地现有 name→id 填 remap 表, 供 key/subgroup 用
			var localGroups []models.Group
			if err := tx.Where("deleted_at IS NULL").Find(&localGroups).Error; err != nil {
				return err
			}
			for _, lg := range localGroups {
				groupIDByName[lg.Name] = lg.ID
			}
		}

		// ---- 2. APIKeys(除非被排除) ----
		if !policy.IsCategoryExcluded("key") {
			// master 侧 group_id → name, 用于把 key 的归属 remap 到本端。
			masterGroupName := map[uint]string{}
			for _, g := range snap.Groups {
				masterGroupName[g.ID] = g.Name
			}
			type keyKey struct {
				GroupID uint
				Hash    string
			}
			masterKeys := map[keyKey]bool{}
			for i := range snap.APIKeys {
				incoming := snap.APIKeys[i]
				name := masterGroupName[incoming.GroupID]
				localGID, ok := groupIDByName[name]
				if !ok {
					continue // 对应 group 不在同步范围/不存在, 跳过该 key
				}
				incoming.GroupID = localGID
				masterKeys[keyKey{localGID, incoming.KeyHash}] = true

				// 同 group: 只匹配活 key(避免命中墓碑复活撞 partial-unique)。
				var existing models.APIKey
				err := tx.Where("group_id = ? AND key_hash = ?", localGID, incoming.KeyHash).First(&existing).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					incoming.ID = 0
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("snapshot create key: %w", err)
					}
				} else {
					incoming.ID = existing.ID
					if err := syncMergeSave(tx, &incoming); err != nil {
						return fmt.Errorf("snapshot update key: %w", err)
					}
				}
				affectedKeyGroups[localGID] = true
			}
			// master 无、本地有的活 key → 镜像软删(仅限同步范围内的 group)
			for _, localGID := range groupIDByName {
				var localKeys []models.APIKey
				if err := tx.Where("group_id = ? AND deleted_at IS NULL", localGID).Find(&localKeys).Error; err != nil {
					return err
				}
				for _, lk := range localKeys {
					if masterKeys[keyKey{localGID, lk.KeyHash}] {
						continue
					}
					if err := tx.Delete(&models.APIKey{}, lk.ID).Error; err != nil {
						return err
					}
					affectedKeyGroups[localGID] = true
				}
			}
		}

		// ---- 3. Settings(除非被排除): 只 upsert master 有的, 不镜像删除(避免误删本机自治项) ----
		if !policy.IsCategoryExcluded("setting") {
			for _, incoming := range snap.Settings {
				if policy.IsFieldExcluded("setting", incoming.SettingKey) {
					continue // 排除字段(SystemSetting 是 kv, 字段=SettingKey): 保留本地
				}
				var existing models.SystemSetting
				err := tx.Unscoped().Where("setting_key = ?", incoming.SettingKey).First(&existing).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if errors.Is(err, gorm.ErrRecordNotFound) {
					incoming.ID = 0
					if err := tx.Create(&incoming).Error; err != nil {
						return fmt.Errorf("snapshot create setting %s: %w", incoming.SettingKey, err)
					}
				} else {
					incoming.ID = existing.ID
					if err := syncMergeSave(tx, &incoming); err != nil {
						return fmt.Errorf("snapshot update setting %s: %w", incoming.SettingKey, err)
					}
				}
			}
		}

		// ---- 4. ModelAliases / SubGroups ----
		if err := s.applySnapshotAliases(tx, snap, policy); err != nil {
			return err
		}
		if err := s.applySnapshotSubGroups(tx, snap, policy, groupIDByName); err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}

	// 事务外: 让 keypool 跟 db 重新对齐(软删的 key 从 active_keys 摘除)。
	if s.keypoolInvalidator != nil {
		for gid := range affectedKeyGroups {
			if err := s.keypoolInvalidator.SyncGroupKeysFromDB(gid); err != nil {
				continue // 不上抛: db 已是真值, 下次启动 LoadKeysFromDB 兜底。
			}
		}
	}
	return nil
}

// applySnapshotAliases 镜像 model_aliases: 按 (alias, real_model) 业务键 upsert +
// master 无则软删。alias 不引用 group_id, 无需 remap。
func (s *SyncService) applySnapshotAliases(tx *gorm.DB, snap *SyncPayload, policy *SyncPolicy) error {
	if policy.IsCategoryExcluded("alias") {
		return nil
	}
	type ak struct{ Alias, Real string }
	master := map[ak]bool{}
	for i := range snap.ModelAliases {
		in := snap.ModelAliases[i]
		master[ak{in.Alias, in.RealModel}] = true
		var existing models.ModelAlias
		err := tx.Where("alias = ? AND real_model = ?", in.Alias, in.RealModel).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			in.ID = 0
			if err := tx.Create(&in).Error; err != nil {
				return fmt.Errorf("snapshot create alias %s: %w", in.Alias, err)
			}
		} else {
			in.ID = existing.ID
			if err := syncMergeSave(tx, &in); err != nil {
				return fmt.Errorf("snapshot update alias %s: %w", in.Alias, err)
			}
		}
	}
	var local []models.ModelAlias
	if err := tx.Where("deleted_at IS NULL").Find(&local).Error; err != nil {
		return err
	}
	for _, la := range local {
		if master[ak{la.Alias, la.RealModel}] {
			continue
		}
		if err := tx.Delete(&models.ModelAlias{}, la.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// applySnapshotSubGroups 镜像聚合关系: 按 (group name, subgroup name) 业务键。用
// groupIDByName 把 master 的 group_id/sub_group_id remap 到本端。
func (s *SyncService) applySnapshotSubGroups(tx *gorm.DB, snap *SyncPayload, policy *SyncPolicy, groupIDByName map[string]uint) error {
	if policy.IsCategoryExcluded("subgroup") {
		return nil
	}
	nameByMasterID := map[uint]string{}
	for _, g := range snap.Groups {
		nameByMasterID[g.ID] = g.Name
	}
	type pair struct{ G, S uint }
	master := map[pair]bool{}
	for i := range snap.SubGroups {
		in := snap.SubGroups[i]
		gID, gok := groupIDByName[nameByMasterID[in.GroupID]]
		sID, sok := groupIDByName[nameByMasterID[in.SubGroupID]]
		if !gok || !sok {
			continue
		}
		in.GroupID, in.SubGroupID = gID, sID
		master[pair{gID, sID}] = true
		var existing models.GroupSubGroup
		err := tx.Where("group_id = ? AND sub_group_id = ?", gID, sID).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			in.ID = 0
			if err := tx.Create(&in).Error; err != nil {
				return err
			}
		} else {
			in.ID = existing.ID
			if err := syncMergeSave(tx, &in); err != nil {
				return err
			}
		}
	}
	var local []models.GroupSubGroup
	if err := tx.Where("deleted_at IS NULL").Find(&local).Error; err != nil {
		return err
	}
	for _, ls := range local {
		if master[pair{ls.GroupID, ls.SubGroupID}] {
			continue
		}
		if err := tx.Delete(&models.GroupSubGroup{}, ls.ID).Error; err != nil {
			return err
		}
	}
	return nil
}
