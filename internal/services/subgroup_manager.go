package services

import (
	"encoding/json"
	"fmt"
	"sync"

	"autogateway/internal/models"
	"autogateway/internal/store"

	"github.com/sirupsen/logrus"
)

// SubGroupManager manages weighted round-robin selection for all aggregate groups
type SubGroupManager struct {
	store     store.Store
	selectors map[uint]*selector
	mu        sync.RWMutex
}

// subGroupItem represents a sub-group with its weight and current weight for round-robin
type subGroupItem struct {
	name            string
	subGroupID      uint
	weight          int
	currentWeight   int
	availableModels map[string]struct{} // 上游缓存的可用模型集合;空 map 视为"未知,可能含任意模型"
	hasModelsCache  bool                // true: availableModels 是真实可信的过滤依据
}

// NewSubGroupManager creates a new sub-group manager service
func NewSubGroupManager(store store.Store) *SubGroupManager {
	return &SubGroupManager{
		store:     store,
		selectors: make(map[uint]*selector),
	}
}

// SelectSubGroup selects an appropriate sub-group for the given aggregate group
func (m *SubGroupManager) SelectSubGroup(group *models.Group) (string, error) {
	return m.SelectSubGroupForModel(group, "")
}

// SelectSubGroupForModel 在 SelectSubGroup 基础上加一层"必须包含 requestedModel"的过滤.
// requestedModel 为空 → 等同 SelectSubGroup. 过滤后无候选 → 退化到全量(graceful degrade).
func (m *SubGroupManager) SelectSubGroupForModel(group *models.Group, requestedModel string) (string, error) {
	if group.GroupType != "aggregate" {
		return "", nil
	}

	selector := m.getSelector(group)
	if selector == nil {
		return "", fmt.Errorf("no valid sub-groups available for aggregate group '%s'", group.Name)
	}

	selectedName := selector.selectNextForModel(requestedModel)
	if selectedName == "" {
		return "", fmt.Errorf("no sub-groups with active keys for aggregate group '%s'", group.Name)
	}

	logrus.WithFields(logrus.Fields{
		"aggregate_group": group.Name,
		"selected_group":  selectedName,
		"requested_model": requestedModel,
	}).Debug("Selected sub-group from aggregate")

	return selectedName, nil
}

// RebuildSelectors rebuild all selectors based on the incoming group
func (m *SubGroupManager) RebuildSelectors(groups map[string]*models.Group) {
	newSelectors := make(map[uint]*selector)

	// 反索引: SubGroupID -> *Group, 用于快速取每个子分组的 AvailableModels
	byID := make(map[uint]*models.Group, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}

	for _, group := range groups {
		if group.GroupType == "aggregate" && len(group.SubGroups) > 0 {
			if sel := m.createSelector(group); sel != nil {
				// 注入每个子分组的 available_models 到 selector items
				for i := range sel.subGroups {
					if sub, ok := byID[sel.subGroups[i].subGroupID]; ok {
						if set, has := parseModelsJSON(sub.AvailableModels); has {
							sel.subGroups[i].availableModels = set
							sel.subGroups[i].hasModelsCache = true
						}
					}
				}
				newSelectors[group.ID] = sel
			}
		}
	}

	m.mu.Lock()
	m.selectors = newSelectors
	m.mu.Unlock()

	logrus.WithField("new_count", len(newSelectors)).Debug("Rebuilt selectors for aggregate groups")
}

