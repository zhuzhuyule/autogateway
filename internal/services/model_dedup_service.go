package services

import (
	"encoding/json"
	"fmt"
	"sort"

	"autogateway/internal/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type ModelDedupService struct {
	groupManager *GroupManager
}

func NewModelDedupService(groupManager *GroupManager) *ModelDedupService {
	return &ModelDedupService{groupManager: groupManager}
}

// DedupCandidate is one (group, real_model) row that can become a ModelAlias
// member when the dedup suggestion is accepted.
type DedupCandidate struct {
	GroupID   uint   `json:"group_id"`
	GroupName string `json:"group_name"`
	RealModel string `json:"real_model"`
}

// DedupSuggestion proposes unifying multiple groups that serve the same
// model behind one alias. SuggestedAlias defaults to the bare model name;
// frontend may override before POSTing back.
type DedupSuggestion struct {
	ModelName      string           `json:"model_name"`
	SuggestedAlias string           `json:"suggested_alias"`
	Candidates     []DedupCandidate `json:"candidates"`
}

func (s *ModelDedupService) GetDedupSuggestions() []DedupSuggestion {
	groups := s.groupManager.GetAllGroups()
	modelToCandidates := make(map[string][]DedupCandidate)

	for _, group := range groups {
		if group.GroupType == "aggregate" {
			continue
		}

		for exposed, real := range group.ModelRedirectMap {
			if real == "" {
				real = exposed
			}
			modelToCandidates[exposed] = appendUniqueCandidate(
				modelToCandidates[exposed],
				DedupCandidate{GroupID: group.ID, GroupName: group.Name, RealModel: real},
			)
		}

		if group.TestModel != "" && len(group.ModelRedirectMap) == 0 {
			modelToCandidates[group.TestModel] = appendUniqueCandidate(
				modelToCandidates[group.TestModel],
				DedupCandidate{GroupID: group.ID, GroupName: group.Name, RealModel: group.TestModel},
			)
		}
	}

	suggestions := make([]DedupSuggestion, 0)
	for model, cands := range modelToCandidates {
		if len(cands) > 1 {
			suggestions = append(suggestions, DedupSuggestion{
				ModelName:      model,
				SuggestedAlias: model,
				Candidates:     cands,
			})
		}
	}

	return suggestions
}

func appendUniqueCandidate(slice []DedupCandidate, item DedupCandidate) []DedupCandidate {
	for _, c := range slice {
		if c.GroupID == item.GroupID && c.RealModel == item.RealModel {
			return slice
		}
	}
	return append(slice, item)
}

// DedupFamily groups all candidate (group, real_model) pairs by their
// derived family key. group_count is the number of distinct groups
// offering at least one model in this family — used by the UI to decide
// which families to auto-expand.
type DedupFamily struct {
	Family     string            `json:"family"`
	GroupCount int               `json:"group_count"`
	Models     []DedupModelEntry `json:"models"`
}

// DedupModelEntry is one (group, real_model) candidate. Aliases lists every
// model_aliases row whose (alias, group_id, real_model) matches and is enabled.
type DedupModelEntry struct {
	GroupID   uint     `json:"group_id"`
	GroupName string   `json:"group_name"`
	RealModel string   `json:"real_model"`
	Aliases   []string `json:"aliases"`
}

// GetModelsByFamily returns every candidate model from non-aggregate groups,
// grouped by deriveFamily(real_model). Filters:
//   - skips aggregate groups (alias targets are upstream-side only)
//   - in specified routing mode: candidate set = exposed_models, falling back
//     to available_models if the exposed list is empty (matches picker UX)
//   - in passthrough mode: candidate set = available_models
//   - removes anything in blocked_models
func (s *ModelDedupService) GetModelsByFamily(db *gorm.DB) ([]DedupFamily, error) {
	groups := s.groupManager.GetAllGroups()

	type entry struct {
		groupID   uint
		groupName string
		realModel string
	}

	familyToEntries := map[string][]entry{}
	familyGroups := map[string]map[uint]struct{}{}

	for _, group := range groups {
		if group.GroupType == "aggregate" {
			continue
		}
		candidates := candidateModelsForGroup(group)
		blocked := parseStringSet(group.BlockedModels)
		for m := range candidates {
			if _, isBlocked := blocked[m]; isBlocked {
				continue
			}
			fam := deriveFamily(m)
			familyToEntries[fam] = append(familyToEntries[fam], entry{
				groupID:   group.ID,
				groupName: group.Name,
				realModel: m,
			})
			if familyGroups[fam] == nil {
				familyGroups[fam] = map[uint]struct{}{}
			}
			familyGroups[fam][group.ID] = struct{}{}
		}
	}

	// Batched lookup: every (group_id, real_model) → []alias.
	type aliasRow struct {
		Alias     string
		GroupID   uint
		RealModel string
	}
	var aliasRows []aliasRow
	if err := db.Model(&models.ModelAlias{}).
		Select("alias, group_id, real_model").
		Where("enabled = ?", true).
		Scan(&aliasRows).Error; err != nil {
		return nil, err
	}
	aliasIndex := map[string][]string{}
	for _, r := range aliasRows {
		key := aliasKey(r.GroupID, r.RealModel)
		aliasIndex[key] = append(aliasIndex[key], r.Alias)
	}

	// Assemble + sort.
	out := make([]DedupFamily, 0, len(familyToEntries))
	for fam, entries := range familyToEntries {
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].realModel != entries[j].realModel {
				return entries[i].realModel < entries[j].realModel
			}
			return entries[i].groupName < entries[j].groupName
		})
		modelEntries := make([]DedupModelEntry, 0, len(entries))
		for _, e := range entries {
			aliases := aliasIndex[aliasKey(e.groupID, e.realModel)]
			sort.Strings(aliases)
			modelEntries = append(modelEntries, DedupModelEntry{
				GroupID:   e.groupID,
				GroupName: e.groupName,
				RealModel: e.realModel,
				Aliases:   aliases,
			})
		}
		out = append(out, DedupFamily{
			Family:     fam,
			GroupCount: len(familyGroups[fam]),
			Models:     modelEntries,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GroupCount != out[j].GroupCount {
			return out[i].GroupCount > out[j].GroupCount
		}
		if len(out[i].Models) != len(out[j].Models) {
			return len(out[i].Models) > len(out[j].Models)
		}
		return out[i].Family < out[j].Family
	})
	return out, nil
}

// candidateModelsForGroup returns the set of real_model strings the group
// is willing to serve, applying the routing-mode rules described above.
func candidateModelsForGroup(g *models.Group) map[string]struct{} {
	out := map[string]struct{}{}
	if g.ModelRoutingMode == "specified" {
		for m := range parseStringSet(g.ExposedModels) {
			out[m] = struct{}{}
		}
		if len(out) > 0 {
			return out
		}
		// fall through to available_models — matches picker degrade behaviour
	}
	for m := range parseStringSet(g.AvailableModels) {
		out[m] = struct{}{}
	}
	return out
}

// parseStringSet decodes a datatypes.JSON containing a string array into a set.
// Empty / invalid JSON yields an empty set.
func parseStringSet(raw datatypes.JSON) map[string]struct{} {
	out := map[string]struct{}{}
	if len(raw) == 0 {
		return out
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return out
	}
	for _, s := range arr {
		if s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}

func aliasKey(groupID uint, realModel string) string {
	return fmt.Sprintf("%d::%s", groupID, realModel)
}
