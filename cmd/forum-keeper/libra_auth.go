package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/Sunyin0818/forum-keeper/internal/config"
	"github.com/Sunyin0818/forum-keeper/internal/libra"
)

// runLibraAuth performs the one-time OAuth authorization. The daemon cannot
// listen on a port, so the callback is loopback: the user pastes the full
// redirect URL back (the code is single-use and expires after 10 minutes).
func runLibraAuth(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	if cfg.Libra.ClientID == "" || cfg.Libra.ClientSecret == "" || cfg.Libra.RedirectURI == "" {
		return errors.New("libra.client_id, libra.client_secret and libra.redirect_uri are required")
	}
	c, err := libra.New(libra.Config{
		BaseURL:      cfg.Libra.BaseURL,
		ClientID:     cfg.Libra.ClientID,
		ClientSecret: cfg.Libra.ClientSecret,
		RedirectURI:  cfg.Libra.RedirectURI,
		TokenFile:    cfg.LibraTokenFile(),
		Scopes:       cfg.Libra.Scopes,
		Timeout:      cfg.Server.HTTPTimeout,
	})
	if err != nil {
		return err
	}
	req, err := c.BeginAuth()
	if err != nil {
		return err
	}

	fmt.Println("1. 在浏览器打开下面的授权页面并点击「同意」：")
	fmt.Println()
	fmt.Println("   " + req.URL)
	fmt.Println()
	fmt.Println("2. 浏览器会跳到 " + cfg.Libra.RedirectURI + "（端口可能被省略；打不开是正常的）。")
	fmt.Println("   从地址栏复制整条回调 URL，粘贴到下面并回车：")
	fmt.Print("> ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return fmt.Errorf("read callback URL: %w", err)
	}
	code, state, err := parseLibraCallback(strings.TrimSpace(line))
	if err != nil {
		return err
	}
	if state != req.State {
		return errors.New("state mismatch: the pasted URL does not belong to this authorization attempt")
	}
	if err := c.Exchange(ctx, code, req.Verifier); err != nil {
		return err
	}
	id, err := c.Summary(ctx)
	if err != nil {
		return fmt.Errorf("token stored, but the identity probe failed: %w", err)
	}
	logger.Info("2libra authorization stored",
		"username", id.Username,
		"token_file", cfg.LibraTokenFile(),
		"scope", c.Scope(),
	)
	fmt.Printf("授权成功：@%s\n令牌已写入 %s（0600）。现在可以启动服务。\n", id.Username, cfg.LibraTokenFile())
	return nil
}

// parseLibraCallback accepts a full redirect URL or a bare query string and
// extracts code/state. A bare code is rejected: without the state value the
// CSRF check is impossible.
func parseLibraCallback(raw string) (code, state string, err error) {
	if raw == "" {
		return "", "", errors.New("empty callback URL")
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", fmt.Errorf("parse callback URL: %w", perr)
	}
	q := u.Query()
	if len(q) == 0 {
		// Allow "code=...&state=..." without the leading '?'.
		if parsed, qerr := url.ParseQuery(strings.TrimPrefix(raw, "?")); qerr == nil {
			q = parsed
		}
	}
	if e := q.Get("error"); e != "" {
		return "", "", fmt.Errorf("2libra denied the authorization: %s %s", e, q.Get("error_description"))
	}
	code = q.Get("code")
	state = q.Get("state")
	if code == "" {
		return "", "", errors.New("no code in the pasted URL; copy the full address bar value")
	}
	if state == "" {
		return "", "", errors.New("no state in the pasted URL; copy the full address bar value")
	}
	return code, state, nil
}
