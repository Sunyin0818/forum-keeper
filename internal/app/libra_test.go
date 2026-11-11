package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sunyin0818/forum-keeper/internal/config"
	"github.com/Sunyin0818/forum-keeper/internal/libra"
	"github.com/Sunyin0818/forum-keeper/internal/store"
)

type libraFeed struct {
	mu    sync.Mutex
	items []libra.Notification
	err   error
}

func (f *libraFeed) set(items ...libra.Notification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = items
}

func (f *libraFeed) add(n ...libra.Notification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = append(n, f.items...)
}

func (f *libraFeed) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"c":403,"code":"insufficient_scope","m":"缺少权限范围：read:me","d":null}`))
		return
	}
	body := map[string]any{"c": 0, "d": map[string]any{"list": f.items, "total": len(f.items)}}
	json.NewEncoder(w).Encode(body)
}

func newLibraTestApp(t *testing.T, feed *libraFeed, bot *fakeNotifier, firstRun string) (*App, *store.Store) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(feed.handler))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "libra_token.json")
	raw, _ := json.Marshal(libra.Token{
		AccessToken:  "at",
		RefreshToken: "rt",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	cfg := baseConfig()
	cfg.Libra.BaseURL = srv.URL
	cfg.Libra.ClientID = "cid"
	cfg.Libra.ClientSecret = "secret"
	cfg.Libra.RedirectURI = "http://127.0.0.1:8973/callback"
	cfg.Libra.TokenFile = tokenFile
	cfg.Reminders.FirstRun = firstRun

	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	a := NewWithDeps(cfg, nopLogger(), false, nil, st, bot)
	lc, err := libra.New(libra.Config{
		BaseURL:      srv.URL,
		ClientID:     "cid",
		ClientSecret: "secret",
		RedirectURI:  "http://127.0.0.1:8973/callback",
		TokenFile:    tokenFile,
		Scopes:       "profile read:me",
		Timeout:      5 * time.Second,
	})
	if err != nil {
		t.Fatalf("libra.New: %v", err)
	}
	a.SetLibraClient(lc)
	return a, st
}

func libraNotif(id, typ string) libra.Notification {
	return libra.Notification{
		ID:       id,
		Type:     typ,
		FromUser: &libra.User{Username: "alice"},
		Payload:  libra.NotificationPayload{PostTitle: "标题", PostShortID: "s1", PostNodeSlug: "work"},
	}
}

func TestPollLibraOnceFirstRunSkipThenPush(t *testing.T) {
	feed := &libraFeed{}
	feed.set(libraNotif("n1", "reply"), libraNotif("n2", "mention"))
	bot := &fakeNotifier{}
	a, st := newLibraTestApp(t, feed, bot, config.FirstRunSkip)

	n, err := a.PollLibraOnce(context.Background())
	if err != nil {
		t.Fatalf("PollLibraOnce: %v", err)
	}
	if n != 0 || len(bot.libraBatches) != 0 {
		t.Fatalf("first run pushed %d/%d, want 0", n, len(bot.libraBatches))
	}
	if done, err := st.LibraFirstRunDone(); err != nil || !done {
		t.Fatalf("first run flag = %v, %v; want true", done, err)
	}

	feed.add(libraNotif("n3", "reply"))
	n, err = a.PollLibraOnce(context.Background())
	if err != nil {
		t.Fatalf("PollLibraOnce: %v", err)
	}
	if n != 1 || len(bot.libraBatches) != 1 || len(bot.libraBatches[0]) != 1 || bot.libraBatches[0][0].ID != "n3" {
		t.Fatalf("second run = %d, batches %+v", n, bot.libraBatches)
	}

	if n, err = a.PollLibraOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("third run = %d, %v; want 0", n, err)
	}
}

func TestPollLibraOnceFirstRunPush(t *testing.T) {
	feed := &libraFeed{}
	feed.set(libraNotif("n1", "reply"))
	bot := &fakeNotifier{}
	a, _ := newLibraTestApp(t, feed, bot, config.FirstRunPush)

	n, err := a.PollLibraOnce(context.Background())
	if err != nil {
		t.Fatalf("PollLibraOnce: %v", err)
	}
	if n != 1 || len(bot.libraBatches) != 1 {
		t.Fatalf("pushed = %d, batches = %d", n, len(bot.libraBatches))
	}
}

func TestPollLibraOnceDoesNotMarkOnPushFailure(t *testing.T) {
	feed := &libraFeed{}
	feed.set(libraNotif("n1", "reply"))
	bot := &fakeNotifier{err: errors.New("webhook down")}
	a, _ := newLibraTestApp(t, feed, bot, config.FirstRunPush)

	if _, err := a.PollLibraOnce(context.Background()); err == nil {
		t.Fatal("expected push failure")
	}
	bot.err = nil
	n, err := a.PollLibraOnce(context.Background())
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n != 1 {
		t.Fatalf("retry pushed %d, want 1", n)
	}
}

func TestPollLibraStopsOnInsufficientScope(t *testing.T) {
	feed := &libraFeed{err: errors.New("scope revoked")}
	bot := &fakeNotifier{}
	a, _ := newLibraTestApp(t, feed, bot, config.FirstRunSkip)

	a.pollLibraAndLog(context.Background())
	if !a.libraStopped {
		t.Fatal("libra polling should stop on insufficient_scope")
	}
	if len(bot.alerts) != 1 || !strings.Contains(bot.alerts[0], "read:me") {
		t.Fatalf("alerts = %v", bot.alerts)
	}
}
