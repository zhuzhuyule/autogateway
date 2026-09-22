package services

import (
	"testing"
	"time"

	"autogateway/internal/failover"
	"autogateway/internal/store"
)

// preloadActiveKeys 给每个 subGroupID 在 store 里塞一个 active key 标记,
// 让 selector.hasActiveKeys 返回 true. 把 selector 行为隔离到纯路由层.
func preloadActiveKeys(s store.Store, ids ...uint) {
	for _, id := range ids {
		key := "group:" + uintToStr(id) + ":active_keys"
		_ = s.LPush(key, "stub-key")
	}
}

func uintToStr(u uint) string {
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	return string(buf[i:])
}

// newTestSelector 直接构造 selector 内部状态.
func newTestSelector(items []subGroupItem) *selector {
	st := store.NewMemoryStore()
	ids := make([]uint, len(items))
	for i, it := range items {
		ids[i] = it.subGroupID
	}
	preloadActiveKeys(st, ids...)
	return &selector{
		groupID:   1,
		groupName: "test-aggregate",
		subGroups: items,
		store:     st,
		policy:    failover.DefaultCooldownPolicy(),
	}
}

// TestSelectNextForModel_StrictWinsOverUnknown 复现 v1.1.2 之前的 bug:
// zhipu (hasModelsCache=true 含 GLM-4-Flash) 必须严格优于 groq (hasModelsCache=false).
// 旧逻辑把"未知能力"提到第 1 阶段平等参与 SWRR → 50% 选到 groq → 上游 404.
func TestSelectNextForModel_StrictWinsOverUnknown(t *testing.T) {
	items := []subGroupItem{
		{
			name:            "zhipu",
			subGroupID:      13,
			weight:          1,
			availableModels: map[string]struct{}{"GLM-4-Flash": {}},
			hasModelsCache:  true,
		},
		{
			name:           "groq",
			subGroupID:     4,
			weight:         1,
			hasModelsCache: false, // 未知能力 (passthrough mode 且没拉过 /v1/models)
		},
	}
	sel := newTestSelector(items)

	// 跑 50 次, 必须全部选 zhipu (groq 不该在 strict 阶段被选)
	for i := 0; i < 50; i++ {
		got := sel.selectNextForModelExcluding("GLM-4-Flash", nil)
		if got != "zhipu" {
			t.Fatalf("iter %d: expected zhipu, got %q (groq leaking into strict phase!)", i, got)
		}
	}
}

// TestSelectNextForModel_NoMatchReturnsEmpty 严格语义: 已知能力的 sub-group
// 都不含 model 时, 返回 "" 让上层 (handler / proxy) 报"未发现", 不再 fallback
// 到未知能力的 sub-group. 不允许碰运气打上游。
func TestSelectNextForModel_NoMatchReturnsEmpty(t *testing.T) {
	items := []subGroupItem{
		{
			name:            "zhipu",
			subGroupID:      13,
			weight:          1,
			availableModels: map[string]struct{}{"GLM-4-Flash": {}}, // 不含 quux
			hasModelsCache:  true,
		},
		{
			name:           "groq",
			subGroupID:     4,
			weight:         1,
			hasModelsCache: false,
		},
	}
	sel := newTestSelector(items)

	// "quux" 没有任何 sub-group 声明能 serve → 返回空串
	got := sel.selectNextForModelExcluding("quux", nil)
	if got != "" {
		t.Fatalf("expected empty (refuse to fallback), got %q", got)
	}
}

// TestSelectNextForModel_AllKnownNoMatchReturnsEmpty 全部 sub-group 都已知能力
// 且都不含 model → 不再硬碰, 返回 "" (旧 v1.1.4 行为是退化到全量 SWRR).
func TestSelectNextForModel_AllKnownNoMatchReturnsEmpty(t *testing.T) {
	items := []subGroupItem{
		{
			name:            "a",
			subGroupID:      100,
			weight:          1,
			availableModels: map[string]struct{}{"foo": {}},
			hasModelsCache:  true,
		},
		{
			name:            "b",
			subGroupID:      101,
			weight:          1,
			availableModels: map[string]struct{}{"bar": {}},
			hasModelsCache:  true,
		},
	}
	sel := newTestSelector(items)

	got := sel.selectNextForModelExcluding("quux", nil)
	if got != "" {
		t.Fatalf("expected empty (no fallback), got %q", got)
	}
}

// TestSelectNextForModel_EmptyModelStillFallsBack requestedModel 为空时
// (e.g. 不是 chat/completions, 或 body 没有 model 字段) 退化为全量 SWRR,
// 不受严格语义限制.
func TestSelectNextForModel_EmptyModelStillFallsBack(t *testing.T) {
	items := []subGroupItem{
		{
			name:            "a",
			subGroupID:      100,
			weight:          1,
			availableModels: map[string]struct{}{"foo": {}},
			hasModelsCache:  true,
		},
		{
			name:           "b",
			subGroupID:     101,
			weight:         1,
			hasModelsCache: false,
		},
	}
	sel := newTestSelector(items)

	got := sel.selectNextForModelExcluding("", nil)
	if got != "a" && got != "b" {
		t.Fatalf("empty-model fallback should pick one of {a,b}, got %q", got)
	}
}

