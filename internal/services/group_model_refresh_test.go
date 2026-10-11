package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"autogateway/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// newModelRefreshTestService 构造一个只依赖 DB + registry 的 GroupService,
// 上游用 httptest 假扮. registry 的 providerMeta.apiBaseUrl 指向假上游,
// 让 host 反查能命中 (extractHost 会去掉端口).
func newModelRefreshTestService(t *testing.T, upstreamURL string, regModels []FreeModelMeta) (*GroupService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Group{}, &models.APIKey{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	reg := &FreeModelsRegistry{}
	env := freeModelsEnvelope{Models: regModels}
	if upstreamURL != "" {
		env.ProviderMeta = map[string]FreeProviderMeta{
			"groq": {Name: "groq", DisplayName: "Groq Cloud", APIBaseURL: upstreamURL},
		}
	}
	reg.replaceIndex(env)
	svc := NewGroupService(db, nil, &GroupManager{}, nil, nil, nil, nil, reg)
	return svc, db
}

// seedGroqGroup 建一个 standard/openai 分组, 可带一个 active key.
func seedGroup(t *testing.T, db *gorm.DB, upstreamURL string, withKey bool) uint {
	t.Helper()
	upstreams, err := json.Marshal([]map[string]any{{"url": upstreamURL, "weight": 1}})
	if err != nil {
		t.Fatalf("marshal upstreams: %v", err)
	}
	group := models.Group{
		Name:        "groq",
		DisplayName: "Groq Cloud",
		GroupType:   "standard",
		ChannelType: "openai",
		Upstreams:   upstreams,
	}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if withKey {
		key := models.APIKey{GroupID: group.ID, KeyValue: "gsk_test", Status: models.KeyStatusActive}
		if err := db.Create(&key).Error; err != nil {
			t.Fatalf("create key: %v", err)
		}
	}
	return group.ID
}

func registryGroqModels() []FreeModelMeta {
	return []FreeModelMeta{
		{Provider: "groq", ModelID: "llama-3.3-70b-versatile", IsFree: true},
		{Provider: "groq", ModelID: "openai/gpt-oss-120b", IsFree: true},
		{Provider: "groq", ModelID: "groq/premium-only", IsFree: false},
	}
}

func readGroupModels(t *testing.T, db *gorm.DB, groupID uint) (models.Group, []string, string) {
	t.Helper()
	var group models.Group
	if err := db.First(&group, groupID).Error; err != nil {
		t.Fatalf("reload group: %v", err)
	}
	var ids []string
	if len(group.AvailableModels) > 0 {
		if err := json.Unmarshal(group.AvailableModels, &ids); err != nil {
			t.Fatalf("unmarshal available_models: %v", err)
		}
	}
	return group, ids, group.ModelsSource
}

// 上游 /v1/models 正常时: 清单来自上游, 来源标 upstream, free 子集求交.
func TestRefreshModels_UpstreamHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "llama-3.3-70b-versatile"}, {"id": "not-in-registry"}},
		})
	}))
	defer srv.Close()

	svc, db := newModelRefreshTestService(t, srv.URL, registryGroqModels())
	id := seedGroup(t, db, srv.URL, true)

	res, err := svc.RefreshModelsWithSource(context.Background(), id, "")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.Source != ModelsSourceUpstream {
		t.Fatalf("source = %q, want %q", res.Source, ModelsSourceUpstream)
	}
	if len(res.Models) != 2 {
		t.Fatalf("models = %v, want 2 entries", res.Models)
	}

	group, persisted, source := readGroupModels(t, db, id)
	if len(persisted) != 2 || source != ModelsSourceUpstream {
		t.Fatalf("persisted = %v/%q, want 2 entries/upstream", persisted, source)
	}
	var free []string
	if err := json.Unmarshal(group.FreeModels, &free); err != nil {
		t.Fatalf("unmarshal free_models: %v", err)
	}
	if len(free) != 1 || free[0] != "llama-3.3-70b-versatile" {
		t.Fatalf("free_models = %v, want only the registry-known free id", free)
	}
}

// 上游 401 (key 被拒) 时: 过去直接 500, 现在用 FreeModels 兜底并标来源.
func TestRefreshModels_FallbackOnUpstream401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	svc, db := newModelRefreshTestService(t, srv.URL, registryGroqModels())
	id := seedGroup(t, db, srv.URL, true)

	res, err := svc.RefreshModelsWithSource(context.Background(), id, "")
	if err != nil {
		t.Fatalf("refresh should fall back to registry, got err: %v", err)
	}
	if res.Source != ModelsSourceFreeModels {
		t.Fatalf("source = %q, want %q", res.Source, ModelsSourceFreeModels)
	}
	if len(res.Models) != 3 {
		t.Fatalf("models = %v, want all 3 registry ids (free + paid)", res.Models)
	}
	if _, _, source := readGroupModels(t, db, id); source != ModelsSourceFreeModels {
		t.Fatalf("persisted source = %q, want %q", source, ModelsSourceFreeModels)
	}
}

