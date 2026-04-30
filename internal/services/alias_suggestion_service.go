package services

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

// AliasSuggestion is one suggestion row. `Kind` discriminates the payload:
//   - "single": one unrecognized model — legacy shape, fields Model/Count/LastSeen.
//   - "family": several models (logs + group siblings) share a coarse-grained
//     family key derived from their model id. UI offers a one-click action
//     to create a single alias whose targets are all those models.
//
// Sub-fields use `omitempty` so the wire shape stays compact for either kind.
// The frontend switches on Kind.
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

// AliasSuggestionService scans request_logs for unrecognized models and
// recommends bulk-alias actions by grouping them under coarse-grained
// family keys derived heuristically from the model id (CDN's
// `model_family` field is too granular — `gemini-2.5-flash-lite` and
// `gemini-2.5-pro` get distinct families upstream, defeating aggregation).
type AliasSuggestionService struct {
	db *gorm.DB
}

func NewAliasSuggestionService(db *gorm.DB) *AliasSuggestionService {
	return &AliasSuggestionService{db: db}
}

// minFamilyMembers — the family must hold at least this many distinct model
// names (logs ∪ group siblings combined) to be promoted to a `kind=family`
// suggestion. Anything below is emitted as `kind=single`.
const minFamilyMembers = 2

type seedEntry struct {
	model    string
	count    int64
	lastSeen time.Time
}

type familyBucket struct {
	family   string
	seeds    []seedEntry
	total    int64
	lastSeen time.Time
}

type groupModelMatch struct {
	model    string
	groupIDs map[uint]struct{}
}

