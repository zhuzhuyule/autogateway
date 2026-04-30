package services

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

// AliasSuggestion is one suggestion row. `Kind` discriminates the payload:
//   - "single": one unrecognized model — legacy shape, fields Model/Count/LastSeen.
//   - "family": several unrecognized models share a `model_family` (looked up
//     in FreeModelsRegistry). UI offers a one-click action to create a single
//     alias whose targets are the in-group siblings of that family.
//
// Nesting/sub-fields are explicitly `omitempty` so the wire shape stays
// compact for either kind. The frontend switches on Kind.
type AliasSuggestion struct {
	Kind     string    `json:"kind"`
	Model    string    `json:"model,omitempty"`
	Count    int64     `json:"count,omitempty"`
	LastSeen time.Time `json:"last_seen,omitempty"`

	// family-only
	Family        string        `json:"family,omitempty"`
	Models        []FamilyModel `json:"models,omitempty"`
	ExistingAlias string        `json:"existing_alias,omitempty"`
}

// FamilyModel describes one model under a family bucket — either a recently
// observed unrecognized client model (FromLogs=true) or a sibling already
// hosted by one of the user's groups (FromLogs=false).
type FamilyModel struct {
	Name       string     `json:"name"`
	Count      int64      `json:"count,omitempty"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`
	FromLogs   bool       `json:"from_logs"`
	InGroupIDs []uint     `json:"in_group_ids,omitempty"`
}

// AliasSuggestionService scans request_logs for models the gateway didn't
// know and weren't already aliased, then optionally enriches with
// model_family info to recommend bulk-alias actions.
type AliasSuggestionService struct {
	db       *gorm.DB
	registry *FreeModelsRegistry // optional; nil → service emits legacy single-only suggestions
}

// NewAliasSuggestionService constructs the service. `registry` may be nil
// (tests, or before registry has finished its first fetch) — when nil the
// service degrades to legacy single-suggestion behavior.
func NewAliasSuggestionService(db *gorm.DB, registry *FreeModelsRegistry) *AliasSuggestionService {
	return &AliasSuggestionService{db: db, registry: registry}
}

// minFamilyDistinct / minFamilyHits are the thresholds before a family
// bucket is promoted to a `kind=family` suggestion. Below either threshold
// each model is emitted as its own `kind=single` row, preserving today's UX.
const (
	minFamilyDistinct = 2
	minFamilyHits     = 3
)

