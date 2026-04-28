// Package router_engine implements the Model Routing rewrite (§13).
//
// Decision tree:
//
//	model == "auto"          → estimate tokens → tier (simple/medium/complex)
//	                            → resolve auto-{tier} alias → SWRR pick
//	model in model_aliases   → resolve alias → SWRR pick
//	otherwise                → cross-group exact-name lookup → SWRR pick
//
// Errors / 429 responses feed back into a cooldown store keyed by
// (group_id, real_model) with exponential backoff, so a saturated
// upstream stops draining the pool.
package router_engine

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

// Tier names (sync with internal/autoroute, kept independent for the
// rewrite's clean cut).
type Tier string

const (
	TierSimple  Tier = "simple"
	TierMedium  Tier = "medium"
	TierComplex Tier = "complex"
)

// ReservedAlias maps a tier to its reserved alias name.
func ReservedAlias(t Tier) string { return "auto-" + string(t) }

// Candidate is one (group, model) destination eligible for routing.
type Candidate struct {
	AliasID   uint
	Alias     string
	GroupID   uint
	RealModel string
	Weight    int
	Priority  int
}

// Settings controls smart-routing thresholds (token-based tier picking).
// In Phase 1 we keep them as a tiny in-memory struct seeded from env or
// system_settings; future work can move them to a dedicated routing_settings
// table if needed.
type Settings struct {
	Enabled          bool
	SimpleThreshold  int // tokens < this → simple
	ComplexThreshold int // tokens >= this → complex
}

func DefaultSettings() Settings {
	return Settings{
		Enabled:          true,
		SimpleThreshold:  2000,
		ComplexThreshold: 8000,
	}
}

// Selector picks the next destination for an alias using SWRR + cooldown.
type Selector struct {
	db        *gorm.DB
	cooldown  *cooldownStore
	swrrState *swrrStateMap
	settings  Settings
	mu        sync.RWMutex
}

func NewSelector(db *gorm.DB) *Selector {
	return &Selector{
		db:        db,
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
	}
}

func (s *Selector) UpdateSettings(cfg Settings) {
	s.mu.Lock()
	s.settings = cfg
	s.mu.Unlock()
}

func (s *Selector) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// PickByAlias returns the SWRR-selected (group, real_model) for an alias.
//
// Candidates are filtered against each candidate group's exposed_models when
// that group is in "specified" routing mode, so an alias whose target lives
// in a non-exposed model silently falls through (per design — alias 兜底
// 但仍受 exposed_models 准入).
func (s *Selector) PickByAlias(ctx context.Context, alias string) (*Candidate, error) {
	cands, err := s.loadCandidates(ctx, alias)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("no candidates for alias %q", alias)
	}
	cands = s.filterByExposed(ctx, cands)
	if len(cands) == 0 {
		return nil, fmt.Errorf("alias %q has no exposed candidates", alias)
	}
	alive := s.filterCooldown(cands)
	if len(alive) == 0 {
		// Everyone is on cooldown — release and try again so we never
		// black-hole an alias just because every model temporarily 429'd.
		alive = cands
	}
	return s.swrr(alias, alive), nil
}

