// internal/backup/exporter.go
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"autogateway/internal/encryption"
	"autogateway/internal/models"

	"gorm.io/gorm"
)

// Exporter 从数据库构建 BackupV1。
type Exporter struct {
	db      *gorm.DB
	enc     encryption.Service
	version string
}

func NewExporter(db *gorm.DB, enc encryption.Service, version string) *Exporter {
	return &Exporter{db: db, enc: enc, version: version}
}

// Export 在单事务里读全部相关表。返回 (payload, warnings, error)。
// per-row decryption failures 记 warning 而不整体失败。
func (e *Exporter) Export(ctx context.Context) (*BackupV1, []string, error) {
	out := &BackupV1{
		SchemaVersion: CurrentSchemaVersion,
		ExportedAt:    time.Now().UTC(),
		ExportedBy:    e.version,
	}
	var warnings []string

	err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. SystemSettings — 全表
		var ss []models.SystemSetting
		if err := tx.Find(&ss).Error; err != nil {
			return fmt.Errorf("read system_settings: %w", err)
		}
		out.Data.SystemSettings = make([]SystemSettingDTO, 0, len(ss))
		for _, s := range ss {
			out.Data.SystemSettings = append(out.Data.SystemSettings, SystemSettingDTO{
				SettingKey: s.SettingKey, SettingValue: s.SettingValue, Description: s.Description,
			})
		}

		// 2. Groups (standard only)
		var groups []models.Group
		if err := tx.Where("is_system = ?", false).Find(&groups).Error; err != nil {
			return fmt.Errorf("read groups: %w", err)
		}
		nameByID := make(map[uint]string, len(groups))
		out.Data.Groups = make([]GroupDTO, 0, len(groups))
		for _, g := range groups {
			nameByID[g.ID] = g.Name
			out.Data.Groups = append(out.Data.Groups, groupModelToDTO(&g))
		}

		// 系统聚合的 name → ID 也要收，给 GroupSubGroups 用
		var sysGroups []models.Group
		if err := tx.Where("is_system = ?", true).Find(&sysGroups).Error; err != nil {
			return fmt.Errorf("read sys groups: %w", err)
		}
		// models.Group.Name has a unique constraint at the SQL level, so the
		// standard + system passes can share nameByID safely.
		for _, g := range sysGroups {
			nameByID[g.ID] = g.Name
		}

		// 3. APIKeys (只导出 standard group 下的)
		standardIDs := make([]uint, 0, len(groups))
		for _, g := range groups {
			standardIDs = append(standardIDs, g.ID)
		}
		if len(standardIDs) > 0 {
			var keys []models.APIKey
			if err := tx.Where("group_id IN ?", standardIDs).Find(&keys).Error; err != nil {
				return fmt.Errorf("read api_keys: %w", err)
			}
			out.Data.APIKeys = make([]APIKeyDTO, 0, len(keys))
			for _, k := range keys {
				plain, derr := e.enc.Decrypt(k.KeyValue)
				if derr != nil {
					warnings = append(warnings, fmt.Sprintf("api_key id=%d decrypt failed, skipped", k.ID))
					continue
				}
				// Safe lookup: k.GroupID came from standardIDs which was derived from
				// the same `groups` slice that populated nameByID.
				out.Data.APIKeys = append(out.Data.APIKeys, APIKeyDTO{
					GroupName: nameByID[k.GroupID],
					KeyValue:  plain,
					Status:    k.Status,
					Notes:     k.Notes,
				})
			}
		}

		// 4. ModelAliases (只导出 standard group 下的)
		if len(standardIDs) > 0 {
			var aliases []models.ModelAlias
			if err := tx.Where("group_id IN ?", standardIDs).Find(&aliases).Error; err != nil {
				return fmt.Errorf("read model_aliases: %w", err)
			}
			out.Data.ModelAliases = make([]ModelAliasDTO, 0, len(aliases))
			for _, a := range aliases {
				// Safe lookup: a.GroupID came from standardIDs which was derived from
				// the same `groups` slice that populated nameByID.
				out.Data.ModelAliases = append(out.Data.ModelAliases, ModelAliasDTO{
					Alias:      a.Alias,
					GroupName:  nameByID[a.GroupID],
					RealModel:  a.RealModel,
					Weight:     a.Weight,
					Priority:   a.Priority,
					Enabled:    a.Enabled,
					IsReserved: a.IsReserved,
				})
			}
		}

		// 5. GroupSubGroups —— 两端必须 name 可解析；标准侧必须是被导出过的 standard
		standardSet := make(map[uint]struct{}, len(groups))
		for _, g := range groups {
			standardSet[g.ID] = struct{}{}
		}
		var subs []models.GroupSubGroup
		if err := tx.Find(&subs).Error; err != nil {
			return fmt.Errorf("read group_sub_groups: %w", err)
		}
		for _, s := range subs {
			parentName, ok1 := nameByID[s.GroupID]
			subName, ok2 := nameByID[s.SubGroupID]
			if !ok1 || !ok2 {
				warnings = append(warnings, fmt.Sprintf("group_sub_group %d→%d name unresolved, skipped", s.GroupID, s.SubGroupID))
				continue
			}
			if _, isStd := standardSet[s.SubGroupID]; !isStd {
				// 只有 standard 在 sub 侧的关系才被备份
				warnings = append(warnings, fmt.Sprintf("group_sub_group sub %s not standard, skipped", subName))
				continue
			}
			out.Data.GroupSubGroups = append(out.Data.GroupSubGroups, GroupSubGroupDTO{
				ParentName: parentName, SubGroupName: subName, Weight: s.Weight,
			})
		}

		return nil
	})

	if err != nil {
		return nil, warnings, err
	}
	return out, warnings, nil
}

func groupModelToDTO(g *models.Group) GroupDTO {
	dto := GroupDTO{
		Name:                g.Name,
		DisplayName:         g.DisplayName,
		ProxyKeys:           g.ProxyKeys,
		Description:         g.Description,
		GroupType:           g.GroupType,
		IsSystem:            false, // export 端永远抹平
		SystemRole:          g.SystemRole,
		ValidationEndpoint:  g.ValidationEndpoint,
		ChannelType:         g.ChannelType,
		Sort:                g.Sort,
		TestModel:           g.TestModel,
		ParamOverrides:      g.ParamOverrides,
		Config:              g.Config,
		ModelRedirectRules:  g.ModelRedirectRules,
		ModelRedirectStrict: g.ModelRedirectStrict,
		ModelRoutingMode:    g.ModelRoutingMode,
	}
	// Upstreams 在 DB 是 NOT NULL —— nil/空字节必须规范化为 []，
	// 否则会 JSON-marshal 成 null，违反 schema.go 的契约。
	if len(g.Upstreams) == 0 {
		dto.Upstreams = json.RawMessage(`[]`)
	} else {
		dto.Upstreams = json.RawMessage(g.Upstreams)
	}
	if g.HeaderRules != nil {
		dto.HeaderRules = jsonRaw(g.HeaderRules)
	}
	if g.ExposedModels != nil {
		dto.ExposedModels = jsonRaw(g.ExposedModels)
	}
	if g.BlockedModels != nil {
		dto.BlockedModels = jsonRaw(g.BlockedModels)
	}
	return dto
}

// jsonRaw 把 datatypes.JSON ([]byte) 转成 any 以便 json.Marshal 原样输出。
// 空值 → nil（配合 GroupDTO 上的 omitempty 即从 JSON 中省略）。
func jsonRaw(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}
