package services

import (
	"context"
	"encoding/json"

	"autogateway/internal/models"
)

// syncPolicySettingKey 是 sync_policy 在 system_settings 表里的 setting_key。
const syncPolicySettingKey = "sync_policy"

// SyncPolicy 是 master 集中定义的"哪些不同步"规则。默认(空)= 全同步; master 新增任何
// 字段, 因不在清单里, 默认自动纳入同步。随快照下发给 follower, 由 follower 在
// ApplySnapshot 时执行: 排除的类别整体跳过, 排除的字段保留本地值。
//
// 类别取值: group / subgroup / key / alias / setting。
type SyncPolicy struct {
	ExcludedCategories []string            `json:"excludedCategories"`
	ExcludedFields     map[string][]string `json:"excludedFields"`
}

// DefaultSyncPolicy 预置"本机专属字段"排除 — 每台机器不同, 必须本地自治。
// setting 类别里额外排除 sync_policy 本身: 它是 master 专属规则, 不能被 follower 反向
// 覆盖回 master(否则 follower 一镜像就把 master 的规则清了)。
func DefaultSyncPolicy() *SyncPolicy {
	return &SyncPolicy{
		ExcludedCategories: []string{},
		ExcludedFields: map[string][]string{
			// Group.Config 里的 proxy_url 是本机代理地址
			"group": {"proxy_url"},
			// SystemSettings 里的本机地址 / 代理 / 同步自身配置 / policy 自身
			"setting": {"app_url", "proxy_url", "sync_enabled", "sync_key", "sync_policy"},
		},
	}
}

// IsCategoryExcluded 报告某整类是否不同步。nil policy = 全同步(不排除)。
func (p *SyncPolicy) IsCategoryExcluded(category string) bool {
	if p == nil {
		return false
	}
	for _, c := range p.ExcludedCategories {
		if c == category {
			return true
		}
	}
	return false
}

// IsFieldExcluded 报告某类别里的某字段是否保留本地(不被 master 覆盖)。nil = 不排除。
func (p *SyncPolicy) IsFieldExcluded(category, field string) bool {
	if p == nil || p.ExcludedFields == nil {
		return false
	}
	for _, f := range p.ExcludedFields[category] {
		if f == field {
			return true
		}
	}
	return false
}

// LoadSyncPolicy 从 sync_policy setting 读; 缺失/解析失败 → DefaultSyncPolicy。
func (s *SyncService) LoadSyncPolicy(ctx context.Context) *SyncPolicy {
	var row models.SystemSetting
	err := s.db.WithContext(ctx).Where("setting_key = ?", syncPolicySettingKey).First(&row).Error
	if err != nil {
		return DefaultSyncPolicy()
	}
	var p SyncPolicy
	if json.Unmarshal([]byte(row.SettingValue), &p) != nil {
		return DefaultSyncPolicy()
	}
	return &p
}

// SaveSyncPolicy 落库到 sync_policy setting(upsert by setting_key)。
func (s *SyncService) SaveSyncPolicy(ctx context.Context, p *SyncPolicy) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var row models.SystemSetting
	err = s.db.WithContext(ctx).Where("setting_key = ?", syncPolicySettingKey).First(&row).Error
	if err != nil {
		return s.db.WithContext(ctx).Create(&models.SystemSetting{
			SettingKey: syncPolicySettingKey, SettingValue: string(b),
		}).Error
	}
	return s.db.WithContext(ctx).Model(&row).Update("setting_value", string(b)).Error
}
