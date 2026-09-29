package v2ex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotificationsParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer tok")
		}
		if r.URL.Path != "/notifications" || r.URL.Query().Get("p") != "2" {
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("X-Rate-Limit-Limit", "600")
		w.Header().Set("X-Rate-Limit-Remaining", "590")
		w.Header().Set("X-Rate-Limit-Reset", "1700000000")
		_, _ = w.Write([]byte(`{"result":[{"id":7,"text":"回复了你的主题","member":{"username":"alice"}}],"message":"20/123"}`))
	}))
	defer srv.Close()

	c, err := NewClient("tok", srv.URL, "", 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	items, err := c.Notifications(context.Background(), 2)
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	if len(items) != 1 || items[0].ID != 7 || items[0].Member.Username != "alice" {
		t.Fatalf("unexpected items: %+v", items)
	}
	rl := c.RateLimit()
	if rl.Limit != 600 || rl.Remaining != 590 || rl.Reset.Unix() != 1700000000 {
		t.Fatalf("unexpected rate limit: %+v", rl)
	}
}

func TestUnauthorizedIsNotRetried(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := NewClient("bad", srv.URL, "", 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Notifications(context.Background(), 1)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected 1 attempt, got %d", got)
	}
}

func TestServerErrorIsRetried(t *testing.T) {
	old := backoffBase
	backoffBase = time.Millisecond
	defer func() { backoffBase = old }()

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("upstream boom"))
			return
		}
		_, _ = w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()

	c, err := NewClient("tok", srv.URL, "", 5*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Notifications(context.Background(), 1); err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}

func TestNewClientRejectsBadProxy(t *testing.T) {
	if _, err := NewClient("tok", "https://www.v2ex.com/api/v2", "://bad", time.Second); err == nil {
		t.Fatal("expected error for invalid proxy URL")
	}
}
