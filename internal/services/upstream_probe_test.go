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
	got, err := ProbeUpstream(ctx, srv.URL)
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
		if r.URL.Path == "/v1/models" && r.Header.Get("anthropic-version") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := ProbeUpstream(context.Background(), srv.URL)
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

	got, err := ProbeUpstream(context.Background(), srv.URL)
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

func TestProbeUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := ProbeUpstream(context.Background(), srv.URL)
	if err == nil {
		t.Fatalf("expected error for unknown upstream, got nil")
	}
}

func TestProbeRejectsNonHTTP(t *testing.T) {
	_, err := ProbeUpstream(context.Background(), "file:///etc/passwd")
	if err == nil {
		t.Fatalf("expected scheme rejection, got nil")
	}
}