// 没有活跃 key (全 invalid) 时: 不再 400 中止, 直接走 FreeModels 兜底.
func TestRefreshModels_FallbackWhenNoActiveKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called without an active key")
	}))
	defer srv.Close()

	svc, db := newModelRefreshTestService(t, srv.URL, registryGroqModels())
	id := seedGroup(t, db, srv.URL, false)

	res, err := svc.RefreshModelsWithSource(context.Background(), id, "")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.Source != ModelsSourceFreeModels || len(res.Models) != 3 {
		t.Fatalf("res = %+v, want 3 registry models from freemodels source", res)
	}
}

// registry 不认识这个 host 时: 兜底也拿不到清单, 原错误继续上报, 旧缓存保留.
func TestRefreshModels_RegistryMissKeepsOldCacheAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`forbidden`))
	}))
	defer srv.Close()

	svc, db := newModelRefreshTestService(t, "https://api.groq.com/openai/v1", registryGroqModels())
	// 分组指向另一个未收录的 host (srv), 与 registry 的 groq host 不匹配.
	upstreams, _ := json.Marshal([]map[string]any{{"url": srv.URL, "weight": 1}})
	group := models.Group{
		Name: "unknown-relay", GroupType: "standard", ChannelType: "openai",
		Upstreams:       upstreams,
		AvailableModels: datatypes.JSON(`["stale-model"]`),
		ModelsSource:    ModelsSourceUpstream,
	}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Create(&models.APIKey{GroupID: group.ID, KeyValue: "k", Status: models.KeyStatusActive}).Error; err != nil {
		t.Fatalf("create key: %v", err)
	}

	_, err := svc.RefreshModelsWithSource(context.Background(), group.ID, "")
	if err == nil {
		t.Fatal("want error when neither upstream nor registry can supply the list")
	}
	if _, persisted, source := readGroupModels(t, db, group.ID); len(persisted) != 1 || persisted[0] != "stale-model" || source != ModelsSourceUpstream {
		t.Fatalf("cache mutated on failure: %v/%q", persisted, source)
	}
}

// 显式指定 source=freemodels: 即便上游可用也用 FreeModels 清单.
func TestRefreshModels_ExplicitFreeModelsSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called when source=freemodels")
	}))
	defer srv.Close()

	svc, db := newModelRefreshTestService(t, srv.URL, registryGroqModels())
	id := seedGroup(t, db, srv.URL, true)

	res, err := svc.RefreshModelsWithSource(context.Background(), id, ModelsSourceFreeModels)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.Source != ModelsSourceFreeModels || len(res.Models) != 3 {
		t.Fatalf("res = %+v, want 3 registry models", res)
	}
}

// 显式 source 不可用 (registry 无此 provider) 时报错, 不静默回落到上游.
func TestRefreshModels_ExplicitSourceMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called when source=freemodels")
	}))
	defer srv.Close()

	svc, db := newModelRefreshTestService(t, "https://api.groq.com/openai/v1", registryGroqModels())
	upstreams, _ := json.Marshal([]map[string]any{{"url": srv.URL, "weight": 1}})
	group := models.Group{Name: "other", GroupType: "standard", ChannelType: "openai", Upstreams: upstreams}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}

	if _, err := svc.RefreshModelsWithSource(context.Background(), group.ID, ModelsSourceFreeModels); err == nil {
		t.Fatal("want error when registry has no such provider")
	}
}

// 聚合分组没有独立上游, 任何 source 都拒绝.
func TestRefreshModels_AggregateGroupRejected(t *testing.T) {
	svc, db := newModelRefreshTestService(t, "https://api.groq.com/openai/v1", registryGroqModels())
	group := models.Group{Name: "agg", GroupType: "aggregate", ChannelType: "openai", Upstreams: datatypes.JSON("[]")}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if _, err := svc.RefreshModelsWithSource(context.Background(), group.ID, ""); err == nil {
		t.Fatal("aggregate group must be rejected")
	}
}

// 旧入口保持兼容: 返回签名不变, 上游成功时行为与老版本一致.
func TestRefreshModels_LegacyWrapperKeepsSignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "llama-3.1-8b-instant"}},
		})
	}))
	defer srv.Close()

	svc, db := newModelRefreshTestService(t, srv.URL, registryGroqModels())
	id := seedGroup(t, db, srv.URL, true)

	ids, err := svc.RefreshAvailableModels(context.Background(), id)
	if err != nil {
		t.Fatalf("legacy refresh: %v", err)
	}
	if len(ids) != 1 || ids[0] != "llama-3.1-8b-instant" {
		t.Fatalf("ids = %v", ids)
	}
}
