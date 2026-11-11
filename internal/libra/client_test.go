package libra

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	dir := t.TempDir()
	c, err := New(Config{
		BaseURL:      base,
		ClientID:     "cid",
		ClientSecret: "secret",
		RedirectURI:  "http://127.0.0.1:8973/callback",
		TokenFile:    filepath.Join(dir, "libra_token.json"),
		Scopes:       "profile read:me",
		Timeout:      5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestBeginAuthBuildsPKCEURL(t *testing.T) {
	c := newTestClient(t, "https://2libra.com")
	req, err := c.BeginAuth()
	if err != nil {
		t.Fatalf("BeginAuth: %v", err)
	}
	for _, want := range []string{
		"client_id=cid",
		"response_type=code",
		"code_challenge_method=S256",
		"scope=profile+read%3Ame",
		"redirect_uri=http%3A%2F%2F127.0.0.1%3A8973%2Fcallback",
		"state=" + req.State,
	} {
		if !strings.Contains(req.URL, want) {
			t.Errorf("authorize URL missing %q:\n%s", want, req.URL)
		}
	}
	if len(req.Verifier) < 43 || len(req.Verifier) > 128 {
		t.Errorf("verifier length = %d, want 43..128", len(req.Verifier))
	}
}

func TestExchangePersistsToken(t *testing.T) {
	var form map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/token" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		form = map[string]string{
			"grant":    r.Form.Get("grant_type"),
			"code":     r.Form.Get("code"),
			"verifier": r.Form.Get("code_verifier"),
			"secret":   r.Form.Get("client_secret"),
			"redirect": r.Form.Get("redirect_uri"),
		}
		w.Write([]byte(`{"c":0,"m":"ok","d":{"access_token":"at1","refresh_token":"rt1","token_type":"Bearer","expires_in":3600,"scope":"profile read:me"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Exchange(context.Background(), "code1", "verifier1"); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if form["grant"] != "authorization_code" || form["code"] != "code1" || form["verifier"] != "verifier1" {
		t.Fatalf("unexpected form: %+v", form)
	}
	if form["secret"] != "secret" || form["redirect"] != "http://127.0.0.1:8973/callback" {
		t.Fatalf("missing client credentials: %+v", form)
	}
	if !c.Authorized() {
		t.Fatal("client should be authorized")
	}

	raw, err := os.ReadFile(c.cfg.TokenFile)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	var tok Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		t.Fatalf("decode token file: %v", err)
	}
	if tok.AccessToken != "at1" || tok.RefreshToken != "rt1" {
		t.Fatalf("stored token = %+v", tok)
	}
	if info, err := os.Stat(c.cfg.TokenFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, %v", info.Mode(), err)
	}
}

func TestGetRefreshesRejectedTokenOnce(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("refresh_token") != "rt1" {
				t.Errorf("refresh_token = %q", r.Form.Get("refresh_token"))
			}
			w.Write([]byte(`{"c":0,"d":{"access_token":"at2","refresh_token":"rt2","expires_in":3600,"scope":"profile read:me"}}`))
		case "/api/v1/me/summary":
			calls++
			if r.Header.Get("Authorization") != "Bearer at2" {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"c":401,"code":"unauthorized","m":"令牌无效","d":null}`))
				return
			}
			w.Write([]byte(`{"c":0,"d":{"id":"u1","username":"alice"},"m":"ok"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.token = Token{AccessToken: "at1", RefreshToken: "rt1", ExpiresAt: time.Now().Add(time.Hour)}

	id, err := c.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if id.Username != "alice" || calls != 2 {
		t.Fatalf("identity = %+v, calls = %d", id, calls)
	}
	if c.token.RefreshToken != "rt2" {
		t.Fatalf("rotated refresh token not kept: %+v", c.token)
	}
}

func TestInsufficientScopeIsTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"c":403,"code":"insufficient_scope","m":"缺少权限范围：read:me","d":null}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.token = Token{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)}
	_, err := c.Summary(context.Background())
	if !errors.Is(err, ErrInsufficientScope) {
		t.Fatalf("err = %v, want ErrInsufficientScope", err)
	}
}

func TestNotificationToleratesOddPayload(t *testing.T) {
	var n Notification
	if err := json.Unmarshal([]byte(`{"id":"x","type":"system","payload":"oops"}`), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n.ID != "x" || n.Type != "system" {
		t.Fatalf("notification = %+v", n)
	}
	if n.Payload.PostTitle != "" {
		t.Fatalf("payload should be empty, got %+v", n.Payload)
	}
}

func TestNotificationsEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/notifications" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("limit") != "50" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		w.Write([]byte(`{"c":0,"d":{"list":[
			{"id":"n1","type":"reply","is_read":false,"created_at":"2026-11-11T10:00:00.000Z",
			 "from_user":{"id":"u1","username":"bob"},
			 "payload":{"postTitle":"标题","postShortId":"a1b2c3d4","postNodeSlug":"work","commentId":"c1","commentText":"正文","commentLevel":1}}
		],"total":1,"page":1,"limit":50}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.token = Token{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)}
	page, err := c.Notifications(context.Background(), 1, 50)
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	if len(page.List) != 1 || page.List[0].Payload.PostTitle != "标题" {
		t.Fatalf("page = %+v", page)
	}
	n := page.List[0]
	if n.Actor() != "bob" {
		t.Errorf("actor = %q", n.Actor())
	}
	if got, want := n.Link("https://2libra.com"), "https://2libra.com/post/work/a1b2c3d4?commentId=c1"; got != want {
		t.Errorf("link = %q, want %q", got, want)
	}
	if n.Created().IsZero() {
		t.Error("created_at was not parsed")
	}
}
