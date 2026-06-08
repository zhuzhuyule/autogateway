package services

import (
	"testing"
	"time"

	"autogateway/internal/failover"
)

func newCooldownTestSelector() *selector {
	return &selector{
		groupName: "agg",
		subGroups: []subGroupItem{{name: "sub-a"}},
		policy:    failover.DefaultCooldownPolicy(),
	}
}

func (s *selector) itemByName(name string) *subGroupItem {
	for i := range s.subGroups {
		if s.subGroups[i].name == name {
			return &s.subGroups[i]
		}
	}
	return nil
}

func TestRecordResultCooldownTiering(t *testing.T) {
	now := time.Now()

	t.Run("per-day 429 长冷却且不增计数", func(t *testing.T) {
		s := newCooldownTestSelector()
		s.recordResult("sub-a", false, 429, "rate limit: requests per day exceeded", 0)
		it := s.itemByName("sub-a")
		if !it.cooldownUntil.After(now.Add(time.Hour)) {
			t.Fatalf("per-day 429 应冷却 >1h, got %v", it.cooldownUntil.Sub(now))
		}
		if it.consecutiveFailures != 0 {
			t.Fatalf("Exhausted 不应增 consecutiveFailures, got %d", it.consecutiveFailures)
		}
	})

	t.Run("per-minute 429 短冷却且不增计数", func(t *testing.T) {
		s := newCooldownTestSelector()
		s.recordResult("sub-a", false, 429, "rate limit: requests per minute", 0)
		it := s.itemByName("sub-a")
		if !it.cooldownUntil.After(now) || it.cooldownUntil.After(now.Add(5*time.Minute)) {
			t.Fatalf("per-min 429 应短冷却(<=~90s), got %v", it.cooldownUntil.Sub(now))
		}
		if it.consecutiveFailures != 0 {
			t.Fatalf("Transient 不应增 consecutiveFailures, got %d", it.consecutiveFailures)
		}
	})

	t.Run("连续 5xx 升级阶梯且计数自增", func(t *testing.T) {
		s := newCooldownTestSelector()
		s.recordResult("sub-a", false, 500, "internal error", 0)
		it := s.itemByName("sub-a")
		d1 := it.cooldownUntil.Sub(time.Now())
		if it.consecutiveFailures != 1 {
			t.Fatalf("ServerError 应增 consecutiveFailures 到 1, got %d", it.consecutiveFailures)
		}
		s.recordResult("sub-a", false, 500, "internal error", 0)
		it = s.itemByName("sub-a")
		d2 := it.cooldownUntil.Sub(time.Now())
		if d2 <= d1 {
			t.Fatalf("第二次 5xx 冷却应更长(升级), d1=%v d2=%v", d1, d2)
		}
		if it.consecutiveFailures != 2 {
			t.Fatalf("应增到 2, got %d", it.consecutiveFailures)
		}
	})

	t.Run("403 不进冷却不动状态", func(t *testing.T) {
		s := newCooldownTestSelector()
		s.recordResult("sub-a", false, 403, "invalid api key", 0)
		it := s.itemByName("sub-a")
		if !it.cooldownUntil.IsZero() {
			t.Fatal("403 不应设 cooldown")
		}
		if it.consecutiveFailures != 0 {
			t.Fatal("403 不应增 consecutiveFailures")
		}
	})

	t.Run("成功清零计数与冷却", func(t *testing.T) {
		s := newCooldownTestSelector()
		s.recordResult("sub-a", false, 500, "err", 0)
		s.recordResult("sub-a", true, 200, "", 0)
		it := s.itemByName("sub-a")
		if it.consecutiveFailures != 0 || !it.cooldownUntil.IsZero() {
			t.Fatalf("成功应清零, failures=%d cooldownZero=%v", it.consecutiveFailures, it.cooldownUntil.IsZero())
		}
	})
}
