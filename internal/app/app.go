// Package app wires the source, state store and notifier together and owns the
// polling loop.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Sunyin0818/forum-keeper/internal/checkin"
	"github.com/Sunyin0818/forum-keeper/internal/config"
	"github.com/Sunyin0818/forum-keeper/internal/feishu"
	"github.com/Sunyin0818/forum-keeper/internal/store"
	"github.com/Sunyin0818/forum-keeper/internal/v2ex"
)

const (
	// notificationsPageSize is the page size V2EX uses for /notifications.
	notificationsPageSize = 20
	alertThreshold        = 5
	alertMinInterval      = 30 * time.Minute
)

// Fetcher is the read/write surface of the V2EX API used by the app.
type Fetcher interface {
	Notifications(ctx context.Context, page int) ([]v2ex.Notification, error)
	Topic(ctx context.Context, id int) (*v2ex.Topic, error)
	DeleteNotification(ctx context.Context, id int) error
	Member(ctx context.Context) (*v2ex.Member, error)
}

// State records which notification IDs have already been handled.
type State interface {
	FilterNew(ids []int) ([]int, error)
	Mark(ids []int) error
	Count() (int, error)
	LastStartup() (time.Time, error)
	SetStartup(at time.Time) error
	LastCheckin() (time.Time, error)
	SetCheckin(at time.Time) error
}

// Notifier delivers messages to the outside world.
type Notifier interface {
	Notify(ctx context.Context, items []v2ex.Notification) error
	Alert(ctx context.Context, text string) error
	Startup(ctx context.Context, lines []string) error
	Checkin(ctx context.Context, entries []feishu.CheckinEntry) error
}

// rateLimiter is implemented by the real client; used only for logging.
type rateLimiter interface {
	RateLimit() v2ex.RateLimit
}

// App is the notification service.
type App struct {
	cfg    *config.Config
	log    *slog.Logger
	src    Fetcher
	state  State
	bot    Notifier
	dryRun bool
	closer func() error

	failures  int
	lastAlert time.Time

	version string
	member  *v2ex.Member
	sites   []checkin.Site
}

// New builds an App from configuration, opening the state database and HTTP
// clients.
func New(cfg *config.Config, logger *slog.Logger, dryRun bool) (*App, error) {
	src, err := v2ex.NewClient(cfg.V2EXToken, cfg.V2EXBaseURL, cfg.HTTPTimeout)
	if err != nil {
		return nil, err
	}
	st, err := openState(cfg.StatePath, dryRun)
	if err != nil {
		return nil, err
	}
	bot := feishu.NewBot(feishu.NewClient(cfg.FeishuWebhook, cfg.FeishuSecret, cfg.HTTPTimeout), src)
	return NewWithDeps(cfg, logger, dryRun, src, st, bot), nil
}

// openState opens the state database. Dry runs use a read-only store so they
// never create or modify anything.
func openState(path string, dryRun bool) (State, error) {
	if dryRun {
		return store.OpenRead(path)
	}
	return store.Open(path)
}

// NewWithDeps builds an App with injected dependencies (used by tests).
func NewWithDeps(cfg *config.Config, logger *slog.Logger, dryRun bool, src Fetcher, st State, bot Notifier) *App {
	if logger == nil {
		logger = slog.Default()
	}
	a := &App{cfg: cfg, log: logger, src: src, state: st, bot: bot, dryRun: dryRun}
	if closer, ok := st.(interface{ Close() error }); ok {
		a.closer = closer.Close
	}
	a.sites = buildCheckinSites(cfg)
	return a
}

