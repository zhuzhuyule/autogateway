package services

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"autogateway/internal/models"
	"autogateway/internal/store"

	"github.com/sirupsen/logrus"
)

// 子分组熔断器参数:连续 N 次失败 → 冷却 D 秒,期间 SWRR 跳过该子分组.
// 全部子分组都在冷却 → 仍然挑一个最早恢复的(graceful degrade).
const (
	subGroupBreakerThreshold = 3
	subGroupBreakerCooldown  = 30 * time.Second
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

	// 熔断器状态(由 selector.mu 保护)
	consecutiveFailures int
	cooldownUntil       time.Time
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
	return m.SelectSubGroupForModelExcluding(group, requestedModel, nil)
}

// RecordSubGroupResult 上层(proxy server)在每次请求结束后调用,驱动子分组级熔断器.
// aggregateGroupID 必须是 aggregate 类型;如果原始请求是直接命中 standard 分组,该方法 no-op.
func (m *SubGroupManager) RecordSubGroupResult(aggregateGroupID uint, subGroupName string, success bool) {
	if subGroupName == "" {
		return
	}
	m.mu.RLock()
	sel, ok := m.selectors[aggregateGroupID]
	m.mu.RUnlock()
	if !ok || sel == nil {
		return
	}
	sel.recordResult(subGroupName, success)
}

