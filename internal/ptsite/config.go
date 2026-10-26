// Package ptsite implements the generic PT-site sign-in and inbox reader.
//
// A site is described by a small rule set: the sign-in endpoint and the
// page that renders the inbox. Most private trackers run NexusPHP, so the
// defaults work without any per-site code; the rule fields exist for the
// variants (custom attendance paths, two-step forms, JSON APIs).
//
// The package deliberately has no browser dependency. Cloudflare / captcha
// pages are reported as a clear failure instead of being hidden behind a
// retry that can never succeed.
package ptsite

import (
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

// SiteConfig describes one tracker. The rule fields only matter for sites
// that differ from the NexusPHP defaults.
type SiteConfig struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`

	// Cookie is the Cookie request header. Prefer CookieEnv so the secret
	// stays in the process environment.
	Cookie    string `yaml:"cookie"`
	CookieEnv string `yaml:"cookie_env"`
	UA        string `yaml:"ua"`
	Timeout   string `yaml:"timeout"`

	// Preset selects a built-in rule set by name. Empty means auto-match by
	// host; "none" disables auto-matching.
	Preset   string         `yaml:"preset"`
	Signin   SigninConfig   `yaml:"signin"`
	Messages MessagesConfig `yaml:"messages"`

	// Resolved at load time; never serialized back.
	timeout        time.Duration
	host           string
	base           *url.URL
	resolvedCookie bool
}

// SigninConfig controls the daily sign-in or login keep-alive.
type SigninConfig struct {
	// Mode is "checkin" (default), "login" (only verify the session) or "off".
	Mode string `yaml:"mode"`
	// Path is the sign-in page, default /attendance.php.
	Path string `yaml:"path"`
	// Method is GET (default) or POST.
	Method string `yaml:"method"`
	// Data is sent as query parameters for GET or form fields for POST.
	Data map[string]string `yaml:"data"`
	// SubmitText selects the form button that triggers the sign-in.
	SubmitText string `yaml:"submit_text"`
	// Success / Already / LoginRequired override the default page patterns.
	Success       string `yaml:"success"`
	Already       string `yaml:"already"`
	LoginRequired string `yaml:"login_required"`
	// AbsentKeywords are success indicators that disappear after sign-in.
	AbsentKeywords []string `yaml:"absent_keywords"`
	// VerifyPath is re-read after a POST, default Path.
	VerifyPath string `yaml:"verify_path"`
	// TwoStep handles sites where the first page only carries a token.
	TwoStep *TwoStepConfig `yaml:"two_step"`
	// Bonus is an optional regex for the reward sentence.
	Bonus string `yaml:"bonus"`
}

// TwoStepConfig describes a token page followed by the real sign-in request.
type TwoStepConfig struct {
	Path   string            `yaml:"path"`
	Method string            `yaml:"method"`
	Data   map[string]string `yaml:"data"`
	// Tokens maps a form field name to a regex whose first group is the value.
	Tokens map[string]string `yaml:"tokens"`
	// Headers are extra request headers for the second request.
	Headers map[string]string `yaml:"headers"`
}

// MessagesConfig controls inbox reading.
type MessagesConfig struct {
	// Engine is "nexusphp" (default) or "off".
	Engine string `yaml:"engine"`
	// Mode is "full" (fetch the body, which marks the message read on
	// NexusPHP) or "list" (only titles/dates, leaving unread state alone).
	Mode       string `yaml:"mode"`
	InboxPath  string `yaml:"inbox_path"`
	SystemPath string `yaml:"system_path"`
	MaxPages   int    `yaml:"max_pages"`
	// Selectors overrides the built-in NexusPHP selectors when a theme differs.
	Selectors *MessageSelectors `yaml:"selectors"`
}

// MessageSelectors is an escape hatch for custom themes. Empty fields use the
// built-in selectors.
type MessageSelectors struct {
	UnreadRow string `yaml:"unread_row"`
	Link      string `yaml:"link"`
	Title     string `yaml:"title"`
	Date      string `yaml:"date"`
	Body      string `yaml:"body"`
}

func (c MessagesConfig) enabled() bool {
	return c.Engine != "off"
}

func (c SigninConfig) enabled() bool {
	return c.Mode != "off"
}

// Host returns the normalized host key used in state and dedupe keys.
func (s SiteConfig) Host() string { return s.host }

// Normalize validates the site rules and resolves the cookie. A missing
// cookie is an error rather than a silently disabled site.
func (s *SiteConfig) Normalize() error {
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
