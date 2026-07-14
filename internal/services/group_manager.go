package services

import (
	"autogateway/internal/config"
	"autogateway/internal/failover"
	"autogateway/internal/models"
	"autogateway/internal/store"
	"autogateway/internal/syncer"
	"autogateway/internal/utils"
	"context"
	"encoding/json"
	"fmt"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const GroupUpdateChannel = "groups:updated"

// GroupManager manages the caching of group data.
type GroupManager struct {
	syncer          *syncer.CacheSyncer[map[string]*models.Group]
	db              *gorm.DB
	store           store.Store
	settingsManager *config.SystemSettingsManager
	subGroupManager *SubGroupManager
}

// NewGroupManager creates a new, uninitialized GroupManager.
func NewGroupManager(
	db *gorm.DB,
	store store.Store,
	settingsManager *config.SystemSettingsManager,
	subGroupManager *SubGroupManager,
) *GroupManager {
	return &GroupManager{
		db:              db,
		store:           store,
		settingsManager: settingsManager,
		subGroupManager: subGroupManager,
	}
}

// Initialize sets up the CacheSyncer. This is called separately to handle potential
func (gm *GroupManager) Initialize() error {
	loader := func() (map[string]*models.Group, error) {
		var groups []*models.Group
		if err := gm.db.Find(&groups).Error; err != nil {
			return nil, fmt.Errorf("failed to load groups from db: %w", err)
		}

		// Load all sub-group relationships for aggregate groups (only valid ones with weight > 0)
		var allSubGroups []models.GroupSubGroup
		if err := gm.db.Where("weight > 0").Find(&allSubGroups).Error; err != nil {
			return nil, fmt.Errorf("failed to load valid sub groups: %w", err)
		}

		// Group sub-groups by aggregate group ID
		subGroupsByAggregateID := make(map[uint][]models.GroupSubGroup)
		for _, sg := range allSubGroups {
			subGroupsByAggregateID[sg.GroupID] = append(subGroupsByAggregateID[sg.GroupID], sg)
		}

		// Create group ID to group object mapping for sub-group lookups
		groupByID := make(map[uint]*models.Group)
		for _, group := range groups {
			groupByID[group.ID] = group
		}

		groupMap := make(map[string]*models.Group, len(groups))
		for _, group := range groups {
			g := *group
			g.EffectiveConfig = gm.settingsManager.GetEffectiveConfig(g.Config)
			g.ProxyKeysMap = utils.StringToSet(g.ProxyKeys, ",")

			// 解析 failover_status_codes 到 matcher 缓存. 解析失败保持零值 matcher
			// (Match 永远 false), 配合 proxy 层 alias-routed 404 兜底依然能 retry.
			if matcher, err := failover.ParseStatusCodeMatcher(g.EffectiveConfig.FailoverStatusCodes); err != nil {
				logrus.WithFields(logrus.Fields{
					"group_name": g.Name,
					"spec":       g.EffectiveConfig.FailoverStatusCodes,
					"error":      err,
				}).Warn("Invalid failover_status_codes spec, falling back to empty matcher")
			} else {
				g.FailoverStatusCodeMatcher = matcher
			}

			// Parse header rules with error handling
			if len(group.HeaderRules) > 0 {
				if err := json.Unmarshal(group.HeaderRules, &g.HeaderRuleList); err != nil {
					logrus.WithError(err).WithField("group_name", g.Name).Warn("Failed to parse header rules for group")
					g.HeaderRuleList = []models.HeaderRule{}
				}
			} else {
				g.HeaderRuleList = []models.HeaderRule{}
			}

			// Parse model redirect rules with error handling
			g.ModelRedirectMap = make(map[string]string)
			if len(group.ModelRedirectRules) > 0 {
				hasInvalidRules := false
				for key, value := range group.ModelRedirectRules {
					if valueStr, ok := value.(string); ok {
						g.ModelRedirectMap[key] = valueStr
					} else {
						logrus.WithFields(logrus.Fields{
							"group_name": g.Name,
							"rule_key":   key,
							"value_type": fmt.Sprintf("%T", value),
							"value":      value,
						}).Error("Invalid model redirect rule value type, skipping this rule")
						hasInvalidRules = true
					}
				}
				if hasInvalidRules {
					logrus.WithField("group_name", g.Name).Warn("Group has invalid model redirect rules, some rules were skipped. Please check the configuration.")
				}
			}

			// Load sub-groups for aggregate groups
			if g.GroupType == "aggregate" {
				if subGroups, ok := subGroupsByAggregateID[g.ID]; ok {
					g.SubGroups = make([]models.GroupSubGroup, len(subGroups))
					for i, sg := range subGroups {
						g.SubGroups[i] = sg
						if subGroup, exists := groupByID[sg.SubGroupID]; exists {
							g.SubGroups[i].SubGroupName = subGroup.Name
						}
					}
				}
			}

			groupMap[g.Name] = &g
			logrus.WithFields(logrus.Fields{
				"group_name":                 g.Name,
				"effective_config":           g.EffectiveConfig,
				"header_rules_count":         len(g.HeaderRuleList),
				"model_redirect_rules_count": len(g.ModelRedirectMap),
				"model_redirect_strict":      g.ModelRedirectStrict,
				"sub_group_count":            len(g.SubGroups),
			}).Debug("Loaded group with effective config")
		}

		return groupMap, nil
	}

	afterReload := func(newCache map[string]*models.Group) {
		gm.subGroupManager.RebuildSelectors(newCache)
	}

	syncer, err := syncer.NewCacheSyncer(
		loader,
		gm.store,
		GroupUpdateChannel,
		logrus.WithField("syncer", "groups"),
		afterReload,
	)
	if err != nil {
		return fmt.Errorf("failed to create group syncer: %w", err)
	}
	gm.syncer = syncer
	return nil
}

// GetGroupByName retrieves a single group by its name from the cache.
func (gm *GroupManager) GetGroupByName(name string) (*models.Group, error) {
	if gm.syncer == nil {
		return nil, fmt.Errorf("GroupManager is not initialized")
	}

	groups := gm.syncer.Get()
	group, ok := groups[name]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return group, nil
}

// GetGroupNameByID looks up a group's name by its primary key. Used by the
// router_engine middleware to translate a Selector candidate's GroupID
// into the group_name that the proxy handler expects in c.Param.
func (gm *GroupManager) GetGroupNameByID(id uint) (string, bool) {
	if gm.syncer == nil {
		return "", false
	}
	for _, g := range gm.syncer.Get() {
		if g.ID == id {
			return g.Name, true
		}
	}
	return "", false
}

// GetAllGroups returns all groups from the cache.
func (gm *GroupManager) GetAllGroups() map[string]*models.Group {
	if gm.syncer == nil {
		return nil
	}
	return gm.syncer.Get()
}

// AggregateCandidateModels 返回聚合分组所有(weight>0)子分组"可调模型"的并集(去重排序).
// 每个子分组: specified 用 exposed_models(空则 available), 其它用 available_models, 再去 blocked.
// 聚合分组的 /v1/models 用它构建完整、稳定的列表, 取代"随机 SWRR 命中单个子分组转发".
func (gm *GroupManager) AggregateCandidateModels(aggregateGroup *models.Group) []string {
	if aggregateGroup == nil || aggregateGroup.GroupType != "aggregate" || len(aggregateGroup.SubGroups) == 0 {
		return nil
	}
	all := gm.GetAllGroups()
	byID := make(map[uint]*models.Group, len(all))
	for _, g := range all {
		byID[g.ID] = g
	}
	subs := make([]*models.Group, 0, len(aggregateGroup.SubGroups))
	for _, sg := range aggregateGroup.SubGroups {
		if sub, ok := byID[sg.SubGroupID]; ok {
			subs = append(subs, sub)
		}
	}
	return aggregateCandidateModelIDs(subs)
}

// GetGroupBySystemRole 通过 system_role 查找系统默认聚合分组(如 'default-openai').
// 找不到时返回 ErrRecordNotFound;不依赖于分组名,避免用户改名后路由失效.
func (gm *GroupManager) GetGroupBySystemRole(role string) (*models.Group, error) {
	if gm.syncer == nil {
		return nil, fmt.Errorf("GroupManager is not initialized")
	}
	for _, g := range gm.syncer.Get() {
		if g.IsSystem && g.SystemRole == role {
			return g, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

// Invalidate triggers a cache reload across all instances.
func (gm *GroupManager) Invalidate() error {
	if gm.syncer == nil {
		return fmt.Errorf("GroupManager is not initialized")
	}
	return gm.syncer.Invalidate()
}

// Stop gracefully stops the GroupManager's background syncer.
func (gm *GroupManager) Stop(ctx context.Context) {
	if gm.syncer != nil {
		gm.syncer.Stop()
	}
}
