// Command notifier polls V2EX notifications and forwards new ones to a Feishu
// bot webhook.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Sunyin0818/v2ex-notifier/internal/app"
	"github.com/Sunyin0818/v2ex-notifier/internal/config"
	"github.com/Sunyin0818/v2ex-notifier/internal/v2ex"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var (
		once          = flag.Bool("once", false, "run a single poll and exit")
		dryRun        = flag.Bool("dry-run", false, "log what would be pushed without sending or mutating state")
		notifyStartup = flag.Bool("notify-startup", false, "send the startup message and exit (webhook smoke test)")
		showVersion   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("v2ex-notifier", version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(2)
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	service, err := app.New(cfg, logger, *dryRun)
	if err != nil {
		logger.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := service.Close(); err != nil {
			logger.Warn("closing state store", "err", err)
		}
	}()
	service.SetVersion(version)

	// Fail fast on an invalid token; tolerate transient API outages.
	if err := service.CheckToken(ctx); err != nil {
		if errors.Is(err, v2ex.ErrUnauthorized) {
			logger.Error("V2EX rejected the token; rotate V2EX_TOKEN", "err", err)
			os.Exit(1)
		}
		logger.Warn("could not verify token at startup, continuing", "err", err)
	}

	if *notifyStartup {
		if err := service.NotifyStartup(ctx, true); err != nil {
			logger.Error("startup message failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if *once {
		if _, err := service.PollOnce(ctx); err != nil {
			logger.Error("poll failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := service.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("service stopped", "err", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
