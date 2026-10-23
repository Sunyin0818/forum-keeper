// Package ptsite implements the generic PT-site sign-in and inbox reader.
//
// A site is described by a small JSON rule set: the sign-in endpoint and the
// page that renders the inbox. Most private trackers run NexusPHP, so the
// defaults work without any per-site code; the rule fields exist for the
// variants (custom attendance paths, two-step forms, JSON APIs).
//
// The package deliberately has no browser dependency. Cloudflare / captcha
// pages are reported as a clear failure instead of being hidden behind a
// retry that can never succeed.
package ptsite

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Default values for the sign-in and message rules. They match the common
// NexusPHP layout; a site only needs to override what differs.
const (
	DefaultSigninPath = "/attendance.php"
	DefaultIndexPath  = "/index.php"
	DefaultInboxPath  = "messages.php?action=viewmailbox&box=1&unread=yes"
	DefaultSystemPath = "messages.php?action=viewmailbox&box=-2&unread=yes"
	DefaultTimeout    = 20 * time.Second
	DefaultPoll       = 2 * time.Hour
)

const (
	FirstRunSkip = "skip"
	FirstRunPush = "push"
)

// Config is the parsed pt-sites.json file.
type Config struct {
	// PollInterval is how often unread inboxes are checked. Defaults to 2h.
	PollInterval string `json:"poll_interval,omitempty"`
	// FirstRun is "skip" (record existing unread without pushing) or "push".
	FirstRun string       `json:"first_run,omitempty"`
	Sites    []SiteConfig `json:"sites"`

	// Poll is PollInterval parsed; not part of the file.
	Poll time.Duration `json:"-"`
}

// SiteConfig describes one tracker.
type SiteConfig struct {
	Name string `json:"name,omitempty"`
	URL  string `json:"url"`

	// Cookie is the Cookie request header. Prefer CookieEnv so the secret
	// stays in the process environment.
	Cookie    string `json:"cookie,omitempty"`
	CookieEnv string `json:"cookie_env,omitempty"`
	UA        string `json:"ua,omitempty"`
	Timeout   string `json:"timeout,omitempty"`

	// Preset selects a built-in rule set by name. Empty means auto-match by
	// host; "none" disables auto-matching.
	Preset   string         `json:"preset,omitempty"`
	Signin   SigninConfig   `json:"signin"`
	Messages MessagesConfig `json:"messages"`

	// Resolved at load time; never serialized back.
	timeout        time.Duration
	host           string
	base           *url.URL
	resolvedCookie bool
}

// SigninConfig controls the daily sign-in or login keep-alive.
type SigninConfig struct {
	// Mode is "checkin" (default), "login" (only verify the session) or "off".
	Mode string `json:"mode,omitempty"`
	// Path is the sign-in page, default /attendance.php.
	Path string `json:"path,omitempty"`
	// Method is GET (default) or POST.
	Method string `json:"method,omitempty"`
	// Data is sent as query parameters for GET or form fields for POST.
	Data map[string]string `json:"data,omitempty"`
	// SubmitText selects the form button that triggers the sign-in.
	SubmitText string `json:"submit_text,omitempty"`
	// Success / Already / LoginRequired override the default page patterns.
	Success       string `json:"success,omitempty"`
	Already       string `json:"already,omitempty"`
	LoginRequired string `json:"login_required,omitempty"`
	// AbsentKeywords are success indicators that disappear after sign-in.
	AbsentKeywords []string `json:"absent_keywords,omitempty"`
	// VerifyPath is re-read after a POST, default Path.
	VerifyPath string `json:"verify_path,omitempty"`
	// TwoStep handles sites where the first page only carries a token.
	TwoStep *TwoStepConfig `json:"two_step,omitempty"`
	// Bonus is an optional regex for the reward sentence.
	Bonus string `json:"bonus,omitempty"`
}

