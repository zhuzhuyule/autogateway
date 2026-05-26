package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestUpgradeSvc(t *testing.T) (*UpgradeService, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".upgrade-request")
	return &UpgradeService{signalPath: path}, path
}

func TestUpgradeService_RejectsBadVersion(t *testing.T) {
	svc, _ := newTestUpgradeSvc(t)
	cases := []string{"", "garbage", "v1", "v1.2", "1.2.3.4-beta", "1.x.y"}
	for _, v := range cases {
		err := svc.RequestUpgrade(UpgradeRequest{TargetVersion: v, RequestedBy: "test"})
		if err == nil {
			t.Errorf("expected error for invalid version %q, got nil", v)
		}
	}
}

func TestUpgradeService_RejectsDowngrade(t *testing.T) {
	svc, _ := newTestUpgradeSvc(t)
	// Test assumes version.Version = "v2.4.10".
	err := svc.RequestUpgrade(UpgradeRequest{TargetVersion: "v2.4.9", RequestedBy: "test"})
	if err == nil {
		t.Error("expected downgrade rejection, got nil")
	} else if !strings.Contains(err.Error(), "downgrade") {
		t.Errorf("expected downgrade error, got %v", err)
	}
}

func TestUpgradeService_AcceptsUpgrade(t *testing.T) {
	svc, path := newTestUpgradeSvc(t)
	err := svc.RequestUpgrade(UpgradeRequest{TargetVersion: "v99.0.0", RequestedBy: "test"})
	if err != nil {
		t.Fatalf("RequestUpgrade failed: %v", err)
	}
	// 信号文件应存在
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected signal file at %s, got %v", path, err)
	}
}

func TestUpgradeService_RejectsConcurrentPending(t *testing.T) {
	svc, _ := newTestUpgradeSvc(t)
	if err := svc.RequestUpgrade(UpgradeRequest{TargetVersion: "v99.0.0", RequestedBy: "first"}); err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	err := svc.RequestUpgrade(UpgradeRequest{TargetVersion: "v99.0.1", RequestedBy: "second"})
	if err == nil {
		t.Error("expected pending rejection on second request, got nil")
	} else if !strings.Contains(err.Error(), "pending") {
		t.Errorf("expected 'pending' error, got %v", err)
	}
}

func TestUpgradeService_Status(t *testing.T) {
	svc, _ := newTestUpgradeSvc(t)

	if s := svc.Status(); s.Pending {
		t.Error("expected no pending before request")
	}

	_ = svc.RequestUpgrade(UpgradeRequest{
		TargetVersion: "v99.0.0",
		RequestedBy:   "tester",
		RequestedAt:   time.Now().Add(-3 * time.Second),
	})
	s := svc.Status()
	if !s.Pending {
		t.Fatal("expected pending after request")
	}
	if s.Request == nil || s.Request.TargetVersion != "v99.0.0" {
		t.Errorf("unexpected request payload: %+v", s.Request)
	}
	if s.WaitingSecs < 2 {
		t.Errorf("expected waiting_secs ≥ 2, got %d", s.WaitingSecs)
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		sign int // -1, 0, 1
	}{
		{"v2.4.10", "v2.4.9", 1},
		{"v2.4.9", "v2.4.10", -1},
		{"v2.4.10", "v2.4.10", 0},
		{"v3.0.0", "v2.4.10", 1},
		{"v2.4.10", "v3.0.0", -1},
		{"2.4.10", "v2.4.10", 0},
	}
	for _, c := range cases {
		got := compareSemver(c.a, c.b)
		switch {
		case c.sign > 0 && got <= 0:
			t.Errorf("compareSemver(%q, %q) = %d, expected positive", c.a, c.b, got)
		case c.sign < 0 && got >= 0:
			t.Errorf("compareSemver(%q, %q) = %d, expected negative", c.a, c.b, got)
		case c.sign == 0 && got != 0:
			t.Errorf("compareSemver(%q, %q) = %d, expected 0", c.a, c.b, got)
		}
	}
}
