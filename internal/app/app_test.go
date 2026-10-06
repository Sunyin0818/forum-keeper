package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sunyin0818/v2ex-notifier/internal/checkin"
	"github.com/Sunyin0818/v2ex-notifier/internal/config"
	"github.com/Sunyin0818/v2ex-notifier/internal/feishu"
	"github.com/Sunyin0818/v2ex-notifier/internal/v2ex"
)

// --- test doubles ----------------------------------------------------------

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseConfig() *config.Config {
	return &config.Config{
		V2EXToken:              "test-token",
		V2EXBaseURL:            "https://example.invalid/api/v2",
		FirstRun:               config.FirstRunSkip,
		MaxPages:               3,
		PollInterval:           time.Minute,
		AlertOnError:           true,
		StartupMessage:         true,
		StartupMessageCooldown: 10 * time.Minute,
	}
}

type fakeFetcher struct {
	pages   map[int][]v2ex.Notification
	topics  map[int]*v2ex.Topic
	deleted []int
	calls   []int
	err     error
}

func (f *fakeFetcher) Notifications(_ context.Context, page int) ([]v2ex.Notification, error) {
	f.calls = append(f.calls, page)
	if f.err != nil {
		return nil, f.err
	}
	return f.pages[page], nil
}

func (f *fakeFetcher) Topic(_ context.Context, id int) (*v2ex.Topic, error) {
	if t, ok := f.topics[id]; ok {
		return t, nil
	}
	return nil, errors.New("topic not found")
}

func (f *fakeFetcher) DeleteNotification(_ context.Context, id int) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeFetcher) Member(context.Context) (*v2ex.Member, error) {
	return &v2ex.Member{ID: 1, Username: "me"}, nil
}

type fakeState struct {
	seen    map[int]bool
	markErr error
	startup time.Time
	checkin time.Time
}

func newFakeState() *fakeState { return &fakeState{seen: map[int]bool{}} }

func (s *fakeState) LastStartup() (time.Time, error) { return s.startup, nil }

func (s *fakeState) SetStartup(at time.Time) error {
	s.startup = at
	return nil
}

func (s *fakeState) LastCheckin() (time.Time, error) { return s.checkin, nil }

func (s *fakeState) SetCheckin(at time.Time) error {
	s.checkin = at
	return nil
}

func (s *fakeState) FilterNew(ids []int) ([]int, error) {
	var out []int
	for _, id := range ids {
		if !s.seen[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *fakeState) Mark(ids []int) error {
	if s.markErr != nil {
		return s.markErr
	}
	for _, id := range ids {
		s.seen[id] = true
	}
	return nil
}

func (s *fakeState) Count() (int, error) { return len(s.seen), nil }

type fakeNotifier struct {
	batches  [][]int
	alerts   []string
	startups [][]string
	checkins [][]feishu.CheckinEntry
	err      error
}

func (n *fakeNotifier) Notify(_ context.Context, items []v2ex.Notification) error {
	if n.err != nil {
		return n.err
	}
	ids := make([]int, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	n.batches = append(n.batches, ids)
	return nil
}

func (n *fakeNotifier) Alert(_ context.Context, text string) error {
	n.alerts = append(n.alerts, text)
	return nil
}

func (n *fakeNotifier) Startup(_ context.Context, lines []string) error {
	if n.err != nil {
		return n.err
	}
	n.startups = append(n.startups, lines)
	return nil
}

func (n *fakeNotifier) Checkin(_ context.Context, entries []feishu.CheckinEntry) error {
	if n.err != nil {
		return n.err
	}
	n.checkins = append(n.checkins, entries)
	return nil
}

// --- helpers ---------------------------------------------------------------

func notifs(ids ...int) []v2ex.Notification {
	out := make([]v2ex.Notification, 0, len(ids))
	for _, id := range ids {
		out = append(out, v2ex.Notification{
			ID:     id,
			Text:   "回复了你的主题",
			Member: v2ex.Member{Username: "alice"},
		})
	}
	return out
}

func fullPage(start, n int) []v2ex.Notification {
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, start+i)
	}
	return notifs(ids...)
}

// --- tests -----------------------------------------------------------------

func TestOpenStateDryRunIsSideEffectFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.db")

	s, err := openState(path, true)
	if err != nil {
		t.Fatalf("openState(dry-run): %v", err)
	}
	if err := s.(interface{ Close() error }).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("dry-run created state directory: %v", err)
	}

	s2, err := openState(path, false)
	if err != nil {
		t.Fatalf("openState(normal): %v", err)
	}
	if err := s2.(interface{ Close() error }).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("normal run should create the database: %v", err)
	}
}

