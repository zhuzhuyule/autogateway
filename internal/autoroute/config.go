package autoroute

import (
	"sync"
)

type ConfigStore interface {
	Load(key string) (string, error)
	Save(key string, value string) error
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

func (s *MemoryConfigStore) Load(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.data[key]; ok {
		return v, nil
	}
	return "", nil
}

func (s *MemoryConfigStore) Save(key string, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
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
	data, err := m.store.Load("auto_routing_config")
	if err != nil {
		return err
	}

	if data == "" {
		m.cache.Lock()
		m.config = DefaultRouteConfig()
		m.cache.Unlock()
		return nil
	}

	cfg, err := RouteConfigFromJSON(data)
	if err != nil {
		return err
	}

	m.cache.Lock()
	m.config = cfg
	m.cache.Unlock()

	return nil
}

func (m *ConfigManager) Save(cfg *RouteConfig) error {
	data, err := cfg.ToJSON()
	if err != nil {
		return err
	}

	if err := m.store.Save("auto_routing_config", data); err != nil {
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
		return DefaultRouteConfig()
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
