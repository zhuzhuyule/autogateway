package keypool

import (
	"fmt"
	"testing"
	"time"

	"autogateway/internal/ratelimit"
	"autogateway/internal/store"
)

// 冷却中的 key 应被跳过, SelectKey 只返回未冷却的那把。
func TestSelectKey_CooldownSkipsKey(t *testing.T) {
	s := store.NewMemoryStore()
	enc := newNoopEncryption(t)
	const groupID uint = 11
	seedKey(t, s, groupID, 40, "sk-key-40")
	seedKey(t, s, groupID, 41, "sk-key-41")

	p := newTestProvider(s, enc, nil)
	p.CoolDownKey(41, time.Minute) // 冷却 41

	// 连选多次都不应选到冷却中的 41。
	for i := 0; i < 6; i++ {
		apiKey, err := p.SelectKey(groupID, ratelimit.Limits{})
		if err != nil {
			t.Fatalf("SelectKey: %v", err)
		}
		if apiKey.ID == 41 {
			t.Fatalf("iter %d: selected cooled key 41, expected only 40", i)
		}
		if apiKey.ID != 40 {
			t.Fatalf("iter %d: expected key 40, got %d", i, apiKey.ID)
		}
	}
}

// 整池都在冷却时, SelectKey 应 fallback 退回用冷却 key(而不是误报无可用 key),
// 让请求仍能打到上游拿真实错误。
func TestSelectKey_AllCooledFallsBack(t *testing.T) {
	s := store.NewMemoryStore()
	enc := newNoopEncryption(t)
	const groupID uint = 12
	seedKey(t, s, groupID, 50, "sk-key-50")

	p := newTestProvider(s, enc, nil)
	p.CoolDownKey(50, time.Minute)

	apiKey, err := p.SelectKey(groupID, ratelimit.Limits{})
	if err != nil {
		t.Fatalf("expected fallback to cooled key, got error: %v", err)
	}
	if apiKey.ID != 50 {
		t.Fatalf("expected fallback key 50, got %d", apiKey.ID)
	}
}

// 两把都冷却也应 fallback(取扫描到的第一把), 不返回 NoActiveKeys。
func TestSelectKey_MultipleAllCooledFallsBack(t *testing.T) {
	s := store.NewMemoryStore()
	enc := newNoopEncryption(t)
	const groupID uint = 13
	seedKey(t, s, groupID, 60, "sk-key-60")
	seedKey(t, s, groupID, 61, "sk-key-61")

	p := newTestProvider(s, enc, nil)
	p.CoolDownKey(60, time.Minute)
	p.CoolDownKey(61, time.Minute)

	apiKey, err := p.SelectKey(groupID, ratelimit.Limits{})
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	if apiKey.ID != 60 && apiKey.ID != 61 {
		t.Fatalf("expected fallback to 60 or 61, got %d", apiKey.ID)
	}
}

// CoolDownKey 设标记; dur<=0 不设。
func TestCoolDownKey(t *testing.T) {
	s := store.NewMemoryStore()
	p := newTestProvider(s, newNoopEncryption(t), nil)

	p.CoolDownKey(70, time.Minute)
	if ok, _ := s.Exists(fmt.Sprintf("key:%d:cooldown", 70)); !ok {
		t.Error("expected cooldown marker for key 70")
	}

	p.CoolDownKey(71, 0) // 非正时长, 不应设
	if ok, _ := s.Exists(fmt.Sprintf("key:%d:cooldown", 71)); ok {
		t.Error("dur<=0 should not set a cooldown marker")
	}
}