// buildCheckinSites turns credentials in the configuration into sites. A site
// is enabled exactly when its credential is present, so an existing deployment
// that never configured check-in keeps working unchanged.
func buildCheckinSites(cfg *config.Config) []checkin.Site {
	var sites []checkin.Site
	if cfg.V2EXCookie != "" {
		sites = append(sites, checkin.NewV2EXSite(cfg.V2EXCookie, cfg.V2EXWebBaseURL, cfg.HTTPTimeout))
	}
	if cfg.LibraCookie != "" {
		sites = append(sites, checkin.NewLibraSite(cfg.LibraCookie, cfg.LibraBaseURL, cfg.HTTPTimeout))
	}
	return sites
}

// SetCheckinSites overrides the configured sites (used by tests).
func (a *App) SetCheckinSites(sites []checkin.Site) {
	a.sites = sites
}

// Close releases resources held by the app.
func (a *App) Close() error {
	if a.closer == nil {
		return nil
	}
	return a.closer()
}

// SetVersion records the build version so the startup message can report it.
func (a *App) SetVersion(v string) {
	a.version = v
}

// CheckToken validates the personal access token and logs the identity.
func (a *App) CheckToken(ctx context.Context) error {
	m, err := a.src.Member(ctx)
	if err != nil {
		return err
	}
	a.member = m
	a.log.Info("authenticated with V2EX", "username", m.Username, "member_id", m.ID)
	return nil
}

// RemindersEnabled reports whether V2EX notification polling is configured.
// V2EX_TOKEN is optional: without it the service runs the daily check-in only.
func (a *App) RemindersEnabled() bool { return a.cfg.V2EXToken != "" }

// NotifyStartup sends the "service started" card.
//
// With force=false it honours NOTIFY_STARTUP and skips the message when one
// was sent within NOTIFY_STARTUP_COOLDOWN (so a restart loop cannot spam the
// group). force=true is used by the --notify-startup flag and bypasses both.
func (a *App) NotifyStartup(ctx context.Context, force bool) error {
	if !force {
		if !a.cfg.StartupMessage {
			return nil
		}
		if last, err := a.state.LastStartup(); err != nil {
			a.log.Warn("could not read last startup time", "err", err)
		} else if !last.IsZero() && a.cfg.StartupMessageCooldown > 0 {
			if since := time.Since(last); since < a.cfg.StartupMessageCooldown {
				a.log.Info("startup message skipped (cooldown)",
					"last_sent", last.Format(time.RFC3339), "ago", since.Round(time.Second))
				return nil
			}
		}
	}

	lines := a.startupLines()
	if a.dryRun {
		a.log.Info("dry-run: would send startup message", "lines", lines)
		return nil
	}
	if err := a.bot.Startup(ctx, lines); err != nil {
		return fmt.Errorf("send startup message: %w", err)
	}
	a.log.Info("startup message sent")
	if err := a.state.SetStartup(time.Now()); err != nil {
		a.log.Warn("could not record startup time", "err", err)
	}
	return nil
}

func (a *App) startupLines() []string {
	version := a.version
	if version == "" {
		version = "dev"
	}
	lines := []string{"版本: " + version}
	if a.member != nil {
		lines = append(lines, fmt.Sprintf("账号: @%s (id %d)", a.member.Username, a.member.ID))
	}
	lines = append(lines, "每日签到: "+a.checkinLabel())
	if a.RemindersEnabled() {
		lines = append(lines,
			"提醒: V2EX",
			"轮询间隔: "+a.cfg.PollInterval.String(),
			"首轮策略: "+a.cfg.FirstRun,
			"标记已读: "+yesNo(a.cfg.MarkRead),
			"类型过滤: "+filterLabel(a.cfg.FilterTypes),
		)
	} else {
		lines = append(lines, "提醒: 未启用（未设置 V2EX_TOKEN）")
	}
	lines = append(lines,
		"V2EX 代理: "+proxyLabel(),
		"状态文件: "+a.cfg.StatePath,
	)
	return lines
}

