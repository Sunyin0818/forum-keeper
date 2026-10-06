// Package config loads and validates runtime configuration from environment
// variables. The service is designed to run as a container, so all knobs are
// 12-factor style env vars with safe defaults.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Allowed values for FirstRun.
const (
	FirstRunSkip = "skip" // record existing notifications without pushing
	FirstRunPush = "push" // push existing notifications on first poll

	DefaultBaseURL = "https://www.v2ex.com/api/v2"

	// Web origin used by the V2EX daily-bonus mission. The API token does not
	// authenticate this page; it needs the A2 session cookie.
	DefaultV2EXWebBaseURL = "https://www.v2ex.com"
	// 2libra forum origin, used by the check-in POST /api/sign.
	DefaultLibraBaseURL = "https://2libra.com"

	// DefaultDailyCheckinTime is the local time of the daily check-in run.
	DefaultDailyCheckinTime = "06:00"
	// DefaultCheckinTimezone keeps the schedule at a fixed wall-clock time no
	// matter where the container runs (Docker defaults to UTC).
	DefaultCheckinTimezone = "Asia/Shanghai"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// V2EX / source
	V2EXToken   string
	V2EXBaseURL string
	HTTPTimeout time.Duration // per-request timeout
	MaxPages    int           // how many notification pages to walk per poll

	// Feishu
	FeishuWebhook string
	FeishuSecret  string

	// Polling / state
	PollInterval time.Duration
	StatePath    string
	FirstRun     string
	MarkRead     bool
	FilterTypes  []string
	AlertOnError bool
	LogLevel     string

	// Startup notification
	StartupMessage         bool
	StartupMessageCooldown time.Duration

	// Daily check-in
	CheckinEnabled  bool           // master switch for the check-in scheduler
	CheckinOnStart  bool           // also run once at startup (guarded per day)
	CheckinHour     int            // 0-23, in CheckinLocation
	CheckinMinute   int            // 0-59
	CheckinLocation *time.Location // time zone of the schedule above
	V2EXCookie      string         // A2 (and A2O for 2FA) cookie for the mission page
	V2EXWebBaseURL  string
	LibraCookie     string // access_token cookie for 2libra
	LibraToken      string // alternative Authorization: Bearer token for 2libra
	LibraBaseURL    string
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	var err error
	cfg := &Config{
		V2EXToken:     envStr("V2EX_TOKEN", ""),
		V2EXBaseURL:   strings.TrimRight(envStr("V2EX_BASE_URL", DefaultBaseURL), "/"),
		FeishuWebhook: envStr("FEISHU_WEBHOOK", ""),
		FeishuSecret:  envStr("FEISHU_SECRET", ""),
		StatePath:     envStr("STATE_PATH", "state.db"),
		FirstRun:      strings.ToLower(envStr("FIRST_RUN", FirstRunSkip)),
		LogLevel:      strings.ToLower(envStr("LOG_LEVEL", "info")),
		FilterTypes:   envList("FILTER_TYPES"),

		V2EXCookie:     envStr("V2EX_COOKIE", ""),
		V2EXWebBaseURL: strings.TrimRight(envStr("V2EX_WEB_BASE_URL", DefaultV2EXWebBaseURL), "/"),
		LibraCookie:    envStr("LIBRA_COOKIE", ""),
		LibraToken:     envStr("LIBRA_TOKEN", ""),
		LibraBaseURL:   strings.TrimRight(envStr("LIBRA_BASE_URL", DefaultLibraBaseURL), "/"),
	}

	if cfg.HTTPTimeout, err = envDuration("HTTP_TIMEOUT", 20*time.Second); err != nil {
		return nil, err
	}
	if cfg.PollInterval, err = envDuration("POLL_INTERVAL", 60*time.Second); err != nil {
		return nil, err
	}
	if cfg.MaxPages, err = envInt("MAX_PAGES", 3); err != nil {
		return nil, err
	}
	if cfg.MarkRead, err = envBool("MARK_READ", false); err != nil {
		return nil, err
	}
	if cfg.AlertOnError, err = envBool("ALERT_ON_ERROR", true); err != nil {
		return nil, err
	}
	if cfg.StartupMessage, err = envBool("STARTUP_MESSAGE", true); err != nil {
		return nil, err
	}
	if cfg.StartupMessageCooldown, err = envDuration("STARTUP_MESSAGE_COOLDOWN", 10*time.Minute); err != nil {
		return nil, err
	}
	if cfg.CheckinEnabled, err = envBool("CHECKIN_ENABLED", true); err != nil {
		return nil, err
	}
	if cfg.CheckinOnStart, err = envBool("CHECKIN_ON_START", true); err != nil {
		return nil, err
	}
	if cfg.CheckinHour, cfg.CheckinMinute, err = parseClock(envStr("CHECKIN_TIME", DefaultDailyCheckinTime)); err != nil {
		return nil, err
	}
	tz := envStr("CHECKIN_TZ", DefaultCheckinTimezone)
	if cfg.CheckinLocation, err = time.LoadLocation(tz); err != nil {
		return nil, fmt.Errorf("config: CHECKIN_TZ=%q is not a valid IANA time zone: %w", tz, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.V2EXToken == "" {
		return fmt.Errorf("config: V2EX_TOKEN is required")
	}
	if c.FeishuWebhook == "" {
		return fmt.Errorf("config: FEISHU_WEBHOOK is required")
	}
	if c.V2EXBaseURL == "" {
		return fmt.Errorf("config: V2EX_BASE_URL must not be empty")
	}
	switch c.FirstRun {
	case FirstRunSkip, FirstRunPush:
	default:
		return fmt.Errorf("config: FIRST_RUN must be %q or %q, got %q", FirstRunSkip, FirstRunPush, c.FirstRun)
	}
	// 600 requests/hour/IP means a poll faster than every 6s would blow the
	// budget. Refuse anything under 5s rather than letting the user self-DoS.
	if c.PollInterval < 5*time.Second {
		return fmt.Errorf("config: POLL_INTERVAL must be >= 5s, got %s", c.PollInterval)
	}
	if c.MaxPages < 1 {
		return fmt.Errorf("config: MAX_PAGES must be >= 1, got %d", c.MaxPages)
	}
	if c.StartupMessageCooldown < 0 {
		return fmt.Errorf("config: STARTUP_MESSAGE_COOLDOWN must not be negative, got %s", c.StartupMessageCooldown)
	}
	if c.StatePath == "" {
		return fmt.Errorf("config: STATE_PATH must not be empty")
	}
	for _, f := range c.FilterTypes {
		switch f {
		case "reply", "mention", "thanks", "other":
		default:
			return fmt.Errorf("config: FILTER_TYPES contains unknown type %q (allowed: reply, mention, thanks, other)", f)
		}
	}
	return nil
}

// parseClock parses "HH:MM" into hour and minute.
func parseClock(raw string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("config: CHECKIN_TIME=%q must look like HH:MM", raw)
	}
	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("config: CHECKIN_TIME=%q has an invalid hour: %w", raw, err)
	}
	minute, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("config: CHECKIN_TIME=%q has an invalid minute: %w", raw, err)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("config: CHECKIN_TIME=%q is out of range (00:00-23:59)", raw)
	}
	return hour, minute, nil
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return def
}

func envList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not a valid duration: %w", key, raw, err)
	}
	return d, nil
}

func envInt(key string, def int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not a valid integer: %w", key, raw, err)
	}
	return n, nil
}

func envBool(key string, def bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("config: %s=%q is not a valid boolean: %w", key, raw, err)
	}
	return b, nil
}
