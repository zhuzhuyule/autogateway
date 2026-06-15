package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeOpenAIStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusUnauthorized) // 401 = endpoint exists, just no auth
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := ProbeUpstream(ctx, srv.URL, "")
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "openai" {
		t.Errorf("expected channel openai, got %q", got.ChannelType)
	}
	if got.VersionPrefix != "/v1" {
		t.Errorf("expected version prefix /v1, got %q", got.VersionPrefix)
	}
}

func TestProbeAnthropicStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages" && r.Header.Get("anthropic-version") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "anthropic" {
		t.Errorf("expected channel anthropic, got %q", got.ChannelType)
	}
}

func TestProbeGeminiStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta/models" {
			w.WriteHeader(http.StatusForbidden) // gemini returns 403 when key missing
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "gemini" {
		t.Errorf("expected gemini, got %q", got.ChannelType)
	}
	if got.VersionPrefix != "/v1beta" {
		t.Errorf("expected /v1beta, got %q", got.VersionPrefix)
	}
}

// 全协议未命中不再返回 error — 而是返回 200 + 空 channel_type,
// 让前端 UI 显示 ⚠ unknown, 避免误触全局 toast.
func TestProbeUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("expected nil error for unknown upstream, got %v", err)
	}
	if got == nil {
		t.Fatalf("expected non-nil result for unknown upstream")
	}
	if got.ChannelType != "" {
		t.Errorf("expected empty channel_type, got %q", got.ChannelType)
	}
	if got.NormalizedURL != srv.URL {
		t.Errorf("expected normalized url %q, got %q", srv.URL, got.NormalizedURL)
	}
}

func TestProbeRejectsNonHTTP(t *testing.T) {
	_, err := ProbeUpstream(context.Background(), "file:///etc/passwd", "")
	if err == nil {
		t.Fatalf("expected scheme rejection, got nil")
	}
}

// 多协议 provider (e.g. 智谱 bigmodel): /v1/messages 与 /v1/models 都返回 401,
// 默认 rank 会落到 anthropic. 调用方传 prefer="openai" 应优先返回 openai.
func TestProbePreferOverridesRank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL, "openai")
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "openai" {
		t.Errorf("expected openai (prefer override), got %q", got.ChannelType)
	}
}

// baseUrl 已带版本段时 (e.g. https://ai.gitee.com/v1), probe 应剥掉再拼,
// 不能拼成 /v1/v1/models 然后全协议 404.
func TestProbeBaseUrlWithVersionSegment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL+"/v1", "openai")
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "openai" {
		t.Errorf("expected openai for base+/v1, got %q", got.ChannelType)
	}
	if got.NormalizedURL != srv.URL+"/v1" {
		t.Errorf("expected normalized url to preserve original, got %q", got.NormalizedURL)
	}
}

// /v1beta 后缀也要被剥掉, 避免 gemini probe 拼成 /v1beta/v1beta/models.
func TestProbeBaseUrlWithV1betaSegment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta/models" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL+"/v1beta", "")
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "gemini" {
		t.Errorf("expected gemini for base+/v1beta, got %q", got.ChannelType)
	}
}

// ---------- ProbeUpstreamModels tests ----------

func TestProbeUpstreamModels(t *testing.T) {
	t.Run("OpenAI returns data[].id list", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		got, err := ProbeUpstreamModels(context.Background(), srv.URL, "openai", "test-key")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 models, got %d: %v", len(got), got)
		}
		want := map[string]bool{"gpt-4o": true, "gpt-4o-mini": true}
		for _, id := range got {
			if !want[id] {
				t.Errorf("unexpected model id %q", id)
			}
		}
	})

	t.Run("Gemini returns models[].name stripped of models/ prefix", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1beta/models" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash"},{"name":"models/gemini-2.0-flash"}]}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		got, err := ProbeUpstreamModels(context.Background(), srv.URL, "gemini", "test-key")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 models, got %d: %v", len(got), got)
		}
		want := map[string]bool{"gemini-2.5-flash": true, "gemini-2.0-flash": true}
		for _, id := range got {
			if !want[id] {
				t.Errorf("unexpected model id %q", id)
			}
		}
	})

	t.Run("upstream 401 returns non-nil error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		_, err := ProbeUpstreamModels(context.Background(), srv.URL, "openai", "bad-key")
		if err == nil {
			t.Fatal("expected error for 401 response, got nil")
		}
	})

	t.Run("base_url with version tail is normalised", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		// Pass base URL already ending in /v1 — should not become /v1/v1/models
		got, err := ProbeUpstreamModels(context.Background(), srv.URL+"/v1", "openai", "k")
		if err != nil {
			t.Fatalf("unexpected error with version-suffixed base url: %v", err)
		}
		if len(got) != 1 || got[0] != "gpt-4o" {
			t.Errorf("expected [gpt-4o], got %v", got)
		}
	})
}

func TestProbeRealAnthropicShape(t *testing.T) {
	// Real api.anthropic.com returns 401 to /v1/models AND /v1/messages.
	// /v1/messages is the anthropic-exclusive disambiguator.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("ProbeUpstream returned error: %v", err)
	}
	if got.ChannelType != "anthropic" {
		t.Errorf("expected anthropic (since /v1/messages exists), got %q", got.ChannelType)
	}
}
