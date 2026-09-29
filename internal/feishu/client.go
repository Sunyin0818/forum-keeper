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

const maxAttempts = 3

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
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * retryBase):
			}
		}
		retryable, err := c.attempt(ctx, body)
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

func (c *Client) attempt(ctx context.Context, body []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhook, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("feishu: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, fmt.Errorf("feishu: request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code          int    `json:"code"`
		Msg           string `json:"msg"`
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return true, fmt.Errorf("feishu: HTTP %d (unparseable body): %w", resp.StatusCode, err)
		}
		return false, fmt.Errorf("feishu: HTTP %d (unparseable body): %w", resp.StatusCode, err)
	}

	// Feishu returns 200 with code != 0 for logical failures.
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return true, fmt.Errorf("feishu: HTTP %d: %s", resp.StatusCode, describe(result.Code, result.Msg, result.StatusCode, result.StatusMessage))
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("feishu: HTTP %d: %s", resp.StatusCode, describe(result.Code, result.Msg, result.StatusCode, result.StatusMessage))
	}
	if result.Code != 0 || result.StatusCode != 0 {
		return false, fmt.Errorf("feishu: rejected: %s", describe(result.Code, result.Msg, result.StatusCode, result.StatusMessage))
	}
	return false, nil
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

// retryBase is a variable so tests can shrink the retry delay.
var retryBase = time.Second

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
