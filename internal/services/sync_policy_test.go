package services

import (
	"context"
	"testing"

	"autogateway/internal/models"

	"gorm.io/datatypes"
)

func TestDefaultSyncPolicy_ExcludesLocalFields(t *testing.T) {
	p := DefaultSyncPolicy()
	if !p.IsFieldExcluded("group", "proxy_url") {
		t.Fatal("group.proxy_url 应默认排除")
	}
	if !p.IsFieldExcluded("setting", "app_url") {
		t.Fatal("setting.app_url 应默认排除")
	}
	if !p.IsFieldExcluded("setting", "sync_policy") {
		t.Fatal("setting.sync_policy 应默认排除(防 follower 反向覆盖 master 规则)")
	}
	// 未列入的字段默认同步(不排除)
	if p.IsFieldExcluded("group", "channel_type") {
		t.Fatal("group.channel_type 不应排除")
	}
}

func TestSyncPolicy_CategoryExcluded(t *testing.T) {
	p := &SyncPolicy{ExcludedCategories: []string{"setting"}}
	if !p.IsCategoryExcluded("setting") {
		t.Fatal("setting 类别应排除")
	}
	if p.IsCategoryExcluded("key") {
		t.Fatal("key 类别不应排除")
	}
}

func TestSyncPolicy_NilSafe(t *testing.T) {
	var p *SyncPolicy // nil = 全同步
	if p.IsCategoryExcluded("group") || p.IsFieldExcluded("group", "proxy_url") {
		t.Fatal("nil policy 应视为全同步(不排除任何东西)")
	}
}

func TestPreserveExcludedGroupFields(t *testing.T) {
	policy := DefaultSyncPolicy()
	incoming := &models.Group{Name: "g", Config: datatypes.JSONMap{"proxy_url": "http://master-proxy", "channel_type": "openai"}}
	existing := &models.Group{Name: "g", Config: datatypes.JSONMap{"proxy_url": "http://local-proxy"}}

	preserveExcludedGroupFields(incoming, existing, policy)

	if incoming.Config["proxy_url"] != "http://local-proxy" {
		t.Fatalf("proxy_url 应保留本地, got %v", incoming.Config["proxy_url"])
	}
	if incoming.Config["channel_type"] != "openai" {
		t.Fatal("非排除字段应跟随 master")
	}
}

func TestPreserveExcludedGroupFields_LocalMissing(t *testing.T) {
	policy := DefaultSyncPolicy()
	incoming := &models.Group{Name: "g", Config: datatypes.JSONMap{"proxy_url": "http://master-proxy"}}
	existing := &models.Group{Name: "g", Config: datatypes.JSONMap{}} // 本地没配 proxy

	preserveExcludedGroupFields(incoming, existing, policy)

	// 本地没值 → 删掉 incoming 的, 不从 master 继承本机字段
	if _, ok := incoming.Config["proxy_url"]; ok {
		t.Fatal("本地无 proxy_url 时应删除 incoming 的, 不继承 master")
	}
}

func TestSaveLoadSyncPolicy_RoundTrip(t *testing.T) {
	s, _ := newTestSyncService(t)
	ctx := context.Background()
	p := &SyncPolicy{ExcludedCategories: []string{"alias"}, ExcludedFields: map[string][]string{"group": {"proxy_url"}}}
	if err := s.SaveSyncPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	got := s.LoadSyncPolicy(ctx)
	if !got.IsCategoryExcluded("alias") || !got.IsFieldExcluded("group", "proxy_url") {
		t.Fatal("round-trip 丢失 policy")
	}
}

func TestLoadSyncPolicy_DefaultWhenAbsent(t *testing.T) {
	s, _ := newTestSyncService(t)
	got := s.LoadSyncPolicy(context.Background())
	if !got.IsFieldExcluded("group", "proxy_url") {
		t.Fatal("无存储时应回退默认 policy")
	}
}
