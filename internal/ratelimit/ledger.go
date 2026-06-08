// Package ratelimit 实现 (group,key) 维度的固定窗口 RPM/RPD 准入账本。
package ratelimit

import (
	"fmt"
	"time"

	"autogateway/internal/store"
)

// Limits 是一个 (group,key) 维度的上限快照（<=0 = 该维不限）。
type Limits struct {
	RPM int
	RPD int
}

// IsZero 报告是否完全不限流（两维都 <=0）。
func (l Limits) IsZero() bool { return l.RPM <= 0 && l.RPD <= 0 }

// Ledger 基于 store 的固定窗口计数器做准入与记账。
type Ledger struct {
	store store.Store
	now   func() time.Time
}

func NewLedger(s store.Store) *Ledger {
	return &Ledger{store: s, now: time.Now}
}

func (l *Ledger) rpmKey(groupID, keyID uint) string {
	return fmt.Sprintf("rl:rpm:%d:%d:%d", groupID, keyID, l.now().Unix()/60)
}

func (l *Ledger) rpdKey(groupID, keyID uint) string {
	return fmt.Sprintf("rl:rpd:%d:%d:%s", groupID, keyID, l.now().UTC().Format("20060102"))
}

// Allow 检查当前 RPM/RPD 窗口是否仍有余量。limits.IsZero() 直接放行。
func (l *Ledger) Allow(groupID, keyID uint, limits Limits) (bool, error) {
	if limits.IsZero() {
		return true, nil
	}
	if limits.RPM > 0 {
		n, err := l.store.GetInt(l.rpmKey(groupID, keyID))
		if err != nil {
			return false, err
		}
		if int(n) >= limits.RPM {
			return false, nil
		}
	}
	if limits.RPD > 0 {
		n, err := l.store.GetInt(l.rpdKey(groupID, keyID))
		if err != nil {
			return false, err
		}
		if int(n) >= limits.RPD {
			return false, nil
		}
	}
	return true, nil
}

// Record 对启用的维度各 +1（预占）。limits.IsZero() 时 no-op。
func (l *Ledger) Record(groupID, keyID uint, limits Limits) error {
	if limits.IsZero() {
		return nil
	}
	if limits.RPM > 0 {
		if _, err := l.store.IncrWithTTL(l.rpmKey(groupID, keyID), 120*time.Second); err != nil {
			return err
		}
	}
	if limits.RPD > 0 {
		if _, err := l.store.IncrWithTTL(l.rpdKey(groupID, keyID), 26*time.Hour); err != nil {
			return err
		}
	}
	return nil
}
