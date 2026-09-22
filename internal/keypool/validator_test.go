package keypool

import (
	"testing"

	"autogateway/internal/models"
)

func TestResolveProbeModel(t *testing.T) {
	t.Run("no rules pass the name through", func(t *testing.T) {
		g := &models.Group{Name: "plain"}
		got, err := resolveProbeModel(g, "gpt-4o")
		if err != nil || got != "gpt-4o" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("redirect rule maps the exposed name to the upstream name", func(t *testing.T) {
		g := &models.Group{Name: "aliased"}
		g.ModelRedirectMap = map[string]string{"fast": "gpt-4o-mini"}
		got, err := resolveProbeModel(g, "fast")
		if err != nil || got != "gpt-4o-mini" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("non-strict group probes unknown names as-is", func(t *testing.T) {
		g := &models.Group{Name: "mixed"}
		g.ModelRedirectMap = map[string]string{"fast": "gpt-4o-mini"}
		got, err := resolveProbeModel(g, "gpt-4o")
		if err != nil || got != "gpt-4o" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("strict group rejects names outside its rules", func(t *testing.T) {
		g := &models.Group{Name: "strict", ModelRedirectStrict: true}
		g.ModelRedirectMap = map[string]string{"fast": "gpt-4o-mini"}
		if _, err := resolveProbeModel(g, "gpt-4o"); err == nil {
			t.Fatal("expected an error for a model the upstream never serves")
		}
	})

	t.Run("strict group with no rules still passes through", func(t *testing.T) {
		g := &models.Group{Name: "strict-empty", ModelRedirectStrict: true}
		got, err := resolveProbeModel(g, "gpt-4o")
		if err != nil || got != "gpt-4o" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})
}