func TestFirstRunRecordsWithoutPushing(t *testing.T) {
	f := &fakeFetcher{pages: map[int][]v2ex.Notification{1: notifs(1, 2)}}
	st := newFakeState()
	n := &fakeNotifier{}
	a := NewWithDeps(baseConfig(), nopLogger(), false, f, st, n)

	got, err := a.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got != 0 {
		t.Fatalf("pushes = %d, want 0", got)
	}
	if len(n.batches) != 0 {
		t.Fatalf("unexpected pushes: %v", n.batches)
	}
	if len(st.seen) != 2 {
		t.Fatalf("expected 2 recorded ids, got %v", st.seen)
	}
}

func TestFirstRunPushMode(t *testing.T) {
	cfg := baseConfig()
	cfg.FirstRun = config.FirstRunPush

	f := &fakeFetcher{pages: map[int][]v2ex.Notification{1: notifs(1, 2)}}
	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), false, f, newFakeState(), n)

	got, err := a.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got != 2 || len(n.batches) != 1 || n.batches[0][0] != 1 || n.batches[0][1] != 2 {
		t.Fatalf("unexpected result: pushes=%d batches=%v", got, n.batches)
	}
}

func TestPushesOnlyNewNotifications(t *testing.T) {
	f := &fakeFetcher{pages: map[int][]v2ex.Notification{1: notifs(2, 1)}}
	st := newFakeState()
	st.seen[1] = true
	n := &fakeNotifier{}
	a := NewWithDeps(baseConfig(), nopLogger(), false, f, st, n)

	got, err := a.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got != 1 || len(n.batches) != 1 || n.batches[0][0] != 2 {
		t.Fatalf("unexpected result: pushes=%d batches=%v", got, n.batches)
	}
}

func TestPushFailureDoesNotMarkSeen(t *testing.T) {
	f := &fakeFetcher{pages: map[int][]v2ex.Notification{1: notifs(1)}}
	st := newFakeState()
	st.seen[999] = true // a previous run already happened
	n := &fakeNotifier{err: errors.New("webhook down")}
	a := NewWithDeps(baseConfig(), nopLogger(), false, f, st, n)

	if _, err := a.PollOnce(context.Background()); err == nil {
		t.Fatal("expected push error")
	}
	if st.seen[1] {
		t.Fatal("state must not record the notification after a failed push")
	}

	// Recover: the notification must be retried, not lost.
	n.err = nil
	got, err := a.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected retry to push 1, got %d", got)
	}
}

func TestFilterTypesPushesMatchingButMarksAll(t *testing.T) {
	cfg := baseConfig()
	cfg.FilterTypes = []string{"mention"}

	f := &fakeFetcher{pages: map[int][]v2ex.Notification{1: {
		{ID: 1, Text: "回复了你的主题"},
		{ID: 2, Text: "在回复中提到了你"},
		{ID: 3, Text: "感谢了你的主题"},
	}}}
	st := newFakeState()
	st.seen[999] = true // a previous run already happened
	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), false, f, st, n)

	got, err := a.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got != 1 || len(n.batches) != 1 || n.batches[0][0] != 2 {
		t.Fatalf("unexpected result: pushes=%d batches=%v", got, n.batches)
	}
	for _, id := range []int{1, 2, 3} {
		if !st.seen[id] {
			t.Fatalf("id %d should be marked seen, got %v", id, st.seen)
		}
	}
}

func TestMarkReadDeletesPushedNotifications(t *testing.T) {
	cfg := baseConfig()
	cfg.MarkRead = true

	f := &fakeFetcher{pages: map[int][]v2ex.Notification{1: notifs(1, 2)}}
	st := newFakeState()
	st.seen[999] = true // a previous run already happened
	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), false, f, st, n)

	if _, err := a.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(f.deleted) != 2 {
		t.Fatalf("expected 2 deletes, got %v", f.deleted)
	}
}

