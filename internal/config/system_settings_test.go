package config

import (
	"testing"

	"autogateway/internal/db"
	"autogateway/internal/models"
	"autogateway/internal/types"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupResetTestDB(t *testing.T) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.DB = gdb
}

func settingValue(t *testing.T, key string) string {
	t.Helper()
	var row models.SystemSetting
	if err := db.DB.Where("setting_key = ?", key).First(&row).Error; err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return row.SettingValue
}

// 一次性重置的完整语义: 首次应用 → 同值重启跳过 → UI 改动不被覆盖 → 换新值再触发。
func TestResetAuthKey_OneShotFingerprint(t *testing.T) {
	setupResetTestDB(t)
	sm := NewSystemSettingsManager()
	envAuth := types.AuthConfig{Key: "sk-env-initial"}

	// 首次初始化: auth_key = env 值 (RESET_AUTH_KEY 未设)
	if err := sm.EnsureSettingsInitialized(envAuth); err != nil {
		t.Fatalf("init: %v", err)
	}
	if got := settingValue(t, "auth_key"); got != "sk-env-initial" {
		t.Fatalf("initial auth_key = %q, want sk-env-initial", got)
	}

	// 设 RESET_AUTH_KEY → 应用一次
	t.Setenv("RESET_AUTH_KEY", "sk-reset-1")
	if err := sm.EnsureSettingsInitialized(envAuth); err != nil {
		t.Fatalf("reset-1: %v", err)
	}
	if got := settingValue(t, "auth_key"); got != "sk-reset-1" {
		t.Fatalf("after reset-1, auth_key = %q, want sk-reset-1", got)
	}

	// 模拟用户在 UI 里改成别的值
	if err := db.DB.Model(&models.SystemSetting{}).Where("setting_key = ?", "auth_key").
		Update("setting_value", "sk-ui-changed").Error; err != nil {
		t.Fatalf("ui change: %v", err)
	}

	// 同一个 RESET_AUTH_KEY 再启动 → 指纹一致, 跳过, 不覆盖 UI 改动 (一次性的核心)
	if err := sm.EnsureSettingsInitialized(envAuth); err != nil {
		t.Fatalf("reset-1 again: %v", err)
	}
	if got := settingValue(t, "auth_key"); got != "sk-ui-changed" {
		t.Fatalf("one-shot broken: auth_key = %q, want sk-ui-changed (同值不应重复应用)", got)
	}

	// 改成新的 RESET_AUTH_KEY → 指纹不同, 再次触发
	t.Setenv("RESET_AUTH_KEY", "sk-reset-2")
	if err := sm.EnsureSettingsInitialized(envAuth); err != nil {
		t.Fatalf("reset-2: %v", err)
	}
	if got := settingValue(t, "auth_key"); got != "sk-reset-2" {
		t.Fatalf("after reset-2, auth_key = %q, want sk-reset-2", got)
	}
	// proxy_keys 跟着一起被重置 (聚合分组凭证同步)
	if got := settingValue(t, "proxy_keys"); got != "sk-reset-2" {
		t.Fatalf("proxy_keys = %q, want sk-reset-2", got)
	}
}

// 未设 RESET_AUTH_KEY 时不应改动 auth_key。
func TestResetAuthKey_UnsetIsNoop(t *testing.T) {
	setupResetTestDB(t)
	sm := NewSystemSettingsManager()
	t.Setenv("RESET_AUTH_KEY", "")

	if err := sm.EnsureSettingsInitialized(types.AuthConfig{Key: "sk-env-x"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	if got := settingValue(t, "auth_key"); got != "sk-env-x" {
		t.Fatalf("auth_key = %q, want sk-env-x", got)
	}
}