// getSelector retrieves or creates a selector for the aggregate group
func (m *SubGroupManager) getSelector(group *models.Group) *selector {
	m.mu.RLock()
	if sel, exists := m.selectors[group.ID]; exists {
		m.mu.RUnlock()
		return sel
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	if sel, exists := m.selectors[group.ID]; exists {
		return sel
	}

	sel := m.createSelector(group)
	if sel != nil {
		m.selectors[group.ID] = sel
		logrus.WithFields(logrus.Fields{
			"group_id":        group.ID,
			"group_name":      group.Name,
			"sub_group_count": len(sel.subGroups),
		}).Debug("Created sub-group selector")
	}

	return sel
}

// createSelector creates a new selector for an aggregate group
func (m *SubGroupManager) createSelector(group *models.Group) *selector {
	if group.GroupType != "aggregate" || len(group.SubGroups) == 0 {
		return nil
	}

	// 通过 SubGroupID 反查每个子分组的 available_models;调用方传入的 group.SubGroups 只有 ID/Weight,
	// 真实的 model 列表在 cache 里(由 GroupManager 加载时填充).
	// 这里只能拿到 SubGroupName,available_models 需要通过外部 lookup,稍后在 RebuildSelectors 注入.
	var items []subGroupItem
	for _, sg := range group.SubGroups {
		items = append(items, subGroupItem{
			name:          sg.SubGroupName,
			subGroupID:    sg.SubGroupID,
			weight:        sg.Weight,
			currentWeight: 0,
		})
	}

	if len(items) == 0 {
		return nil
	}

	return &selector{
		groupID:   group.ID,
		groupName: group.Name,
		subGroups: items,
		store:     m.store,
	}
}

// parseModelsJSON 解析 datatypes.JSON 形式的模型 ID 数组,返回 lookup map.
func parseModelsJSON(raw []byte) (map[string]struct{}, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false
	}
	if len(arr) == 0 {
		return nil, false
	}
	out := make(map[string]struct{}, len(arr))
	for _, m := range arr {
		out[m] = struct{}{}
	}
	return out, true
}

// selector encapsulates the weighted round-robin algorithm for a single aggregate group
type selector struct {
	groupID   uint
	groupName string
	subGroups []subGroupItem
	store     store.Store
	mu        sync.Mutex
}

// selectNext uses weighted round-robin algorithm to select a sub-group with active keys
func (s *selector) selectNext() string {
	return s.selectNextForModel("")
}

// selectNextForModel 基于 SWRR 选可用子分组,可选按 requestedModel 过滤.
// 若按 model 过滤后无候选(可能因为 available_models 还没缓存),退化到不过滤的 SWRR.
func (s *selector) selectNextForModel(requestedModel string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.subGroups) == 0 {
		return ""
	}

	// 第一遍: 严格按 model 过滤(只考虑 hasModelsCache 且包含 model 的子分组,
	// 以及尚未拉取过 model 列表的 sub-group——它们的能力未知,先给它们一次机会)
	if requestedModel != "" {
		if name := s.selectAmong(func(it *subGroupItem) bool {
			if !it.hasModelsCache {
				return true // 未知,允许尝试
			}
			_, ok := it.availableModels[requestedModel]
			return ok
		}); name != "" {
			return name
		}
		// 严格过滤无候选 → 优雅降级走全量
		logrus.WithFields(logrus.Fields{
			"aggregate_group": s.groupName,
			"requested_model": requestedModel,
		}).Debug("No sub-group matched requested model, falling back to full SWRR")
	}

	return s.selectAmong(func(_ *subGroupItem) bool { return true })
}

// selectAmong 在 SWRR 之上按 predicate 过滤,直到选到有 active keys 的子分组.
func (s *selector) selectAmong(pred func(*subGroupItem) bool) string {
	if len(s.subGroups) == 1 {
		it := &s.subGroups[0]
		if pred(it) && s.hasActiveKeys(it.subGroupID) {
			return it.name
		}
		return ""
	}

	attempted := make(map[uint]bool)
	for len(attempted) < len(s.subGroups) {
		item := s.selectByWeight()
		if item == nil {
			break
		}

		if attempted[item.subGroupID] {
			continue
		}
		attempted[item.subGroupID] = true

		if !pred(item) {
			continue
		}

		if s.hasActiveKeys(item.subGroupID) {
			logrus.WithFields(logrus.Fields{
				"aggregate_group": s.groupName,
				"selected_group":  item.name,
				"attempts":        len(attempted),
			}).Debug("Selected sub-group with active keys")
			return item.name
		}
	}

	return ""
}

// selectByWeight implements smooth weighted round-robin algorithm
func (s *selector) selectByWeight() *subGroupItem {
	totalWeight := 0
	var best *subGroupItem

	for i := range s.subGroups {
		item := &s.subGroups[i]
		totalWeight += item.weight
		item.currentWeight += item.weight

		if best == nil || item.currentWeight > best.currentWeight {
			best = item
		}
	}

	if best == nil {
		return &s.subGroups[0]
	}

	best.currentWeight -= totalWeight
	return best
}

// hasActiveKeys checks if a sub-group has available API keys
func (s *selector) hasActiveKeys(groupID uint) bool {
	key := fmt.Sprintf("group:%d:active_keys", groupID)
	length, err := s.store.LLen(key)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"group_id": groupID,
			"error":    err,
		}).Debug("Error checking active keys, assuming available")
		return true
	}
	return length > 0
}
