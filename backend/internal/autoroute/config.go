package autoroute

import (
	"encoding/json"
	"sync"
)

type ConfigStore interface {
	Load() ([]byte, error)
	Save(data []byte) error
}

type MemoryConfigStore struct {
	data map[string]string
	mu   sync.RWMutex
}

func NewMemoryConfigStore() *MemoryConfigStore {
	return &MemoryConfigStore{
		data: make(map[string]string),
	}
}

func (s *MemoryConfigStore) Load() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.data["auto_routing_config"]; ok {
		return []byte(v), nil
	}
	return nil, nil
}

func (s *MemoryConfigStore) Save(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data["auto_routing_config"] = string(data)
	return nil
}

type ConfigManager struct {
	store  ConfigStore
	cache  *sync.RWMutex
	config *RouteConfig
}

func NewConfigManager(store ConfigStore) *ConfigManager {
	if store == nil {
		store = NewMemoryConfigStore()
	}
	return &ConfigManager{
		store: store,
		cache: &sync.RWMutex{},
	}
}

func (m *ConfigManager) Load() error {
	data, err := m.store.Load()
	if err != nil {
		return err
	}

	if data == nil {
		m.cache.Lock()
		m.config = &RouteConfig{
			Enabled:          false,
			SimpleThreshold:  2000,
			ComplexThreshold: 8000,
			GroupMapping:     make(map[string]GroupMapping),
		}
		m.cache.Unlock()
		return nil
	}

	var cfg RouteConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	m.cache.Lock()
	m.config = &cfg
	m.cache.Unlock()

	return nil
}

func (m *ConfigManager) Save(cfg *RouteConfig) error {
	if err := m.Validate(cfg); err != nil {
		return err
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	if err := m.store.Save(data); err != nil {
		return err
	}

	m.cache.Lock()
	m.config = cfg
	m.cache.Unlock()

	return nil
}

func (m *ConfigManager) GetConfig() *RouteConfig {
	m.cache.RLock()
	defer m.cache.RUnlock()
	if m.config == nil {
		return &RouteConfig{
			Enabled:          false,
			SimpleThreshold:  2000,
			ComplexThreshold: 8000,
			GroupMapping:     make(map[string]GroupMapping),
		}
	}
	return m.config
}

func (m *ConfigManager) Validate(cfg *RouteConfig) error {
	if cfg.SimpleThreshold <= 0 {
		cfg.SimpleThreshold = 2000
	}
	if cfg.ComplexThreshold <= 0 {
		cfg.ComplexThreshold = 8000
	}
	if cfg.SimpleThreshold >= cfg.ComplexThreshold {
		cfg.ComplexThreshold = cfg.SimpleThreshold * 2
	}
	if cfg.GroupMapping == nil {
		cfg.GroupMapping = make(map[string]GroupMapping)
	}
	return nil
}

func (m *ConfigManager) GetConfigJSON() string {
	cfg := m.GetConfig()
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return string(data)
}

func (m *ConfigManager) UpdateFromJSON(jsonStr string) error {
	var cfg RouteConfig
	if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
		return err
	}
	return m.Save(&cfg)
}

type ConfigAPI struct {
	manager *ConfigManager
}

func NewConfigAPI(manager *ConfigManager) *ConfigAPI {
	return &ConfigAPI{manager: manager}
}

type ConfigResponse struct {
	Success bool        `json:"success"`
	Config  *RouteConfig `json:"config,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type SaveConfigRequest struct {
	Enabled          bool                 `json:"enabled"`
	SimpleThreshold  int                  `json:"simple_threshold"`
	ComplexThreshold int                  `json:"complex_threshold"`
	GroupMapping     map[string]GroupMapping `json:"group_mapping"`
}

func (api *ConfigAPI) GetConfig() *ConfigResponse {
	return &ConfigResponse{
		Success: true,
		Config:  api.manager.GetConfig(),
	}
}

func (api *ConfigAPI) SaveConfig(req *SaveConfigRequest) *ConfigResponse {
	cfg := &RouteConfig{
		Enabled:          req.Enabled,
		SimpleThreshold:  req.SimpleThreshold,
		ComplexThreshold: req.ComplexThreshold,
		GroupMapping:     req.GroupMapping,
	}

	if err := api.manager.Save(cfg); err != nil {
		return &ConfigResponse{
			Success: false,
			Error:   err.Error(),
		}
	}

	return &ConfigResponse{
		Success: true,
		Config:  cfg,
	}
}

type TestRouteRequest struct {
	GroupName   string                 `json:"group_name"`
	RequestBody map[string]interface{} `json:"request_body"`
}

type TestRouteResponse struct {
	Success      bool             `json:"success"`
	TargetGroup  string           `json:"target_group,omitempty"`
	Analysis     *RequestAnalysis `json:"analysis,omitempty"`
	Error        string           `json:"error,omitempty"`
	FallbackUsed bool             `json:"fallback_used,omitempty"`
}

func (api *ConfigAPI) TestRoute(classifier *Classifier, req *TestRouteRequest) *TestRouteResponse {
	cfg := api.manager.GetConfig()

	bodyData, err := json.Marshal(req.RequestBody)
	if err != nil {
		return &TestRouteResponse{
			Success: false,
			Error:   err.Error(),
		}
	}

	analysis, err := classifier.Analyze(bodyData)
	if err != nil {
		return &TestRouteResponse{
			Success: false,
			Error:   err.Error(),
		}
	}

	mapping, found := cfg.GroupMapping[req.GroupName]
	if !found {
		return &TestRouteResponse{
			Success:     true,
			TargetGroup: req.GroupName,
			Analysis:    analysis,
		}
	}

	targetGroup := selectTargetGroup(mapping, analysis.Level)

	return &TestRouteResponse{
		Success:     true,
		TargetGroup: targetGroup,
		Analysis:    analysis,
	}
}
