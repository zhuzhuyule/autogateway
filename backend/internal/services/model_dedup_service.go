package services

import "github.com/zhuzhuyule/b4a/internal/models"

type ModelDedupService struct {
	groupManager models.GroupManager
}

func NewModelDedupService(groupManager models.GroupManager) *ModelDedupService {
	if groupManager == nil {
		groupManager = &DefaultGroupManager{}
	}
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

		for sourceModel := range group.ModelRedirect {
			modelToGroups[sourceModel] = appendUnique(
				modelToGroups[sourceModel], group.Name)
		}

		if group.TestModel != "" && len(group.ModelRedirect) == 0 {
			modelToGroups[group.TestModel] = appendUnique(
				modelToGroups[group.TestModel], group.Name)
		}
	}

	suggestions := make([]DedupSuggestion, 0)
	for model, groups := range modelToGroups {
		if len(groups) > 1 {
			suggestions = append(suggestions, DedupSuggestion{
				ModelName:              model,
				SourceGroups:           groups,
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

type DefaultGroupManager struct{}

func (m *DefaultGroupManager) GetAllGroups() []models.GroupInfo {
	return []models.GroupInfo{}
}
