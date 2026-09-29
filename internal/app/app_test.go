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

	"github.com/Sunyin0818/v2ex-notifier/internal/config"
	"github.com/Sunyin0818/v2ex-notifier/internal/v2ex"
)

// --- test doubles ----------------------------------------------------------

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseConfig() *config.Config {
	return &config.Config{
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
}

func newFakeState() *fakeState { return &fakeState{seen: map[int]bool{}} }

func (s *fakeState) LastStartup() (time.Time, error) { return s.startup, nil }

func (s *fakeState) SetStartup(at time.Time) error {
	s.startup = at
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
		t.Fatalf("STARTUP_MESSAGE=false must not send, got %d", len(n.startups))
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
	cfg := baseConfig()
	cfg.V2EXProxy = "http://alice:s3cret@proxy.internal:7897"

	a := NewWithDeps(cfg, nopLogger(), false, &fakeFetcher{}, newFakeState(), &fakeNotifier{})
	lines := strings.Join(a.startupLines(), "\n")

	for _, secret := range []string{"alice", "s3cret", "proxy.internal"} {
		if strings.Contains(lines, secret) {
			t.Fatalf("startup message leaked %q:\n%s", secret, lines)
		}
	}
	if !strings.Contains(lines, "V2EX 代理: 已设置") {
		t.Fatalf("proxy should be reported as configured:\n%s", lines)
	}
}
