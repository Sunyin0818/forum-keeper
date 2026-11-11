package libra

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthRequest is one pending authorization. State and Verifier must be kept
// until the user pastes the callback URL back.
type AuthRequest struct {
	URL      string
	State    string
	Verifier string
}

// BeginAuth creates a PKCE verifier, a state value and the authorize URL.
func (c *Client) BeginAuth() (AuthRequest, error) {
	if c.cfg.ClientID == "" || c.cfg.RedirectURI == "" {
		return AuthRequest{}, errors.New("libra: client_id and redirect_uri are required for authorization")
	}
	verifier, err := randomURL(48)
	if err != nil {
		return AuthRequest{}, err
	}
	state, err := randomURL(16)
	if err != nil {
		return AuthRequest{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	u, err := url.Parse(c.cfg.BaseURL + "/oauth/authorize")
	if err != nil {
		return AuthRequest{}, fmt.Errorf("libra: authorize URL: %w", err)
	}
	q := u.Query()
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", c.cfg.RedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", c.cfg.Scopes)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return AuthRequest{URL: u.String(), State: state, Verifier: verifier}, nil
}

// Exchange turns an authorization code into a stored token pair.
func (c *Client) Exchange(ctx context.Context, code, verifier string) error {
	if code == "" || verifier == "" {
		return errors.New("libra: authorization code and PKCE verifier are required")
	}
	tok, err := c.postToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.cfg.RedirectURI},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"code_verifier": {verifier},
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = *tok
	return c.saveTokenLocked()
}

// refreshFor replaces the token after the server rejected stale. If another
// goroutine already refreshed, it keeps that newer token instead of rotating
// twice (which would invalidate the winner's refresh token).
func (c *Client) refreshFor(ctx context.Context, stale string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if stale != "" && c.token.AccessToken != stale && c.token.AccessToken != "" {
		return nil
	}
	return c.refreshLocked(ctx)
}

// refreshLocked rotates the token. The caller must hold c.mu.
func (c *Client) refreshLocked(ctx context.Context) error {
	if c.token.RefreshToken == "" {
		return ErrUnauthorized
	}
	tok, err := c.postToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.token.RefreshToken},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	})
	if err != nil {
		// invalid_grant and 400/401 mean the chain is gone; a 5xx or timeout is
		// transient and must keep the current refresh token for the next try.
		var apiErr *APIError
		if errors.Is(err, ErrUnauthorized) || (errors.As(err, &apiErr) && apiErr.Status == 400) {
			c.token = Token{}
			_ = c.saveTokenLocked()
			return ErrUnauthorized
		}
		return err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = c.token.RefreshToken
	}
	if tok.Scope == "" {
		tok.Scope = c.token.Scope
	}
	c.token = *tok
	return c.saveTokenLocked()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (c *Client) postToken(ctx context.Context, form url.Values) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/api/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("libra: token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", browserUA)

	res, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("libra: token request: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("libra: read token response: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("libra: decode token response (HTTP %d): %w", res.StatusCode, err)
	}
	if res.StatusCode >= 400 || env.C != 0 {
		return nil, &APIError{Status: res.StatusCode, Code: env.Code, Message: env.M}
	}
	var d tokenResponse
	if len(env.D) > 0 && string(env.D) != "null" {
		if err := json.Unmarshal(env.D, &d); err != nil {
			return nil, fmt.Errorf("libra: decode token payload: %w", err)
		}
	}
	if d.AccessToken == "" {
		return nil, errors.New("libra: token endpoint returned no access_token")
	}
	if d.ExpiresIn <= 0 {
		d.ExpiresIn = 3600
	}
	return &Token{
		AccessToken:  d.AccessToken,
		RefreshToken: d.RefreshToken,
		TokenType:    d.TokenType,
		Scope:        d.Scope,
		ExpiresAt:    c.now().Add(time.Duration(d.ExpiresIn) * time.Second),
	}, nil
}

func randomURL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("libra: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