// checkinLabel summarises the check-in schedule for the startup card.
func (a *App) checkinLabel() string {
	if len(a.sites) == 0 {
		return "未启用"
	}
	names := make([]string, 0, len(a.sites))
	for _, s := range a.sites {
		names = append(names, s.Name())
	}
	loc := a.cfg.CheckinLocation
	if loc == nil {
		loc = time.Local
	}
	return fmt.Sprintf("%s（每天 %02d:%02d %s）",
		strings.Join(names, " + "), a.cfg.CheckinHour, a.cfg.CheckinMinute, loc)
}

// checkinLoc returns the configured schedule zone, falling back to the process
// zone when it is unset (tests build a Config directly).
func (a *App) checkinLoc() *time.Location {
	if a.cfg.CheckinLocation != nil {
		return a.cfg.CheckinLocation
	}
	return time.Local
}

// Run polls until the context is cancelled.
func (a *App) Run(ctx context.Context) error {
	a.log.Info("forum-keeper starting",
		"version", a.version,
		"interval", a.cfg.PollInterval.String(),
		"dry_run", a.dryRun,
		"first_run", a.cfg.FirstRun,
		"mark_read", a.cfg.MarkRead,
		"state", a.cfg.StatePath,
		"filter_types", a.cfg.FilterTypes,
	)
	if err := a.NotifyStartup(ctx, false); err != nil {
		// A failed startup message must not stop the service.
		a.log.Warn("startup message failed", "err", err)
	}
	if len(a.sites) > 0 {
		a.maybeCheckinOnStart(ctx)
		go a.checkinLoop(ctx)
	}

	if !a.RemindersEnabled() {
		a.log.Info("V2EX reminders disabled (V2EX_TOKEN is empty); running check-in only")
		<-ctx.Done()
		a.log.Info("shutting down")
		return ctx.Err()
	}

	a.pollAndLog(ctx)

	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.log.Info("shutting down")
			return ctx.Err()
		case <-ticker.C:
			a.pollAndLog(ctx)
		}
	}
}

// RunCheckins signs in to every configured site once and reports the outcome as
// a single Feishu card. It is safe to call repeatedly: every site treats
// "already signed in today" as a success.
func (a *App) RunCheckins(ctx context.Context) error {
	if len(a.sites) == 0 {
		a.log.Info("check-in skipped: no site configured")
		return nil
	}

	results := checkin.Run(ctx, a.log, a.sites)
	ok := 0
	for _, r := range results {
		if r.OK() {
			ok++
		}
	}
	a.log.Info("check-in finished", "ok", ok, "total", len(results))

	if a.dryRun {
		for _, r := range results {
			a.log.Info("dry-run: check-in result", "site", r.Site, "status", string(r.Status), "detail", r.Detail)
		}
		return nil
	}

	entries := make([]feishu.CheckinEntry, 0, len(results))
	for _, r := range results {
		entries = append(entries, feishu.CheckinEntry{Site: r.Site, OK: r.OK(), Detail: r.Detail})
	}
	if err := a.bot.Checkin(ctx, entries); err != nil {
		return fmt.Errorf("send check-in card: %w", err)
	}
	if err := a.state.SetCheckin(time.Now()); err != nil {
		a.log.Warn("could not record check-in time", "err", err)
	}
	return nil
}

// maybeCheckinOnStart catches up at startup when CHECKIN_ON_START is enabled:
// if today's sign-in has not happened yet (the container was down or restarting
// across the scheduled time), it runs immediately. The per-day guard matters
// because containers restart often and a restart loop must not spam the group.
func (a *App) maybeCheckinOnStart(ctx context.Context) {
	if !a.cfg.CheckinOnStart {
		return
	}
	if a.checkedInToday() {
		a.log.Info("check-in on start skipped: already ran today")
		return
	}
	a.log.Info("running check-in on start (catch-up)")
	if err := a.RunCheckins(ctx); err != nil {
		a.log.Warn("check-in on start failed", "err", err)
	}
}

