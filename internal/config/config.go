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
)

// Config is the fully resolved runtime configuration.
//
// Fields are grouped in the same layers as .env.example:
//
//  1. required (service cannot start without them)
//  2. credentials (fill these; each enables one capability)
//  3. sensible defaults (change only when needed)
//  4. advanced (endpoints)
type Config struct {
	// --- 1. required -------------------------------------------------------
	FeishuWebhook string
	FeishuSecret  string

	// --- 2. credentials -----------------------------------------------------
	// There is no enable switch: a site is enabled precisely when its
	// credential is set, and disabling it means clearing that line.
	V2EXCookie  string // A2 (and A2O for 2FA) cookie for the mission page
	LibraCookie string // 2libra credential: "access_token=..." cookie or a raw bearer token
	V2EXToken   string // reminders only; empty = check-in only

	// --- 3. sensible defaults ----------------------------------------------
	CheckinHour            int
	CheckinMinute          int
	CheckinOnStart         bool
	CheckinLocation        *time.Location // always time.Local; not an env knob
	PollInterval           time.Duration
	MaxPages               int
	FirstRun               string
	MarkRead               bool
	FilterTypes            []string
	AlertOnError           bool
	StartupMessage         bool
	StartupMessageCooldown time.Duration
	HTTPTimeout            time.Duration
	StatePath              string
	LogLevel               string

	// --- 4. advanced (defaults are the public sites) ------------------------
	V2EXBaseURL    string // V2EX API 2.0
	V2EXWebBaseURL string // V2EX web, for the daily mission
	LibraBaseURL   string // 2libra
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	var err error
	cfg := &Config{
		// 1. required
		FeishuWebhook: envStr("FEISHU_WEBHOOK", ""),
		FeishuSecret:  envStr("FEISHU_SECRET", ""),

		// 2. credentials
		V2EXCookie:  envStr("V2EX_COOKIE", ""),
		LibraCookie: envStr("LIBRA_COOKIE", ""),
		V2EXToken:   envStr("V2EX_TOKEN", ""),

		// 3. sensible defaults
		FirstRun:    strings.ToLower(envStr("V2EX_FIRST_RUN", FirstRunSkip)),
		FilterTypes: envList("V2EX_FILTER_TYPES"),
		StatePath:   envStr("STATE_PATH", "state.db"),
		LogLevel:    strings.ToLower(envStr("LOG_LEVEL", "info")),

		// 4. advanced
		V2EXBaseURL:    strings.TrimRight(envStr("V2EX_API_BASE_URL", DefaultBaseURL), "/"),
		V2EXWebBaseURL: strings.TrimRight(envStr("V2EX_WEB_BASE_URL", DefaultV2EXWebBaseURL), "/"),
		LibraBaseURL:   strings.TrimRight(envStr("LIBRA_BASE_URL", DefaultLibraBaseURL), "/"),
	}

	if cfg.HTTPTimeout, err = envDuration("HTTP_TIMEOUT", 20*time.Second); err != nil {
		return nil, err
	}
	if cfg.PollInterval, err = envDuration("V2EX_POLL_INTERVAL", 60*time.Second); err != nil {
		return nil, err
	}
	if cfg.MaxPages, err = envInt("V2EX_MAX_PAGES", 3); err != nil {
		return nil, err
	}
	if cfg.MarkRead, err = envBool("V2EX_MARK_READ", false); err != nil {
		return nil, err
	}
	if cfg.AlertOnError, err = envBool("NOTIFY_ALERT_ON_ERROR", true); err != nil {
		return nil, err
	}
	if cfg.StartupMessage, err = envBool("NOTIFY_STARTUP", true); err != nil {
		return nil, err
	}
	if cfg.StartupMessageCooldown, err = envDuration("NOTIFY_STARTUP_COOLDOWN", 10*time.Minute); err != nil {
		return nil, err
	}
	if cfg.CheckinOnStart, err = envBool("CHECKIN_ON_START", true); err != nil {
		return nil, err
	}
	if cfg.CheckinHour, cfg.CheckinMinute, err = parseClock(envStr("CHECKIN_TIME", DefaultDailyCheckinTime)); err != nil {
		return nil, err
	}
	// The schedule follows the process time zone, so there is exactly one
	// timezone knob: the standard TZ (set by compose.yaml).
	cfg.CheckinLocation = time.Local

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	// V2EX_TOKEN is optional: without it the service only runs the daily
	// check-in. But something has to be configured, or the container would sit
	// there doing nothing.
	if c.V2EXToken == "" && c.V2EXCookie == "" && c.LibraCookie == "" {
		return fmt.Errorf("config: nothing to do: set V2EX_TOKEN (reminders) and/or V2EX_COOKIE or LIBRA_COOKIE (check-in)")
	}
	if c.FeishuWebhook == "" {
		return fmt.Errorf("config: FEISHU_WEBHOOK is required")
	}
	if c.V2EXBaseURL == "" {
		return fmt.Errorf("config: V2EX_API_BASE_URL must not be empty")
	}
	switch c.FirstRun {
	case FirstRunSkip, FirstRunPush:
	default:
		return fmt.Errorf("config: V2EX_FIRST_RUN must be %q or %q, got %q", FirstRunSkip, FirstRunPush, c.FirstRun)
	}
	// 600 requests/hour/IP means a poll faster than every 6s would blow the
	// budget. Refuse anything under 5s rather than letting the user self-DoS.
	if c.PollInterval < 5*time.Second {
		return fmt.Errorf("config: V2EX_POLL_INTERVAL must be >= 5s, got %s", c.PollInterval)
	}
	if c.MaxPages < 1 {
		return fmt.Errorf("config: V2EX_MAX_PAGES must be >= 1, got %d", c.MaxPages)
	}
	if c.StartupMessageCooldown < 0 {
		return fmt.Errorf("config: NOTIFY_STARTUP_COOLDOWN must not be negative, got %s", c.StartupMessageCooldown)
	}
	if c.StatePath == "" {
		return fmt.Errorf("config: STATE_PATH must not be empty")
	}
	for _, f := range c.FilterTypes {
		switch f {
		case "reply", "mention", "thanks", "other":
		default:
			return fmt.Errorf("config: V2EX_FILTER_TYPES contains unknown type %q (allowed: reply, mention, thanks, other)", f)
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
