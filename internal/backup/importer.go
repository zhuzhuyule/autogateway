// internal/backup/importer.go
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"autogateway/internal/encryption"
	"autogateway/internal/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Counts 是 import 报告的分项计数。
type Counts struct {
	SystemSettings int `json:"system_settings"`
	Groups         int `json:"groups"`
	APIKeys        int `json:"api_keys"`
	ModelAliases   int `json:"model_aliases"`
	GroupSubGroups int `json:"group_sub_groups"`
}

// Report 是 Import 返回的完整结果。
type Report struct {
	Applied   Counts   `json:"applied"`
	Skipped   Counts   `json:"skipped"`
	Warnings  []string `json:"warnings"`
	ElapsedMs int64    `json:"elapsed_ms"`
}

// Importer 把 BackupV1 写入数据库。
type Importer struct {
	db  *gorm.DB
	enc encryption.Service
}

func NewImporter(db *gorm.DB, enc encryption.Service) *Importer {
	return &Importer{db: db, enc: enc}
}

// Import 单事务写入，按 strategy 处理冲突。
func (i *Importer) Import(ctx context.Context, bk *BackupV1, strat Strategy) (*Report, error) {
	if bk == nil {
		return nil, errors.New("nil backup payload")
	}
	if bk.SchemaVersion != CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported schema_version %d (supported: %d)", bk.SchemaVersion, CurrentSchemaVersion)
	}

	rep := &Report{}
	start := time.Now()

	err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// replace 模式先清空 standard groups (cascade keys/aliases/sub-rels)
		if strat == StrategyReplace {
			if err := wipeStandardGroups(tx); err != nil {
				return err
			}
		}

		// 1. SystemSettings (跳过 encryption_key)
		for _, s := range bk.Data.SystemSettings {
			if s.SettingKey == "encryption_key" {
				rep.Warnings = append(rep.Warnings, "system_settings.encryption_key skipped (protected)")
				rep.Skipped.SystemSettings++
				continue
			}
			applied, err := upsertSystemSetting(tx, s, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.SystemSettings++
			} else {
				rep.Skipped.SystemSettings++
			}
		}

		// 2. Groups (跳过 is_system)
		groupIDByName := make(map[string]uint, len(bk.Data.Groups))
		// 把当前 DB 里所有 group 的 name→id 索引一遍（含系统聚合，用于 sub_groups 查找）
		var existing []models.Group
		if err := tx.Find(&existing).Error; err != nil {
			return err
		}
		for _, g := range existing {
			groupIDByName[g.Name] = g.ID
		}
		skippedGroupNames := make(map[string]struct{}) // skip 模式下被跳过的，下面 keys/aliases 也要跳

		for _, dto := range bk.Data.Groups {
			if dto.IsSystem {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("group %q skipped (is_system=true)", dto.Name))
				rep.Skipped.Groups++
				continue
			}
			id, applied, err := upsertGroup(tx, dto, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.Groups++
				groupIDByName[dto.Name] = id
			} else {
				rep.Skipped.Groups++
				skippedGroupNames[dto.Name] = struct{}{}
			}
		}

		// 3. APIKeys
		for _, k := range bk.Data.APIKeys {
			if _, skipped := skippedGroupNames[k.GroupName]; skipped {
				rep.Skipped.APIKeys++
				continue
			}
			gid, ok := groupIDByName[k.GroupName]
			if !ok {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("api_key references unknown group %q", k.GroupName))
				rep.Skipped.APIKeys++
				continue
			}
			applied, err := upsertAPIKey(tx, k, gid, i.enc, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.APIKeys++
			} else {
				rep.Skipped.APIKeys++
			}
		}

		// 4. ModelAliases
		for _, a := range bk.Data.ModelAliases {
			if _, skipped := skippedGroupNames[a.GroupName]; skipped {
				rep.Skipped.ModelAliases++
				continue
			}
			gid, ok := groupIDByName[a.GroupName]
			if !ok {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("alias %q references unknown group %q", a.Alias, a.GroupName))
				rep.Skipped.ModelAliases++
				continue
			}
			applied, err := upsertAlias(tx, a, gid, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.ModelAliases++
			} else {
				rep.Skipped.ModelAliases++
			}
		}

		// 5. GroupSubGroups
		for _, s := range bk.Data.GroupSubGroups {
			parentID, okP := groupIDByName[s.ParentName]
			subID, okS := groupIDByName[s.SubGroupName]
			if !okP || !okS {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("group_sub_group %q→%q: name unresolved", s.ParentName, s.SubGroupName))
				rep.Skipped.GroupSubGroups++
				continue
			}
			applied, err := upsertSubGroup(tx, parentID, subID, s.Weight, strat)
			if err != nil {
				return err
			}
			if applied {
				rep.Applied.GroupSubGroups++
			} else {
				rep.Skipped.GroupSubGroups++
			}
		}

		return nil
	})

	rep.ElapsedMs = time.Since(start).Milliseconds()
	if err != nil {
		return nil, err
	}
	return rep, nil
}