// checkedInToday reports whether a check-in already ran on the current
// calendar day in the configured zone.
func (a *App) checkedInToday() bool {
	last, err := a.state.LastCheckin()
	if err != nil {
		a.log.Warn("could not read last check-in time", "err", err)
		return false
	}
	return !last.IsZero() && checkin.SameLocalDay(last, time.Now(), a.checkinLoc())
}

// checkinLoop sleeps until the configured wall-clock time and then signs in.
func (a *App) checkinLoop(ctx context.Context) {
	for {
		now := time.Now()
		delay := checkin.UntilNext(now, a.cfg.CheckinHour, a.cfg.CheckinMinute, a.checkinLoc())
		next := now.Add(delay).In(a.checkinLoc())
		a.log.Info("next check-in scheduled",
			"at", next.Format("2006-01-02 15:04:05"),
			"in", delay.Round(time.Second).String())

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		// A start-up catch-up (or an earlier run today) may already have signed
		// in; do not send a second card.
		if a.checkedInToday() {
			a.log.Info("scheduled check-in skipped: already ran today")
			continue
		}

		runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := a.RunCheckins(runCtx)
		cancel()
		if err != nil {
			a.log.Error("check-in failed", "err", err)
		}
	}
}

// PollOnce performs a single fetch/diff/push cycle and returns how many
// notifications were pushed.
func (a *App) PollOnce(ctx context.Context) (int, error) {
	items, err := a.fetch(ctx)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		a.log.Debug("no notifications returned")
		return 0, nil
	}

	ids := make([]int, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}

	newIDs, err := a.state.FilterNew(ids)
	if err != nil {
		return 0, err
	}
	if len(newIDs) == 0 {
		a.log.Debug("no new notifications", "fetched", len(items))
		return 0, nil
	}

	count, err := a.state.Count()
	if err != nil {
		return 0, err
	}

	// On the very first ever run the store is empty. By default we only record
	// the current notifications so a fresh deploy does not spam the group.
	if count == 0 && a.cfg.FirstRun == config.FirstRunSkip {
		if a.dryRun {
			a.log.Info("dry-run: first run would record existing notifications without pushing", "count", len(ids))
			for _, it := range items {
				a.log.Info("dry-run: would record (not push)",
					"id", it.ID,
					"member", it.Member.Username,
					"text", it.Text,
					"topic", feishu.TopicID(it),
					"created", time.Unix(it.Created, 0).Format(time.RFC3339),
				)
			}
			return 0, nil
		}
		a.log.Info("first run: recording existing notifications without pushing", "count", len(ids))
		if err := a.state.Mark(ids); err != nil {
			return 0, err
		}
		return 0, nil
	}

	isNew := make(map[int]struct{}, len(newIDs))
	for _, id := range newIDs {
		isNew[id] = struct{}{}
	}

	fresh := make([]v2ex.Notification, 0, len(newIDs))
	for _, it := range items {
		if _, ok := isNew[it.ID]; !ok {
			continue
		}
		if !a.passFilter(it) {
			continue
		}
		fresh = append(fresh, it)
	}

	if a.dryRun {
		for _, it := range fresh {
			a.log.Info("dry-run: would push",
				"id", it.ID,
				"member", it.Member.Username,
				"text", it.Text,
				"topic", feishu.TopicID(it),
				"snippet", feishu.Snippet(it),
			)
		}
		if len(fresh) == 0 {
			a.log.Info("dry-run: filtered out all new notifications", "new", len(newIDs))
		}
		return len(fresh), nil
	}

	if len(fresh) == 0 {
		// Everything new was filtered out. Remember it so we do not re-evaluate
		// the same entries on every poll.
		if err := a.state.Mark(newIDs); err != nil {
			return 0, err
		}
		return 0, nil
	}

	if err := a.bot.Notify(ctx, fresh); err != nil {
		return 0, fmt.Errorf("push notifications: %w", err)
	}

	// Log here rather than in pollAndLog so --once reports pushes too.
	a.log.Info("pushed notifications", "count", len(fresh))

	// Persist only after a successful push: a failing webhook is retried on the
	// next poll instead of being silently dropped.
	if err := a.state.Mark(newIDs); err != nil {
		return len(fresh), err
	}

	if a.cfg.MarkRead {
		for _, it := range fresh {
			if err := a.src.DeleteNotification(ctx, it.ID); err != nil {
				a.log.Warn("mark read failed", "id", it.ID, "err", err)
			}
		}
	}
	return len(fresh), nil
}

