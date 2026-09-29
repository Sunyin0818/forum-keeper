package v2ex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnauthorized is returned when the API rejects the personal access token.
// Callers should treat this as fatal and require a token rotation.
var ErrUnauthorized = errors.New("v2ex: unauthorized (token invalid or expired)")

const (
	maxAttempts = 3
	userAgent   = "v2ex-notifier/1.0 (+https://github.com/Sunyin0818/v2ex-notifier)"
)

// RateLimit mirrors the X-Rate-Limit-* response headers.
type RateLimit struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

// Client is a minimal V2EX API 2.0 client.
type Client struct {
	base  string
	token string
	hc    *http.Client

	mu   sync.Mutex
	rate RateLimit
}

// NewClient builds a client for the given base URL. When proxyURL is empty the
// standard environment proxy variables are honoured; otherwise the given proxy
// is used exclusively. This lets v2ex.com tunnel through a local proxy while
// other clients (Feishu) stay direct.
func NewClient(token, baseURL, proxyURL string, timeout time.Duration) (*Client, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return nil, errors.New("v2ex: base URL must not be empty")
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("v2ex: invalid base URL %q: %w", baseURL, err)
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

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
		ExpectContinueTimeout: 1 * time.Second,
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("v2ex: invalid V2EX_PROXY %q: %w", proxyURL, err)
		}
		transport.Proxy = http.ProxyURL(u)
	}

	return &Client{
		base:  baseURL,
		token: token,
		hc:    &http.Client{Transport: transport, Timeout: timeout},
	}, nil
}

// RateLimit returns the most recently observed rate limit headers.
func (c *Client) RateLimit() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rate
}

// Notifications fetches one page of notifications (newest first).
func (c *Client) Notifications(ctx context.Context, page int) ([]Notification, error) {
	if page < 1 {
		page = 1
	}
	var resp struct {
		Result  []Notification `json:"result"`
		Message string         `json:"message"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/notifications?p=%d", c.base, page), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// Topic fetches a single topic. Used to render human readable card titles.
func (c *Client) Topic(ctx context.Context, id int) (*Topic, error) {
	var resp struct {
		Result Topic `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/topics/%d", c.base, id), nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

// Member fetches the authenticated member profile. Useful as a startup check.
func (c *Client) Member(ctx context.Context) (*Member, error) {
	var resp struct {
		Result Member `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, c.base+"/member", nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

// DeleteNotification removes a notification. Used when MARK_READ is enabled.
func (c *Client) DeleteNotification(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/notifications/%d", c.base, id), nil, nil)
}

func (c *Client) do(ctx context.Context, method, rawURL string, body []byte, out any) error {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			delay := backoffDelay(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		retryable, err := c.attempt(ctx, method, rawURL, body, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable {
			return err
		}
	}
	return lastErr
}

// attempt performs one HTTP round trip and reports whether the failure is
// worth retrying.
func (c *Client) attempt(ctx context.Context, method, rawURL string, body []byte, out any) (retryable bool, err error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, fmt.Errorf("v2ex: request failed: %w", err)
	}
	defer resp.Body.Close()
	c.captureRateLimit(resp.Header)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, fmt.Errorf("%w: HTTP %d", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return true, fmt.Errorf("v2ex: rate limited: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		return true, fmt.Errorf("v2ex: HTTP %d: %s", resp.StatusCode, readSnippet(resp.Body))
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("v2ex: HTTP %d: %s", resp.StatusCode, readSnippet(resp.Body))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, nil
	}
	if err := decodeJSON(resp.Body, out); err != nil {
		return true, fmt.Errorf("v2ex: decode response: %w", err)
	}
	return false, nil
}

func (c *Client) captureRateLimit(h http.Header) {
	rl := RateLimit{
		Limit:     atoiDefault(h.Get("X-Rate-Limit-Limit")),
		Remaining: atoiDefault(h.Get("X-Rate-Limit-Remaining")),
	}
	if reset := atoiDefault(h.Get("X-Rate-Limit-Reset")); reset > 0 {
		rl.Reset = time.Unix(int64(reset), 0)
	}
	c.mu.Lock()
	c.rate = rl
	c.mu.Unlock()
}

func readSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}

func atoiDefault(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// backoffBase is a variable so tests can shrink the retry delay.
var backoffBase = time.Second

func backoffDelay(attempt int) time.Duration {
	// 2s, 4s, ... capped at 30s.
	d := time.Duration(1<<uint(attempt)) * backoffBase
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}
