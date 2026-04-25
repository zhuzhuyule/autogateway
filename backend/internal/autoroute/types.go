package autoroute

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

type RouteConfig struct {
	Enabled          bool                     `json:"enabled"`
	SimpleThreshold  int                      `json:"simple_threshold"`
	ComplexThreshold int                      `json:"complex_threshold"`
	GroupMapping     map[string]GroupMapping  `json:"group_mapping"`
}

type GroupMapping struct {
	SimpleGroup  string `json:"simple_group"`
	MediumGroup  string `json:"medium_group"`
	ComplexGroup string `json:"complex_group"`
}

type FallbackStrategy struct {
	PrimaryGroup   string `json:"primary_group"`
	FallbackGroup  string `json:"fallback_group"`
	FallbackGroup2 string `json:"fallback_group2"`
}
