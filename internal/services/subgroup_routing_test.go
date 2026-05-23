package services

import (
	"testing"

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

// TestSelectNextForModel_UnknownFallback 当没有任何 sub-group 严格匹配时,
// "未知能力"的 sub-group 才参与候选 (阶段 2).
func TestSelectNextForModel_UnknownFallback(t *testing.T) {
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

	// "quux" 在 zhipu 白名单不存在 → strict 阶段无候选 → 退到阶段 2 → groq 中标
	got := sel.selectNextForModelExcluding("quux", nil)
	if got != "groq" {
		t.Fatalf("expected groq (UNKNOWN fallback), got %q", got)
	}
}

// TestSelectNextForModel_FullFallback 全部 sub-group 都已知能力且都不含 model →
// 退到阶段 3 (硬碰所有), 返回非空.
func TestSelectNextForModel_FullFallback(t *testing.T) {
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
	if got != "a" && got != "b" {
		t.Fatalf("FULL fallback should pick one of {a,b}, got %q", got)
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
