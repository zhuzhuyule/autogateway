package services

type ModelDedupService struct {
	groupManager *GroupManager
}

func NewModelDedupService(groupManager *GroupManager) *ModelDedupService {
	return &ModelDedupService{groupManager: groupManager}
}

type DedupSuggestion struct {
	ModelName              string   `json:"model_name"`
	SourceGroups           []string `json:"source_groups"`
	SuggestedAggregateName string   `json:"suggested_aggregate_name"`
}

func (s *ModelDedupService) GetDedupSuggestions() []DedupSuggestion {
	groups := s.groupManager.GetAllGroups()
	modelToGroups := make(map[string][]string)

	for _, group := range groups {
		if group.GroupType == "aggregate" {
			continue
		}

		for sourceModel := range group.ModelRedirectMap {
			modelToGroups[sourceModel] = appendUnique(
				modelToGroups[sourceModel], group.Name)
		}

		if group.TestModel != "" && len(group.ModelRedirectMap) == 0 {
			modelToGroups[group.TestModel] = appendUnique(
				modelToGroups[group.TestModel], group.Name)
		}
	}

	suggestions := make([]DedupSuggestion, 0)
	for model, grps := range modelToGroups {
		if len(grps) > 1 {
			suggestions = append(suggestions, DedupSuggestion{
				ModelName:              model,
				SourceGroups:           grps,
				SuggestedAggregateName: model + "-aggregate",
			})
		}
	}

	return suggestions
}

func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}
