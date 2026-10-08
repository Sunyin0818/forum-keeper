// Package feishu delivers notifications to a Feishu (Lark) custom bot webhook.
package feishu

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxAttempts = 4
	// codeFrequencyLimited is returned (with HTTP 200) when Feishu throttles a
	// webhook. It is transient and must be retried: treating it as a permanent
	// rejection drops the message for good.
	codeFrequencyLimited = 11232
)

// Client is a webhook client for a Feishu custom bot.
type Client struct {
	webhook string
	secret  string
	hc      *http.Client
}

// NewClient builds a client. secret may be empty when signature verification
// is disabled on the bot.
func NewClient(webhook, secret string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	transport := &http.Transport{
		// Feishu is reachable directly; deliberately ignore HTTP_PROXY so a
		// proxy configured for v2ex.com does not hijack these calls.
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        5,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &Client{
		webhook: webhook,
		secret:  secret,
		hc:      &http.Client{Transport: transport, Timeout: timeout},
	}
}

// SendText posts a plain text message.
func (c *Client) SendText(ctx context.Context, text string) error {
	return c.Send(ctx, map[string]any{
		"msg_type": "text",
		"content":  map[string]any{"text": text},
	})
}

// SendCard posts an interactive card.
func (c *Client) SendCard(ctx context.Context, card any) error {
	return c.Send(ctx, map[string]any{
		"msg_type": "interactive",
		"card":     card,
	})
}

// Send posts a raw webhook payload, adding the signature when configured.
func (c *Client) Send(ctx context.Context, payload map[string]any) error {
	if c.secret != "" {
		ts := time.Now().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = Sign(c.secret, ts)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("feishu: marshal payload: %w", err)
	}

	var lastErr error
	var rateLimited bool
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff(attempt, rateLimited)):
			}
		}
		outcome, err := c.attempt(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if !outcome.retryable {
			return err
		}
		rateLimited = outcome.rateLimited
	}
	return lastErr
}

// backoff returns how long to wait before a retry. A throttled webhook needs a
// much longer window than a transport error: Feishu's frequency limit is not
// cleared by the short step used for 5xx/network failures.
func backoff(attempt int, rateLimited bool) time.Duration {
	if rateLimited {
		return time.Duration(attempt-1) * rateLimitBase
	}
	return time.Duration(attempt) * retryBase
}

// attemptOutcome tells Send how to react to a failed attempt.
type attemptOutcome struct {
	// retryable means the same payload may succeed on a later attempt.
	retryable bool
	// rateLimited means Feishu throttled the webhook, which needs a longer
	// backoff than a transport error.
	rateLimited bool
}

func (c *Client) attempt(ctx context.Context, body []byte) (attemptOutcome, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhook, bytes.NewReader(body))
	if err != nil {
		return attemptOutcome{}, fmt.Errorf("feishu: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return attemptOutcome{}, ctx.Err()
		}
		return attemptOutcome{retryable: true}, fmt.Errorf("feishu: request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code          int    `json:"code"`
		Msg           string `json:"msg"`
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&result)
	// Classify throttling before anything else: some responses carry 11232
	// alongside an HTTP 429 or an unparseable body, and all of those are
	// transient rather than a permanent rejection.
	rateLimited := isFrequencyLimited(result.Code, result.StatusCode)

	if decodeErr != nil {
		out := attemptOutcome{retryable: resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests, rateLimited: rateLimited}
		return out, fmt.Errorf("feishu: HTTP %d (unparseable body): %w", resp.StatusCode, decodeErr)
	}

	// Feishu returns 200 with code != 0 for logical failures.
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return attemptOutcome{retryable: true, rateLimited: rateLimited}, fmt.Errorf("feishu: HTTP %d: %s", resp.StatusCode, describe(result.Code, result.Msg, result.StatusCode, result.StatusMessage))
	}
	if resp.StatusCode != http.StatusOK {
		return attemptOutcome{}, fmt.Errorf("feishu: HTTP %d: %s", resp.StatusCode, describe(result.Code, result.Msg, result.StatusCode, result.StatusMessage))
	}
	if result.Code != 0 || result.StatusCode != 0 {
		return attemptOutcome{retryable: rateLimited, rateLimited: rateLimited}, fmt.Errorf("feishu: rejected: %s", describe(result.Code, result.Msg, result.StatusCode, result.StatusMessage))
	}
	return attemptOutcome{}, nil
}

// isFrequencyLimited reports whether any of the codes is Feishu's "frequency
// limited". The difference matters: a throttled message is not lost, it only
// has to wait.
func isFrequencyLimited(codes ...int) bool {
	for _, code := range codes {
		if code == codeFrequencyLimited {
			return true
		}
	}
	return false
}

func describe(code int, msg string, statusCode int, statusMessage string) string {
	if msg == "" {
		msg = statusMessage
	}
	if msg == "" {
		msg = "unknown error"
	}
	c := code
	if c == 0 {
		c = statusCode
	}
	return fmt.Sprintf("code=%d msg=%s", c, strings.TrimSpace(msg))
}

// retryBase backs off transport errors; rateLimitBase backs off a throttled
// webhook. Both are variables so tests can shrink the delays.
var (
	retryBase     = time.Second
	rateLimitBase = 10 * time.Second
)

// Sign implements Feishu's HMAC-SHA256 webhook signature.
//
//	key  = timestamp + "\n" + secret
//	data = "" (empty)
//	digest is base64 encoded
func Sign(secret string, timestamp int64) string {
	key := fmt.Sprintf("%d\n%s", timestamp, secret)
	mac := hmac.New(sha256.New, []byte(key))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
