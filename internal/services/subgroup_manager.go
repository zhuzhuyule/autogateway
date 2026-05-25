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
	// P5.1: 429 (rate limit) 跟 5xx (服务故障) 性质不同 -- 不算 consecutive
	// failure, 直接进 cooldown. 默认 60s, 上游给了 Retry-After 用真值.
	rateLimitDefaultCooldown = 60 * time.Second
	rateLimitMaxCooldown     = 10 * time.Minute
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

	// P5.3: 延迟 EWMA, 用于动态调整 SWRR 有效权重让快的 sub-group 优先.
	// 0 = cold start (无 sample), selectByWeight 退化到 raw weight.
	latencyEWMA time.Duration
}

const (
	// P5.3: EWMA 平滑系数. alpha 越大对新样本越敏感.
	// 0.3 在"反应快"和"抗抖动"之间折中, 大约 6 个样本收敛.
	latencyEWMAAlpha = 0.3
	// effective weight clamp. 慢的最多减到 0.3x raw weight, 快的不加权.
	// 不加权理由: SWRR 总权重稳定才能保证公平 + 抗振荡.
	latencyWeightScaleMin = 0.3
	latencyWeightScaleMax = 1.0
)

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

// SubGroupHealth P6: 单个 sub-group 在 aggregate selector 中的运行时快照.
// 供 admin dashboard 展示, 用于诊断"为什么这家被跳过 / 这家凭什么优先".
type SubGroupHealth struct {
	Name                string    `json:"name"`
	SubGroupID          uint      `json:"sub_group_id"`
	Weight              int       `json:"weight"`              // 配置 raw weight
	EffectiveWeight     int       `json:"effective_weight"`    // P5.3 latency-adjusted
	LatencyEWMAMs       int64     `json:"latency_ewma_ms"`     // 0 = cold start
	ConsecutiveFailures int       `json:"consecutive_failures"` // 5xx/network 累计 (P5.1)
	CooldownUntil       time.Time `json:"cooldown_until,omitempty"` // zero = 不在冷却
	InCooldown          bool      `json:"in_cooldown"`
	HasModelsCache      bool      `json:"has_models_cache"`    // 是否有 available_models 缓存
	ModelsCount         int       `json:"models_count"`        // 缓存模型数 (0 = 无缓存或空)
}

// Snapshot P6: 返回该 aggregate selector 内部状态快照. 仅 aggregate group 有数据,
// standard / 未初始化 selector 返回 nil. 内部加 RLock + EFFECTIVE weight 重算.
func (m *SubGroupManager) Snapshot(aggregateGroupID uint) []SubGroupHealth {
	m.mu.RLock()
	sel, ok := m.selectors[aggregateGroupID]
	m.mu.RUnlock()
	if !ok || sel == nil {
		return nil
	}
	sel.mu.Lock()
	defer sel.mu.Unlock()
	now := time.Now()
	effWeights := sel.computeEffectiveWeights()
	out := make([]SubGroupHealth, 0, len(sel.subGroups))
	for i := range sel.subGroups {
		it := &sel.subGroups[i]
		health := SubGroupHealth{
			Name:                it.name,
			SubGroupID:          it.subGroupID,
			Weight:              it.weight,
			EffectiveWeight:     effWeights[i],
			LatencyEWMAMs:       it.latencyEWMA.Milliseconds(),
			ConsecutiveFailures: it.consecutiveFailures,
			HasModelsCache:      it.hasModelsCache,
			ModelsCount:         len(it.availableModels),
			InCooldown:          it.inCooldown(now),
		}
		if !it.cooldownUntil.IsZero() {
			health.CooldownUntil = it.cooldownUntil
		}
		out = append(out, health)
	}
	return out
}

// RecordLatency P5.3: 上层在成功响应后调用, 更新该 sub-group 的 latency EWMA.
// 仅 aggregate 子分组场景需要 (standard 直连路径 no-op). 失败/超时不记录
// (避免熔断超时污染延迟统计).
func (m *SubGroupManager) RecordLatency(aggregateGroupID uint, subGroupName string, d time.Duration) {
	if subGroupName == "" || d <= 0 {
		return
	}
	m.mu.RLock()
	sel, ok := m.selectors[aggregateGroupID]
	m.mu.RUnlock()
	if !ok || sel == nil {
		return
	}
	sel.mu.Lock()
	defer sel.mu.Unlock()
	for i := range sel.subGroups {
		it := &sel.subGroups[i]
		if it.name != subGroupName {
			continue
		}
		if it.latencyEWMA == 0 {
			it.latencyEWMA = d
		} else {
			it.latencyEWMA = time.Duration(float64(it.latencyEWMA)*(1-latencyEWMAAlpha) + float64(d)*latencyEWMAAlpha)
		}
		return
	}
}