func TestPaginationStopsWhenPageFullyKnown(t *testing.T) {
	f := &fakeFetcher{pages: map[int][]v2ex.Notification{
		1: fullPage(100, notificationsPageSize),
		2: fullPage(200, notificationsPageSize),
	}}
	st := newFakeState()
	for _, it := range f.pages[1] {
		st.seen[it.ID] = true
	}
	n := &fakeNotifier{}
	a := NewWithDeps(baseConfig(), nopLogger(), false, f, st, n)

	got, err := a.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got != 0 {
		t.Fatalf("pushes = %d, want 0", got)
	}
	if len(f.calls) != 1 || f.calls[0] != 1 {
		t.Fatalf("expected only page 1 to be fetched, got %v", f.calls)
	}
}

func TestPaginationRespectsMaxPages(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxPages = 2
	cfg.FirstRun = config.FirstRunPush

	f := &fakeFetcher{pages: map[int][]v2ex.Notification{
		1: fullPage(100, notificationsPageSize),
		2: fullPage(200, notificationsPageSize),
		3: fullPage(300, notificationsPageSize),
	}}
	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), false, f, newFakeState(), n)

	got, err := a.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got != 2*notificationsPageSize {
		t.Fatalf("pushes = %d, want %d", got, 2*notificationsPageSize)
	}
	if len(f.calls) != 2 {
		t.Fatalf("expected 2 page fetches, got %v", f.calls)
	}
}

func TestDryRunNeitherPushesNorMarks(t *testing.T) {
	cfg := baseConfig()
	cfg.FirstRun = config.FirstRunPush

	f := &fakeFetcher{pages: map[int][]v2ex.Notification{1: notifs(1, 2)}}
	st := newFakeState()
	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), true, f, st, n)

	got, err := a.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got != 2 {
		t.Fatalf("dry-run should report 2, got %d", got)
	}
	if len(n.batches) != 0 || len(st.seen) != 0 {
		t.Fatalf("dry-run mutated state: batches=%v seen=%v", n.batches, st.seen)
	}
}

func TestAlertsAfterRepeatedFailures(t *testing.T) {
	f := &fakeFetcher{err: errors.New("proxy unreachable")}
	n := &fakeNotifier{}
	a := NewWithDeps(baseConfig(), nopLogger(), false, f, newFakeState(), n)
	ctx := context.Background()

	for i := 0; i < alertThreshold; i++ {
		a.pollAndLog(ctx)
	}
	if len(n.alerts) != 1 {
		t.Fatalf("expected exactly 1 alert, got %d (%v)", len(n.alerts), n.alerts)
	}

	// Further failures within the throttle window must not spam.
	a.pollAndLog(ctx)
	if len(n.alerts) != 1 {
		t.Fatalf("expected alert throttling, got %d alerts", len(n.alerts))
	}
}

// --- startup message -------------------------------------------------------

func TestNotifyStartupSendsCard(t *testing.T) {
	st := newFakeState()
	n := &fakeNotifier{}
	a := NewWithDeps(baseConfig(), nopLogger(), false, &fakeFetcher{}, st, n)
	a.SetVersion("1.2.3")

	if err := a.NotifyStartup(context.Background(), false); err != nil {
		t.Fatalf("NotifyStartup: %v", err)
	}
	if len(n.startups) != 1 {
		t.Fatalf("expected 1 startup message, got %d", len(n.startups))
	}
	lines := strings.Join(n.startups[0], "\n")
	for _, want := range []string{"1.2.3", "轮询间隔", "首轮策略", "状态文件"} {
		if !strings.Contains(lines, want) {
			t.Errorf("startup message missing %q:\n%s", want, lines)
		}
	}
	if st.startup.IsZero() {
		t.Fatal("startup time was not recorded")
	}
}

func TestNotifyStartupRespectsCooldown(t *testing.T) {
	st := newFakeState()
	st.startup = time.Now().Add(-time.Minute) // a restart loop, not a real start
	n := &fakeNotifier{}
	a := NewWithDeps(baseConfig(), nopLogger(), false, &fakeFetcher{}, st, n)

	if err := a.NotifyStartup(context.Background(), false); err != nil {
		t.Fatalf("NotifyStartup: %v", err)
	}
	if len(n.startups) != 0 {
		t.Fatalf("cooldown must suppress the message, got %d", len(n.startups))
	}
}

func TestNotifyStartupForceBypassesCooldownAndToggle(t *testing.T) {
	cfg := baseConfig()
	cfg.StartupMessage = false

	st := newFakeState()
	st.startup = time.Now()
	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), false, &fakeFetcher{}, st, n)

	if err := a.NotifyStartup(context.Background(), true); err != nil {
		t.Fatalf("NotifyStartup(force): %v", err)
	}
	if len(n.startups) != 1 {
		t.Fatalf("force must send regardless, got %d", len(n.startups))
	}
}

