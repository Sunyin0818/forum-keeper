package ptsite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

const (
	maxBodyBytes = 2 << 20
	maxAttempts  = 2
)

const defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// Site is one tracker with its own cookie jar and rate-limited client.
type Site struct {
	cfg  SiteConfig
	log  *slog.Logger
	hc   *http.Client
	base *url.URL

	dryRun      bool
	verifyDelay time.Duration
	now         func() time.Time
}

type page struct {
	Text     string
	Status   int
	FinalURL *url.URL
	Header   http.Header
}

// NewSite builds a site from an already validated config. It is exported so
// tests can drive the engine against an httptest server.
func NewSite(cfg SiteConfig, log *slog.Logger, dryRun bool) (*Site, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("ptsite: cookie jar: %w", err)
	}
	jar.SetCookies(cfg.base, parseCookieHeader(cfg.Cookie))

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	hc := &http.Client{
		Transport: transport,
		Jar:       jar,
		Timeout:   cfg.timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	return &Site{
		cfg:         cfg,
		log:         log,
		hc:          hc,
		base:        cfg.base,
		dryRun:      dryRun,
		verifyDelay: time.Second,
		now:         time.Now,
	}, nil
}

// Name implements checkin.Site.
func (s *Site) Name() string { return s.cfg.Name }

// Host returns the normalized host used as the state key.
func (s *Site) Host() string { return s.cfg.host }

// Config returns a copy of the resolved site config.
func (s *Site) Config() SiteConfig { return s.cfg }

// SetVerifyDelay is used by tests to avoid sleeping after a POST.
func (s *Site) SetVerifyDelay(d time.Duration) { s.verifyDelay = d }

func (s *Site) userAgent() string {
	if strings.TrimSpace(s.cfg.UA) != "" {
		return strings.TrimSpace(s.cfg.UA)
	}
	return defaultUA
}

// get fetches a same-site page.
func (s *Site) get(ctx context.Context, path string) (page, error) {
	return s.getWithHeaders(ctx, path, nil)
}

func (s *Site) getWithHeaders(ctx context.Context, path string, headers map[string]string) (page, error) {
	target, ok := s.resolve(path)
	if !ok {
		return page{}, fmt.Errorf("refusing to request a different host: %q", path)
	}
	return s.do(ctx, http.MethodGet, target, nil, headers)
}

// postForm submits a same-site form.
func (s *Site) postForm(ctx context.Context, path string, form url.Values) (page, error) {
	return s.postFormWithHeaders(ctx, path, form, nil)
}

func (s *Site) postFormWithHeaders(ctx context.Context, path string, form url.Values, headers map[string]string) (page, error) {
	target, ok := s.resolve(path)
	if !ok {
		return page{}, fmt.Errorf("refusing to submit to a different host: %q", path)
	}
	return s.do(ctx, http.MethodPost, target, form, headers)
}

func (s *Site) do(ctx context.Context, method string, target *url.URL, form url.Values, extra map[string]string) (page, error) {
	var body []byte
	if form != nil {
		body = []byte(form.Encode())
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return page{}, ctx.Err()
			case <-time.After(time.Duration(attempt-1) * time.Second):
			}
		}
		p, retry, err := s.attempt(ctx, method, target, body, extra)
		if err == nil {
			return p, nil
		}
		lastErr = err
		if !retry {
			return page{}, err
		}
	}
	return page{}, lastErr
}

func (s *Site) attempt(ctx context.Context, method string, target *url.URL, body []byte, extra map[string]string) (page, bool, error) {
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return page{}, false, err
	}
	req.Header.Set("User-Agent", s.userAgent())
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.8,*/*;q=0.7")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", s.base.String()+"/")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}

	resp, err := s.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return page{}, false, ctx.Err()
		}
		return page{}, true, fmt.Errorf("request %s: %w", target.Redacted(), err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return page{}, true, fmt.Errorf("read %s: %w", target.Redacted(), err)
	}
	text, err := decodeBody(raw, resp.Header.Get("Content-Type"))
	if err != nil {
		return page{}, false, fmt.Errorf("decode %s: %w", target.Redacted(), err)
	}

	p := page{Text: text, Status: resp.StatusCode, FinalURL: resp.Request.URL, Header: resp.Header}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return p, true, fmt.Errorf("HTTP %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		return p, false, nil // the caller classifies 404/405 and login redirects
	}
	return p, false, nil
}

func decodeBody(raw []byte, contentType string) (string, error) {
	reader, err := charset.NewReader(bytes.NewReader(raw), contentType)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Site) resolve(ref string) (*url.URL, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "/"
	}
	u, err := url.Parse(ref)
	if err != nil {
		return nil, false
	}
	if !u.IsAbs() {
		u = s.base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false
	}
	if !sameHost(u.Hostname(), s.base.Hostname()) {
		return nil, false
	}
	return u, true
}

func sameHost(a, b string) bool {
	a = strings.ToLower(strings.TrimPrefix(a, "www."))
	b = strings.ToLower(strings.TrimPrefix(b, "www."))
	return a == b
}

func parseCookieHeader(header string) []*http.Cookie {
	var out []*http.Cookie
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, &http.Cookie{Name: name, Value: strings.Trim(strings.TrimSpace(value), `"`)})
	}
	return out
}

// --- small HTML helpers ----------------------------------------------------

var (
	tagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	scriptRe  = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<noscript\b[^>]*>.*?</noscript>`)
	spaceRe   = regexp.MustCompile(`[ \t\r\f\v]+`)
	newlineRe = regexp.MustCompile(`\n{3,}`)
)

// visibleText renders the visible text of an HTML fragment. It is used for
// pattern matching only, so whitespace is collapsed aggressively. Using the
// tag-stripping helper instead of goquery's Text() keeps a separator between
// block elements ("已签到<br>领取") and avoids gluing words together.
func visibleText(input string) string {
	return normalizeSpace(htmlToText(input))
}

// htmlToText strips tags while preserving paragraph breaks.
func htmlToText(input string) string {
	s := scriptRe.ReplaceAllString(input, " ")
	s = regexp.MustCompile(`(?i)<br\s*/?>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?i)</(p|div|tr|li|h[1-6]|table|section|article)>`).ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, " ")
	s = htmlUnescape(s)
	s = spaceRe.ReplaceAllString(s, " ")
	s = newlineRe.ReplaceAllString(s, "\n")
	return strings.TrimSpace(s)
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(htmlUnescape(s)), " ")
}

func htmlUnescape(s string) string {
	return html.UnescapeString(s)
}