// RecordSubGroupResult 上层(proxy server)在每次请求结束后调用,驱动子分组级熔断器.
// aggregateGroupID 必须是 aggregate 类型;如果原始请求是直接命中 standard 分组,该方法 no-op.
//
// statusCode 是上游 HTTP 响应码 (success=true 时忽略). P5.1: 429 走专项路径
// (不算 consecutive failure, 直接 cooldown), 5xx/network 错误走老熔断逻辑.
// retryAfter 是 Retry-After 头解析出的秒数 (429 时用); 0 表示用默认 cooldown.
func (m *SubGroupManager) RecordSubGroupResult(aggregateGroupID uint, subGroupName string, success bool, statusCode int, retryAfter time.Duration) {
	if subGroupName == "" {
		return
	}
	m.mu.RLock()
	sel, ok := m.selectors[aggregateGroupID]
	m.mu.RUnlock()
	if !ok || sel == nil {
		return
	}
	sel.recordResult(subGroupName, success, statusCode, retryAfter)
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

	// 严格路由 (聚合层契约):
	//   - 聚合的"已知模型集合" = 各 sub-group 的 (ExposedModels ∪ AvailableModels)
	//   - 请求 model 必须落在某 sub-group 的已知集合里; 命中者之间 SWRR;
	//     无命中 → 返回 "" 让上层报"未发现该模型", 不去碰运气打上游.
	//
	// 设计原则: 聚合层不能做 "passthrough", 透传只是 sub-group 自身的属性.
	// passthrough 模式的 sub-group 想被聚合调用, 必须提供模型列表 (手动
	// exposed_models 白名单或后端拉取 /v1/models 填充 available_models),
	// 否则它在聚合下永远不会被选中 — 这是用户语义契约的必然推论.
	//
	// 兜底仍保留: requestedModel 为空 (e.g. proxy 收到的请求没 body 或不是
	// chat/completions) 退化为全量 SWRR.
	if requestedModel != "" {
		if name := s.selectAmong(func(it *subGroupItem) bool {
			if !notAttempted(it) || !it.hasModelsCache {
				return false
			}
			_, ok := it.availableModels[requestedModel]
			return ok
		}); name != "" {
			return name
		}
		logrus.WithFields(logrus.Fields{
			"aggregate_group": s.groupName,
			"requested_model": requestedModel,
		}).Debug("No sub-group declares it serves this model — refusing to fallback")
		return ""
	}

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
// statusCode + retryAfter 用于 P5.1: 区分 429 (rate limit) vs 5xx (服务故障).
func (s *selector) recordResult(name string, success bool, statusCode int, retryAfter time.Duration) {
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

		// P5.1: 429 (rate limit) 是配额耗尽不是服务故障 -- 直接进 cooldown,
		// 不算 consecutive failure (避免触发 5xx 的 3-次失败长熔断).
		// cooldown 时长按 Retry-After 头优先, 没头用默认 60s. clamp 到 10 分钟避免恶意值.
		if statusCode == 429 {
			cooldown := rateLimitDefaultCooldown
			if retryAfter > 0 {
				cooldown = retryAfter
				if cooldown > rateLimitMaxCooldown {
					cooldown = rateLimitMaxCooldown
				}
			}
			it.cooldownUntil = time.Now().Add(cooldown)
			logrus.WithFields(logrus.Fields{
				"aggregate_group":  s.groupName,
				"sub_group":        it.name,
				"cooldown_seconds": int(cooldown.Seconds()),
				"reason":           "429 rate limit",
				"retry_after_hdr":  retryAfter > 0,
			}).Info("Sub-group rate-limited, cooling down")
			return
		}

		// 5xx / network error: 老熔断逻辑 (consecutive failures + 30s cooldown)
		it.consecutiveFailures++
		if it.consecutiveFailures >= subGroupBreakerThreshold {
			it.cooldownUntil = time.Now().Add(subGroupBreakerCooldown)
			logrus.WithFields(logrus.Fields{
				"aggregate_group":  s.groupName,
				"sub_group":        it.name,
				"failures":         it.consecutiveFailures,
				"cooldown_seconds": int(subGroupBreakerCooldown.Seconds()),
				"status_code":      statusCode,
			}).Warn("Sub-group circuit breaker tripped")
		}
		return
	}
}

// inCooldown 是否处于熔断冷却期(无锁,调用方应已持锁或确认无并发).
func (it *subGroupItem) inCooldown(now time.Time) bool {
	return !it.cooldownUntil.IsZero() && now.Before(it.cooldownUntil)
}

// selectByWeight implements smooth weighted round-robin algorithm.
// P5.3: 用 latency-adjusted effective weight 替代 raw weight, 让快的 sub-group
// 在 SWRR 中拿更大的有效权重. raw weight 仍是配置上限, 仅"减权慢的".
func (s *selector) selectByWeight() *subGroupItem {
	effWeights := s.computeEffectiveWeights()
	totalWeight := 0
	var best *subGroupItem

	for i := range s.subGroups {
		item := &s.subGroups[i]
		w := effWeights[i]
		totalWeight += w
		item.currentWeight += w

		if w > 0 && (best == nil || item.currentWeight > best.currentWeight) {
			best = item
		}
	}

	if best == nil {
		return &s.subGroups[0]
	}

	best.currentWeight -= totalWeight
	return best
}

// computeEffectiveWeights P5.3: 按 latencyEWMA 算 latency-adjusted weight.
// 规则:
//   - weight <= 0 → 0 (禁用语义保留)
//   - 没 EWMA 数据 (cold start) → 用 raw weight
//   - 池中找 base = min EWMA 作基线; 比基线慢的按 base/latency 比例减权
//   - clamp 到 [0.3, 1.0] × raw weight, 至少 1 (避免完全饿死)
func (s *selector) computeEffectiveWeights() []int {
	out := make([]int, len(s.subGroups))
	var base time.Duration
	for i := range s.subGroups {
		it := &s.subGroups[i]
		if it.weight <= 0 || it.latencyEWMA == 0 {
			continue
		}
		if base == 0 || it.latencyEWMA < base {
			base = it.latencyEWMA
		}
	}
	for i := range s.subGroups {
		it := &s.subGroups[i]
		if it.weight <= 0 {
			out[i] = 0
			continue
		}
		if base == 0 || it.latencyEWMA == 0 {
			out[i] = it.weight
			continue
		}
		scale := float64(base) / float64(it.latencyEWMA)
		if scale < latencyWeightScaleMin {
			scale = latencyWeightScaleMin
		}
		if scale > latencyWeightScaleMax {
			scale = latencyWeightScaleMax
		}
		eff := int(float64(it.weight) * scale)
		if eff < 1 {
			eff = 1
		}
		out[i] = eff
	}
	return out
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