func TestNotifyStartupDisabled(t *testing.T) {
	cfg := baseConfig()
	cfg.StartupMessage = false

	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), false, &fakeFetcher{}, newFakeState(), n)

	if err := a.NotifyStartup(context.Background(), false); err != nil {
		t.Fatalf("NotifyStartup: %v", err)
	}
	if len(n.startups) != 0 {
		t.Fatalf("NOTIFY_STARTUP=false must not send, got %d", len(n.startups))
	}
}

func TestNotifyStartupDryRunSendsNothing(t *testing.T) {
	st := newFakeState()
	n := &fakeNotifier{}
	a := NewWithDeps(baseConfig(), nopLogger(), true, &fakeFetcher{}, st, n)

	if err := a.NotifyStartup(context.Background(), false); err != nil {
		t.Fatalf("NotifyStartup: %v", err)
	}
	if len(n.startups) != 0 {
		t.Fatalf("dry-run must not send, got %d", len(n.startups))
	}
	if !st.startup.IsZero() {
		t.Fatal("dry-run must not record a startup time")
	}
}

func TestStartupLinesDoNotLeakProxyCredentials(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://alice:s3cret@proxy.internal:7897")

	a := NewWithDeps(baseConfig(), nopLogger(), false, &fakeFetcher{}, newFakeState(), &fakeNotifier{})
	lines := strings.Join(a.startupLines(), "\n")

	for _, secret := range []string{"alice", "s3cret", "proxy.internal"} {
		if strings.Contains(lines, secret) {
			t.Fatalf("startup message leaked %q:\n%s", secret, lines)
		}
	}
	if !strings.Contains(lines, "V2EX 代理: 已设置") {
		t.Fatalf("proxy should be reported as configured:\n%s", lines)
	}
	// Report which variable supplied it - the name is useful, the value is not.
	if !strings.Contains(lines, "HTTPS_PROXY") {
		t.Fatalf("proxy label should name the variable:\n%s", lines)
	}
}

// --- daily check-in --------------------------------------------------------

type fakeSite struct {
	name   string
	result checkin.Result
	calls  int
}

func (s *fakeSite) Name() string { return s.name }

func (s *fakeSite) SignIn(context.Context) checkin.Result {
	s.calls++
	res := s.result
	res.Site = s.name
	res.At = time.Now()
	return res
}

func TestRunCheckinsReportsEverySite(t *testing.T) {
	st := newFakeState()
	n := &fakeNotifier{}
	a := NewWithDeps(baseConfig(), nopLogger(), false, &fakeFetcher{}, st, n)

	v2 := &fakeSite{name: "V2EX", result: checkin.Result{Status: checkin.StatusSuccess, Detail: "获得 18 铜币"}}
	libra := &fakeSite{name: "2libra", result: checkin.Result{Status: checkin.StatusFailed, Detail: "Cookie 已失效"}}
	a.SetCheckinSites([]checkin.Site{v2, libra})

	if err := a.RunCheckins(context.Background()); err != nil {
		t.Fatalf("RunCheckins: %v", err)
	}
	if v2.calls != 1 || libra.calls != 1 {
		t.Fatalf("each site must run once: v2=%d libra=%d", v2.calls, libra.calls)
	}
	if len(n.checkins) != 1 {
		t.Fatalf("expected one check-in card, got %d", len(n.checkins))
	}
	entries := n.checkins[0]
	if len(entries) != 2 || entries[0].Site != "V2EX" || entries[1].Site != "2libra" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if !entries[0].OK || entries[1].OK {
		t.Fatalf("OK flags wrong: %+v", entries)
	}
	if st.checkin.IsZero() {
		t.Fatal("check-in time should be recorded after a successful send")
	}
}

func TestRunCheckinsRecordsNothingWhenCardFails(t *testing.T) {
	st := newFakeState()
	n := &fakeNotifier{err: errors.New("webhook down")}
	a := NewWithDeps(baseConfig(), nopLogger(), false, &fakeFetcher{}, st, n)
	a.SetCheckinSites([]checkin.Site{&fakeSite{name: "V2EX", result: checkin.Result{Status: checkin.StatusSuccess}}})

	if err := a.RunCheckins(context.Background()); err == nil {
		t.Fatal("expected a send error")
	}
	if !st.checkin.IsZero() {
		t.Fatal("a failed card must not be recorded as done")
	}
}

