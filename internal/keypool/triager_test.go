package keypool

import (
	"context"
	"testing"

	"autogateway/internal/models"
	"autogateway/internal/types"
)

// fakeTriager 是 ErrorTriager 的测试替身,返回预置的 (count, ok)。
type fakeTriager struct {
	count  bool
	ok     bool
	called bool
}

func (f *fakeTriager) ShouldCountAgainstKey(_ context.Context, _ *models.Group, _ int, _ string) (bool, bool) {
	f.called = true
	return f.count, f.ok
}

func groupWith(triageEnabled bool) *models.Group {
	return &models.Group{
		EffectiveConfig: types.SystemSettings{EnableLLMErrorTriage: triageEnabled},
	}
}

func TestShouldCountFailure_RulesOnly(t *testing.T) {
	p := &KeyProvider{} // triager nil
	cases := []struct {
		name   string
		status int
		msg    string
		want   bool
	}{
		{"audio format 400 not counted", 400, "Invalid audio file. Format not recognised.", false},
		{"401 auth counted", 401, "Unauthorized", true},
		{"429 rate limit not counted", 429, "Rate limit reached", false},
		{"500 provider not counted", 500, "Internal Server Error", false},
		{"unknown conservatively counted", 0, "some weird transport error", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.shouldCountFailure(groupWith(false), tc.status, tc.msg); got != tc.want {
				t.Errorf("shouldCountFailure(%d, %q) = %v, want %v", tc.status, tc.msg, got, tc.want)
			}
		})
	}
}

func TestShouldCountFailure_LLMTriage(t *testing.T) {
	const unknownStatus, unknownMsg = 0, "some weird transport error" // → CategoryUnknown

	t.Run("opt-in + LLM says don't count → false", func(t *testing.T) {
		ft := &fakeTriager{count: false, ok: true}
		p := &KeyProvider{triager: ft}
		if got := p.shouldCountFailure(groupWith(true), unknownStatus, unknownMsg); got != false {
			t.Errorf("got %v, want false", got)
		}
		if !ft.called {
			t.Error("triager should have been consulted")
		}
	})

	t.Run("opt-in + LLM says count → true", func(t *testing.T) {
		ft := &fakeTriager{count: true, ok: true}
		p := &KeyProvider{triager: ft}
		if got := p.shouldCountFailure(groupWith(true), unknownStatus, unknownMsg); got != true {
			t.Errorf("got %v, want true", got)
		}
	})

	t.Run("opt-in + LLM can't decide (ok=false) → conservative true", func(t *testing.T) {
		ft := &fakeTriager{count: false, ok: false}
		p := &KeyProvider{triager: ft}
		if got := p.shouldCountFailure(groupWith(true), unknownStatus, unknownMsg); got != true {
			t.Errorf("got %v, want true (fallback)", got)
		}
	})

	t.Run("opt-out → triager NOT consulted, conservative true", func(t *testing.T) {
		ft := &fakeTriager{count: false, ok: true}
		p := &KeyProvider{triager: ft}
		if got := p.shouldCountFailure(groupWith(false), unknownStatus, unknownMsg); got != true {
			t.Errorf("got %v, want true", got)
		}
		if ft.called {
			t.Error("triager must not be consulted when opt-out")
		}
	})

	t.Run("definite key_error never consults LLM (saves a call)", func(t *testing.T) {
		ft := &fakeTriager{count: false, ok: true}
		p := &KeyProvider{triager: ft}
		// 401 → CategoryKeyError → 直接计入, 不问 LLM。
		if got := p.shouldCountFailure(groupWith(true), 401, "unauthorized"); got != true {
			t.Errorf("got %v, want true", got)
		}
		if ft.called {
			t.Error("triager must not be consulted for a definite key_error")
		}
	})
}
