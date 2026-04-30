package router_engine

import (
	"testing"
	"time"
)

// TestSWRRDistribution verifies that smooth weighted round-robin honors
// the weight ratios over a sample of picks. With weights 3:1 we expect
// roughly a 3:1 split (we allow ±10% slack since SWRR is deterministic
// per-call but interleaves selections).
func TestSWRRDistribution(t *testing.T) {
	s := &Selector{swrrState: newSWRRStateMap(), cooldown: newCooldownStore(), settings: DefaultSettings()}
	cands := []Candidate{
		{AliasID: 1, GroupID: 10, RealModel: "model-a", Weight: 3, Priority: 100},
		{AliasID: 2, GroupID: 11, RealModel: "model-b", Weight: 1, Priority: 100},
	}
	picks := map[string]int{}
	for i := 0; i < 100; i++ {
		got := s.swrr("test", cands)
		picks[got.RealModel]++
	}
	if picks["model-a"] < 70 || picks["model-a"] > 80 {
		t.Errorf("expected ~75 picks for weight-3 candidate, got %d", picks["model-a"])
	}
	if picks["model-b"] < 20 || picks["model-b"] > 30 {
		t.Errorf("expected ~25 picks for weight-1 candidate, got %d", picks["model-b"])
	}
}

// TestSWRREqualWeightOrder verifies that equal-weight candidates pick in
// priority order on the first round (the user's spec: "权重相同时按先后
// 顺序排序").
func TestSWRREqualWeightOrder(t *testing.T) {
	s := &Selector{swrrState: newSWRRStateMap(), cooldown: newCooldownStore(), settings: DefaultSettings()}
	cands := []Candidate{
		{AliasID: 1, GroupID: 10, RealModel: "low-pri", Weight: 1, Priority: 200},
		{AliasID: 2, GroupID: 11, RealModel: "high-pri", Weight: 1, Priority: 50},
	}
	first := s.swrr("equal", cands)
	if first.RealModel != "high-pri" {
		t.Errorf("expected priority 50 to win the tie, got %s", first.RealModel)
	}
}

// TestCooldownBumpAndReset confirms 429 starts the cooldown and a 2xx
// clears the failure streak.
func TestCooldownBumpAndReset(t *testing.T) {
	s := NewSelector(nil)
	c := Candidate{GroupID: 99, RealModel: "test"}
	s.MarkResponse(c, 429)
	cands := []Candidate{c, {AliasID: 7, GroupID: 100, RealModel: "fresh", Weight: 1, Priority: 100}}
	alive := s.filterCooldown(cands)
	if len(alive) != 1 || alive[0].RealModel != "fresh" {
		t.Errorf("expected 'fresh' to survive cooldown filter, got %+v", alive)
	}
	s.MarkResponse(c, 200)
	alive2 := s.filterCooldown(cands)
	if len(alive2) != 2 {
		t.Errorf("expected reset after 2xx; alive count = %d", len(alive2))
	}
}

func TestCooldownBumpsOnNon2xxStatus(t *testing.T) {
	s := NewSelector(nil)
	c := Candidate{GroupID: 99, RealModel: "test"}
	s.MarkResponse(c, 500)
	cands := []Candidate{c, {AliasID: 7, GroupID: 100, RealModel: "fresh", Weight: 1, Priority: 100}}

	alive := s.filterCooldown(cands)
	if len(alive) != 1 || alive[0].RealModel != "fresh" {
		t.Errorf("expected non-2xx candidate to enter cooldown, got %+v", alive)
	}
}

// TestPickForAutoTierSelection exercises the token threshold logic.
func TestPickForAutoTierSelection(t *testing.T) {
	s := NewSelector(nil)
	cfg := Settings{Enabled: true, SimpleThreshold: 2000, ComplexThreshold: 8000}
	s.UpdateSettings(cfg)
	cases := []struct {
		tokens int
		alias  string
	}{
		{500, "simple"},
		{2000, "medium"}, // boundary: not < 2000 → medium
		{5000, "medium"},
		{8000, "complex"}, // boundary: >= 8000 → complex
		{20000, "complex"},
	}
	for _, tc := range cases {
		// We can't actually call PickForAuto without a DB; just verify
		// the tier→alias mapping the function uses internally is what
		// PickByAlias would receive.
		want := tc.alias
		got := tierAliasFor(s, tc.tokens)
		if got != want {
			t.Errorf("tokens=%d expected %s, got %s", tc.tokens, want, got)
		}
	}
}

// tierAliasFor mirrors the tier-selection logic in PickForAuto so we can
// unit-test it without a database.
func tierAliasFor(s *Selector, tokens int) string {
	cfg := s.GetSettings()
	tier := TierMedium
	if tokens < cfg.SimpleThreshold {
		tier = TierSimple
	} else if tokens >= cfg.ComplexThreshold {
		tier = TierComplex
	}
	return ReservedAlias(tier)
}

// TestCooldownExpires checks that an entry naturally falls out of
// cooldown after the configured wait elapses.
func TestCooldownExpires(t *testing.T) {
	c := newCooldownStore()
	c.bump("k")
	now := time.Now()
	if !c.isCooling("k", now) {
		t.Fatalf("expected fresh bump to be cooling")
	}
	// Travel forward 6 minutes (past 5 min cap + jitter)
	if c.isCooling("k", now.Add(6*time.Minute)) {
		t.Fatalf("expected cooldown to expire after 6 minutes")
	}
}