// Suggest returns up to ~20 unmatched models, optionally bucketed by
// model_family. Ordering: family suggestions first (by total hits desc),
// then leftover single suggestions (by hits desc).
func (s *AliasSuggestionService) Suggest(ctx context.Context, lookback time.Duration) ([]AliasSuggestion, error) {
	since := time.Now().Add(-lookback)
	type row struct {
		Model    string
		Count    int64
		LastSeen string
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Model(&models.RequestLog{}).
		Select("model, COUNT(*) as count, MAX(timestamp) as last_seen").
		Where("status_code IN (404, 405) AND is_success = ? AND model <> '' AND timestamp >= ?", false, since).
		Group("model").
		Order("count DESC").
		Limit(20).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return nil, nil
	}

	candidates := make([]string, 0, len(rows))
	for _, r := range rows {
		candidates = append(candidates, r.Model)
	}
	var aliased []string
	if err := s.db.WithContext(ctx).
		Model(&models.ModelAlias{}).
		Where("alias IN ? AND enabled = ?", candidates, true).
		Distinct("alias").
		Pluck("alias", &aliased).Error; err != nil {
		return nil, err
	}
	skip := make(map[string]bool, len(aliased))
	for _, a := range aliased {
		skip[a] = true
	}

	type seedEntry struct {
		model    string
		count    int64
		lastSeen time.Time
	}
	seeds := make([]seedEntry, 0, len(rows))
	for _, r := range rows {
		if skip[r.Model] {
			continue
		}
		seeds = append(seeds, seedEntry{
			model:    r.Model,
			count:    r.Count,
			lastSeen: parseLogTimestamp(r.LastSeen),
		})
	}

	if len(seeds) == 0 {
		return nil, nil
	}

	// Without registry we fall back to the legacy single-only shape.
	if s.registry == nil {
		out := make([]AliasSuggestion, 0, len(seeds))
		for _, e := range seeds {
			out = append(out, AliasSuggestion{Kind: "single", Model: e.model, Count: e.count, LastSeen: e.lastSeen})
		}
		return out, nil
	}

	// 1) bucket seeds by family.
	type bucket struct {
		family string
		seeds  []seedEntry
		total  int64
	}
	buckets := map[string]*bucket{}
	leftovers := make([]seedEntry, 0, len(seeds)) // family lookup miss → fall back to single
	for _, e := range seeds {
		fam := familyForModel(s.registry, e.model)
		if fam == "" {
			leftovers = append(leftovers, e)
			continue
		}
		b, ok := buckets[fam]
		if !ok {
			b = &bucket{family: fam}
			buckets[fam] = b
		}
		b.seeds = append(b.seeds, e)
		b.total += e.count
	}

	// 2) promote buckets meeting threshold; demote others back to leftovers.
	type promoted struct {
		fam       string
		seeds     []seedEntry
		total     int64
		lastSeen  time.Time
		distinct  int
	}
	var families []promoted
	for fam, b := range buckets {
		distinct := len(uniqueModels(b.seeds))
		if distinct < minFamilyDistinct || b.total < minFamilyHits {
			leftovers = append(leftovers, b.seeds...)
			continue
		}
		var maxLastSeen time.Time
		for _, e := range b.seeds {
			if e.lastSeen.After(maxLastSeen) {
				maxLastSeen = e.lastSeen
			}
		}
		families = append(families, promoted{fam: fam, seeds: b.seeds, total: b.total, lastSeen: maxLastSeen, distinct: distinct})
	}

	// 3) for each promoted family, scan user's groups for in-group siblings.
	familyToGroupModels, err := s.scanGroupsByFamily(ctx, families)
	if err != nil {
		return nil, err
	}

	// 4) detect existing aliases for these family names.
	existingAlias := map[string]bool{}
	if len(families) > 0 {
		famNames := make([]string, 0, len(families))
		for _, f := range families {
			famNames = append(famNames, f.fam)
		}
		var aliasNames []string
		if err := s.db.WithContext(ctx).
			Model(&models.ModelAlias{}).
			Where("alias IN ? AND enabled = ?", famNames, true).
			Distinct("alias").
			Pluck("alias", &aliasNames).Error; err != nil {
			return nil, err
		}
		for _, n := range aliasNames {
			existingAlias[n] = true
		}
	}

	// 5) assemble output: family rows ordered by total desc, then single rows.
	sort.Slice(families, func(i, j int) bool { return families[i].total > families[j].total })

	out := make([]AliasSuggestion, 0, len(families)+len(leftovers))
	for _, f := range families {
		fm := buildFamilyModels(f.seeds, familyToGroupModels[f.fam])
		s := AliasSuggestion{
			Kind:     "family",
			Family:   f.fam,
			Models:   fm,
			Count:    f.total,
			LastSeen: f.lastSeen,
		}
		if existingAlias[f.fam] {
			s.ExistingAlias = f.fam
		}
		out = append(out, s)
	}
	sort.Slice(leftovers, func(i, j int) bool { return leftovers[i].count > leftovers[j].count })
	for _, e := range leftovers {
		out = append(out, AliasSuggestion{Kind: "single", Model: e.model, Count: e.count, LastSeen: e.lastSeen})
	}
	return out, nil
}

// uniqueModels returns the unique model name set inside a seed slice.
func uniqueModels(seeds []struct {
	model    string
	count    int64
	lastSeen time.Time
}) map[string]struct{} {
	out := map[string]struct{}{}
	for _, e := range seeds {
		out[e.model] = struct{}{}
	}
	return out
}

