package libra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// browserUA matches the site-facing requests: 2libra sits behind Cloudflare
// and rejects obviously scripted clients on some routes.
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"

// tokenSkew refreshes slightly before expiry so a request never races the
// one-hour access-token lifetime.
const tokenSkew = 2 * time.Minute

// Config is what the client needs to authenticate and call the API.
type Config struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	TokenFile    string
	Scopes       string
	Timeout      time.Duration
}

// Client is a 2libra v1 API client with a self-refreshing token.
type Client struct {
	cfg   Config
	hc    *http.Client
	now   func() time.Time
	mu    sync.Mutex
	token Token
}

// New loads the persisted token (if any). A missing file is not an error: the
// caller runs --libra-auth and the next process picks the token up.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://2libra.com"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.Scopes == "" {
		cfg.Scopes = "profile read:me"
	}
	c := &Client{
		cfg: cfg,
		hc:  &http.Client{Timeout: cfg.Timeout},
		now: time.Now,
	}
	if cfg.TokenFile != "" {
		tok, err := loadToken(cfg.TokenFile)
		if err != nil {
			return nil, err
		}
		c.token = tok
	}
	return c, nil
}

// WebBase is the site origin used to build notification links.
func (c *Client) WebBase() string { return c.cfg.BaseURL }

// Authorized reports whether a token or refresh token is available.
func (c *Client) Authorized() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token.AccessToken != "" || c.token.RefreshToken != ""
}

// Summary returns the cheap identity snapshot. It is the startup / --check-config
// probe recommended by the API docs.
func (c *Client) Summary(ctx context.Context) (*Identity, error) {
	var out Identity
	if err := c.get(ctx, "/api/v1/me/summary", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Notifications fetches one page. limit is capped by the server at 50.
func (c *Client) Notifications(ctx context.Context, page, limit int) (*NotificationPage, error) {
	q := url.Values{}
	q.Set("page", fmt.Sprint(page))
	q.Set("limit", fmt.Sprint(limit))
	var out NotificationPage
	if err := c.get(ctx, "/api/v1/me/notifications", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type envelope struct {
	C    int             `json:"c"`
	Code string          `json:"code"`
	M    string          `json:"m"`
	D    json.RawMessage `json:"d"`
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.cfg.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var lastErr error
	for attempt := range 2 {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return fmt.Errorf("libra: request %s: %w", path, err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", browserUA)

		res, err := c.hc.Do(req)
		if err != nil {
			return fmt.Errorf("libra: %s: %w", path, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("libra: read %s: %w", path, readErr)
		}
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 {
			// One refresh per request: a rejected token is either rotated by
			// another goroutine or genuinely dead.
			if err := c.refreshFor(ctx, tok); err != nil {
				return err
			}
			lastErr = &APIError{Status: res.StatusCode, Code: "unauthorized", Message: "token rejected"}
			continue
		}
		return decodeEnvelope(path, res.StatusCode, raw, out)
	}
	if lastErr == nil {
		lastErr = ErrUnauthorized
	}
	return lastErr
}

func decodeEnvelope(path string, status int, raw []byte, out any) error {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("libra: decode %s (HTTP %d): %w", path, status, err)
	}
	if status >= 400 || env.C != 0 {
		code := env.Code
		if code == "" && status == http.StatusUnauthorized {
			code = "unauthorized"
		}
		msg := env.M
		if msg == "" {
			msg = http.StatusText(status)
		}
		return &APIError{Status: status, Code: code, Message: msg}
	}
	if out == nil || len(env.D) == 0 || string(env.D) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.D, out); err != nil {
		return fmt.Errorf("libra: decode %s payload: %w", path, err)
	}
	return nil
}

// accessToken returns a token that is valid for at least tokenSkew, refreshing
// it under the same mutex that serialises concurrent refreshes.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token.AccessToken == "" && c.token.RefreshToken == "" {
		return "", ErrUnauthorized
	}
	if c.token.AccessToken == "" || !c.now().Add(tokenSkew).Before(c.token.ExpiresAt) {
		if err := c.refreshLocked(ctx); err != nil {
			return "", err
		}
	}
	return c.token.AccessToken, nil
}

func (c *Client) saveTokenLocked() error {
	if c.cfg.TokenFile == "" {
		return nil
	}
	return saveToken(c.cfg.TokenFile, c.token)
}

func loadToken(path string) (Token, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Token{}, nil
		}
		return Token{}, fmt.Errorf("libra: read token file %s: %w", path, err)
	}
	var tok Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		return Token{}, fmt.Errorf("libra: parse token file %s (run --libra-auth again): %w", path, err)
	}
	return tok, nil
}

func saveToken(path string, tok Token) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("libra: create token directory %s: %w", dir, err)
		}
	}
	raw, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("libra: encode token: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".libra-token-*")
	if err != nil {
		return fmt.Errorf("libra: create token file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("libra: chmod token file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("libra: write token file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("libra: close token file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("libra: replace token file: %w", err)
	}
	return nil
}

// Scope returns the scope list granted to the stored token, for logs. It
// never includes the token itself.
func (c *Client) Scope() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token.Scope
}