// Suggest returns up to ~20 unmatched models, optionally bucketed by
// derived family. Ordering: family suggestions first (by total hits desc),
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

	// 1) Bucket seeds by derived family. Empty family → leftover (single only).
	buckets := map[string]*familyBucket{}
	leftovers := make([]seedEntry, 0, len(seeds))
	for _, e := range seeds {
		fam := deriveFamily(e.model)
		if fam == "" {
			leftovers = append(leftovers, e)
			continue
		}
		b, ok := buckets[fam]
		if !ok {
			b = &familyBucket{family: fam}
			buckets[fam] = b
		}
		b.seeds = append(b.seeds, e)
		b.total += e.count
		if e.lastSeen.After(b.lastSeen) {
			b.lastSeen = e.lastSeen
		}
	}

	// 2) Scan all non-aggregate groups once, indexing models by derived family.
	//    We always scan — even families with a single seed may pick up siblings.
	familyToGroupModels, err := s.scanGroupsByFamily(ctx)
	if err != nil {
		return nil, err
	}

	// 3) Promote buckets where total distinct (logs ∪ siblings) ≥ threshold.
	families := make([]*familyBucket, 0, len(buckets))
	for _, b := range buckets {
		distinct := len(distinctModelNames(b.seeds))
		// add group-side siblings whose name isn't already in the seed list
		seen := distinctModelNames(b.seeds)
		for name := range familyToGroupModels[b.family] {
			if _, dup := seen[name]; !dup {
				distinct++
				seen[name] = struct{}{}
			}
		}
		if distinct < minFamilyMembers {
			leftovers = append(leftovers, b.seeds...)
			continue
		}
		families = append(families, b)
	}

	// 4) Detect existing aliases for these family names.
	existingAlias := map[string]bool{}
	if len(families) > 0 {
		famNames := make([]string, 0, len(families))
		for _, f := range families {
			famNames = append(famNames, f.family)
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

	// 5) Assemble: family rows ordered by total desc, then single rows.
	sort.Slice(families, func(i, j int) bool { return families[i].total > families[j].total })

	out := make([]AliasSuggestion, 0, len(families)+len(leftovers))
	for _, f := range families {
		fm := buildFamilyModels(f.seeds, familyToGroupModels[f.family])
		row := AliasSuggestion{
			Kind:     "family",
			Family:   f.family,
			Models:   fm,
			Count:    f.total,
			LastSeen: f.lastSeen,
		}
		if existingAlias[f.family] {
			row.ExistingAlias = f.family
		}
		out = append(out, row)
	}
	sort.Slice(leftovers, func(i, j int) bool { return leftovers[i].count > leftovers[j].count })
	for _, e := range leftovers {
		out = append(out, AliasSuggestion{Kind: "single", Model: e.model, Count: e.count, LastSeen: e.lastSeen})
	}
	return out, nil
}

func distinctModelNames(seeds []seedEntry) map[string]struct{} {
	out := map[string]struct{}{}
	for _, e := range seeds {
		out[e.model] = struct{}{}
	}
	return out
}

// variantTokens are sub-segments that, once seen (after the brand prefix),
// terminate the family key. They mark a *variant* of the family rather than
// part of the family name itself.
//
// Why these specifically: empirically these are the words that distinguish
// sibling SKUs from the same release line — `gemini-2.5-flash` vs
// `gemini-2.5-pro`, `claude-sonnet-4` vs `claude-haiku-4`. Without them
// every variant ends up in its own bucket, defeating aggregation.
var variantTokens = map[string]struct{}{
	// size class
	"lite": {}, "mini": {}, "nano": {}, "tiny": {},
	"small": {}, "medium": {}, "large": {}, "xl": {}, "xxl": {},
	"pro": {}, "plus": {}, "max": {}, "ultra": {},
	// modality / behaviour
	"flash": {}, "fast": {}, "turbo": {}, "thinking": {}, "reasoner": {},
	"vision": {}, "image": {}, "video": {}, "audio": {},
	"tts": {}, "embed": {}, "embedding": {}, "rerank": {},
	"chat": {}, "instruct": {}, "code": {}, "coder": {},
	// release stage
	"preview": {}, "experimental": {}, "exp": {}, "beta": {}, "rc": {},
	"free": {}, "trial": {},
	// claude family (sibling SKUs differ only by these words)
	"haiku": {}, "sonnet": {}, "opus": {},
}

// sizeRe matches parameter-size markers like "120b", "20b", "7b", "1.5b".
var sizeRe = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?[bm]$`)

// datelikeRe matches release-date / version markers tacked onto the end:
// "20240620", "2025-09", "09-2025", "v3", "rev2", etc.
var datelikeRe = regexp.MustCompile(`^(?:[0-9]{6,8}|[0-9]{4}|[vr][0-9]+|rev[0-9]+)$`)

// deriveFamily computes a coarse family key from a bare model id by
// stripping provider prefix, parenthesised tails, ":free"-style qualifiers,
// then walking dash-segments left to right and stopping at the first
// variant/size/date token. Empty input → empty output.
//
// Examples (also covered by tests):
//
//	gemini-2.5-flash-lite        -> gemini-2.5
//	gemini-2.5-flash             -> gemini-2.5
//	gemini-2.5-pro               -> gemini-2.5
//	gemini-2.0-flash             -> gemini-2.0
//	gpt-oss-120b                 -> gpt-oss
//	gpt-oss-20b                  -> gpt-oss
//	gpt-4o-mini                  -> gpt-4o
//	claude-sonnet-4-5-20251022   -> claude
//	claude-haiku-4               -> claude
//	qwen-2.5-72b-instruct        -> qwen-2.5
//	openai/gpt-oss-20b:free      -> gpt-oss
//	deepseek-r1                  -> deepseek-r1
//	openrouter/auto              -> auto
func deriveFamily(modelID string) string {
	s := strings.ToLower(strings.TrimSpace(modelID))
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "("); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "-")
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i > 0 && isVariantToken(p) {
			break
		}
		out = append(out, p)
	}
	return strings.Join(out, "-")
}

func isVariantToken(p string) bool {
	if _, ok := variantTokens[p]; ok {
		return true
	}
	if sizeRe.MatchString(p) {
		return true
	}
	if datelikeRe.MatchString(p) {
		return true
	}
	return false
}

// scanGroupsByFamily walks all non-aggregate groups and indexes their
// available/exposed models by derived family. Returns family → model → match.
func (s *AliasSuggestionService) scanGroupsByFamily(ctx context.Context) (map[string]map[string]*groupModelMatch, error) {
	var groups []models.Group
	if err := s.db.WithContext(ctx).
		Select("id, group_type, available_models, exposed_models").
		Where("group_type <> ?", "aggregate").
		Find(&groups).Error; err != nil {
		return nil, err
	}
	out := map[string]map[string]*groupModelMatch{}
	for _, g := range groups {
		modelSet := map[string]struct{}{}
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
			fam := deriveFamily(m)
			if fam == "" {
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
// (FromLogs=false) into a stable, de-duplicated, sorted list. FromLogs first
// (by count desc), then siblings (by name).
func buildFamilyModels(seeds []seedEntry, groupMatches map[string]*groupModelMatch) []FamilyModel {
	byName := map[string]*FamilyModel{}
	for _, e := range seeds {
		ls := e.lastSeen
		byName[e.model] = &FamilyModel{Name: e.model, Count: e.count, LastSeen: &ls, FromLogs: true}
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

// parseLogTimestamp tolerates SQLite (text) vs MySQL/Postgres (time.Time).
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