// PickByExactName falls back when the user's model name is not aliased.
// Walks Group.AvailableModels JSON across all groups for a literal match.
//
// In specified mode, candidates are further filtered by the group's
// exposed_models (an upstream model that the admin hasn't exposed must not
// be reachable cross-group via this fallback).
func (s *Selector) PickByExactName(ctx context.Context, model string) (*Candidate, error) {
	type row struct {
		GroupID         uint
		AvailableModels string
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Table("groups").
		Select("id as group_id, available_models").
		Where("group_type = ? AND available_models IS NOT NULL AND available_models <> ''", "standard").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	cands := make([]Candidate, 0)
	for _, r := range rows {
		if !jsonContainsString(r.AvailableModels, model) {
			continue
		}
		cands = append(cands, Candidate{
			GroupID:   r.GroupID,
			RealModel: model,
			Weight:    1,
			Priority:  100,
		})
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("no group exposes model %q", model)
	}
	cands = s.filterByExposed(ctx, cands)
	if len(cands) == 0 {
		return nil, fmt.Errorf("model %q has no exposed group", model)
	}
	alive := s.filterCooldown(cands)
	if len(alive) == 0 {
		alive = cands
	}
	return s.swrr("__exact_"+model, alive), nil
}

// PickForAuto resolves the smart-routing pool by token estimate.
func (s *Selector) PickForAuto(ctx context.Context, estimatedTokens int) (*Candidate, error) {
	cfg := s.GetSettings()
	tier := TierMedium
	if estimatedTokens < cfg.SimpleThreshold {
		tier = TierSimple
	} else if estimatedTokens >= cfg.ComplexThreshold {
		tier = TierComplex
	}
	return s.PickByAlias(ctx, ReservedAlias(tier))
}

// MarkResponse feeds an upstream HTTP status back into the cooldown
// machinery. 429 starts/extends a backoff window; anything in 2xx clears
// the failure streak for that (group, model).
func (s *Selector) MarkResponse(c Candidate, status int) {
	if c.GroupID == 0 || c.RealModel == "" {
		return
	}
	key := fmt.Sprintf("%d:%s", c.GroupID, c.RealModel)
	if status == 429 {
		s.cooldown.bump(key)
	} else if status >= 200 && status < 400 {
		s.cooldown.reset(key)
	}
}

// ----- helpers -----

func (s *Selector) loadCandidates(ctx context.Context, alias string) ([]Candidate, error) {
	var rows []models.ModelAlias
	if err := s.db.WithContext(ctx).
		Where("alias = ? AND enabled = ? AND group_id <> 0", alias, true).
		Order("weight desc, priority asc, id asc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, Candidate{
			AliasID:   r.ID,
			Alias:     r.Alias,
			GroupID:   r.GroupID,
			RealModel: r.RealModel,
			Weight:    r.Weight,
			Priority:  r.Priority,
		})
	}
	return out, nil
}

// filterByExposed removes candidates whose group is in "specified" routing
// mode and whose RealModel is NOT in that group's exposed_models JSON.
// passthrough mode (or missing/empty mode) lets all candidates through.
//
// One DB roundtrip looks up mode + exposed_models for the unique group IDs.
// On query error we fall back to passing candidates through (fail open),
// since gating routing by exposure is meant to be a UX guardrail not a
// security boundary — proxy_keys / auth still gate access.
func (s *Selector) filterByExposed(ctx context.Context, cands []Candidate) []Candidate {
	if len(cands) == 0 {
		return cands
	}
	idSet := make(map[uint]struct{}, len(cands))
	for _, c := range cands {
		idSet[c.GroupID] = struct{}{}
	}
	ids := make([]uint, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	type row struct {
		ID                uint
		ModelRoutingMode  string
		ExposedModels     string
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Table("groups").
		Select("id, model_routing_mode, exposed_models").
		Where("id IN ?", ids).
		Scan(&rows).Error; err != nil {
		return cands
	}
	info := make(map[uint]row, len(rows))
	for _, r := range rows {
		info[r.ID] = r
	}
	out := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		r, ok := info[c.GroupID]
		if !ok {
			continue
		}
		if r.ModelRoutingMode != "specified" {
			out = append(out, c)
			continue
		}
		if jsonContainsString(r.ExposedModels, c.RealModel) {
			out = append(out, c)
		}
	}
	return out
}

func (s *Selector) filterCooldown(cands []Candidate) []Candidate {
	now := time.Now()
	alive := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		key := fmt.Sprintf("%d:%s", c.GroupID, c.RealModel)
		if !s.cooldown.isCooling(key, now) {
			alive = append(alive, c)
		}
	}
	return alive
}