func wipeStandardGroups(tx *gorm.DB) error {
	// 找所有 is_system=false 的 id
	var ids []uint
	if err := tx.Model(&models.Group{}).Where("is_system = ?", false).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Where("group_id IN ?", ids).Delete(&models.APIKey{}).Error; err != nil {
		return err
	}
	if err := tx.Where("group_id IN ?", ids).Delete(&models.ModelAlias{}).Error; err != nil {
		return err
	}
	if err := tx.Where("group_id IN ? OR sub_group_id IN ?", ids, ids).Delete(&models.GroupSubGroup{}).Error; err != nil {
		return err
	}
	if err := tx.Where("id IN ?", ids).Delete(&models.Group{}).Error; err != nil {
		return err
	}
	return nil
}

func upsertSystemSetting(tx *gorm.DB, s SystemSettingDTO, strat Strategy) (bool, error) {
	var existing models.SystemSetting
	err := tx.Where("setting_key = ?", s.SettingKey).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, tx.Create(&models.SystemSetting{
			SettingKey: s.SettingKey, SettingValue: s.SettingValue, Description: s.Description,
		}).Error
	}
	if err != nil {
		return false, err
	}
	if strat == StrategySkip {
		return false, nil
	}
	existing.SettingValue = s.SettingValue
	existing.Description = s.Description
	return true, tx.Save(&existing).Error
}

func upsertGroup(tx *gorm.DB, dto GroupDTO, strat Strategy) (uint, bool, error) {
	var existing models.Group
	err := tx.Where("name = ?", dto.Name).First(&existing).Error
	newRow := dtoToGroupModel(dto)

	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := tx.Create(&newRow).Error; err != nil {
			return 0, false, err
		}
		return newRow.ID, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	if strat == StrategySkip {
		return existing.ID, false, nil
	}
	newRow.ID = existing.ID
	newRow.CreatedAt = existing.CreatedAt
	if err := tx.Save(&newRow).Error; err != nil {
		return 0, false, err
	}
	return existing.ID, true, nil
}

