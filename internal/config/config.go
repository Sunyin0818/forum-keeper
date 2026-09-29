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
