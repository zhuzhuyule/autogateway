package autoroute

import "encoding/json"

type ComplexityLevel string

const (
	Simple  ComplexityLevel = "simple"
	Medium  ComplexityLevel = "medium"
	Complex ComplexityLevel = "complex"
)

type RequestAnalysis struct {
	EstimatedTokens int             `json:"estimated_tokens"`
	HasTools        bool            `json:"has_tools"`
	HasVision       bool            `json:"has_vision"`
	ToolCount       int             `json:"tool_count"`
	MessageCount    int             `json:"message_count"`
	MaxMsgLength    int             `json:"max_msg_length"`
	Level           ComplexityLevel `json:"level"`
}

type ClassifierConfig struct {
	SimpleTokenThreshold   int `json:"simple_threshold"`
	ComplexTokenThreshold  int `json:"complex_threshold"`
	ToolComplexityWeight   int `json:"tool_complexity_weight"`
	VisionComplexityWeight int `json:"vision_complexity_weight"`
}

type GroupMapping struct {
	SimpleGroup  string `json:"simple_group"`
	MediumGroup  string `json:"medium_group"`
	ComplexGroup string `json:"complex_group"`
}

type RouteConfig struct {
	Enabled          bool                     `json:"enabled"`
	SimpleThreshold  int                      `json:"simple_threshold"`
	ComplexThreshold int                      `json:"complex_threshold"`
	GroupMapping     map[string]GroupMapping `json:"group_mapping"`
}

type FallbackStrategy struct {
	PrimaryGroup   string `json:"primary_group"`
	FallbackGroup  string `json:"fallback_group"`
	FallbackGroup2 string `json:"fallback_group2"`
}

func DefaultClassifierConfig() *ClassifierConfig {
	return &ClassifierConfig{
		SimpleTokenThreshold:   2000,
		ComplexTokenThreshold:  8000,
		ToolComplexityWeight:   500,
		VisionComplexityWeight: 1000,
	}
}

func DefaultRouteConfig() *RouteConfig {
	return &RouteConfig{
		Enabled:          false,
		SimpleThreshold:  2000,
		ComplexThreshold: 8000,
		GroupMapping:     make(map[string]GroupMapping),
	}
}

func (c *RouteConfig) ToJSON() (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func RouteConfigFromJSON(jsonStr string) (*RouteConfig, error) {
	var cfg RouteConfig
	if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
		return nil, err
	}
	if cfg.GroupMapping == nil {
		cfg.GroupMapping = make(map[string]GroupMapping)
	}
	return &cfg, nil
}