func dtoToGroupModel(dto GroupDTO) models.Group {
	g := models.Group{
		Name:                dto.Name,
		DisplayName:         dto.DisplayName,
		ProxyKeys:           dto.ProxyKeys,
		Description:         dto.Description,
		GroupType:           dto.GroupType,
		IsSystem:            false, // 强制
		SystemRole:          dto.SystemRole,
		ValidationEndpoint:  dto.ValidationEndpoint,
		ChannelType:         dto.ChannelType,
		Sort:                dto.Sort,
		TestModel:           dto.TestModel,
		ParamOverrides:      datatypes.JSONMap(dto.ParamOverrides),
		Config:              datatypes.JSONMap(dto.Config),
		ModelRedirectRules:  datatypes.JSONMap(dto.ModelRedirectRules),
		ModelRedirectStrict: dto.ModelRedirectStrict,
		ModelRoutingMode:    dto.ModelRoutingMode,
	}
	if dto.Upstreams != nil {
		if raw, err := json.Marshal(dto.Upstreams); err == nil {
			g.Upstreams = datatypes.JSON(raw)
		}
	}
	if g.Upstreams == nil {
		g.Upstreams = datatypes.JSON([]byte("[]"))
	}
	if dto.HeaderRules != nil {
		if raw, err := json.Marshal(dto.HeaderRules); err == nil {
			g.HeaderRules = datatypes.JSON(raw)
		}
	}
	if dto.ExposedModels != nil {
		if raw, err := json.Marshal(dto.ExposedModels); err == nil {
			g.ExposedModels = datatypes.JSON(raw)
		}
	}
	if dto.BlockedModels != nil {
		if raw, err := json.Marshal(dto.BlockedModels); err == nil {
			g.BlockedModels = datatypes.JSON(raw)
		}
	}
	return g
}

// upsertAPIKey upserts an APIKey row using (group_id, key_hash) as the business key.
//
// Cross-instance note: key_hash is computed via encryption.Service.Hash, which is
// HMAC-SHA256 keyed by ENCRYPTION_KEY. Same plaintext under different ENCRYPTION_KEY
// yields different hashes — so migrating the same key between two instances that
// already independently held it may produce a duplicate row, not a dedup. This is
// the expected behavior: cross-instance dedup would require plaintext comparison
// (decrypting every existing row), which is out of scope for v1.
func upsertAPIKey(tx *gorm.DB, k APIKeyDTO, gid uint, enc encryption.Service, strat Strategy) (bool, error) {
	hash := enc.Hash(k.KeyValue) // HMAC-SHA256 keyed by ENCRYPTION_KEY — matches project convention
	var existing models.APIKey
	err := tx.Where("group_id = ? AND key_hash = ?", gid, hash).First(&existing).Error

	enced, encErr := enc.Encrypt(k.KeyValue)
	if encErr != nil {
		return false, fmt.Errorf("encrypt key: %w", encErr)
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, tx.Create(&models.APIKey{
			GroupID: gid, KeyValue: enced, KeyHash: hash,
			Status: k.Status, Notes: k.Notes,
		}).Error
	}
	if err != nil {
		return false, err
	}
	if strat == StrategySkip {
		return false, nil
	}
	existing.Status = k.Status
	existing.Notes = k.Notes
	return true, tx.Save(&existing).Error
}

func upsertAlias(tx *gorm.DB, a ModelAliasDTO, gid uint, strat Strategy) (bool, error) {
	var existing models.ModelAlias
	err := tx.Where("group_id = ? AND alias = ? AND real_model = ?", gid, a.Alias, a.RealModel).First(&existing).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, tx.Create(&models.ModelAlias{
			Alias: a.Alias, GroupID: gid, RealModel: a.RealModel,
			Weight: a.Weight, Priority: a.Priority,
			Enabled: a.Enabled, IsReserved: a.IsReserved,
		}).Error
	}
	if err != nil {
		return false, err
	}
	if strat == StrategySkip {
		return false, nil
	}
	existing.Weight = a.Weight
	existing.Priority = a.Priority
	existing.Enabled = a.Enabled
	return true, tx.Save(&existing).Error
}

func upsertSubGroup(tx *gorm.DB, parentID, subID uint, weight int, strat Strategy) (bool, error) {
	var existing models.GroupSubGroup
	err := tx.Where("group_id = ? AND sub_group_id = ?", parentID, subID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, tx.Create(&models.GroupSubGroup{
			GroupID: parentID, SubGroupID: subID, Weight: weight,
		}).Error
	}
	if err != nil {
		return false, err
	}
	if strat == StrategySkip {
		return false, nil
	}
	existing.Weight = weight
	return true, tx.Save(&existing).Error
}
