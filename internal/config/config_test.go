package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes a config.yaml into a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	path := writeConfig(t, `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  token: token
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Checkin.OnStart {
		t.Error("catch-up on start should default to true")
	}
	hour, minute, err := cfg.CheckinClock()
	if err != nil {
		t.Fatalf("CheckinClock: %v", err)
	}
	if hour != 8 || minute != 10 {
		t.Errorf("check-in time = %02d:%02d, want 08:10", hour, minute)
	}
	if cfg.Checkin.Timezone != DefaultTimezone {
		t.Errorf("timezone = %q, want %q", cfg.Checkin.Timezone, DefaultTimezone)
	}
	if loc, err := cfg.Location(); err != nil || loc == nil {
		t.Errorf("Location: %v", err)
	}
	if cfg.V2EX.Cookie != "" || cfg.Libra.Cookie != "" {
		t.Errorf("credentials should default to empty: %q %q", cfg.V2EX.Cookie, cfg.Libra.Cookie)
	}
	if cfg.V2EX.WebBaseURL != DefaultV2EXWebBaseURL || cfg.Libra.BaseURL != DefaultLibraBaseURL {
		t.Errorf("unexpected base URLs: %q %q", cfg.V2EX.WebBaseURL, cfg.Libra.BaseURL)
	}
	if cfg.Reminders.Interval != 60*time.Second || cfg.Reminders.MaxPages != 3 {
		t.Errorf("reminder defaults = %s %d", cfg.Reminders.Interval, cfg.Reminders.MaxPages)
	}
	if cfg.Notify.StartupCooldown != 10*time.Minute || !cfg.Notify.AlertOnError {
		t.Errorf("notify defaults = %s %v", cfg.Notify.StartupCooldown, cfg.Notify.AlertOnError)
	}
}

func TestLoadReadsFile(t *testing.T) {
	path := writeConfig(t, `
feishu:
  webhook: 'https://example.invalid/hook'
  secret: s3cret
v2ex:
  cookie: 'A2="2|1:0|48:ZjYx"; PB3_SESSION="2|1:0|40:djJleA=="'
  token: tok
libra:
  cookie: 'access_token=eyJhbGci'
checkin:
  time: '23:45'
  on_start: false
  timezone: 'Asia/Tokyo'
reminders:
  interval: 30s
  max_pages: 5
  first_run: push
  mark_read: true
  filter_types: [reply, mention]
notify:
  startup: false
  startup_cooldown: 5m
  alert_on_error: false
pt:
  poll_interval: 30m
  first_run: push
  retries: 1
  concurrency: 2
  sites:
    - url: 'https://pt.example.invalid'
      cookie: 'uid=1; pass=2'
server:
  http_timeout: 45s
  state_path: /data/custom.db
  log_level: debug
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Feishu.Secret != "s3cret" {
		t.Errorf("feishu.secret = %q", cfg.Feishu.Secret)
	}
	if cfg.V2EX.Cookie != `A2="2|1:0|48:ZjYx"; PB3_SESSION="2|1:0|40:djJleA=="` {
		t.Errorf("v2ex.cookie = %q", cfg.V2EX.Cookie)
	}
	if cfg.Libra.Cookie != "access_token=eyJhbGci" {
		t.Errorf("libra.cookie = %q", cfg.Libra.Cookie)
	}
	hour, minute, _ := cfg.CheckinClock()
	if hour != 23 || minute != 45 {
		t.Errorf("check-in time = %02d:%02d, want 23:45", hour, minute)
	}
	if cfg.Checkin.Timezone != "Asia/Tokyo" || cfg.Checkin.OnStart {
		t.Errorf("checkin = %+v", cfg.Checkin)
	}
	if cfg.Reminders.Interval != 30*time.Second || cfg.Reminders.MaxPages != 5 ||
		cfg.Reminders.FirstRun != FirstRunPush || !cfg.Reminders.MarkRead ||
		len(cfg.Reminders.FilterTypes) != 2 {
		t.Errorf("reminders = %+v", cfg.Reminders)
	}
	if cfg.Notify.Startup || cfg.Notify.StartupCooldown != 5*time.Minute || cfg.Notify.AlertOnError {
		t.Errorf("notify = %+v", cfg.Notify)
	}
	if cfg.PT.PollInterval != 30*time.Minute || cfg.PT.FirstRun != FirstRunPush ||
		cfg.PT.Retries != 1 || cfg.PT.Concurrency != 2 || len(cfg.PT.Sites) != 1 {
		t.Errorf("pt = %+v", cfg.PT)
	}
	if cfg.PT.Sites[0].URL != "https://pt.example.invalid" {
		t.Errorf("pt site = %+v", cfg.PT.Sites[0])
	}
	if cfg.Server.HTTPTimeout != 45*time.Second || cfg.Server.StatePath != "/data/custom.db" ||
		cfg.Server.LogLevel != "debug" {
		t.Errorf("server = %+v", cfg.Server)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := writeConfig(t, `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  token: tok
reminders:
  intervl: 30s
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "intervl") {
		t.Fatalf("expected an unknown-key error, got %v", err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"checkin.time": `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  token: tok
checkin:
  time: '25:00'
`,
		"checkin.timezone": `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  token: tok
checkin:
  timezone: 'Not/AZone'
`,
		"reminders.first_run": `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  token: tok
reminders:
  first_run: sometimes
`,
		"pt.first_run": `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  token: tok
pt:
  first_run: sometimes
`,
		"reminders.interval": `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  token: tok
reminders:
  interval: 1s
`,
		"reminders.filter_types": `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  token: tok
reminders:
  filter_types: [bogus]
`,
	}
	for name, body := range cases {
		path := writeConfig(t, body)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLoadRequiresWebhook(t *testing.T) {
	path := writeConfig(t, `
v2ex:
  token: tok
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "feishu.webhook") {
		t.Fatalf("expected a webhook error, got %v", err)
	}
}

func TestLoadRejectsNothingToDo(t *testing.T) {
	path := writeConfig(t, `
feishu:
  webhook: 'https://example.invalid/hook'
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "nothing to do") {
		t.Fatalf("expected a nothing-to-do error, got %v", err)
	}
}

// The environment overrides a single knob of the file; everything else stays.
func TestEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  cookie: 'A2=from-file'
  token: tok
checkin:
  time: '08:10'
  timezone: 'Asia/Shanghai'
reminders:
  interval: 60s
`)
	t.Setenv("V2EX_COOKIE", "A2=from-env")
	t.Setenv("CHECKIN_TIME", "23:45")
	t.Setenv("TZ", "Asia/Tokyo")
	t.Setenv("V2EX_POLL_INTERVAL", "30s")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.V2EX.Cookie != "A2=from-env" {
		t.Errorf("v2ex.cookie = %q, want the environment value", cfg.V2EX.Cookie)
	}
	if cfg.V2EX.Token != "tok" {
		t.Errorf("v2ex.token = %q, want the file value", cfg.V2EX.Token)
	}
	hour, minute, _ := cfg.CheckinClock()
	if hour != 23 || minute != 45 {
		t.Errorf("check-in time = %02d:%02d, want 23:45", hour, minute)
	}
	if cfg.Checkin.Timezone != "Asia/Tokyo" {
		t.Errorf("timezone = %q, want the environment value", cfg.Checkin.Timezone)
	}
	if cfg.Reminders.Interval != 30*time.Second {
		t.Errorf("reminders.interval = %s, want the environment value", cfg.Reminders.Interval)
	}
}

// A cookie with a dollar sign, a semicolon and quotes must survive the file
// round trip untouched.
func TestLoadPreservesCookieVerbatim(t *testing.T) {
	const cookie = `A2="2|1:0|10:178|48:ZjYx"; PB3_SESSION="2|1:0|40:djJleA=="; _ga=GA1.1.6; v=1`
	path := writeConfig(t, `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  cookie: '`+cookie+`'
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.V2EX.Cookie != cookie {
		t.Errorf("cookie = %q, want %q", cfg.V2EX.Cookie, cookie)
	}
}

// The timezone in the file must be honoured by the process: the schedule, the
// card timestamps and the log timestamps all follow it.
func TestTimezoneIsUsable(t *testing.T) {
	path := writeConfig(t, `
feishu:
  webhook: 'https://example.invalid/hook'
v2ex:
  token: tok
checkin:
  timezone: 'Asia/Tokyo'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	loc, err := cfg.Location()
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if got := now.In(loc).Format("15:04"); got != "21:00" {
		t.Fatalf("Asia/Tokyo offset = %s, want 21:00", got)
	}
}