// TwoStepConfig describes a token page followed by the real sign-in request.
type TwoStepConfig struct {
	Path   string            `json:"path"`
	Method string            `json:"method,omitempty"`
	Data   map[string]string `json:"data,omitempty"`
	// Tokens maps a form field name to a regex whose first group is the value.
	Tokens map[string]string `json:"tokens,omitempty"`
	// Headers are extra request headers for the second request.
	Headers map[string]string `json:"headers,omitempty"`
}

// MessagesConfig controls inbox reading.
type MessagesConfig struct {
	// Engine is "nexusphp" (default) or "off".
	Engine string `json:"engine,omitempty"`
	// Mode is "full" (fetch the body, which marks the message read on
	// NexusPHP) or "list" (only titles/dates, leaving unread state alone).
	Mode       string `json:"mode,omitempty"`
	InboxPath  string `json:"inbox_path,omitempty"`
	SystemPath string `json:"system_path,omitempty"`
	MaxPages   int    `json:"max_pages,omitempty"`
	// Selectors overrides the built-in NexusPHP selectors when a theme differs.
	Selectors *MessageSelectors `json:"selectors,omitempty"`
}

// MessageSelectors is an escape hatch for custom themes. Empty fields use the
// built-in selectors.
type MessageSelectors struct {
	UnreadRow string `json:"unread_row,omitempty"`
	Link      string `json:"link,omitempty"`
	Title     string `json:"title,omitempty"`
	Date      string `json:"date,omitempty"`
	Body      string `json:"body,omitempty"`
}

func (c MessagesConfig) enabled() bool {
	return c.Engine != "off"
}

func (c SigninConfig) enabled() bool {
	return c.Mode != "off"
}

// Host returns the normalized host key used in state and dedupe keys.
func (c SiteConfig) Host() string { return c.host }

// Load reads and validates a pt-sites.json file. CookieEnv is resolved here;
// a missing environment variable is a startup error rather than a silently
// disabled site.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ptsite: read %s: %w", path, err)
	}
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("ptsite: parse %s: %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) normalize() error {
	if len(c.Sites) == 0 {
		return fmt.Errorf("ptsite: no sites configured")
	}
	c.PollInterval = strings.TrimSpace(c.PollInterval)
	if c.PollInterval == "" {
		c.PollInterval = DefaultPoll.String()
	}
	poll, err := time.ParseDuration(c.PollInterval)
	if err != nil {
		return fmt.Errorf("ptsite: poll_interval %q: %w", c.PollInterval, err)
	}
	if poll < time.Minute {
		return fmt.Errorf("ptsite: poll_interval must be >= 1m, got %s", poll)
	}
	c.Poll = poll

	c.FirstRun = strings.ToLower(strings.TrimSpace(c.FirstRun))
	if c.FirstRun == "" {
		c.FirstRun = FirstRunSkip
	}
	if c.FirstRun != FirstRunSkip && c.FirstRun != FirstRunPush {
		return fmt.Errorf("ptsite: first_run must be %q or %q, got %q", FirstRunSkip, FirstRunPush, c.FirstRun)
	}

	seen := make(map[string]struct{}, len(c.Sites))
	for i := range c.Sites {
		if err := c.Sites[i].normalize(); err != nil {
			return fmt.Errorf("ptsite: site %q: %w", c.Sites[i].Name, err)
		}
		if _, dup := seen[c.Sites[i].host]; dup {
			return fmt.Errorf("ptsite: duplicate site host %q", c.Sites[i].host)
		}
		seen[c.Sites[i].host] = struct{}{}
	}
	return nil
}

