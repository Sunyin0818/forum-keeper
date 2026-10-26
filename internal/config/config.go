// Package config loads and validates the runtime configuration.
//
// The service runs as a container, so a deployment ships one YAML file
// (config.yaml) mounted read-only into the container. The file is the source
// of truth; environment variables only override single knobs, which keeps
// one-off runs and CI working without a second file format.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Sunyin0818/forum-keeper/internal/ptsite"
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

	// DefaultDailyCheckinTime is the local time of the daily check-in run. It sits
	// after 08:00 UTC+8 on purpose: V2EX opens its mission day at 08:00, so an
	// earlier run can only ever see the previous day already claimed.
	DefaultDailyCheckinTime = "08:10"

	// DefaultTimezone is used when the file does not name one.
	DefaultTimezone = "Asia/Shanghai"
)

// Config is the runtime configuration. The YAML file maps onto these fields
// one to one; the field names are the file keys.
type Config struct {
	Feishu    FeishuConfig    `yaml:"feishu"`
	V2EX      V2EXConfig      `yaml:"v2ex"`
	Libra     LibraConfig     `yaml:"libra"`
	Checkin   CheckinConfig   `yaml:"checkin"`
	Reminders RemindersConfig `yaml:"reminders"`
	Notify    NotifyConfig    `yaml:"notify"`
	PT        PTConfig        `yaml:"pt"`
	Server    ServerConfig    `yaml:"server"`
}

// FeishuConfig holds the bot credentials. The webhook is the credential: the
// path token of the URL lets anyone post to the group.
type FeishuConfig struct {
	Webhook string `yaml:"webhook"`
	// Secret is only needed when the bot has signature verification on.
	Secret string `yaml:"secret"`
}

// V2EXConfig holds both V2EX credentials. The API token and the web session
// cookie authenticate different surfaces and are not interchangeable.
type V2EXConfig struct {
	// Cookie is the A2 session cookie used by the daily-bonus page.
	Cookie string `yaml:"cookie"`
	// Token is the API 2.0 personal access token used for reminders.
	Token string `yaml:"token"`
	// APIBaseURL is the API 2.0 root, web_base_url the web origin. Both
	// point at the public site unless a mirror is used.
	APIBaseURL string `yaml:"api_base_url"`
	WebBaseURL string `yaml:"web_base_url"`
}

// LibraConfig holds the 2libra credential: a cookie or a bare bearer token.
type LibraConfig struct {
	Cookie string `yaml:"cookie"`
	// BaseURL is the 2libra origin.
	BaseURL string `yaml:"base_url"`
}

// CheckinConfig controls the daily check-in schedule.
type CheckinConfig struct {
	// Time is the local clock time of the daily run, "HH:MM".
	Time string `yaml:"time"`
	// OnStart runs a catch-up check-in when the service starts.
	OnStart bool `yaml:"on_start"`
	// Timezone is the IANA zone the schedule and every timestamp use.
	Timezone string `yaml:"timezone"`
}

// RemindersConfig controls the V2EX reminder poller.
type RemindersConfig struct {
	Interval time.Duration `yaml:"interval"`
	MaxPages int           `yaml:"max_pages"`
	// FirstRun is "skip" or "push".
	FirstRun string `yaml:"first_run"`
	// MarkRead deletes a V2EX notification after it has been pushed.
	MarkRead bool `yaml:"mark_read"`
	// FilterTypes limits which notification kinds are pushed.
	FilterTypes []string `yaml:"filter_types"`
}

// NotifyConfig controls the service's own notifications.
type NotifyConfig struct {
	Startup         bool          `yaml:"startup"`
	StartupCooldown time.Duration `yaml:"startup_cooldown"`
	AlertOnError    bool          `yaml:"alert_on_error"`
}

// PTConfig groups the tracker settings. Sites is empty when PT is unused.
type PTConfig struct {
	PollInterval time.Duration `yaml:"poll_interval"`
	// FirstRun is "skip" or "push".
	FirstRun string `yaml:"first_run"`
	// Retries is the number of retries after a failed site check-in.
	Retries int `yaml:"retries"`
	// Concurrency is how many sites are checked in at the same time.
	Concurrency int `yaml:"concurrency"`
	// Sites describes the trackers. An empty list keeps the PT feature off.
	Sites []ptsite.SiteConfig `yaml:"sites"`
}

