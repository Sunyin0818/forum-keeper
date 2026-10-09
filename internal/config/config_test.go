package config

import (
	"strings"
	"testing"
)

func TestLoadCheckinDefaults(t *testing.T) {
	t.Setenv("V2EX_TOKEN", "token")
	t.Setenv("FEISHU_WEBHOOK", "https://example.invalid/hook")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.CheckinOnStart {
		t.Error("catch-up on start should default to true")
	}
	if cfg.CheckinHour != 8 || cfg.CheckinMinute != 10 {
		t.Errorf("check-in time = %02d:%02d, want 08:10", cfg.CheckinHour, cfg.CheckinMinute)
	}
	if cfg.CheckinLocation == nil {
		t.Error("check-in location must be set (time.Local)")
	}
	if cfg.V2EXCookie != "" || cfg.LibraCookie != "" {
		t.Errorf("credentials should default to empty: %q %q", cfg.V2EXCookie, cfg.LibraCookie)
	}
	if cfg.V2EXWebBaseURL != DefaultV2EXWebBaseURL || cfg.LibraBaseURL != DefaultLibraBaseURL {
		t.Errorf("unexpected base URLs: %q %q", cfg.V2EXWebBaseURL, cfg.LibraBaseURL)
	}
}

func TestLoadCheckinOverrides(t *testing.T) {
	t.Setenv("V2EX_TOKEN", "token")
	t.Setenv("FEISHU_WEBHOOK", "https://example.invalid/hook")
	t.Setenv("CHECKIN_ON_START", "false")
	t.Setenv("CHECKIN_TIME", "23:45")
	t.Setenv("V2EX_COOKIE", "A2=x")
	t.Setenv("LIBRA_COOKIE", "access_token=y")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CheckinOnStart {
		t.Error("CHECKIN_ON_START=false was ignored")
	}
	if cfg.CheckinHour != 23 || cfg.CheckinMinute != 45 {
		t.Errorf("check-in time = %02d:%02d, want 23:45", cfg.CheckinHour, cfg.CheckinMinute)
	}
	if cfg.V2EXCookie != "A2=x" || cfg.LibraCookie != "access_token=y" {
		t.Errorf("credentials not loaded: %q %q", cfg.V2EXCookie, cfg.LibraCookie)
	}
}

func TestLoadAllowsCheckinOnly(t *testing.T) {
	t.Setenv("FEISHU_WEBHOOK", "https://example.invalid/hook")
	t.Setenv("V2EX_COOKIE", "A2=x")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.V2EXToken != "" {
		t.Errorf("V2EX_TOKEN should be empty, got %q", cfg.V2EXToken)
	}
	if cfg.V2EXCookie != "A2=x" {
		t.Errorf("V2EX_COOKIE = %q", cfg.V2EXCookie)
	}
}

func TestLoadRejectsNothingToDo(t *testing.T) {
	t.Setenv("FEISHU_WEBHOOK", "https://example.invalid/hook")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "nothing to do") {
		t.Fatalf("expected a nothing-to-do error, got %v", err)
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
