package services

import "github.com/zhuzhuyule/b4a/internal/models"

type AggregateSuggestionService struct {
	groupManager models.GroupManager
}

func NewAggregateSuggestionService(groupManager models.GroupManager) *AggregateSuggestionService {
	if groupManager == nil {
		groupManager = &DefaultGroupManager{}
	}
	return &AggregateSuggestionService{groupManager: groupManager}
}

type SubGroupConfig struct {
	Name     string            `json:"name"`
	Weight   int               `json:"weight"`
	Redirect map[string]string `json:"redirect"`
}

type AggregateGroupSuggestion struct {
	AggregateName string           `json:"aggregate_name"`
	ModelName     string           `json:"model_name"`
	SubGroups     []SubGroupConfig `json:"sub_groups"`
}

func (s *AggregateSuggestionService) GetSuggestion(modelName string, sourceGroups []string) *AggregateGroupSuggestion {
	subGroups := make([]SubGroupConfig, len(sourceGroups))
	weight := 100 / len(sourceGroups)

	for i, groupName := range sourceGroups {
		redirect := make(map[string]string)
		redirect[modelName] = modelName

		subGroups[i] = SubGroupConfig{
			Name:     groupName,
			Weight:   weight,
			Redirect: redirect,
		}
	}

	return &AggregateGroupSuggestion{
		AggregateName: modelName + "-aggregate",
		ModelName:     modelName,
		SubGroups:     subGroups,
	}
}

func (s *AggregateSuggestionService) CreateAggregateConfig(suggestion *AggregateGroupSuggestion) map[string]interface{} {
	return map[string]interface{}{
		"name":       suggestion.AggregateName,
		"type":       "aggregate",
		"sub_groups": suggestion.SubGroups,
		"model_list": []string{suggestion.ModelName},
	}
}
