// internal/backup/schema.go
package backup

import "time"

// CurrentSchemaVersion 是当前 BackupV1 payload 的版本号。
const CurrentSchemaVersion = 1

// BackupV1 是 .acb 解密后的 JSON 顶层结构。
type BackupV1 struct {
	SchemaVersion int       `json:"schema_version"`
	ExportedAt    time.Time `json:"exported_at"`
	ExportedBy    string    `json:"exported_by"`
	Data          DataV1    `json:"data"`
}

// DataV1 包含所有被备份的实体集合。
type DataV1 struct {
	SystemSettings []SystemSettingDTO `json:"system_settings"`
	Groups         []GroupDTO         `json:"groups"`
	GroupSubGroups []GroupSubGroupDTO `json:"group_sub_groups"`
	APIKeys        []APIKeyDTO        `json:"api_keys"`
	ModelAliases   []ModelAliasDTO    `json:"model_aliases"`
}

type SystemSettingDTO struct {
	SettingKey   string `json:"setting_key"`
	SettingValue string `json:"setting_value"`
	Description  string `json:"description"`
}

type GroupDTO struct {
	Name                string                 `json:"name"`
	DisplayName         string                 `json:"display_name"`
	ProxyKeys           string                 `json:"proxy_keys"`
	Description         string                 `json:"description"`
	GroupType           string                 `json:"group_type"`
	IsSystem            bool                   `json:"is_system"`
	SystemRole          string                 `json:"system_role"`
	// Upstreams 在 DB 是 NOT NULL —— exporter 必须把 nil 规范化为 []。
	Upstreams           any                    `json:"upstreams"`
	ValidationEndpoint  string                 `json:"validation_endpoint"`
	ChannelType         string                 `json:"channel_type"`
	Sort                int                    `json:"sort"`
	TestModel           string                 `json:"test_model"`
	ParamOverrides      map[string]any         `json:"param_overrides,omitempty"`
	Config              map[string]any         `json:"config,omitempty"`
	HeaderRules         any                    `json:"header_rules,omitempty"`
	ModelRedirectRules  map[string]any         `json:"model_redirect_rules,omitempty"`
	ModelRedirectStrict bool                   `json:"model_redirect_strict"`
	ModelRoutingMode    string                 `json:"model_routing_mode"`
	ExposedModels       any                    `json:"exposed_models,omitempty"`
	BlockedModels       any                    `json:"blocked_models,omitempty"`
}

type GroupSubGroupDTO struct {
	ParentName   string `json:"parent_name"`
	SubGroupName string `json:"sub_group_name"`
	Weight       int    `json:"weight"`
}

// APIKeyDTO 是 api_keys 表的备份形态。KeyHash 字段未导出，
// 导入端按 KeyValue 重算。
type APIKeyDTO struct {
	GroupName string `json:"group_name"`
	KeyValue  string `json:"key_value"` // PLAINTEXT
	Status    string `json:"status"`
	Notes     string `json:"notes"`
}

type ModelAliasDTO struct {
	Alias      string `json:"alias"`
	GroupName  string `json:"group_name"`
	RealModel  string `json:"real_model"`
	Weight     int    `json:"weight"`
	Priority   int    `json:"priority"`
	Enabled    bool   `json:"enabled"`
	IsReserved bool   `json:"is_reserved"`
}

// Strategy 是 importer 的冲突处理策略。
type Strategy string

const (
	StrategyMerge   Strategy = "merge"
	StrategySkip    Strategy = "skip"
	StrategyReplace Strategy = "replace"
)

// ParseStrategy 校验并归一化 strategy 字符串。
func ParseStrategy(s string) (Strategy, bool) {
	switch Strategy(s) {
	case StrategyMerge, StrategySkip, StrategyReplace:
		return Strategy(s), true
	}
	return "", false
}
