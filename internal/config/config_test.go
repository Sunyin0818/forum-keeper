package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadCheckinDefaults(t *testing.T) {
	t.Setenv("V2EX_TOKEN", "token")
	t.Setenv("FEISHU_WEBHOOK", "https://example.invalid/hook")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.CheckinEnabled {
		t.Error("check-in should be enabled by default")
	}
	if !cfg.CheckinOnStart {
		t.Error("catch-up on start should default to true")
	}
	if cfg.CheckinHour != 6 || cfg.CheckinMinute != 0 {
		t.Errorf("check-in time = %02d:%02d, want 06:00", cfg.CheckinHour, cfg.CheckinMinute)
	}
	if cfg.CheckinLocation == nil || cfg.CheckinLocation.String() != "Asia/Shanghai" {
		t.Errorf("check-in location = %v, want Asia/Shanghai", cfg.CheckinLocation)
	}
	if cfg.V2EXCookie != "" || cfg.LibraCookie != "" || cfg.LibraToken != "" {
		t.Errorf("cookies should default to empty: %q %q %q", cfg.V2EXCookie, cfg.LibraCookie, cfg.LibraToken)
	}
	if cfg.V2EXWebBaseURL != DefaultV2EXWebBaseURL || cfg.LibraBaseURL != DefaultLibraBaseURL {
		t.Errorf("unexpected base URLs: %q %q", cfg.V2EXWebBaseURL, cfg.LibraBaseURL)
	}
}

func TestLoadCheckinOverrides(t *testing.T) {
	t.Setenv("V2EX_TOKEN", "token")
	t.Setenv("FEISHU_WEBHOOK", "https://example.invalid/hook")
	t.Setenv("CHECKIN_ENABLED", "false")
	t.Setenv("CHECKIN_ON_START", "true")
	t.Setenv("CHECKIN_TIME", "23:45")
	t.Setenv("CHECKIN_TZ", "UTC")
	t.Setenv("V2EX_COOKIE", "A2=x")
	t.Setenv("LIBRA_TOKEN", "jwt")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CheckinEnabled {
		t.Error("CHECKIN_ENABLED=false was ignored")
	}
	if !cfg.CheckinOnStart {
		t.Error("CHECKIN_ON_START=true was ignored")
	}
	if cfg.CheckinHour != 23 || cfg.CheckinMinute != 45 {
		t.Errorf("check-in time = %02d:%02d, want 23:45", cfg.CheckinHour, cfg.CheckinMinute)
	}
	if cfg.CheckinLocation != time.UTC {
		t.Errorf("check-in location = %v, want UTC", cfg.CheckinLocation)
	}
	if cfg.V2EXCookie != "A2=x" || cfg.LibraToken != "jwt" {
		t.Errorf("credentials not loaded: %q %q", cfg.V2EXCookie, cfg.LibraToken)
	}
}

func TestLoadRejectsInvalidCheckinTime(t *testing.T) {
	t.Setenv("V2EX_TOKEN", "token")
	t.Setenv("FEISHU_WEBHOOK", "https://example.invalid/hook")
	t.Setenv("CHECKIN_TIME", "25:00")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CHECKIN_TIME") {
		t.Fatalf("expected a CHECKIN_TIME error, got %v", err)
	}
}

func TestLoadRejectsInvalidTimezone(t *testing.T) {
	t.Setenv("V2EX_TOKEN", "token")
	t.Setenv("FEISHU_WEBHOOK", "https://example.invalid/hook")
	t.Setenv("CHECKIN_TZ", "Mars/Olympus")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CHECKIN_TZ") {
		t.Fatalf("expected a CHECKIN_TZ error, got %v", err)
	}
}