// fetch walks notification pages (newest first) until it has seen everything
// worth looking at.
func (a *App) fetch(ctx context.Context) ([]v2ex.Notification, error) {
	var all []v2ex.Notification
	seen := make(map[int]struct{})

	for page := 1; page <= a.cfg.MaxPages; page++ {
		items, err := a.src.Notifications(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("fetch notifications page %d: %w", page, err)
		}
		if len(items) == 0 {
			break
		}

		pageIDs := make([]int, 0, len(items))
		for _, it := range items {
			if _, dup := seen[it.ID]; dup {
				continue
			}
			seen[it.ID] = struct{}{}
			all = append(all, it)
			pageIDs = append(pageIDs, it.ID)
		}

		if len(items) < notificationsPageSize {
			break
		}
		// Notifications are ordered newest first, so an entirely-known page
		// means every following page is known too.
		newOnPage, err := a.state.FilterNew(pageIDs)
		if err != nil {
			return nil, err
		}
		if len(newOnPage) == 0 {
			break
		}
	}
	return all, nil
}

func (a *App) passFilter(it v2ex.Notification) bool {
	if len(a.cfg.FilterTypes) == 0 {
		return true
	}
	kind := feishu.KindOf(it.Text)
	for _, f := range a.cfg.FilterTypes {
		if strings.EqualFold(f, kind) {
			return true
		}
	}
	return false
}

func (a *App) pollAndLog(ctx context.Context) {
	if !a.RemindersEnabled() {
		return
	}
	_, err := a.PollOnce(ctx)
	if err != nil {
		a.failures++
		a.log.Error("poll failed", "err", err, "consecutive_failures", a.failures)
		a.maybeAlert(ctx, err)
		return
	}
	if a.failures > 0 {
		a.log.Info("recovered after failures", "previous_failures", a.failures)
		a.failures = 0
	}
	if rl, ok := a.src.(rateLimiter); ok {
		if r := rl.RateLimit(); r.Limit > 0 {
			a.log.Debug("rate limit", "remaining", r.Remaining, "limit", r.Limit, "reset", r.Reset.Format(time.RFC3339))
		}
	}
}

func (a *App) maybeAlert(ctx context.Context, cause error) {
	if !a.cfg.AlertOnError || a.failures < alertThreshold {
		return
	}
	if time.Since(a.lastAlert) < alertMinInterval {
		return
	}
	a.lastAlert = time.Now()

	text := fmt.Sprintf("⚠️ V2EX 通知服务异常\n连续失败 %d 次\n最近错误: %s", a.failures, truncateErr(cause, 300))

	// The alert must still go out even if the parent context is cancelled.
	alertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := a.bot.Alert(alertCtx, text); err != nil {
		a.log.Error("failed to send alert", "err", err)
	}
}

func truncateErr(err error, max int) string {
	s := err.Error()
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max])) + "…"
}

func yesNo(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func filterLabel(types []string) string {
	if len(types) == 0 {
		return "全部"
	}
	return strings.Join(types, ", ")
}

// proxyLabel reports whether a proxy is configured for outgoing V2EX requests,
// and which standard variable supplied it. Only the variable *name* is reported:
// its value may embed credentials and would otherwise end up in the Feishu
// message as plain text.
func proxyLabel() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return "已设置（" + key + "）"
		}
	}
	return "未设置（直连）"
}
