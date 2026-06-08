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
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"autogateway/internal/failover"
	"autogateway/internal/models"
	"autogateway/internal/store"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Settings 持久化在 system_settings 表里的 key, 用前缀避开 SystemSettingsManager
// 的 typed-config 校验 (它的 ValidateSettings 只认 types.SystemSettings 已声明的字段,
// 不会触碰这些前缀的 key).
const (
	settingKeyRoutingEnabled  = "routing_enabled"
	settingKeyRoutingSimple   = "routing_simple_threshold"
	settingKeyRoutingComplex  = "routing_complex_threshold"
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
// 命名直接用 tier 名 (simple/medium/complex), 比 "auto-simple" 等更紧凑;
// 智能路由仍由 model == "auto" 关键字触发, 这里只是 reserved alias 池的名字。
func ReservedAlias(t Tier) string { return string(t) }

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
	policy    failover.CooldownPolicy
	store     store.Store
	mu        sync.RWMutex
	meta      MetaProvider
	stats     map[string]*candidateStat
	statsMu   sync.Mutex
}

func NewSelector(db *gorm.DB, st store.Store, meta MetaProvider) *Selector {
	s := &Selector{
		db:        db,
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		policy:    failover.DefaultCooldownPolicy(),
		store:     st,
		meta:      meta,
		stats:     make(map[string]*candidateStat),
	}
	s.loadSettingsFromDB()
	return s
}

// candidateStat holds rolling success/failure counters and latency EWMA
// for a single (groupID, realModel) candidate.
type candidateStat struct {
	success     int64
	fail        int64
	latencyEWMA float64 // ms, 0 = no sample yet
}

// statRollThreshold — when success+fail exceeds this, both counters are
// halved so recent observations dominate (approximate exponential decay).
const statRollThreshold = 200

func (s *Selector) statKey(c Candidate) string {
	return fmt.Sprintf("%d:%s", c.GroupID, c.RealModel)
}