func TestRunCheckinsNoSitesIsNoop(t *testing.T) {
	st := newFakeState()
	n := &fakeNotifier{}
	a := NewWithDeps(baseConfig(), nopLogger(), false, &fakeFetcher{}, st, n)

	if err := a.RunCheckins(context.Background()); err != nil {
		t.Fatalf("RunCheckins: %v", err)
	}
	if len(n.checkins) != 0 || !st.checkin.IsZero() {
		t.Fatalf("no sites should do nothing: checkins=%d recorded=%v", len(n.checkins), st.checkin)
	}
}

func TestCheckinOnStartIsGuardedPerDay(t *testing.T) {
	cfg := baseConfig()
	cfg.CheckinOnStart = true
	cfg.CheckinLocation = time.UTC

	st := newFakeState()
	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), false, &fakeFetcher{}, st, n)
	s := &fakeSite{name: "V2EX", result: checkin.Result{Status: checkin.StatusSuccess}}
	a.SetCheckinSites([]checkin.Site{s})

	a.maybeCheckinOnStart(context.Background())
	if s.calls != 1 {
		t.Fatalf("first start should run the check-in, calls=%d", s.calls)
	}

	// A restart later the same day must not sign in (or notify) again.
	a.maybeCheckinOnStart(context.Background())
	if s.calls != 1 {
		t.Fatalf("same-day restart re-ran the check-in, calls=%d", s.calls)
	}
	if len(n.checkins) != 1 {
		t.Fatalf("expected exactly one card, got %d", len(n.checkins))
	}
}

func TestCheckinOnStartSkippedYesterdayRuns(t *testing.T) {
	cfg := baseConfig()
	cfg.CheckinOnStart = true
	cfg.CheckinLocation = time.UTC

	st := newFakeState()
	st.checkin = time.Now().UTC().Add(-25 * time.Hour)
	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), false, &fakeFetcher{}, st, n)
	s := &fakeSite{name: "V2EX", result: checkin.Result{Status: checkin.StatusAlready}}
	a.SetCheckinSites([]checkin.Site{s})

	a.maybeCheckinOnStart(context.Background())
	if s.calls != 1 {
		t.Fatalf("yesterday's run must not block today, calls=%d", s.calls)
	}
}

func TestCheckinOnStartDisabled(t *testing.T) {
	cfg := baseConfig()
	cfg.CheckinOnStart = false

	n := &fakeNotifier{}
	a := NewWithDeps(cfg, nopLogger(), false, &fakeFetcher{}, newFakeState(), n)
	s := &fakeSite{name: "V2EX", result: checkin.Result{Status: checkin.StatusSuccess}}
	a.SetCheckinSites([]checkin.Site{s})

	a.maybeCheckinOnStart(context.Background())
	if s.calls != 0 {
		t.Fatalf("CHECKIN_ON_START=false must not run, calls=%d", s.calls)
	}
}

func TestRemindersDisabledStartupLines(t *testing.T) {
	cfg := baseConfig()
	cfg.V2EXToken = ""

	a := NewWithDeps(cfg, nopLogger(), false, &fakeFetcher{}, newFakeState(), &fakeNotifier{})
	if a.RemindersEnabled() {
		t.Fatal("RemindersEnabled should be false without V2EX_TOKEN")
	}

	lines := strings.Join(a.startupLines(), "\n")
	if !strings.Contains(lines, "提醒: 未启用") {
		t.Fatalf("startup lines should say reminders are off:\n%s", lines)
	}
	if strings.Contains(lines, "轮询间隔") {
		t.Fatalf("reminder tuning should be hidden when disabled:\n%s", lines)
	}
}

func TestBuildCheckinSitesHonoursCredentials(t *testing.T) {
	none := buildCheckinSites(&config.Config{})
	if len(none) != 0 {
		t.Fatalf("no credentials should yield no sites, got %d", len(none))
	}

	v2only := buildCheckinSites(&config.Config{V2EXCookie: "A2=x"})
	if len(v2only) != 1 {
		t.Fatalf("expected 1 site, got %d", len(v2only))
	}

	both := buildCheckinSites(&config.Config{V2EXCookie: "A2=x", LibraCookie: "access_token=y"})
	if len(both) != 2 {
		t.Fatalf("expected 2 sites, got %d", len(both))
	}
}