// ServerConfig holds the operational knobs.
type ServerConfig struct {
	// HTTPTimeout is the per-request timeout for outgoing calls.
	HTTPTimeout time.Duration `yaml:"http_timeout"`
	// StatePath is the bbolt state database.
	StatePath string `yaml:"state_path"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `yaml:"log_level"`
}

// Default returns a configuration with every default filled in. Decoding a
// file on top of it only changes the keys the file actually contains.
func Default() Config {
	return Config{
		V2EX: V2EXConfig{
			APIBaseURL: DefaultBaseURL,
			WebBaseURL: DefaultV2EXWebBaseURL,
		},
		Libra: LibraConfig{
			BaseURL: DefaultLibraBaseURL,
		},
		Checkin: CheckinConfig{
			Time:     DefaultDailyCheckinTime,
			OnStart:  true,
			Timezone: DefaultTimezone,
		},
		Reminders: RemindersConfig{
			Interval: 60 * time.Second,
			MaxPages: 3,
			FirstRun: FirstRunSkip,
		},
		Notify: NotifyConfig{
			Startup:         true,
			StartupCooldown: 10 * time.Minute,
			AlertOnError:    true,
		},
		PT: PTConfig{
			PollInterval: 2 * time.Hour,
			FirstRun:     "skip",
			Retries:      2,
			Concurrency:  4,
		},
		Server: ServerConfig{
			HTTPTimeout: 20 * time.Second,
			StatePath:   "state.db",
			LogLevel:    "info",
		},
	}
}

// Load reads the configuration file and applies the environment overrides on
// top of it. An empty path loads the defaults only.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: read %s: %w", path, err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}
	if err := cfg.applyEnvOverrides(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyEnvOverrides lets a single knob be replaced from the environment. The
// file stays the source of truth; this exists for one-off runs and CI.
func (c *Config) applyEnvOverrides() error {
	if v := envStr("FEISHU_WEBHOOK", ""); v != "" {
		c.Feishu.Webhook = v
	}
	if v := envStr("FEISHU_SECRET", ""); v != "" {
		c.Feishu.Secret = v
	}
	if v := envStr("V2EX_COOKIE", ""); v != "" {
		c.V2EX.Cookie = v
	}
	if v := envStr("V2EX_TOKEN", ""); v != "" {
		c.V2EX.Token = v
	}
	if v := envStr("LIBRA_COOKIE", ""); v != "" {
		c.Libra.Cookie = v
	}
	if v := envStr("V2EX_API_BASE_URL", ""); v != "" {
		c.V2EX.APIBaseURL = v
	}
	if v := envStr("V2EX_WEB_BASE_URL", ""); v != "" {
		c.V2EX.WebBaseURL = v
	}
	if v := envStr("LIBRA_BASE_URL", ""); v != "" {
		c.Libra.BaseURL = v
	}
	if v := envStr("CHECKIN_TIME", ""); v != "" {
		c.Checkin.Time = v
	}
	if v := envStr("TZ", ""); v != "" {
		c.Checkin.Timezone = v
	}
	if v, err := envBool("CHECKIN_ON_START", c.Checkin.OnStart); err != nil {
		return err
	} else {
		c.Checkin.OnStart = v
	}
	if v, err := envDuration("V2EX_POLL_INTERVAL", c.Reminders.Interval); err != nil {
		return err
	} else {
		c.Reminders.Interval = v
	}
	if v, err := envInt("V2EX_MAX_PAGES", c.Reminders.MaxPages); err != nil {
		return err
	} else {
		c.Reminders.MaxPages = v
	}
	if v, err := envBool("V2EX_MARK_READ", c.Reminders.MarkRead); err != nil {
		return err
	} else {
		c.Reminders.MarkRead = v
	}
	if v, err := envDuration("PT_POLL_INTERVAL", c.PT.PollInterval); err != nil {
		return err
	} else {
		c.PT.PollInterval = v
	}
	if v, err := envInt("PT_CHECKIN_RETRIES", c.PT.Retries); err != nil {
		return err
	} else {
		c.PT.Retries = v
	}
	if v, err := envInt("PT_CONCURRENCY", c.PT.Concurrency); err != nil {
		return err
	} else {
		c.PT.Concurrency = v
	}
	if v, err := envBool("NOTIFY_STARTUP", c.Notify.Startup); err != nil {
		return err
	} else {
		c.Notify.Startup = v
	}
	if v, err := envBool("NOTIFY_ALERT_ON_ERROR", c.Notify.AlertOnError); err != nil {
		return err
	} else {
		c.Notify.AlertOnError = v
	}
	if v, err := envDuration("HTTP_TIMEOUT", c.Server.HTTPTimeout); err != nil {
		return err
	} else {
		c.Server.HTTPTimeout = v
	}
	if v := envStr("LOG_LEVEL", ""); v != "" {
		c.Server.LogLevel = v
	}
	if v := envStr("STATE_PATH", ""); v != "" {
		c.Server.StatePath = v
	}
	return nil
}

func (c *Config) validate() error {
	if c.V2EX.APIBaseURL == "" || c.V2EX.WebBaseURL == "" || c.Libra.BaseURL == "" {
		return fmt.Errorf("config: v2ex.api_base_url, v2ex.web_base_url and libra.base_url must not be empty")
	}
	if c.Feishu.Webhook == "" {
		return fmt.Errorf("config: feishu.webhook is required")
	}
	// Without any credential the service would sit there doing nothing.
	if c.V2EX.Token == "" && c.V2EX.Cookie == "" && c.Libra.Cookie == "" && len(c.PT.Sites) == 0 {
		return fmt.Errorf("config: nothing to do: set v2ex.token (reminders), v2ex.cookie/libra.cookie (check-in), or pt.sites")
	}
	if _, _, err := parseClock(c.Checkin.Time); err != nil {
		return err
	}
	if _, err := time.LoadLocation(c.Checkin.Timezone); err != nil {
		return fmt.Errorf("config: checkin.timezone %q: %w", c.Checkin.Timezone, err)
	}
	switch c.Reminders.FirstRun {
	case FirstRunSkip, FirstRunPush:
	default:
		return fmt.Errorf("config: reminders.first_run must be %q or %q, got %q", FirstRunSkip, FirstRunPush, c.Reminders.FirstRun)
	}
	switch c.PT.FirstRun {
	case "", "skip", "push":
	default:
		return fmt.Errorf("config: pt.first_run must be skip or push, got %q", c.PT.FirstRun)
	}
	// 600 requests/hour/IP means a poll faster than every 6s would blow the
	// budget. Refuse anything under 5s rather than letting the user self-DoS.
	if c.Reminders.Interval < 5*time.Second {
		return fmt.Errorf("config: reminders.interval must be >= 5s, got %s", c.Reminders.Interval)
	}
	if c.Reminders.MaxPages < 1 {
		return fmt.Errorf("config: reminders.max_pages must be >= 1, got %d", c.Reminders.MaxPages)
	}
	if c.Notify.StartupCooldown < 0 {
		return fmt.Errorf("config: notify.startup_cooldown must not be negative, got %s", c.Notify.StartupCooldown)
	}
	if c.Server.StatePath == "" {
		return fmt.Errorf("config: server.state_path must not be empty")
	}
	if c.PT.PollInterval < time.Minute {
		return fmt.Errorf("config: pt.poll_interval must be >= 1m, got %s", c.PT.PollInterval)
	}
	if c.PT.Retries < 0 || c.PT.Retries > 10 {
		return fmt.Errorf("config: pt.retries must be between 0 and 10, got %d", c.PT.Retries)
	}
	if c.PT.Concurrency < 1 || c.PT.Concurrency > 16 {
		return fmt.Errorf("config: pt.concurrency must be between 1 and 16, got %d", c.PT.Concurrency)
	}
	for _, f := range c.Reminders.FilterTypes {
		switch f {
		case "reply", "mention", "thanks", "other":
		default:
			return fmt.Errorf("config: reminders.filter_types contains unknown type %q (allowed: reply, mention, thanks, other)", f)
		}
	}
	return nil
}

// CheckinClock returns the hour and minute of the daily schedule.
func (c *Config) CheckinClock() (int, int, error) {
	return parseClock(c.Checkin.Time)
}

// Location returns the timezone the schedule runs in.
func (c *Config) Location() (*time.Location, error) {
	return time.LoadLocation(c.Checkin.Timezone)
}

// parseClock parses "HH:MM" into hour and minute.
func parseClock(raw string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("config: checkin.time=%q must look like HH:MM", raw)
	}
	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("config: checkin.time=%q has an invalid hour: %w", raw, err)
	}
	minute, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("config: checkin.time=%q has an invalid minute: %w", raw, err)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("config: checkin.time=%q is out of range (00:00-23:59)", raw)
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
