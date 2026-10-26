// Package app wires the source, state store and notifier together and owns the
// polling loop.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Sunyin0818/forum-keeper/internal/checkin"
	"github.com/Sunyin0818/forum-keeper/internal/config"
	"github.com/Sunyin0818/forum-keeper/internal/feishu"
	"github.com/Sunyin0818/forum-keeper/internal/ptsite"
	"github.com/Sunyin0818/forum-keeper/internal/store"
	"github.com/Sunyin0818/forum-keeper/internal/v2ex"
)

const (
	// notificationsPageSize is the page size V2EX uses for /notifications.
	notificationsPageSize = 20
	alertThreshold        = 5
	alertMinInterval      = 30 * time.Minute
	// checkinRunTimeout bounds one scheduled run including card retries.
	checkinRunTimeout = 10 * time.Minute
)

// checkinCardAttempts and checkinCardRetryBase bound the retry of the check-in
// card only. Variables so tests can shrink the delays.
var (
	checkinCardAttempts  = 3
	checkinCardRetryBase = time.Minute
)

// Fetcher is the read/write surface of the V2EX API used by the app.
type Fetcher interface {
	Notifications(ctx context.Context, page int) ([]v2ex.Notification, error)
	Topic(ctx context.Context, id int) (*v2ex.Topic, error)
	DeleteNotification(ctx context.Context, id int) error
	Member(ctx context.Context) (*v2ex.Member, error)
}

// pendingCardState stores a check-in card that could not be delivered. It is
// implemented by the bbolt store; tests can omit it.
type pendingCardState interface {
	SavePendingCheckinCard(group string, entries []byte, markDone bool) error
	PendingCheckinCard(group string) ([]byte, bool, bool, error)
	ClearPendingCheckinCard(group string) error
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
	CheckinGroup(ctx context.Context, group string, entries []feishu.CheckinEntry) error
	SiteMessages(ctx context.Context, messages []feishu.SiteMessage) error
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

	// retryAt moves the next scheduled run to a pending site's window (V2EX
	// opens its mission day at 08:00 UTC+8). It is only touched by the check-in
	// path: written while starting up and inside the check-in loop goroutine.
	retryAt time.Time

	version string
	member  *v2ex.Member
	sites   []checkin.Site

	ptRunner       *ptsite.Runner
	ptPollInterval time.Duration
}

// New builds an App from configuration, opening the state database and HTTP
// clients.
func New(cfg *config.Config, logger *slog.Logger, dryRun bool) (*App, error) {
	src, err := v2ex.NewClient(cfg.V2EX.Token, cfg.V2EX.APIBaseURL, cfg.Server.HTTPTimeout)
	if err != nil {
		return nil, err
	}
	st, err := openState(cfg.Server.StatePath, dryRun)
	if err != nil {
		return nil, err
	}
	if pruner, ok := st.(interface{ PrunePTSeen(time.Time) error }); ok {
		if err := pruner.PrunePTSeen(time.Now().Add(-90 * 24 * time.Hour)); err != nil {
			logger.Warn("prune PT message state", "err", err)
		}
	}
	ptSites, err := loadPTSites(cfg, logger, dryRun)
	if err != nil {
		if closer, ok := st.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return nil, err
	}
	bot := feishu.NewBot(feishu.NewClient(cfg.Feishu.Webhook, cfg.Feishu.Secret, cfg.Server.HTTPTimeout), src)
	a := NewWithDeps(cfg, logger, dryRun, src, st, bot)
	if len(ptSites) > 0 {
		a.SetPTSites(ptSites)
	}
	return a, nil
}

// loadPTSites builds one client per configured tracker. A malformed site is a
// startup error: silently disabling a tracker would make a missing sign-in
// look like a completed one.
func loadPTSites(cfg *config.Config, logger *slog.Logger, dryRun bool) ([]*ptsite.Site, error) {
	if len(cfg.PT.Sites) == 0 {
		return nil, nil
	}
	sites := make([]*ptsite.Site, 0, len(cfg.PT.Sites))
	for _, sc := range cfg.PT.Sites {
		site, err := ptsite.NewSite(sc, logger, dryRun)
		if err != nil {
			return nil, fmt.Errorf("ptsite %s: %w", sc.Name, err)
		}
		sites = append(sites, site)
	}
	logger.Info("pt sites loaded", "sites", len(sites), "poll", cfg.PT.PollInterval.String())
	return sites, nil
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
	a.ptPollInterval = cfg.PT.PollInterval
	if a.ptPollInterval < time.Minute {
		a.ptPollInterval = 2 * time.Hour
	}
	return a
}