func (s *SiteConfig) normalize() error {
	s.URL = strings.TrimRight(strings.TrimSpace(s.URL), "/")
	base, err := url.Parse(s.URL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return fmt.Errorf("url must be an absolute http(s) URL, got %q", s.URL)
	}
	s.base = base
	s.host = strings.ToLower(base.Hostname())
	if s.Name == "" {
		s.Name = s.host
	}
	if err := applyPreset(s); err != nil {
		return err
	}

	if s.CookieEnv != "" {
		if s.Cookie != "" && !s.resolvedCookie {
			return fmt.Errorf("cookie and cookie_env are mutually exclusive")
		}
		if !s.resolvedCookie {
			s.Cookie = strings.TrimSpace(os.Getenv(s.CookieEnv))
			if s.Cookie == "" {
				return fmt.Errorf("cookie_env %s is empty or unset", s.CookieEnv)
			}
			s.resolvedCookie = true
		}
	}
	s.Cookie = strings.TrimSpace(s.Cookie)
	if s.Cookie == "" {
		return fmt.Errorf("cookie or cookie_env is required")
	}

	s.Timeout = strings.TrimSpace(s.Timeout)
	if s.Timeout == "" {
		s.timeout = DefaultTimeout
	} else {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil || d < time.Second {
			return fmt.Errorf("timeout %q must be a duration >= 1s", s.Timeout)
		}
		s.timeout = d
	}

	if err := s.Signin.normalize(); err != nil {
		return err
	}
	if err := s.Messages.normalize(); err != nil {
		return err
	}
	if !s.Signin.enabled() && !s.Messages.enabled() {
		return fmt.Errorf("signin.mode and messages.engine are both off")
	}
	return nil
}

func (s *SigninConfig) normalize() error {
	s.Mode = strings.ToLower(strings.TrimSpace(s.Mode))
	if s.Mode == "" {
		s.Mode = "checkin"
	}
	switch s.Mode {
	case "checkin", "login", "off":
	default:
		return fmt.Errorf("signin.mode must be checkin, login or off, got %q", s.Mode)
	}
	if s.Mode == "off" {
		return nil
	}
	s.Path = strings.TrimSpace(s.Path)
	if s.Path == "" {
		s.Path = DefaultSigninPath
	}
	s.Method = strings.ToUpper(strings.TrimSpace(s.Method))
	if s.Method == "" {
		s.Method = "GET"
	}
	if s.Method != "GET" && s.Method != "POST" {
		return fmt.Errorf("signin.method must be GET or POST, got %q", s.Method)
	}
	if s.VerifyPath == "" {
		s.VerifyPath = s.Path
	}
	if s.TwoStep != nil {
		if strings.TrimSpace(s.TwoStep.Path) == "" {
			return fmt.Errorf("signin.two_step.path is required")
		}
		s.TwoStep.Method = strings.ToUpper(strings.TrimSpace(s.TwoStep.Method))
		if s.TwoStep.Method == "" {
			s.TwoStep.Method = "POST"
		}
		if s.TwoStep.Method != "GET" && s.TwoStep.Method != "POST" {
			return fmt.Errorf("signin.two_step.method must be GET or POST")
		}
	}
	return nil
}

func (s *MessagesConfig) normalize() error {
	s.Engine = strings.ToLower(strings.TrimSpace(s.Engine))
	if s.Engine == "" {
		s.Engine = "nexusphp"
	}
	if s.Engine != "nexusphp" && s.Engine != "off" {
		return fmt.Errorf("messages.engine must be nexusphp or off, got %q", s.Engine)
	}
	if s.Engine == "off" {
		return nil
	}
	s.Mode = strings.ToLower(strings.TrimSpace(s.Mode))
	if s.Mode == "" {
		s.Mode = "full"
	}
	if s.Mode != "full" && s.Mode != "list" {
		return fmt.Errorf("messages.mode must be full or list, got %q", s.Mode)
	}
	if s.InboxPath == "" {
		s.InboxPath = DefaultInboxPath
	}
	if s.SystemPath == "" {
		s.SystemPath = DefaultSystemPath
	}
	if s.MaxPages <= 0 {
		s.MaxPages = 5
	}
	return nil
}
