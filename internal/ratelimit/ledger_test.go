package ratelimit

import (
	"testing"
	"time"

	"autogateway/internal/store"
)

func newAt(s store.Store, t time.Time) *Ledger {
	l := NewLedger(s)
	l.now = func() time.Time { return t }
	return l
}

func TestLedgerZeroLimitsAllowsAndSkipsRecord(t *testing.T) {
	s := store.NewMemoryStore()
	l := newAt(s, time.Unix(1_700_000_000, 0))
	ok, err := l.Allow(1, 1, Limits{})
	if err != nil || !ok {
		t.Fatalf("zero limits Allow = (%v,%v), want (true,nil)", ok, err)
	}
	if err := l.Record(1, 1, Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerRPMAdmitsThenBlocks(t *testing.T) {
	s := store.NewMemoryStore()
	l := newAt(s, time.Unix(1_700_000_000, 0))
	lim := Limits{RPM: 2}
	for i := 0; i < 2; i++ {
		ok, _ := l.Allow(1, 7, lim)
		if !ok {
			t.Fatalf("request %d should be allowed", i)
		}
		if err := l.Record(1, 7, lim); err != nil {
			t.Fatal(err)
		}
	}
	ok, _ := l.Allow(1, 7, lim)
	if ok {
		t.Fatal("3rd request should be blocked (RPM=2)")
	}
}

func TestLedgerRPMResetsNextMinute(t *testing.T) {
	s := store.NewMemoryStore()
	base := time.Unix(1_700_000_000, 0)
	l := newAt(s, base)
	lim := Limits{RPM: 1}
	l.Record(1, 7, lim)
	if ok, _ := l.Allow(1, 7, lim); ok {
		t.Fatal("should block within same minute")
	}
	l.now = func() time.Time { return base.Add(time.Minute) }
	if ok, _ := l.Allow(1, 7, lim); !ok {
		t.Fatal("should allow in next minute window")
	}
}

func TestLedgerRPDBlocks(t *testing.T) {
	s := store.NewMemoryStore()
	l := newAt(s, time.Unix(1_700_000_000, 0))
	lim := Limits{RPD: 1}
	l.Record(1, 7, lim)
	if ok, _ := l.Allow(1, 7, lim); ok {
		t.Fatal("should block when RPD reached")
	}
}