// SelectSubGroupForModelExcluding 与 SelectSubGroupForModel 类似,但额外排除 attempted 集合中的子分组.
// 用于聚合 failover: 上一个子分组刚耗尽配额/全部失败,应跳到下一个候选.
// 全部子分组都被排除时返回 ""(调用方据此结束 failover).
func (m *SubGroupManager) SelectSubGroupForModelExcluding(
	group *models.Group, requestedModel string, attempted map[string]bool,
) (string, error) {
	if group.GroupType != "aggregate" {
		return "", nil
	}

	selector := m.getSelector(group)
	if selector == nil {
		return "", fmt.Errorf("no valid sub-groups available for aggregate group '%s'", group.Name)
	}

	selectedName := selector.selectNextForModelExcluding(requestedModel, attempted)
	if selectedName == "" {
		return "", fmt.Errorf("no sub-groups with active keys for aggregate group '%s'", group.Name)
	}

	logrus.WithFields(logrus.Fields{
		"aggregate_group": group.Name,
		"selected_group":  selectedName,
		"requested_model": requestedModel,
		"excluded":        len(attempted),
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
				// 注入每个子分组的候选模型集合到 selector items.
				// 用 candidateModelsForGroup 统一规则:
				//   - specified mode: 先看 ExposedModels (用户白名单),
				//     空了再 fallback 到 AvailableModels (上游 /v1/models 缓存)
				//   - 其他 mode: 直接看 AvailableModels
				// 关键: 跨实例 import 后 AvailableModels 是空的 (backup 不带运行时缓存),
				// 但 ExposedModels 在 backup payload 里, 所以 specified 模式的
				// sub-group 仍然能正确路由, 不会被算法当成 "未知能力" 而随机选中.
				for i := range sel.subGroups {
					if sub, ok := byID[sel.subGroups[i].subGroupID]; ok {
						if set := candidateModelsForGroup(sub); len(set) > 0 {
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
	return s.selectNextForModelExcluding(requestedModel, nil)
}

// selectNextForModelExcluding 在 selectNextForModel 基础上排除 attempted 集合中的子分组(按 name).
func (s *selector) selectNextForModelExcluding(requestedModel string, attempted map[string]bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.subGroups) == 0 {
		return ""
	}

	notAttempted := func(it *subGroupItem) bool {
		return !attempted[it.name]
	}

	// 三阶段路由策略 (按权威性递减):
	//   1. STRICT — 优先选明确声明能 serve 该 model 的 sub-group
	//      (hasModelsCache=true 且 model 在 availableModels 集合里)
	//   2. UNKNOWN — 全部已知 sub-group 都不含该 model 时, 给"未知能力"的
	//      sub-group (hasModelsCache=false, 尚未拉过模型列表) 一次机会
	//   3. FULL — 第 2 阶段也无候选, 退化到全量 SWRR (硬碰)
	//
	// 旧逻辑把 UNKNOWN 和 STRICT 合并在一遍, 导致"未知能力"会跟"明确含 model"
	// 平等参与 SWRR. 实战表现: 聚合代理 GLM-4-Flash, zhipu 白名单含此 model,
	// groq passthrough 模式没配白名单也没拉过 /v1/models → groq 被当未知能力
	// 顶进 SWRR → 50% 概率选 groq → 上游 404. 三阶段分离后, 只要有一个
	// sub-group 明确声明能 serve, 就锁定它(们).
	if requestedModel != "" {
		// 阶段 1: STRICT
		if name := s.selectAmong(func(it *subGroupItem) bool {
			if !notAttempted(it) || !it.hasModelsCache {
				return false
			}
			_, ok := it.availableModels[requestedModel]
			return ok
		}); name != "" {
			return name
		}

		// 阶段 2: UNKNOWN
		if name := s.selectAmong(func(it *subGroupItem) bool {
			return notAttempted(it) && !it.hasModelsCache
		}); name != "" {
			logrus.WithFields(logrus.Fields{
				"aggregate_group": s.groupName,
				"requested_model": requestedModel,
				"selected":        name,
			}).Debug("No strict match; falling through to UNKNOWN-capability sub-group")
			return name
		}

		logrus.WithFields(logrus.Fields{
			"aggregate_group": s.groupName,
			"requested_model": requestedModel,
		}).Debug("No strict or unknown-capability match; FULL fallback")
	}

	// 阶段 3: FULL (or requestedModel 为空, 直接 SWRR)
	return s.selectAmong(notAttempted)
}

// selectAmong 在 SWRR 之上按 predicate 过滤,跳过熔断期内的子分组,直到选到有 active keys 的子分组.
// 若所有候选都在熔断期(graceful degrade),挑 cooldown 最早结束的那个返回.
func (s *selector) selectAmong(pred func(*subGroupItem) bool) string {
	now := time.Now()

	if len(s.subGroups) == 1 {
		it := &s.subGroups[0]
		if pred(it) && s.hasActiveKeys(it.subGroupID) {
			return it.name
		}
		return ""
	}

	attempted := make(map[uint]bool)
	var bestCooldownCandidate *subGroupItem
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

		if !s.hasActiveKeys(item.subGroupID) {
			continue
		}

		if item.inCooldown(now) {
			// 跳过,但记录"最早能用"的,作为 graceful degrade 兜底
			if bestCooldownCandidate == nil || item.cooldownUntil.Before(bestCooldownCandidate.cooldownUntil) {
				bestCooldownCandidate = item
			}
			continue
		}

		logrus.WithFields(logrus.Fields{
			"aggregate_group": s.groupName,
			"selected_group":  item.name,
			"attempts":        len(attempted),
		}).Debug("Selected sub-group with active keys")
		return item.name
	}

	if bestCooldownCandidate != nil {
		logrus.WithFields(logrus.Fields{
			"aggregate_group": s.groupName,
			"selected_group":  bestCooldownCandidate.name,
			"reason":          "all candidates in cooldown, picking earliest-recover",
		}).Warn("Sub-group circuit breaker: graceful degrade")
		return bestCooldownCandidate.name
	}

	return ""
}

// recordResult 记录一次该子分组上游请求的最终成败,驱动熔断器开关.
// 调用方不需要持有 selector.mu;此方法内部加锁.
func (s *selector) recordResult(name string, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subGroups {
		it := &s.subGroups[i]
		if it.name != name {
			continue
		}
		if success {
			if it.consecutiveFailures > 0 || !it.cooldownUntil.IsZero() {
				logrus.WithFields(logrus.Fields{
					"aggregate_group": s.groupName,
					"sub_group":       it.name,
				}).Debug("Sub-group circuit breaker: reset on success")
			}
			it.consecutiveFailures = 0
			it.cooldownUntil = time.Time{}
			return
		}
		it.consecutiveFailures++
		if it.consecutiveFailures >= subGroupBreakerThreshold {
			it.cooldownUntil = time.Now().Add(subGroupBreakerCooldown)
			logrus.WithFields(logrus.Fields{
				"aggregate_group":  s.groupName,
				"sub_group":        it.name,
				"failures":         it.consecutiveFailures,
				"cooldown_seconds": int(subGroupBreakerCooldown.Seconds()),
			}).Warn("Sub-group circuit breaker tripped")
		}
		return
	}
}

// inCooldown 是否处于熔断冷却期(无锁,调用方应已持锁或确认无并发).
func (it *subGroupItem) inCooldown(now time.Time) bool {
	return !it.cooldownUntil.IsZero() && now.Before(it.cooldownUntil)
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
