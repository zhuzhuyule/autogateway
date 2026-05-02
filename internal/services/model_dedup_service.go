package services

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
