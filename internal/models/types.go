package models

import (
	"autogateway/internal/failover"
	"autogateway/internal/types"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Key状态
const (
	KeyStatusActive  = "active"
	KeyStatusInvalid = "invalid"
)

// SystemSetting 对应 system_settings 表
type SystemSetting struct {
	ID           uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	SettingKey   string         `gorm:"type:varchar(255);not null;unique" json:"setting_key"`
	SettingValue string         `gorm:"type:text;not null" json:"setting_value"`
	Description  string         `gorm:"type:varchar(512)" json:"description"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

// GroupConfig 存储特定于分组的配置
type GroupConfig struct {
	RequestTimeout               *int    `json:"request_timeout,omitempty"`
	IdleConnTimeout              *int    `json:"idle_conn_timeout,omitempty"`
	ConnectTimeout               *int    `json:"connect_timeout,omitempty"`
	MaxIdleConns                 *int    `json:"max_idle_conns,omitempty"`
	MaxIdleConnsPerHost          *int    `json:"max_idle_conns_per_host,omitempty"`
	ResponseHeaderTimeout        *int    `json:"response_header_timeout,omitempty"`
	ProxyURL                     *string `json:"proxy_url,omitempty"`
	MaxRetries                   *int    `json:"max_retries,omitempty"`
	BlacklistThreshold           *int    `json:"blacklist_threshold,omitempty"`
	FailoverStatusCodes          *string `json:"failover_status_codes,omitempty"`
	KeyValidationIntervalMinutes *int    `json:"key_validation_interval_minutes,omitempty"`
	KeyValidationConcurrency     *int    `json:"key_validation_concurrency,omitempty"`
	KeyValidationTimeoutSeconds  *int    `json:"key_validation_timeout_seconds,omitempty"`
	EnableRequestBodyLogging     *bool   `json:"enable_request_body_logging,omitempty"`
}

// HeaderRule defines a single rule for header manipulation.
type HeaderRule struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Action string `json:"action"` // "set" or "remove"
}

// GroupSubGroup 聚合分组和子分组的关联表
type GroupSubGroup struct {
	ID         uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	GroupID    uint           `gorm:"not null;uniqueIndex:idx_group_sub" json:"group_id"`
	SubGroupID uint           `gorm:"not null;uniqueIndex:idx_group_sub" json:"sub_group_id"`
	Weight     int            `gorm:"default:0" json:"weight"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`

	// Lightweight association - only store necessary info for performance
	SubGroupName string `gorm:"-" json:"sub_group_name,omitempty"`
}

// SubGroupInfo 用于API响应的子分组信息
type SubGroupInfo struct {
	Group       Group `json:"group"`
	Weight      int   `json:"weight"`
	TotalKeys   int64 `json:"total_keys"`
	ActiveKeys  int64 `json:"active_keys"`
	InvalidKeys int64 `json:"invalid_keys"`
}

// ParentAggregateGroupInfo 用于API响应的父聚合分组信息
type ParentAggregateGroupInfo struct {
	GroupID     uint   `json:"group_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Weight      int    `json:"weight"`
}

// 系统默认聚合分组的 SystemRole 取值
const (
	SystemRoleDefaultOpenAI    = "default-openai"
	SystemRoleDefaultGemini    = "default-gemini"
	SystemRoleDefaultAnthropic = "default-anthropic"
)

// SystemRoleForChannelType 返回对应 channel_type 的系统默认聚合 role,无对应返回 "".
func SystemRoleForChannelType(channelType string) string {
	switch channelType {
	case "openai", "openai-response":
		return SystemRoleDefaultOpenAI
	case "gemini":
		return SystemRoleDefaultGemini
	case "anthropic":
		return SystemRoleDefaultAnthropic
	default:
		return ""
	}
}

// Group 对应 groups 表
type Group struct {
	ID                  uint                 `gorm:"primaryKey;autoIncrement" json:"id"`
	EffectiveConfig     types.SystemSettings `gorm:"-" json:"effective_config,omitempty"`
	Name                string               `gorm:"type:varchar(255);not null;unique" json:"name"`
	Endpoint            string               `gorm:"-" json:"endpoint"`
	DisplayName         string               `gorm:"type:varchar(255)" json:"display_name"`
	ProxyKeys           string               `gorm:"type:text" json:"proxy_keys"`
	Description         string               `gorm:"type:varchar(512)" json:"description"`
	GroupType           string               `gorm:"type:varchar(50);default:'standard'" json:"group_type"` // 'standard' or 'aggregate'
	IsSystem            bool                 `gorm:"default:false;index" json:"is_system"`
	SystemRole          string               `gorm:"type:varchar(50);default:'';index" json:"system_role"`
	Upstreams           datatypes.JSON       `gorm:"type:json;not null" json:"upstreams"`
	ValidationEndpoint  string               `gorm:"type:varchar(255)" json:"validation_endpoint"`
	ChannelType         string               `gorm:"type:varchar(50);not null" json:"channel_type"`
	Sort                int                  `gorm:"default:0" json:"sort"`
	TestModel           string               `gorm:"type:varchar(255);not null" json:"test_model"`
	ParamOverrides      datatypes.JSONMap    `gorm:"type:json" json:"param_overrides"`
	Config              datatypes.JSONMap    `gorm:"type:json" json:"config"`
	HeaderRules         datatypes.JSON       `gorm:"type:json" json:"header_rules"`
	ModelRedirectRules  datatypes.JSONMap    `gorm:"type:json" json:"model_redirect_rules"`
	ModelRedirectStrict bool                 `gorm:"default:false" json:"model_redirect_strict"`
	APIKeys             []APIKey             `gorm:"foreignKey:GroupID" json:"api_keys"`
	KeyCount            int64                `gorm:"-" json:"key_count"`
	SubGroups           []GroupSubGroup      `gorm:"-" json:"sub_groups,omitempty"`
	LastValidatedAt     *time.Time           `json:"last_validated_at"`
	AvailableModels     datatypes.JSON       `gorm:"type:json" json:"available_models"` // 由上游 /v1/models 缓存的真实模型列表
	// FreeModels 是 AvailableModels 中通过 FreeModelsRegistry 严格匹配出的免费子集.
	// 跟 AvailableModels 同一刷新时机产生: provider-aware ID 归一化后跟 Registry 求交.
	// 保留原样 ID (含 ":free" 等后缀); 前端读它直接判 Free badge, 零本地算法.
	FreeModels          datatypes.JSON       `gorm:"type:json" json:"free_models"`
	ModelsRefreshedAt   *time.Time           `json:"models_refreshed_at"`
	// ModelRoutingMode controls model routing strictness:
	//   "passthrough" — 所有上游模型直通 (默认, 兼容老分组)
	//   "specified"   — 只允许 ExposedModels 列出的模型, 否则 405
	// alias 路由始终先于此检查, 但 alias.real_model 不在 ExposedModels 时 alias 静默失效.
	ModelRoutingMode string `gorm:"type:varchar(20);not null;default:'passthrough'" json:"model_routing_mode"`
	// ExposedModels — specified 模式下允许调用的模型白名单 (string[] JSON).
	// passthrough 模式下忽略.
	ExposedModels datatypes.JSON `gorm:"type:json" json:"exposed_models"`
	// BlockedModels — 黑名单 (string[] JSON), 与 routing mode 正交:
	// passthrough / specified 都生效, 包含的模型即使在 exposed 或 alias 命中也会被拒绝。
	BlockedModels datatypes.JSON `gorm:"type:json" json:"blocked_models"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`

	// For cache
	ProxyKeysMap              map[string]struct{}        `gorm:"-" json:"-"`
	HeaderRuleList            []HeaderRule               `gorm:"-" json:"-"`
	ModelRedirectMap          map[string]string          `gorm:"-" json:"-"`
	FailoverStatusCodeMatcher failover.StatusCodeMatcher `gorm:"-" json:"-"`
}

// ModelAlias 对应 model_aliases 表 — Model Routing 重写后的核心承载.
// 一行 = (alias, group, real_model) 三元组 + 权重/优先级.
// 同一 alias 下的多行构成「候选池」, 由 SWRR 选择器轮询挑选.
//
// 三个 reserved alias 名 (auto-simple / auto-medium / auto-complex)
// 由 §13.1 路由决策树用作 smart routing 池, UI 锁定不可改名/删除.
type ModelAlias struct {
	ID uint `gorm:"primaryKey;autoIncrement" json:"id"`
	// idx_alias_group_model — uniqueness on the (alias, group_id, real_model)
	// triple. Each row is one candidate; resubmitting the same triple via the
	// quick-setup UI must be rejected as DUPLICATE_RESOURCE, not silently
	// duplicated (which would double the SWRR weight).
	Alias      string    `gorm:"type:varchar(255);not null;index:idx_alias_enabled;uniqueIndex:idx_alias_group_model,priority:1" json:"alias"`
	GroupID    uint      `gorm:"not null;index;uniqueIndex:idx_alias_group_model,priority:2" json:"group_id"`
	RealModel  string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_alias_group_model,priority:3" json:"real_model"`
	Weight     int       `gorm:"not null;default:1" json:"weight"`
	Priority   int       `gorm:"not null;default:100" json:"priority"`
	Enabled    bool      `gorm:"not null;default:true;index:idx_alias_enabled" json:"enabled"`
	IsReserved bool           `gorm:"not null;default:false" json:"is_reserved"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`
}

// TableName forces the table name even if GORM pluralization changes.
func (ModelAlias) TableName() string { return "model_aliases" }

// APIKey 对应 api_keys 表
type APIKey struct {
	ID           uint       `gorm:"primaryKey;autoIncrement;index:idx_api_keys_group_last_used_id,priority:3" json:"id"`
	KeyValue     string     `gorm:"type:text;not null" json:"key_value"`
	KeyHash      string     `gorm:"type:varchar(128);index" json:"key_hash"`
	GroupID      uint       `gorm:"not null;index;index:idx_api_keys_group_last_used_id,priority:1" json:"group_id"`
	Status       string     `gorm:"type:varchar(50);not null;default:'active';index" json:"status"`
	Notes        string     `gorm:"type:varchar(255);default:''" json:"notes"`
	RequestCount int64      `gorm:"not null;default:0" json:"request_count"`
	FailureCount int64      `gorm:"not null;default:0" json:"failure_count"`
	LastUsedAt   *time.Time     `gorm:"index:idx_api_keys_group_last_used_id,priority:2" json:"last_used_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

// RequestType 请求类型常量
const (
	RequestTypeRetry = "retry"
	RequestTypeFinal = "final"
	// RequestTypeTest 是手动 / 后台 ValidateKey 触发的探活请求, 走 request_logs
	// 表只为前端 live tail 可见 (Logs.vue + errorContains 模糊搜), 不计入业务
	// success/failure 统计 (hourly stats 跳过).
	RequestTypeTest = "test"
)

// RequestLog 对应 request_logs 表
type RequestLog struct {
	ID              string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	Timestamp       time.Time `gorm:"not null;index" json:"timestamp"`
	GroupID         uint      `gorm:"not null;index" json:"group_id"`
	GroupName       string    `gorm:"type:varchar(255);index" json:"group_name"`
	ParentGroupID   uint      `gorm:"index" json:"parent_group_id"`
	ParentGroupName string    `gorm:"type:varchar(255);index" json:"parent_group_name"`
	KeyValue        string    `gorm:"type:text" json:"key_value"`
	KeyHash         string    `gorm:"type:varchar(128);index" json:"key_hash"`
	Model           string    `gorm:"type:varchar(255);index" json:"model"`
	IsSuccess       bool      `gorm:"not null" json:"is_success"`
	SourceIP        string    `gorm:"type:varchar(64)" json:"source_ip"`
	StatusCode      int       `gorm:"not null" json:"status_code"`
	RequestPath     string    `gorm:"type:varchar(500)" json:"request_path"`
	Duration        int64     `gorm:"not null" json:"duration_ms"`
	ErrorMessage    string    `gorm:"type:text" json:"error_message"`
	UserAgent       string    `gorm:"type:varchar(512)" json:"user_agent"`
	RequestType     string    `gorm:"type:varchar(20);not null;default:'final';index" json:"request_type"`
	UpstreamAddr    string    `gorm:"type:varchar(500)" json:"upstream_addr"`
	IsStream        bool      `gorm:"not null" json:"is_stream"`
	RequestBody     string    `gorm:"type:text" json:"request_body"`
}

// StatCard 用于仪表盘的单个统计卡片数据
type StatCard struct {
	Value         float64 `json:"value"`
	SubValue      int64   `json:"sub_value,omitempty"`
	SubValueTip   string  `json:"sub_value_tip,omitempty"`
	Trend         float64 `json:"trend"`
	TrendIsGrowth bool    `json:"trend_is_growth"`
}

// SecurityWarning 用于安全警告信息
type SecurityWarning struct {
	Type       string `json:"type"`       // 警告类型：auth_key, encryption_key 等
	Message    string `json:"message"`    // 警告信息
	Severity   string `json:"severity"`   // 严重程度：low, medium, high
	Suggestion string `json:"suggestion"` // 建议解决方案
}

// DashboardStatsResponse 用于仪表盘基础统计的API响应
type DashboardStatsResponse struct {
	KeyCount         StatCard          `json:"key_count"`
	RPM              StatCard          `json:"rpm"`
	RequestCount     StatCard          `json:"request_count"`
	ErrorRate        StatCard          `json:"error_rate"`
	SecurityWarnings []SecurityWarning `json:"security_warnings"`
}

// ChartDataset 用于图表的数据集
type ChartDataset struct {
	Label string  `json:"label"`
	Data  []int64 `json:"data"`
	Color string  `json:"color"`
}

// ChartData 用于图表的API响应
type ChartData struct {
	Labels   []string       `json:"labels"`
	Datasets []ChartDataset `json:"datasets"`
}

// GroupHourlyStat 对应 group_hourly_stats 表，用于存储每个分组每小时的请求统计
type GroupHourlyStat struct {
	ID           uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Time         time.Time `gorm:"not null;uniqueIndex:idx_group_time" json:"time"` // 整点时间
	GroupID      uint      `gorm:"not null;uniqueIndex:idx_group_time" json:"group_id"`
	SuccessCount int64     `gorm:"not null;default:0" json:"success_count"`
	FailureCount int64     `gorm:"not null;default:0" json:"failure_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SyncPeer 对应 sync_peers 表，用于管理多端同步节点
type SyncPeer struct {
	ID           string         `gorm:"type:varchar(255);primaryKey" json:"id"`
	Name         string         `gorm:"type:varchar(255);not null" json:"name"`
	URL          string         `gorm:"type:varchar(512);not null" json:"url"`
	SyncKey      string         `gorm:"type:varchar(512);not null" json:"sync_key"`
	Role         string         `gorm:"type:varchar(50);not null;default:'client'" json:"role"` // 'server' or 'client'
	Status       string         `gorm:"type:varchar(50);not null;default:'disconnected'" json:"status"`
	SyncAPIKeys  bool           `gorm:"default:false" json:"sync_api_keys"`
	LastSyncedAt *time.Time     `json:"last_synced_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

// SyncLog 对应 sync_logs 表，用于记录配置同步历史与状态
type SyncLog struct {
	ID           uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	PeerID       string    `gorm:"type:varchar(255);index" json:"peer_id"`
	Action       string    `gorm:"type:varchar(50);not null" json:"action"` // 'push' or 'pull'
	Status       string    `gorm:"type:varchar(50);not null" json:"status"` // 'success' or 'error'
	ErrorMessage string    `gorm:"type:text" json:"error_message"`
	Details      string    `gorm:"type:text" json:"details"` // 记录同步的表名/ID等元数据
	Timestamp    time.Time `gorm:"not null;index" json:"timestamp"`
}
