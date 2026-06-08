package router_engine

import (
	"net/http"
	"testing"

	"autogateway/internal/store"
)

func TestParseSessionHeaderPriority(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Id", "abc")
	body := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`)
	key, multi := parseSession(body, h)
	if key != "hdr:abc" {
		t.Fatalf("sessionKey = %q, want hdr:abc", key)
	}
	if !multi {
		t.Fatal("should be multi-turn (has assistant)")
	}
}

func TestParseSessionFallbackSHA1Stable(t *testing.T) {
	body1 := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	body2 := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`)
	k1, m1 := parseSession(body1, http.Header{})
	k2, m2 := parseSession(body2, http.Header{})
	if k1 != k2 {
		t.Fatalf("first-user-msg SHA1 should be stable: %q vs %q", k1, k2)
	}
	if m1 {
		t.Fatal("single user msg → not multi-turn")
	}
	if !m2 {
		t.Fatal("has assistant → multi-turn")
	}
	if k1 == "" {
		t.Fatal("sessionKey should not be empty")
	}
}

func TestParseSessionArrayContent(t *testing.T) {
	k, _ := parseSession([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`), http.Header{})
	kStr, _ := parseSession([]byte(`{"messages":[{"role":"user","content":"hello"}]}`), http.Header{})
	if k != kStr {
		t.Fatalf("array-of-blocks content should hash same as string: %q vs %q", k, kStr)
	}
}

func TestStickyStoreKeyVariesByModel(t *testing.T) {
	a := stickyStoreKey("msg:x", "auto")
	b := stickyStoreKey("msg:x", "gpt-4o")
	if a == b {
		t.Fatal("different model should yield different key")
	}
	if a != stickyStoreKey("msg:x", "auto") {
		t.Fatal("same inputs should be stable")
	}
}

// TestStickyRoundTrip 集成链路：parseSession → stickyStoreKey → SetSticky → GetSticky。
// 不跑真实 Gin context；仅验证 session key 推导链路正确。
func TestStickyRoundTrip(t *testing.T) {
	st := store.NewMemoryStore()
	s := &Selector{
		cooldown:  newCooldownStore(),
		swrrState: newSWRRStateMap(),
		settings:  DefaultSettings(),
		store:     st,
	}

	// 多轮 body
	body := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`)
	sessionKey, isMultiTurn := parseSession(body, http.Header{})
	if !isMultiTurn {
		t.Fatal("expect multi-turn")
	}
	if sessionKey == "" {
		t.Fatal("expect non-empty sessionKey")
	}

	sk := stickyStoreKey(sessionKey, "auto")
	cand := &Candidate{GroupID: 7, RealModel: "gpt-4o", Alias: "auto", Weight: 1}
	s.SetSticky(sk, cand)

	got := s.GetSticky(sk)
	if got == nil {
		t.Fatal("GetSticky should return stored candidate")
	}
	if got.GroupID != 7 || got.RealModel != "gpt-4o" {
		t.Fatalf("GetSticky = %+v, want GroupID=7 RealModel=gpt-4o", got)
	}

	// 同一 body 二次 parseSession 得到相同 key，能命中同一 sticky
	sk2 := stickyStoreKey(sessionKey, "auto")
	if sk != sk2 {
		t.Fatal("stickyStoreKey should be deterministic")
	}
	got2 := s.GetSticky(sk2)
	if got2 == nil || got2.GroupID != 7 {
		t.Fatalf("second GetSticky = %+v, want GroupID=7", got2)
	}
}
