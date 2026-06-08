package store

import (
	"testing"
	"time"
)

func TestMemoryIncrWithTTLAndGetInt(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	if n, _ := s.GetInt("c"); n != 0 {
		t.Fatalf("absent key GetInt = %d, want 0", n)
	}
	v1, err := s.IncrWithTTL("c", time.Minute)
	if err != nil || v1 != 1 {
		t.Fatalf("first IncrWithTTL = (%d,%v), want (1,nil)", v1, err)
	}
	v2, _ := s.IncrWithTTL("c", time.Minute)
	if v2 != 2 {
		t.Fatalf("second IncrWithTTL = %d, want 2", v2)
	}
	if n, _ := s.GetInt("c"); n != 2 {
		t.Fatalf("GetInt = %d, want 2", n)
	}
}

func TestMemoryIncrWithTTLExpiry(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()
	if _, err := s.IncrWithTTL("c", 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if n, _ := s.GetInt("c"); n != 0 {
		t.Fatalf("after expiry GetInt = %d, want 0", n)
	}
	v, _ := s.IncrWithTTL("c", time.Minute)
	if v != 1 {
		t.Fatalf("post-expiry IncrWithTTL = %d, want 1 (reset)", v)
	}
}