// SetPTSites attaches PT sites. It is called by New after loading the config;
// tests use it to inject a runner backed by an httptest server.
func (a *App) SetPTSites(sites []*ptsite.Site) {
	a.ptPollInterval = a.cfg.PT.PollInterval
	if a.ptPollInterval < time.Minute {
		a.ptPollInterval = 2 * time.Hour
	}
	firstRun := a.cfg.PT.FirstRun
	if firstRun == "" {
		firstRun = ptsite.FirstRunSkip
	}
	state, _ := a.state.(ptsite.State)
	a.ptRunner = ptsite.NewRunner(
		sites, state, a.log, firstRun,
		a.cfg.PT.Retries+1, a.cfg.PT.Concurrency, a.dryRun,
	)
}

// buildCheckinSites turns credentials in the configuration into sites. A site
// is enabled exactly when its credential is present, so an existing deployment
// that never configured check-in keeps working unchanged.
func buildCheckinSites(cfg *config.Config) []checkin.Site {
	var sites []checkin.Site
	if cfg.V2EX.Cookie != "" {
		sites = append(sites, checkin.NewV2EXSite(cfg.V2EX.Cookie, cfg.V2EX.WebBaseURL, cfg.Server.HTTPTimeout))
	}
	if cfg.Libra.Cookie != "" {
		sites = append(sites, checkin.NewLibraSite(cfg.Libra.Cookie, cfg.Libra.BaseURL, cfg.Server.HTTPTimeout))
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

// CheckConfig validates the loaded configuration and prints the resolved
// sites without secrets. It is used by --check-config.
func (a *App) CheckConfig() error {
	for _, site := range a.sites {
		a.log.Info("check-in site", "name", site.Name())
	}
	if a.ptRunner == nil {
		a.log.Info("PT sites: disabled (pt.sites is empty)")
		return nil
	}
	for _, site := range a.ptRunner.Sites() {
		cfg := site.Config()
		cookieFrom := "inline"
		if cfg.CookieEnv != "" {
			cookieFrom = "env:" + cfg.CookieEnv
		}
		a.log.Info("PT site",
			"name", cfg.Name,
			"host", cfg.Host(),
			"preset", cfg.Preset,
			"cookie_from", cookieFrom,
			"signin", cfg.Signin.Mode,
			"signin_path", cfg.Signin.Path,
			"messages", cfg.Messages.Engine,
			"messages_mode", cfg.Messages.Mode,
		)
	}
	a.log.Info("config OK", "pt_sites", len(a.ptRunner.Sites()))
	return nil
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
func (a *App) RemindersEnabled() bool { return a.cfg.V2EX.Token != "" }

// NotifyStartup sends the "service started" card.
//
// With force=false it honours NOTIFY_STARTUP and skips the message when one
// was sent within NOTIFY_STARTUP_COOLDOWN (so a restart loop cannot spam the
// group). force=true is used by the --notify-startup flag and bypasses both.
func (a *App) NotifyStartup(ctx context.Context, force bool) error {
	if !force {
		if !a.cfg.Notify.Startup {
			return nil
		}
		if last, err := a.state.LastStartup(); err != nil {
			a.log.Warn("could not read last startup time", "err", err)
		} else if !last.IsZero() && a.cfg.Notify.StartupCooldown > 0 {
			if since := time.Since(last); since < a.cfg.Notify.StartupCooldown {
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
	if a.ptRunner != nil {
		lines = append(lines, fmt.Sprintf("PT 站点: %d 个 · 消息轮询 %s", len(a.ptRunner.Sites()), a.ptPollInterval))
	}
	if a.RemindersEnabled() {
		lines = append(lines,
			"提醒: V2EX",
			"轮询间隔: "+a.cfg.Reminders.Interval.String(),
			"首轮策略: "+a.cfg.Reminders.FirstRun,
			"标记已读: "+yesNo(a.cfg.Reminders.MarkRead),
			"类型过滤: "+filterLabel(a.cfg.Reminders.FilterTypes),
		)
	} else {
		lines = append(lines, "提醒: 未启用（未设置 V2EX_TOKEN）")
	}
	lines = append(lines,
		"V2EX 代理: "+proxyLabel(),
		"状态文件: "+a.cfg.Server.StatePath,
	)
	return lines
}

// checkinLabel summarises the check-in schedule for the startup card.
func (a *App) checkinLabel() string {
	names := make([]string, 0, len(a.sites))
	for _, s := range a.sites {
		names = append(names, s.Name())
	}
	if a.ptRunner != nil {
		for _, s := range a.ptRunner.Sites() {
			if s.Config().Signin.Mode != "off" {
				names = append(names, s.Name())
			}
		}
	}
	if len(names) == 0 {
		return "未启用"
	}
	loc := a.checkinLoc()
	hour, minute, err := a.cfg.CheckinClock()
	if err != nil {
		hour, minute = 0, 0
	}
	return fmt.Sprintf("%s（每天 %02d:%02d %s）",
		strings.Join(names, " + "), hour, minute, loc)
}

// checkinLoc returns the configured schedule zone, falling back to the process
// zone when it is unset (tests build a Config directly).
func (a *App) checkinLoc() *time.Location {
	if loc, err := a.cfg.Location(); err == nil && loc != nil {
		return loc
	}
	return time.Local
}

// Run polls until the context is cancelled.
func (a *App) Run(ctx context.Context) error {
	a.log.Info("forum-keeper starting",
		"version", a.version,
		"interval", a.cfg.Reminders.Interval.String(),
		"dry_run", a.dryRun,
		"first_run", a.cfg.Reminders.FirstRun,
		"mark_read", a.cfg.Reminders.MarkRead,
		"state", a.cfg.Server.StatePath,
		"filter_types", a.cfg.Reminders.FilterTypes,
	)
	if err := a.NotifyStartup(ctx, false); err != nil {
		// A failed startup message must not stop the service.
		a.log.Warn("startup message failed", "err", err)
	}
	pendingForums := false
	if _, _, pending := a.loadPendingCheckinCard(feishu.CheckinGroupForums); pending {
		pendingForums = true
	}
	if _, _, pending := a.loadPendingCheckinCard(feishu.CheckinGroupPT); pending {
		pendingForums = true
	}
	if pendingForums {
		// A card from the previous process was never delivered; retry it even
		// when CHECKIN_ON_START is disabled.
		a.retryAt = time.Now().Add(time.Minute)
	}
	if len(a.sites) > 0 || a.ptRunner != nil {
		a.maybeCheckinOnStart(ctx)
		go a.checkinLoop(ctx)
	}
	if a.ptMessageEnabled() {
		go a.ptMessageLoop(ctx)
	}

	if !a.RemindersEnabled() {
		a.log.Info("V2EX reminders disabled (V2EX_TOKEN is empty); running check-in only")
		<-ctx.Done()
		a.log.Info("shutting down")
		return ctx.Err()
	}

	a.pollAndLog(ctx)

	ticker := time.NewTicker(a.cfg.Reminders.Interval)
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
	return a.runCheckins(ctx, true)
}

// runCheckins is the scheduled path when force is false: a legacy site that
// already reported today is not asked again just because a PT retry woke the
// loop. The public RunCheckins (CLI / startup) keeps the historical
// "always run" behaviour.
func (a *App) runCheckins(ctx context.Context, force bool) error {
	var forumResults, ptResults []checkin.Result
	if len(a.sites) > 0 && (force || !a.legacyCheckinDoneToday()) {
		forumResults = checkin.Run(ctx, a.log, a.sites)
	}
	if a.ptRunner != nil {
		ptResults = a.ptRunner.RunDue(ctx)
		if force {
			day := time.Now().In(a.checkinLoc()).Format("2006-01-02")
			ptResults = mergeCheckinResults(ptResults, a.ptRunner.TerminalResults(day))
		}
	}

	a.retryAt = time.Time{}
	if retry := pendingRetry(forumResults); !retry.IsZero() {
		a.retryAt = retry
		a.log.Info("check-in deferred until the site's daily window opens",
			"at", retry.Format("2006-01-02 15:04:05"),
			"in", time.Until(retry).Round(time.Second).String())
	}
	if a.ptRunner != nil {
		if retry := a.ptRunner.NextRetry(); !retry.IsZero() {
			if a.retryAt.IsZero() || retry.Before(a.retryAt) {
				a.retryAt = retry
			}
			a.log.Info("PT check-in retry scheduled",
				"at", retry.Format("2006-01-02 15:04:05"),
				"in", time.Until(retry).Round(time.Second).String())
		}
	}

	// Do not mark the whole day done while a forum site still has an open
	// daily window; the PT card may be sent first, but the day stays open.
	allowMarkDone := pendingRetry(forumResults).IsZero()
	var firstErr error
	if err := a.deliverCheckinGroup(ctx, feishu.CheckinGroupForums, forumResults, allowMarkDone); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := a.deliverCheckinGroup(ctx, feishu.CheckinGroupPT, ptResults, allowMarkDone); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// deliverCheckinGroup sends one group's card (forums or PT) and keeps its
// pending card separate, so a failed PT card never blocks the forum card and
// vice versa.
func (a *App) deliverCheckinGroup(ctx context.Context, group string, results []checkin.Result, allowMarkDone bool) error {
	pendingEntries, pendingMarkDone, hasPending := a.loadPendingCheckinCard(group)
	if len(results) == 0 && !hasPending {
		a.log.Info("check-in group skipped", "group", group)
		return nil
	}

	ok := 0
	for _, r := range results {
		if r.OK() {
			ok++
		}
	}
	a.log.Info("check-in group finished", "group", group, "ok", ok, "total", len(results), "pending_card", hasPending)

	if a.dryRun {
		for _, r := range results {
			a.log.Info("dry-run: check-in result", "group", group, "site", r.Site, "status", string(r.Status), "detail", r.Detail)
		}
		if hasPending {
			a.log.Info("dry-run: would resend a pending check-in card", "group", group, "entries", len(pendingEntries))
		}
		return nil
	}

	legacyPending := false
	if retry := pendingRetry(results); !retry.IsZero() {
		legacyPending = true
	}
	fresh := make([]feishu.CheckinEntry, 0, len(results))
	for _, r := range results {
		if r.Pending() {
			continue
		}
		fresh = append(fresh, feishu.CheckinEntry{
			Site:   r.Site,
			OK:     r.OK(),
			Detail: r.Detail,
			Status: string(r.Status),
		})
	}
	entries := mergeCheckinEntries(pendingEntries, fresh)
	if len(entries) == 0 {
		return nil
	}
	markDone := allowMarkDone && !legacyPending
	if len(fresh) == 0 && hasPending {
		markDone = allowMarkDone && pendingMarkDone
	}
	err := a.sendCheckinCard(ctx, group, entries, markDone)
	if err != nil {
		// The card is persisted by sendCheckinCard; wake the loop soon instead
		// of waiting for tomorrow's schedule.
		a.retryAt = time.Now().Add(10 * time.Minute)
	}
	return err
}

func (a *App) loadPendingCheckinCard(group string) ([]feishu.CheckinEntry, bool, bool) {
	state, ok := a.state.(pendingCardState)
	if !ok {
		return nil, false, false
	}
	raw, markDone, found, err := state.PendingCheckinCard(group)
	if err != nil {
		a.log.Warn("could not read pending check-in card", "err", err)
		return nil, false, false
	}
	if !found {
		return nil, false, false
	}
	var entries []feishu.CheckinEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		a.log.Warn("could not decode pending check-in card", "err", err)
		return nil, false, false
	}
	return entries, markDone, true
}

// mergeCheckinEntries keeps the pending card's order and replaces a site with
// its fresher result when the retry ran it again.
func mergeCheckinResults(base, extra []checkin.Result) []checkin.Result {
	out := make([]checkin.Result, 0, len(base)+len(extra))
	index := make(map[string]int, len(base)+len(extra))
	add := func(r checkin.Result) {
		if i, ok := index[r.Site]; ok {
			out[i] = r
			return
		}
		index[r.Site] = len(out)
		out = append(out, r)
	}
	for _, r := range base {
		add(r)
	}
	for _, r := range extra {
		add(r)
	}
	return out
}

func mergeCheckinEntries(base, fresh []feishu.CheckinEntry) []feishu.CheckinEntry {
	out := make([]feishu.CheckinEntry, 0, len(base)+len(fresh))
	index := make(map[string]int, len(base)+len(fresh))
	add := func(e feishu.CheckinEntry) {
		if i, ok := index[e.Site]; ok {
			out[i] = e
			return
		}
		index[e.Site] = len(out)
		out = append(out, e)
	}
	for _, e := range base {
		add(e)
	}
	for _, e := range fresh {
		add(e)
	}
	return out
}

// pendingRetry returns the earliest moment a pending site can be retried, or
// the zero time when no site deferred.
func pendingRetry(results []checkin.Result) time.Time {
	var earliest time.Time
	for _, r := range results {
		if !r.Pending() {
			continue
		}
		at := r.RetryAfter
		if at.IsZero() {
			at = time.Now().Add(30 * time.Minute)
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return earliest
}

// sendCheckinCard delivers the check-in card and retries only the delivery.
//
// The sign-in itself has already happened and every site reports a repeat as
// "already signed in", but re-running the sign-in to resend the card would
// waste a request and report a less useful result. A rejected card (Feishu
// throttling, transient outage) must not silently cost the whole day's report.
//
// The day is only marked as done once the card is accepted, so a send that
// never succeeds still lets CHECKIN_ON_START catch up after a restart.
func (a *App) sendCheckinCard(ctx context.Context, group string, entries []feishu.CheckinEntry, markDone bool) error {
	var err error
	for attempt := 1; attempt <= checkinCardAttempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt-1) * checkinCardRetryBase
			a.log.Info("retrying check-in card", "attempt", attempt, "in", delay.Round(time.Second).String())
			select {
			case <-ctx.Done():
				a.savePendingCheckinCard(group, entries, markDone)
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		if err = a.bot.CheckinGroup(ctx, group, entries); err == nil {
			a.clearPendingCheckinCard(group)
			if markDone {
				if rerr := a.state.SetCheckin(time.Now()); rerr != nil {
					a.log.Warn("could not record check-in time", "err", rerr)
				}
			}
			return nil
		}
		a.log.Warn("check-in card rejected", "attempt", attempt, "err", err)
	}
	a.savePendingCheckinCard(group, entries, markDone)
	return fmt.Errorf("send %s check-in card: %w", group, err)
}

func (a *App) savePendingCheckinCard(group string, entries []feishu.CheckinEntry, markDone bool) {
	state, ok := a.state.(pendingCardState)
	if !ok {
		return
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		a.log.Warn("could not encode pending check-in card", "err", err)
		return
	}
	if err := state.SavePendingCheckinCard(group, raw, markDone); err != nil {
		a.log.Warn("could not save pending check-in card", "err", err)
	}
}

func (a *App) clearPendingCheckinCard(group string) {
	state, ok := a.state.(pendingCardState)
	if !ok {
		return
	}
	if err := state.ClearPendingCheckinCard(group); err != nil {
		a.log.Warn("could not clear pending check-in card", "err", err)
	}
}

// ptMessageEnabled reports whether any configured PT site has inbox reading
// turned on.
func (a *App) ptMessageEnabled() bool {
	if a.ptRunner == nil {
		return false
	}
	for _, site := range a.ptRunner.Sites() {
		if site.Config().Messages.Engine != "off" {
			return true
		}
	}
	return false
}

// ptMessageLoop polls tracker inboxes. The first poll runs shortly after
// startup; with PT_FIRST_RUN=skip it only records existing unread messages.
func (a *App) ptMessageLoop(ctx context.Context) {
	delay := 10 * time.Second
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := a.pollPTMessages(ctx); err != nil {
			a.log.Warn("PT message poll failed", "err", err)
		}
		delay = a.ptPollInterval
		if delay < time.Minute {
			delay = 2 * time.Hour
		}
		// Spread requests over a small window so several instances or a
		// restart do not hit every tracker at the same second.
		if spread := int64(delay / 10); spread > 0 {
			delay += time.Duration(time.Now().UnixNano() % spread)
		}
	}
}

// PollPTMessages runs one inbox poll. It is used by the --pt-messages flag.
func (a *App) PollPTMessages(ctx context.Context) error {
	if a.ptRunner == nil {
		a.log.Info("PT message poll skipped: PT is not configured")
		return nil
	}
	return a.pollPTMessages(ctx)
}

// pollPTMessages delivers pending messages first, then fetches new unread
// entries. A message is marked seen only after the card is accepted.
func (a *App) pollPTMessages(ctx context.Context) error {
	if a.ptRunner == nil {
		return nil
	}
	pending, err := a.ptRunner.Pending()
	if err != nil {
		return fmt.Errorf("load pending PT messages: %w", err)
	}
	if len(pending) > 0 {
		if a.dryRun {
			a.log.Info("dry-run: would resend pending PT messages", "count", len(pending))
		} else {
			if err := a.bot.SiteMessages(ctx, toSiteMessages(pending)); err != nil {
				return fmt.Errorf("send pending PT messages: %w", err)
			}
			a.ptRunner.MarkSent(pending)
			a.log.Info("pending PT messages delivered", "count", len(pending))
		}
	}

	msgs, err := a.ptRunner.FetchNew(ctx)
	if err != nil {
		return fmt.Errorf("fetch PT messages: %w", err)
	}
	if len(msgs) == 0 {
		return nil
	}
	if a.dryRun {
		for _, m := range msgs {
			a.log.Info("dry-run: would push PT message", "site", m.Site, "id", m.ID, "title", m.Title)
		}
		return nil
	}
	if err := a.bot.SiteMessages(ctx, toSiteMessages(msgs)); err != nil {
		return fmt.Errorf("send PT messages: %w", err)
	}
	a.ptRunner.MarkSent(msgs)
	a.log.Info("PT messages pushed", "count", len(msgs))
	return nil
}

func toSiteMessages(msgs []ptsite.Message) []feishu.SiteMessage {
	out := make([]feishu.SiteMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, feishu.SiteMessage{
			Site:  m.Site,
			Title: m.Title,
			Date:  m.Date,
			Body:  m.Body,
			URL:   m.URL,
		})
	}
	return out
}

// maybeCheckinOnStart catches up at startup when CHECKIN_ON_START is enabled:
// if today's sign-in has not happened yet (the container was down or restarting
// across the scheduled time), it runs immediately. The per-day guard matters
// because containers restart often and a restart loop must not spam the group.
func (a *App) maybeCheckinOnStart(ctx context.Context) {
	if !a.cfg.Checkin.OnStart {
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
func (a *App) legacyCheckinDoneToday() bool {
	if len(a.sites) == 0 {
		return true
	}
	last, err := a.state.LastCheckin()
	if err != nil {
		a.log.Warn("could not read last check-in time", "err", err)
		return false
	}
	return !last.IsZero() && checkin.SameLocalDay(last, time.Now(), a.checkinLoc())
}

func (a *App) checkedInToday() bool {
	if _, _, pending := a.loadPendingCheckinCard(feishu.CheckinGroupForums); pending {
		return false
	}
	if _, _, pending := a.loadPendingCheckinCard(feishu.CheckinGroupPT); pending {
		return false
	}
	if !a.legacyCheckinDoneToday() {
		return false
	}
	if a.ptRunner == nil {
		return true
	}
	return a.ptRunner.AllTerminal(time.Now().In(a.checkinLoc()).Format("2006-01-02"))
}

// checkinLoop sleeps until the configured wall-clock time and then signs in.
func (a *App) checkinLoop(ctx context.Context) {
	for {
		now := time.Now()
		retry := a.retryAt
		if a.ptRunner != nil {
			if next := a.ptRunner.NextRetry(); !next.IsZero() && (retry.IsZero() || next.Before(retry)) {
				retry = next
			}
		}
		var delay time.Duration
		if !retry.IsZero() && retry.After(now) {
			delay = retry.Sub(now)
			a.log.Info("check-in retry scheduled",
				"at", retry.Format("2006-01-02 15:04:05"),
				"in", delay.Round(time.Second).String())
		} else {
			a.retryAt = time.Time{}
			hour, minute, err := a.cfg.CheckinClock()
			if err != nil {
				return
			}
			delay = checkin.UntilNext(now, hour, minute, a.checkinLoc())
			next := now.Add(delay).In(a.checkinLoc())
			a.log.Info("next check-in scheduled",
				"at", next.Format("2006-01-02 15:04:05"),
				"in", delay.Round(time.Second).String())
		}

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
			a.retryAt = time.Time{}
			continue
		}

		runCtx, cancel := context.WithTimeout(ctx, checkinRunTimeout)
		err := a.runCheckins(runCtx, false)
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
	if count == 0 && a.cfg.Reminders.FirstRun == config.FirstRunSkip {
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

	if a.cfg.Reminders.MarkRead {
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

	for page := 1; page <= a.cfg.Reminders.MaxPages; page++ {
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
	if len(a.cfg.Reminders.FilterTypes) == 0 {
		return true
	}
	kind := feishu.KindOf(it.Text)
	for _, f := range a.cfg.Reminders.FilterTypes {
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
	if !a.cfg.Notify.AlertOnError || a.failures < alertThreshold {
		return
	}
	if time.Since(a.lastAlert) < alertMinInterval {
		return
	}
	a.lastAlert = time.Now()

	text := fmt.Sprintf("⚠️ V2EX 通知服务异常\n连续失败 %d 次\n最近错误: %s", a.failures, feishu.RedactWebhook(truncateErr(cause, 300)))

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
