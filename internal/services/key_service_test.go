package services

import (
	"strings"
	"testing"
)

// TestKeyParseKeysWithNotes covers the `key,备注` input format used by the
// "add keys" flow: comma/tab separates key from an optional per-key note, while
// plain-key lines stay backward compatible (empty notes).
func TestKeyParseKeysWithNotes(t *testing.T) {
	s := &KeyService{}

	t.Run("key with comma note", func(t *testing.T) {
		keys, notes := s.parseKeysWithNotes("sk-abc,my label")
		if len(keys) != 1 || keys[0] != "sk-abc" {
			t.Fatalf("keys = %v, want [sk-abc]", keys)
		}
		if notes["sk-abc"] != "my label" {
			t.Fatalf("notes[sk-abc] = %q, want %q", notes["sk-abc"], "my label")
		}
	})

	t.Run("plain key line has empty note", func(t *testing.T) {
		keys, notes := s.parseKeysWithNotes("sk-plain")
		if len(keys) != 1 || keys[0] != "sk-plain" {
			t.Fatalf("keys = %v, want [sk-plain]", keys)
		}
		if _, ok := notes["sk-plain"]; ok {
			t.Fatalf("expected no note entry, got %v", notes)
		}
	})

	t.Run("note may contain spaces and commas", func(t *testing.T) {
		keys, notes := s.parseKeysWithNotes("sk-x, prod, us-east ")
		if len(keys) != 1 || keys[0] != "sk-x" {
			t.Fatalf("keys = %v, want [sk-x]", keys)
		}
		if notes["sk-x"] != "prod, us-east" {
			t.Fatalf("note = %q, want %q", notes["sk-x"], "prod, us-east")
		}
	})

	t.Run("tab separator", func(t *testing.T) {
		keys, notes := s.parseKeysWithNotes("sk-tab\tlabel here")
		if len(keys) != 1 || keys[0] != "sk-tab" {
			t.Fatalf("keys = %v, want [sk-tab]", keys)
		}
		if notes["sk-tab"] != "label here" {
			t.Fatalf("note = %q, want %q", notes["sk-tab"], "label here")
		}
	})

	t.Run("mixed lines", func(t *testing.T) {
		keys, notes := s.parseKeysWithNotes("sk-1,label1\nsk-2\nsk-3,label3")
		want := []string{"sk-1", "sk-2", "sk-3"}
		if len(keys) != len(want) {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
		for i, w := range want {
			if keys[i] != w {
				t.Fatalf("keys[%d] = %q, want %q", i, keys[i], w)
			}
		}
		if notes["sk-1"] != "label1" || notes["sk-3"] != "label3" {
			t.Fatalf("notes = %v", notes)
		}
		if _, ok := notes["sk-2"]; ok {
			t.Fatalf("sk-2 should carry no note, got %v", notes)
		}
	})

	t.Run("note truncated to 255 runes", func(t *testing.T) {
		long := strings.Repeat("あ", 300)
		keys, notes := s.parseKeysWithNotes("sk-long," + long)
		if len(keys) != 1 {
			t.Fatalf("keys = %v", keys)
		}
		if got := len([]rune(notes["sk-long"])); got != 255 {
			t.Fatalf("note rune length = %d, want 255", got)
		}
	})

	t.Run("legacy whitespace separated keys on one line", func(t *testing.T) {
		keys, notes := s.parseKeysWithNotes("sk-a sk-b sk-c")
		if len(keys) != 3 {
			t.Fatalf("keys = %v, want 3 keys", keys)
		}
		if len(notes) != 0 {
			t.Fatalf("expected no notes, got %v", notes)
		}
	})
}
