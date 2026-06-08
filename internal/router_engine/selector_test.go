package router_engine

import (
	"context"
	"testing"
	"time"

	"autogateway/internal/failover"
	"autogateway/internal/store"
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
	s := NewSelector(nil, nil, nil)
	c := Candidate{GroupID: 99, RealModel: "test"}
	s.MarkResponse(c, 429, "", 0, 0)
	cands := []Candidate{c, {AliasID: 7, GroupID: 100, RealModel: "fresh", Weight: 1, Priority: 100}}
	alive := s.filterCooldown(cands)
	if len(alive) != 1 || alive[0].RealModel != "fresh" {
		t.Errorf("expected 'fresh' to survive cooldown filter, got %+v", alive)
	}
	s.MarkResponse(c, 200, "", 0, 0)
	alive2 := s.filterCooldown(cands)
	if len(alive2) != 2 {
		t.Errorf("expected reset after 2xx; alive count = %d", len(alive2))
	}
}

func TestCooldownBumpsOnNon2xxStatus(t *testing.T) {
	s := NewSelector(nil, nil, nil)
	c := Candidate{GroupID: 99, RealModel: "test"}
	s.MarkResponse(c, 500, "", 0, 0)
	cands := []Candidate{c, {AliasID: 7, GroupID: 100, RealModel: "fresh", Weight: 1, Priority: 100}}

	alive := s.filterCooldown(cands)
	if len(alive) != 1 || alive[0].RealModel != "fresh" {
		t.Errorf("expected non-2xx candidate to enter cooldown, got %+v", alive)
	}
}

// TestPickForAutoTierSelection exercises the token threshold logic.
func TestPickForAutoTierSelection(t *testing.T) {
	s := NewSelector(nil, nil, nil)
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
	c.apply("k", failover.ClassServerError, 0, failover.DefaultCooldownPolicy())
	now := time.Now()
	if !c.isCooling("k", now) {
		t.Fatalf("expected fresh bump to be cooling")
	}
	// Travel forward 6 minutes (past 5 min cap + jitter)
	if c.isCooling("k", now.Add(6*time.Minute)) {
		t.Fatalf("expected cooldown to expire after 6 minutes")
	}
}

func TestMarkResponseTiering(t *testing.T) {
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
	}
	c := Candidate{GroupID: 1, RealModel: "m"}
	key := "1:m"
	now := time.Now()

	// per-day 429 → 长冷却（>1h）
	s.MarkResponse(c, 429, "requests per day limit reached", 0, 0)
	if !s.cooldown.isCooling(key, now.Add(time.Hour)) {
		t.Fatal("per-day 429 should cool >1h")
	}
	// 成功 → reset
	s.MarkResponse(c, 200, "", 0, 0)
	if s.cooldown.isCooling(key, now) {
		t.Fatal("200 should reset cooldown")
	}
	// 403 → ClassNone，不冷却
	s.MarkResponse(c, 403, "invalid api key", 0, 0)
	if s.cooldown.isCooling(key, now) {
		t.Fatal("403 should not set model-level cooldown")
	}
}

func TestStickyGetSet(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		store:     st,
	}
	if got := s.GetSticky("k"); got != nil {
		t.Fatal("absent sticky should be nil")
	}
	c := &Candidate{GroupID: 3, RealModel: "m", Alias: "a", Weight: 2}
	s.SetSticky("k", c)
	got := s.GetSticky("k")
	if got == nil || got.GroupID != 3 || got.RealModel != "m" {
		t.Fatalf("GetSticky = %+v, want GroupID=3 RealModel=m", got)
	}
}

func TestStickyNilStoreNoop(t *testing.T) {
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		store:     nil,
	}
	s.SetSticky("k", &Candidate{GroupID: 1})
	if got := s.GetSticky("k"); got != nil {
		t.Fatal("nil store GetSticky should be nil")
	}
}

func TestIsCandidateAliveNil(t *testing.T) {
	s := &Selector{cooldown: newCooldownStore(), swrrState: newSWRRStateMap(), settings: DefaultSettings(), policy: failover.DefaultCooldownPolicy(), store: store.NewMemoryStore()}
	if s.IsCandidateAlive(context.Background(), nil) {
		t.Fatal("nil candidate should not be alive")
	}
}

func TestRecordStat(t *testing.T) {
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		stats:     make(map[string]*candidateStat),
	}
	c := Candidate{GroupID: 1, RealModel: "m"}
	s.recordStat(c, true, 1000)
	s.recordStat(c, true, 2000)
	s.recordStat(c, false, 0)
	st := s.stats["1:m"]
	if st == nil || st.success != 2 || st.fail != 1 {
		t.Fatalf("stat = %+v, want success=2 fail=1", st)
	}
	if st.latencyEWMA <= 0 {
		t.Fatal("latencyEWMA should be set after success")
	}
}

func TestSwrrFavorsReliable(t *testing.T) {
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		stats:     make(map[string]*candidateStat),
	}
	good := Candidate{GroupID: 1, RealModel: "good", Weight: 1, AliasID: 1}
	bad := Candidate{GroupID: 2, RealModel: "bad", Weight: 1, AliasID: 2}
	for i := 0; i < 50; i++ {
		s.recordStat(good, true, 500*time.Millisecond)
		s.recordStat(bad, false, 0)
	}
	cands := []Candidate{good, bad}
	goodCount := 0
	for i := 0; i < 200; i++ {
		if s.swrr("k", cands).RealModel == "good" {
			goodCount++
		}
	}
	if goodCount < 120 {
		t.Fatalf("good picked %d/200, want >120 (favor reliable)", goodCount)
	}
	if goodCount == 200 {
		t.Fatal("bad should still be explored occasionally (Thompson)")
	}
}

func TestMarkResponseRecordsStat(t *testing.T) {
	s := &Selector{cooldown: newCooldownStore(), swrrState: newSWRRStateMap(), settings: DefaultSettings(), policy: failover.DefaultCooldownPolicy(), stats: make(map[string]*candidateStat)}
	c := Candidate{GroupID: 1, RealModel: "m"}
	s.MarkResponse(c, 200, "", 0, 800*time.Millisecond)
	if s.stats["1:m"] == nil || s.stats["1:m"].success != 1 {
		t.Fatal("MarkResponse(200) should record success")
	}
	s.MarkResponse(c, 500, "err", 0, 0)
	if s.stats["1:m"].fail != 1 {
		t.Fatal("MarkResponse(500) should record fail")
	}
}