// recordStat records one outcome for a candidate.
// latency is only used when success=true; pass 0 for failures.
func (s *Selector) recordStat(c Candidate, success bool, latency time.Duration) {
	if c.GroupID == 0 || c.RealModel == "" {
		return
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.stats == nil {
		s.stats = make(map[string]*candidateStat)
	}
	k := s.statKey(c)
	st := s.stats[k]
	if st == nil {
		st = &candidateStat{}
		s.stats[k] = st
	}
	if success {
		st.success++
		ms := float64(latency) / 1e6 // ns → ms (preserves sub-millisecond values)
		if ms > 0 {
			if st.latencyEWMA == 0 {
				st.latencyEWMA = ms
			} else {
				st.latencyEWMA = 0.3*ms + 0.7*st.latencyEWMA
			}
		}
	} else {
		st.fail++
	}
	if st.success+st.fail > statRollThreshold {
		st.success /= 2
		st.fail /= 2
	}
}

// effWeightScale is the integer multiplier applied inside sampleEffectiveWeight
// so that fractional Thompson/speed scores survive int conversion.
// A scale of 100 gives ~1% granularity; with base Weight=1 the effective range
// is [1, 100] per candidate, preserving meaningful differences for SWRR.
const effWeightScale = 100

// sampleEffectiveWeight calculates the effective weight for a candidate:
// c.Weight × priorWeight × Thompson(Beta) × speedFactor × effWeightScale. Minimum 1.
// When there is no recorded stat history (success+fail == 0), Thompson sampling
// is skipped and the static weight is used directly (× effWeightScale), preserving
// deterministic SWRR behaviour until real observations arrive.
func (s *Selector) sampleEffectiveWeight(c Candidate) int {
	prior := 1.0
	if s.meta != nil {
		prior = priorWeight(s.meta.LookupMeta(c.RealModel))
	}
	s.statsMu.Lock()
	st := s.stats[s.statKey(c)]
	var succ, fail, ewma float64
	if st != nil {
		succ, fail, ewma = float64(st.success), float64(st.fail), st.latencyEWMA
	}
	s.statsMu.Unlock()
	// No history yet: use static weight scaled up; avoids Beta(1,1) noise
	// polluting SWRR before any real observation arrives.
	if succ == 0 && fail == 0 {
		iw := int(float64(c.Weight)*prior*effWeightScale + 0.5)
		if iw < 1 {
			return 1
		}
		return iw
	}
	w := float64(c.Weight) * prior * sampleBeta(succ+1, fail+1) * speedFactor(ewma) * effWeightScale
	iw := int(w + 0.5)
	if iw < 1 {
		return 1
	}
	return iw
}

// StickyTTL 粘性会话锁定时长。
const StickyTTL = 30 * time.Minute

// GetSticky 读取并反序列化粘性 candidate；不存在/解析失败/store 为 nil → nil。
func (s *Selector) GetSticky(storeKey string) *Candidate {
	if s.store == nil {
		return nil
	}
	b, err := s.store.Get(storeKey)
	if err != nil || len(b) == 0 {
		return nil
	}
	var c Candidate
	if err := json.Unmarshal(b, &c); err != nil {
		return nil
	}
	return &c
}

// SetSticky 序列化并写入粘性 candidate，TTL StickyTTL；store 为 nil/序列化失败 → no-op。
func (s *Selector) SetSticky(storeKey string, c *Candidate) {
	if s.store == nil || c == nil {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := s.store.Set(storeKey, b, StickyTTL); err != nil {
		logrus.WithError(err).Debug("sticky SetSticky failed")
	}
}

// IsCandidateAlive 校验 candidate 当前是否仍可用：有 active key、不在 cooldown、未被 exposed/blocked 拦。
func (s *Selector) IsCandidateAlive(ctx context.Context, c *Candidate) bool {
	if c == nil {
		return false
	}
	cands := []Candidate{*c}
	if cands = s.filterByActiveKeys(ctx, cands); len(cands) == 0 {
		return false
	}
	if cands = s.filterByExposed(ctx, cands); len(cands) == 0 {
		return false
	}
	if cands = s.filterCooldown(cands); len(cands) == 0 {
		return false
	}
	return true
}

func (s *Selector) UpdateSettings(cfg Settings) {
	s.mu.Lock()
	s.settings = cfg
	s.mu.Unlock()
	s.persistSettings(cfg)
}

// loadSettingsFromDB 启动时从 system_settings 表读取持久化的路由阈值, 缺失则保留默认值.
func (s *Selector) loadSettingsFromDB() {
	if s.db == nil {
		return
	}
	var rows []models.SystemSetting
	if err := s.db.Where("setting_key IN ?", []string{
		settingKeyRoutingEnabled, settingKeyRoutingSimple, settingKeyRoutingComplex,
	}).Find(&rows).Error; err != nil {
		logrus.WithError(err).Warn("failed to load routing settings from db, using defaults")
		return
	}
	cfg := s.settings
	for _, r := range rows {
		switch r.SettingKey {
		case settingKeyRoutingEnabled:
			cfg.Enabled = r.SettingValue == "true"
		case settingKeyRoutingSimple:
			if v, err := strconv.Atoi(r.SettingValue); err == nil && v > 0 {
				cfg.SimpleThreshold = v
			}
		case settingKeyRoutingComplex:
			if v, err := strconv.Atoi(r.SettingValue); err == nil && v > 0 {
				cfg.ComplexThreshold = v
			}
		}
	}
	if cfg.SimpleThreshold >= cfg.ComplexThreshold {
		cfg.ComplexThreshold = cfg.SimpleThreshold * 2
	}
	s.mu.Lock()
	s.settings = cfg
	s.mu.Unlock()
}

// persistSettings 把当前阈值 upsert 进 system_settings 表, 重启后 loadSettingsFromDB 取回.
func (s *Selector) persistSettings(cfg Settings) {
	if s.db == nil {
		return
	}
	rows := []models.SystemSetting{
		{SettingKey: settingKeyRoutingEnabled, SettingValue: strconv.FormatBool(cfg.Enabled)},
		{SettingKey: settingKeyRoutingSimple, SettingValue: strconv.Itoa(cfg.SimpleThreshold)},
		{SettingKey: settingKeyRoutingComplex, SettingValue: strconv.Itoa(cfg.ComplexThreshold)},
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "setting_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"setting_value", "updated_at"}),
	}).Create(&rows).Error; err != nil {
		logrus.WithError(err).Warn("failed to persist routing settings")
	}
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
	cands = s.filterByActiveKeys(ctx, cands)
	if len(cands) == 0 {
		return nil, fmt.Errorf("alias %q has no candidates with active keys", alias)
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
	cands = s.filterByActiveKeys(ctx, cands)
	if len(cands) == 0 {
		return nil, fmt.Errorf("model %q has no group with active keys", model)
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

// MarkResponse 把上游响应反馈进冷却。parsedError 为上游错误文本（用于分类），
// retryAfter 为 Retry-After 头解析值（0 表示无）。2xx/3xx 清除冷却。
// latency 为本次请求耗时，用于 Thompson 采样权重的速度因子。
func (s *Selector) MarkResponse(c Candidate, status int, parsedError string, retryAfter time.Duration, latency time.Duration) {
	if c.GroupID == 0 || c.RealModel == "" {
		return
	}
	key := s.statKey(c)
	if status >= 200 && status < 400 {
		s.cooldown.reset(key)
		s.recordStat(c, true, latency)
		return
	}
	class := failover.Classify(status, parsedError)
	s.cooldown.apply(key, class, retryAfter, s.policy)
	s.recordStat(c, false, 0)
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

// filterByExposed removes candidates whose group rejects the model:
//   - blocked_models 命中(无视 mode) → 拒绝
//   - specified 模式 + 不在 exposed_models → 拒绝
//   - passthrough 模式 → 仅黑名单生效
//
// One DB roundtrip looks up mode + exposed_models + blocked_models for the
// unique group IDs. On query error we fall back to passing candidates through
// (fail open), since gating routing is a UX guardrail not a security boundary.
func (s *Selector) filterByExposed(ctx context.Context, cands []Candidate) []Candidate {
	if len(cands) == 0 || s.db == nil {
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
		ID               uint
		ModelRoutingMode string
		ExposedModels    string
		BlockedModels    string
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Table("groups").
		Select("id, model_routing_mode, exposed_models, blocked_models").
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
		// 黑名单优先 — 命中即拒绝,无视 mode
		if jsonContainsString(r.BlockedModels, c.RealModel) {
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

func (s *Selector) filterByActiveKeys(ctx context.Context, cands []Candidate) []Candidate {
	if len(cands) == 0 || s.db == nil {
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
		GroupID uint
		Count   int64
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Table("api_keys").
		Select("group_id, count(*) as count").
		Where("group_id IN ? AND status = ?", ids, models.KeyStatusActive).
		Group("group_id").
		Scan(&rows).Error; err != nil {
		return cands
	}

	active := make(map[uint]bool, len(rows))
	for _, r := range rows {
		if r.Count > 0 {
			active[r.GroupID] = true
		}
	}

	out := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if active[c.GroupID] {
			out = append(out, c)
		}
	}
	return out
}

func (s *Selector) filterCooldown(cands []Candidate) []Candidate {
	now := time.Now()
	alive := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if !s.cooldown.isCooling(s.statKey(c), now) {
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

	eff := make([]int, len(sorted))
	total := 0
	for i := range sorted {
		eff[i] = s.sampleEffectiveWeight(sorted[i])
		total += eff[i]
	}
	if total <= 0 {
		// Defensive: should not happen since sampleEffectiveWeight returns minimum 1.
		total = len(sorted)
		for i := range eff {
			eff[i] = 1
		}
	}

	bestIdx := -1
	bestVal := 0
	for i := range sorted {
		state[i] += eff[i]
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

// apply 按冷却性质设置冷却窗口；仅 ServerError 用 failures 做升级。
// dur<=0（ClassNone）不设置冷却。
func (c *cooldownStore) apply(key string, class failover.CooldownClass, retryAfter time.Duration, policy failover.CooldownPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.data[key]
	dur, counts := policy.Decide(class, retryAfter, e.failures)
	if dur <= 0 {
		return
	}
	if counts {
		e.failures++
	}
	jit := time.Duration(rand.Int63n(int64(dur)/5 + 1))
	e.until = time.Now().Add(dur + jit)
	c.data[key] = e
}

func (c *cooldownStore) reset(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, key)
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