// TestSelectNextForModel_StrictAmongMultiple 多个 sub-group 都明确含该 model →
// 在它们之间 SWRR (权重决定分布), 完全不沾未知能力的 sub-group.
func TestSelectNextForModel_StrictAmongMultiple(t *testing.T) {
	items := []subGroupItem{
		{
			name:            "p1",
			subGroupID:      201,
			weight:          1,
			availableModels: map[string]struct{}{"shared-model": {}},
			hasModelsCache:  true,
		},
		{
			name:            "p2",
			subGroupID:      202,
			weight:          1,
			availableModels: map[string]struct{}{"shared-model": {}},
			hasModelsCache:  true,
		},
		{
			name:           "unknown",
			subGroupID:     203,
			weight:         1,
			hasModelsCache: false,
		},
	}
	sel := newTestSelector(items)

	hits := map[string]int{}
	for i := 0; i < 100; i++ {
		hits[sel.selectNextForModelExcluding("shared-model", nil)]++
	}
	if hits["unknown"] != 0 {
		t.Errorf("unknown-capability sub-group must not enter strict phase, got %d hits", hits["unknown"])
	}
	if hits["p1"] == 0 || hits["p2"] == 0 {
		t.Errorf("both p1/p2 should rotate via SWRR, hits=%+v", hits)
	}
}

// ---------- priority 分层 ----------

func TestNormalizePriority(t *testing.T) {
	if got := normalizePriority(0); got != defaultSubGroupPriority {
		t.Errorf("0 should fall back to the default, got %d", got)
	}
	if got := normalizePriority(7); got != 7 {
		t.Errorf("explicit priority must be kept, got %d", got)
	}
}

// 配了不同优先级时: 只从最优先的那一层里选, 低优先级层完全拿不到流量。
func TestSelectAmong_PriorityTierPreferred(t *testing.T) {
	items := []subGroupItem{
		{name: "secondary", subGroupID: 21, weight: 1, priority: 20},
		{name: "primary", subGroupID: 22, weight: 1, priority: 10},
	}
	sel := newTestSelector(items)

	hits := map[string]int{}
	for i := 0; i < 10; i++ {
		hits[sel.selectNextForModelExcluding("", nil)]++
	}
	if hits["primary"] != 10 {
		t.Errorf("primary tier should take all traffic, got %v", hits)
	}
	if hits["secondary"] != 0 {
		t.Errorf("secondary tier must not be used while primary is available, got %v", hits)
	}
}

// 默认(全部同优先级)必须保持原有 SWRR 行为: 按 weight 平滑轮转。
// 这条是"存量配置零影响"的守门测试。
func TestSelectAmong_UniformPriorityKeepsPlainSWRR(t *testing.T) {
	items := []subGroupItem{
		{name: "a", subGroupID: 31, weight: 1, priority: defaultSubGroupPriority},
		{name: "b", subGroupID: 32, weight: 1, priority: defaultSubGroupPriority},
	}
	sel := newTestSelector(items)

	hits := map[string]int{}
	for i := 0; i < 10; i++ {
		hits[sel.selectNextForModelExcluding("", nil)]++
	}
	if hits["a"] != 5 || hits["b"] != 5 {
		t.Errorf("equal weights must alternate evenly, got %v", hits)
	}
}

// 最优先层不可用(熔断中)时, 要降到下一层, 而不是死等。
func TestSelectAmong_FallsBackToLowerTier(t *testing.T) {
	items := []subGroupItem{
		{
			name: "primary", subGroupID: 41, weight: 1, priority: 10,
			cooldownUntil: time.Now().Add(5 * time.Minute), // 熔断中
		},
		{name: "secondary", subGroupID: 42, weight: 1, priority: 20},
	}
	sel := newTestSelector(items)

	for i := 0; i < 5; i++ {
		if got := sel.selectNextForModelExcluding("", nil); got != "secondary" {
			t.Fatalf("should fall back to the lower tier, got %q", got)
		}
	}
}

// 最优先层没有 active key 时同样要降层。
func TestSelectAmong_FallsBackWhenTierHasNoKeys(t *testing.T) {
	items := []subGroupItem{
		{name: "primary", subGroupID: 51, weight: 1, priority: 10},
		{name: "secondary", subGroupID: 52, weight: 1, priority: 20},
	}
	// 只给 secondary 预置 active key
	st := store.NewMemoryStore()
	preloadActiveKeys(st, 52)
	sel := &selector{
		groupID: 1, groupName: "test-aggregate",
		subGroups: items, store: st,
		policy: failover.DefaultCooldownPolicy(),
	}

	for i := 0; i < 5; i++ {
		if got := sel.selectNextForModelExcluding("", nil); got != "secondary" {
			t.Fatalf("should fall back when the top tier has no keys, got %q", got)
		}
	}
}
