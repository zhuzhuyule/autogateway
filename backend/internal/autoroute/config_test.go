package autoroute

import (
	"encoding/json"
	"testing"
)

func TestConfigManager_DefaultConfig(t *testing.T) {
	manager := NewConfigManager(NewMemoryConfigStore())

	err := manager.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := manager.GetConfig()
	if cfg.Enabled {
		t.Error("expected Enabled to be false by default")
	}
	if cfg.SimpleThreshold != 2000 {
		t.Errorf("expected SimpleThreshold 2000, got %d", cfg.SimpleThreshold)
	}
	if cfg.ComplexThreshold != 8000 {
		t.Errorf("expected ComplexThreshold 8000, got %d", cfg.ComplexThreshold)
	}
}

func TestConfigManager_SaveAndLoad(t *testing.T) {
	store := NewMemoryConfigStore()
	manager := NewConfigManager(store)

	cfg := &RouteConfig{
		Enabled:          true,
		SimpleThreshold:  3000,
		ComplexThreshold: 10000,
		GroupMapping: map[string]GroupMapping{
			"gpt-4o": {
				SimpleGroup:  "gpt-4o-lite",
				MediumGroup:  "gpt-4o-pro",
				ComplexGroup: "gpt-4.1-max",
			},
		},
	}

	err := manager.Save(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg2 := manager.GetConfig()
	if !cfg2.Enabled {
		t.Error("expected Enabled to be true")
	}
	if cfg2.SimpleThreshold != 3000 {
		t.Errorf("expected SimpleThreshold 3000, got %d", cfg2.SimpleThreshold)
	}
}

func TestConfigManager_Validate(t *testing.T) {
	manager := NewConfigManager(NewMemoryConfigStore())

	tests := []struct {
		name   string
		input  *RouteConfig
		expect func(*RouteConfig)
	}{
		{
			name: "zero thresholds get defaults",
			input: &RouteConfig{
				Enabled:          true,
				SimpleThreshold:  0,
				ComplexThreshold: 0,
			},
			expect: func(cfg *RouteConfig) {
				if cfg.SimpleThreshold != 2000 {
					t.Errorf("expected 2000, got %d", cfg.SimpleThreshold)
				}
			},
		},
		{
			name: "nil group mapping gets empty map",
			input: &RouteConfig{
				Enabled:          true,
				SimpleThreshold:  2000,
				ComplexThreshold: 8000,
				GroupMapping:     nil,
			},
			expect: func(cfg *RouteConfig) {
				if cfg.GroupMapping == nil {
					t.Error("expected non-nil GroupMapping")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager.Save(tt.input)
			cfg := manager.GetConfig()
			tt.expect(cfg)
		})
	}
}

func TestConfigAPI_GetConfig(t *testing.T) {
	store := NewMemoryConfigStore()
	manager := NewConfigManager(store)
	api := NewConfigAPI(manager)

	resp := api.GetConfig()
	if !resp.Success {
		t.Error("expected success")
	}
	if resp.Config == nil {
		t.Error("expected config")
	}
}

func TestConfigAPI_SaveConfig(t *testing.T) {
	store := NewMemoryConfigStore()
	manager := NewConfigManager(store)
	api := NewConfigAPI(manager)

	req := &SaveConfigRequest{
		Enabled:          true,
		SimpleThreshold:  2500,
		ComplexThreshold: 9000,
		GroupMapping: map[string]GroupMapping{
			"claude": {
				SimpleGroup:  "claude-haiku",
				MediumGroup:  "claude-sonnet",
				ComplexGroup: "claude-opus",
			},
		},
	}

	resp := api.SaveConfig(req)
	if !resp.Success {
		t.Errorf("expected success, got error: %s", resp.Error)
	}
	if resp.Config.SimpleThreshold != 2500 {
		t.Errorf("expected 2500, got %d", resp.Config.SimpleThreshold)
	}
}

func TestConfigAPI_TestRoute(t *testing.T) {
	store := NewMemoryConfigStore()
	manager := NewConfigManager(store)
	api := NewConfigAPI(manager)

	cfg := &RouteConfig{
		Enabled:          true,
		SimpleThreshold:  2000,
		ComplexThreshold: 8000,
		GroupMapping: map[string]GroupMapping{
			"gpt-4o": {
				SimpleGroup:  "gpt-4o-lite",
				MediumGroup:  "gpt-4o-pro",
				ComplexGroup: "gpt-4.1-max",
			},
		},
	}
	manager.Save(cfg)

	classifier := NewClassifier(nil)

	tests := []struct {
		name          string
		groupName     string
		body          map[string]interface{}
		expectedGroup string
	}{
		{
			name:      "simple request",
			groupName: "gpt-4o",
			body: map[string]interface{}{
				"messages": []map[string]interface{}{
					{"role": "user", "content": "hello"},
				},
			},
			expectedGroup: "gpt-4o-lite",
		},
		{
			name:      "request with tools",
			groupName: "gpt-4o",
			body: map[string]interface{}{
				"messages": []map[string]interface{}{
					{"role": "user", "content": "hello"},
				},
				"tools": []map[string]interface{}{
					{"name": "func1"},
				},
			},
			expectedGroup: "gpt-4o-pro",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &TestRouteRequest{
				GroupName:   tt.groupName,
				RequestBody: tt.body,
			}
			resp := api.TestRoute(classifier, req)
			if !resp.Success {
				t.Errorf("expected success, got error: %s", resp.Error)
			}
			if resp.TargetGroup != tt.expectedGroup {
				t.Errorf("expected %s, got %s", tt.expectedGroup, resp.TargetGroup)
			}
		})
	}
}

func TestMemoryConfigStore(t *testing.T) {
	store := NewMemoryConfigStore()

	data := `{"enabled":true,"simple_threshold":2000}`
	err := store.Save([]byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(loaded) != data {
		t.Errorf("expected %s, got %s", data, string(loaded))
	}
}

func TestUpdateFromJSON(t *testing.T) {
	manager := NewConfigManager(NewMemoryConfigStore())

	jsonStr := `{
		"enabled": true,
		"simple_threshold": 3000,
		"complex_threshold": 9000,
		"group_mapping": {
			"deepseek": {
				"simple_group": "deepseek-lite",
				"medium_group": "deepseek-pro",
				"complex_group": "deepseek-max"
			}
		}
	}`

	err := manager.UpdateFromJSON(jsonStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := manager.GetConfig()
	if !cfg.Enabled {
		t.Error("expected Enabled to be true")
	}
	if cfg.SimpleThreshold != 3000 {
		t.Errorf("expected 3000, got %d", cfg.SimpleThreshold)
	}
}

func TestGetConfigJSON(t *testing.T) {
	manager := NewConfigManager(NewMemoryConfigStore())
	manager.Save(&RouteConfig{
		Enabled:          true,
		SimpleThreshold:  2000,
		ComplexThreshold: 8000,
	})

	jsonStr := manager.GetConfigJSON()

	var cfg RouteConfig
	err := json.Unmarshal([]byte(jsonStr), &cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.Enabled {
		t.Error("expected Enabled to be true")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	store := NewMemoryConfigStore()
	manager := NewConfigManager(store)

	original := &RouteConfig{
		Enabled:          true,
		SimpleThreshold:  2500,
		ComplexThreshold: 9000,
		GroupMapping: map[string]GroupMapping{
			"gpt-4o": {
				SimpleGroup:  "lite",
				MediumGroup:  "pro",
				ComplexGroup: "max",
			},
			"claude": {
				SimpleGroup:  "haiku",
				MediumGroup:  "sonnet",
				ComplexGroup: "opus",
			},
		},
	}

	err := manager.Save(original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded := manager.GetConfig()

	if loaded.Enabled != original.Enabled {
		t.Errorf("expected Enabled %v, got %v", original.Enabled, loaded.Enabled)
	}
	if loaded.SimpleThreshold != original.SimpleThreshold {
		t.Errorf("expected SimpleThreshold %d, got %d", original.SimpleThreshold, loaded.SimpleThreshold)
	}
	if len(loaded.GroupMapping) != len(original.GroupMapping) {
		t.Errorf("expected %d mappings, got %d", len(original.GroupMapping), len(loaded.GroupMapping))
	}
}