// familyForModel returns the registry's `model_family` for a bare model id
// (case-insensitive lookup). Empty string when the registry has no entry.
func familyForModel(reg *FreeModelsRegistry, modelID string) string {
	if reg == nil || modelID == "" {
		return ""
	}
	if m := reg.Lookup("", modelID); m != nil {
		return strings.TrimSpace(m.ModelFamily)
	}
	return ""
}

// scanGroupsByFamily walks all non-aggregate groups and indexes their
// available/exposed models by family — but only families we already care
// about (the promoted buckets). Returns family → [{ModelName, GroupIDs…}].
type groupModelMatch struct {
	model    string
	groupIDs map[uint]struct{}
}

func (s *AliasSuggestionService) scanGroupsByFamily(ctx context.Context, families []struct {
	fam       string
	seeds     []struct {
		model    string
		count    int64
		lastSeen time.Time
	}
	total    int64
	lastSeen time.Time
	distinct int
}) (map[string]map[string]*groupModelMatch, error) {
	if len(families) == 0 || s.registry == nil {
		return nil, nil
	}
	wanted := map[string]struct{}{}
	for _, f := range families {
		wanted[f.fam] = struct{}{}
	}
	var groups []models.Group
	if err := s.db.WithContext(ctx).
		Select("id", "group_type", "available_models", "exposed_models").
		Where("group_type <> ?", "aggregate").
		Find(&groups).Error; err != nil {
		return nil, err
	}
	out := map[string]map[string]*groupModelMatch{} // family → model → match
	for _, g := range groups {
		modelSet := map[string]struct{}{}
		// AvailableModels (passthrough source) and ExposedModels (specified
		// whitelist) — union both so we don't miss either mode.
		for _, raw := range []json.RawMessage{json.RawMessage(g.AvailableModels), json.RawMessage(g.ExposedModels)} {
			if len(raw) == 0 {
				continue
			}
			var arr []string
			if err := json.Unmarshal(raw, &arr); err != nil {
				continue
			}
			for _, m := range arr {
				if m != "" {
					modelSet[m] = struct{}{}
				}
			}
		}
		for m := range modelSet {
			fam := familyForModel(s.registry, m)
			if fam == "" {
				continue
			}
			if _, want := wanted[fam]; !want {
				continue
			}
			famMap := out[fam]
			if famMap == nil {
				famMap = map[string]*groupModelMatch{}
				out[fam] = famMap
			}
			match := famMap[m]
			if match == nil {
				match = &groupModelMatch{model: m, groupIDs: map[uint]struct{}{}}
				famMap[m] = match
			}
			match.groupIDs[g.ID] = struct{}{}
		}
	}
	return out, nil
}

// buildFamilyModels merges seed models (FromLogs=true) and in-group siblings
// (FromLogs=false) into a stable, de-duplicated, name-sorted list.
func buildFamilyModels(seeds []struct {
	model    string
	count    int64
	lastSeen time.Time
}, groupMatches map[string]*groupModelMatch) []FamilyModel {
	byName := map[string]*FamilyModel{}
	for _, e := range seeds {
		ls := e.lastSeen
		fm := &FamilyModel{Name: e.model, Count: e.count, LastSeen: &ls, FromLogs: true}
		byName[e.model] = fm
	}
	for name, match := range groupMatches {
		fm, ok := byName[name]
		if !ok {
			fm = &FamilyModel{Name: name, FromLogs: false}
			byName[name] = fm
		}
		ids := make([]uint, 0, len(match.groupIDs))
		for id := range match.groupIDs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		fm.InGroupIDs = ids
	}
	out := make([]FamilyModel, 0, len(byName))
	for _, fm := range byName {
		out = append(out, *fm)
	}
	sort.Slice(out, func(i, j int) bool {
		// FromLogs ones first (the user actually requested them), then by
		// name. Within FromLogs, higher count first.
		if out[i].FromLogs != out[j].FromLogs {
			return out[i].FromLogs
		}
		if out[i].FromLogs && out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// parseLogTimestamp tolerates SQLite (text) vs MySQL/Postgres (proper time).
func parseLogTimestamp(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