// swrr picks a candidate using Smooth Weighted Round Robin. Same algorithm
// as nginx's upstream module: maintains a per-alias "current_weight" array,
// each pick adds Weight and selects the max, then subtracts totalWeight.
//
// When weights are equal, ties break by Priority (lower wins) then by ID
// (stable insertion order), which matches the user's spec: "权重相同时
// 按先后顺序排序".
func (s *Selector) swrr(key string, cands []Candidate) *Candidate {
	if len(cands) == 1 {
		c := cands[0]
		return &c
	}
	state := s.swrrState.get(key, len(cands))

	// Re-sort cands stable by (Weight desc, Priority asc, AliasID asc) so
	// equal-weight ordering is deterministic. The state array is index-aligned
	// to this sorted view.
	sorted := append([]Candidate(nil), cands...)
	stableSort(sorted)

	if len(state) != len(sorted) {
		// Pool size changed since last call: reset state.
		state = make([]int, len(sorted))
		s.swrrState.set(key, state)
	}

	total := 0
	for _, c := range sorted {
		total += c.Weight
	}
	if total <= 0 {
		// Defensive: pick first.
		c := sorted[0]
		return &c
	}

	bestIdx := -1
	bestVal := 0
	for i, c := range sorted {
		state[i] += c.Weight
		if bestIdx == -1 || state[i] > bestVal ||
			(state[i] == bestVal && sorted[i].Priority < sorted[bestIdx].Priority) {
			bestIdx = i
			bestVal = state[i]
		}
	}
	state[bestIdx] -= total
	s.swrrState.set(key, state)

	c := sorted[bestIdx]
	return &c
}

func stableSort(in []Candidate) {
	// Simple insertion sort — n is small (typically ≤10).
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && less(in[j], in[j-1]); j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

func less(a, b Candidate) bool {
	if a.Weight != b.Weight {
		return a.Weight > b.Weight
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	return a.AliasID < b.AliasID
}

// jsonContainsString — cheap match for `available_models` text without
// parsing the full array. Strings inside the JSON are quoted, so we
// search for "model". This is the same trick used by other places that
// peek at AvailableModels.
func jsonContainsString(haystack, needle string) bool {
	q := `"` + needle + `"`
	return contains(haystack, q)
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	// avoid pulling strings.Contains; keep selector self-contained.
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ===== cooldown store =====

type cooldownEntry struct {
	until    time.Time
	failures int
}

type cooldownStore struct {
	mu   sync.RWMutex
	data map[string]cooldownEntry
}

func newCooldownStore() *cooldownStore {
	return &cooldownStore{data: make(map[string]cooldownEntry)}
}

func (c *cooldownStore) isCooling(key string, now time.Time) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.data[key]
	if !ok {
		return false
	}
	return now.Before(e.until)
}

// bump pushes an existing entry's cooldown out exponentially: 60s, 120s,
// 240s, 480s, capped at 5 minutes.
func (c *cooldownStore) bump(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.data[key]
	e.failures++
	wait := time.Duration(60<<min(e.failures-1, 4)) * time.Second
	if wait > 5*time.Minute {
		wait = 5 * time.Minute
	}
	// Add ±10% jitter so we don't bunch up retries from many goroutines.
	jit := time.Duration(rand.Int63n(int64(wait) / 5))
	e.until = time.Now().Add(wait + jit)
	c.data[key] = e
}

func (c *cooldownStore) reset(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, key)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ===== SWRR per-alias state =====

type swrrStateMap struct {
	mu   sync.RWMutex
	data map[string][]int
}

func newSWRRStateMap() *swrrStateMap {
	return &swrrStateMap{data: make(map[string][]int)}
}

func (m *swrrStateMap) get(key string, size int) []int {
	m.mu.RLock()
	v, ok := m.data[key]
	m.mu.RUnlock()
	if ok && len(v) == size {
		return append([]int(nil), v...)
	}
	return make([]int, size)
}

func (m *swrrStateMap) set(key string, state []int) {
	m.mu.Lock()
	m.data[key] = append([]int(nil), state...)
	m.mu.Unlock()
}
