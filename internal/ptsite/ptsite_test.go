package ptsite

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Sunyin0818/forum-keeper/internal/checkin"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestSite(t *testing.T, srv *httptest.Server, mutate func(*SiteConfig)) *Site {
	t.Helper()
	cfg := SiteConfig{
		Name:   "TestPT",
		URL:    srv.URL,
		Cookie: "session=test",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	site, err := NewSite(cfg, testLogger(), false)
	if err != nil {
		t.Fatalf("NewSite: %v", err)
	}
	site.SetVerifyDelay(0)
	return site
}

func TestCheckInAlready(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body>今日已签到</body></html>`)
	}))
	defer srv.Close()

	res := newTestSite(t, srv, nil).CheckIn(context.Background())
	if res.Status != checkin.StatusAlready {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
}

func TestCheckInFormPostAndReward(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `<html><body>
				<form action="/attendance.php" method="post">
					<input type="hidden" name="token" value="abc">
					<input type="submit" name="submit" value="立即签到">
				</form></body></html>`)
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.Form.Get("token") != "abc" || r.Form.Get("submit") != "立即签到" {
				t.Errorf("form = %v", r.Form)
			}
			fmt.Fprint(w, `<html><body>签到成功，获得 20 魔力值</body></html>`)
		}
	}))
	defer srv.Close()

	res := newTestSite(t, srv, nil).CheckIn(context.Background())
	if !res.OK() || res.Status != checkin.StatusSuccess {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "20 魔力值") {
		t.Fatalf("reward missing: %s", res.Detail)
	}
}

func TestCheckInDiscoversHomepageEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attendance.php":
			http.NotFound(w, r)
		case "/index.php":
			fmt.Fprint(w, `<html><body><a href="/signin.php">每日签到</a></body></html>`)
		case "/signin.php":
			fmt.Fprint(w, `<html><body>今日已签到</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := newTestSite(t, srv, nil).CheckIn(context.Background())
	if res.Status != checkin.StatusAlready {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
}

func TestCheckInLoginExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body>请先登录 <a href="/login.php">登录</a></body></html>`)
	}))
	defer srv.Close()

	res := newTestSite(t, srv, nil).CheckIn(context.Background())
	if res.Status != checkin.StatusFailed || !strings.Contains(res.Detail, "Cookie") {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
}

func TestCheckInCloudflare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body>Just a moment... cf-chl-widget</body></html>`)
	}))
	defer srv.Close()

	res := newTestSite(t, srv, nil).CheckIn(context.Background())
	if res.Status != checkin.StatusFailed || !strings.Contains(res.Detail, "Cloudflare") {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
}

func TestCheckInTwoStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attendance.php":
			fmt.Fprint(w, `<html><body><input type="hidden" name="csrf" value="tok-1">请签到</body></html>`)
		case "/signin.php":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.Form.Get("csrf") != "tok-1" {
				t.Errorf("csrf = %q", r.Form.Get("csrf"))
			}
			fmt.Fprint(w, `<html><body>签到成功</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	site := newTestSite(t, srv, func(c *SiteConfig) {
		c.Signin.TwoStep = &TwoStepConfig{
			Path:   "/signin.php",
			Method: "POST",
			Tokens: map[string]string{"csrf": `name="csrf" value="([^"]+)"`},
		}
	})
	res := site.CheckIn(context.Background())
	if !res.OK() {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
}

func TestUnreadAndFetchMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/index.php":
			fmt.Fprint(w, `<html><body><a href="messages.php">消息 (1)</a></body></html>`)
		case r.URL.Path == "/messages.php" && r.URL.Query().Get("action") == "viewmailbox":
			fmt.Fprint(w, `<html><body><table>
				<tr><td><img class="unreadpm" alt="Unread"></td>
					<td><a href="messages.php?action=viewmessage&id=42">Hello</a></td>
					<td>2026-10-23 09:00:00</td></tr>
				<tr><td><img class="readpm" alt="Read"></td>
					<td><a href="messages.php?action=viewmessage&id=41">Old</a></td>
					<td>2026-10-22 09:00:00</td></tr>
			</table></body></html>`)
		case r.URL.Path == "/messages.php" && r.URL.Query().Get("action") == "viewmessage":
			fmt.Fprint(w, `<html><body><h1>Hello</h1>
				<table><tr><td class="colhead">From</td><td class="colhead">Date</td></tr>
				<tr><td>system</td><td>2026-10-23 09:00:00</td></tr>
				<tr><td colspan="2">Line one<br>Line two</td></tr></table>
			</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	site := newTestSite(t, srv, func(c *SiteConfig) {
		c.Signin.Mode = "off"
		c.Messages.Mode = "full"
	})
	msgs, err := site.Unread(context.Background())
	if err != nil {
		t.Fatalf("Unread: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != "42" || msgs[0].Title != "Hello" {
		t.Fatalf("messages = %+v", msgs)
	}
	full, err := site.FetchMessage(context.Background(), msgs[0])
	if err != nil {
		t.Fatalf("FetchMessage: %v", err)
	}
	if !strings.Contains(full.Body, "Line one") || !strings.Contains(full.Body, "Line two") {
		t.Fatalf("body = %q", full.Body)
	}
}

func TestRunnerFirstRunSkipAndDedupe(t *testing.T) {
	unread := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/index.php":
			fmt.Fprintf(w, `<html><body><a href="messages.php">消息 (%d)</a></body></html>`, unread)
		case r.URL.Path == "/messages.php" && r.URL.Query().Get("action") == "viewmailbox":
			fmt.Fprint(w, `<html><body><table>`)
			for i := 1; i <= unread; i++ {
				fmt.Fprintf(w, `<tr><td><img class="unreadpm" alt="Unread"></td><td><a href="messages.php?action=viewmessage&id=%d">Msg %d</a></td><td>2026-10-23 09:00:00</td></tr>`, i, i)
			}
			fmt.Fprint(w, `</table></body></html>`)
		case r.URL.Path == "/messages.php" && r.URL.Query().Get("action") == "viewmessage":
			fmt.Fprint(w, `<html><body><h1>Body</h1><table><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr><tr><td>hello</td></tr></table></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	site := newTestSite(t, srv, func(c *SiteConfig) {
		c.Signin.Mode = "off"
		c.Messages.Mode = "list"
	})
	state := NewMemoryState()
	runner := NewRunner([]*Site{site}, state, testLogger(), FirstRunSkip, 3, 1, false)

	unread = 1
	got, err := runner.FetchNew(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("first run = %+v, %v; want skip", got, err)
	}
	got, err = runner.FetchNew(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("second run = %+v, %v; want dedupe", got, err)
	}

	unread = 2
	got, err = runner.FetchNew(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("third run = %+v, %v; want id 2", got, err)
	}
	runner.MarkSent(got)
	got, err = runner.FetchNew(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("after send = %+v, %v; want none", got, err)
	}
}

func TestRunnerRetrySchedule(t *testing.T) {
	site, err := NewSite(SiteConfig{Name: "x", URL: "https://example.invalid", Cookie: "a=b"}, testLogger(), false)
	if err != nil {
		t.Fatal(err)
	}
	state := NewMemoryState()
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	state.SaveCheckin("2026-10-23", site.Host(), CheckinState{
		Day: "2026-10-23", Host: site.Host(), Status: "failed", Attempts: 1,
		NextAt: now.Add(10 * time.Minute),
	})
	runner := NewRunner([]*Site{site}, state, testLogger(), FirstRunSkip, 3, 1, false)
	runner.SetNow(func() time.Time { return now })
	if got := runner.NextRetry(); !got.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("NextRetry = %s", got)
	}
}

func TestRunnerRunDueRetriesThenTerminal(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			fmt.Fprint(w, `<html><body>没有签到结果</body></html>`)
			return
		}
		fmt.Fprint(w, `<html><body>今日已签到</body></html>`)
	}))
	defer srv.Close()

	site := newTestSite(t, srv, nil)
	state := NewMemoryState()
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	runner := NewRunner([]*Site{site}, state, testLogger(), FirstRunSkip, 3, 1, false)
	runner.SetNow(func() time.Time { return now })

	got := runner.RunDue(context.Background())
	if len(got) != 1 || got[0].Status != checkin.StatusUncertain {
		t.Fatalf("first run = %+v", got)
	}
	st, ok, _ := state.LoadCheckin("2026-10-23", site.Host())
	if !ok || st.Attempts != 1 || st.Terminal || st.NextAt.IsZero() {
		t.Fatalf("state after first run = %+v (ok=%v)", st, ok)
	}
	if runner.AllTerminal("2026-10-23") {
		t.Fatal("a retryable site must not count as terminal")
	}

	now = now.Add(11 * time.Minute)
	got = runner.RunDue(context.Background())
	if len(got) != 1 || !got[0].OK() {
		t.Fatalf("second run = %+v", got)
	}
	if !runner.AllTerminal("2026-10-23") {
		t.Fatal("successful site should be terminal")
	}
}

func TestCheckInAlreadyWithTurnstileWidget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body>今日已签到<div class="cf-turnstile"></div></body></html>`)
	}))
	defer srv.Close()

	res := newTestSite(t, srv, nil).CheckIn(context.Background())
	if res.Status != checkin.StatusAlready {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
}

func TestLoadResolvesCookieEnvOnce(t *testing.T) {
	t.Setenv("PT_TEST_COOKIE", "session=from-env")
	path := t.TempDir() + "/pt-sites.json"
	content := `{"sites":[{"name":"X","url":"https://example.invalid","cookie_env":"PT_TEST_COOKIE","signin":{"mode":"off"},"messages":{"mode":"list"}}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sites[0].Cookie != "session=from-env" {
		t.Fatalf("cookie = %q", cfg.Sites[0].Cookie)
	}
	if _, err := NewSite(cfg.Sites[0], testLogger(), false); err != nil {
		t.Fatalf("NewSite after Load: %v", err)
	}
}

func TestLoginOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><a href="logout.php">退出</a></body></html>`)
	}))
	defer srv.Close()

	site := newTestSite(t, srv, func(c *SiteConfig) { c.Signin.Mode = "login" })
	res := site.CheckIn(context.Background())
	if !res.OK() || res.Detail != "登录有效" {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
}

func TestPresetAutoMatchAndDefaults(t *testing.T) {
	site, err := NewSite(SiteConfig{
		URL:    "https://carpt.net",
		Cookie: "session=x",
		Signin: SigninConfig{Path: "/custom.php"},
	}, testLogger(), false)
	if err != nil {
		t.Fatalf("NewSite: %v", err)
	}
	cfg := site.Config()
	if cfg.Preset != "carpt" {
		t.Fatalf("preset = %q", cfg.Preset)
	}
	if cfg.Signin.Path != "/custom.php" {
		t.Fatalf("user path was overwritten: %q", cfg.Signin.Path)
	}
	if cfg.Signin.Success != "签到成功|簽到成功" || cfg.Signin.Already != "今日已签到|今天已签到|已签到" {
		t.Fatalf("preset patterns missing: %+v", cfg.Signin)
	}
}

func TestPresetNoneAndUnknown(t *testing.T) {
	site, err := NewSite(SiteConfig{
		URL:    "https://pt.btschool.club",
		Cookie: "session=x",
		Preset: "none",
	}, testLogger(), false)
	if err != nil {
		t.Fatalf("NewSite: %v", err)
	}
	if site.Config().Signin.Path != DefaultSigninPath {
		t.Fatalf("preset none should keep default path, got %q", site.Config().Signin.Path)
	}

	if _, err := NewSite(SiteConfig{
		URL:    "https://example.invalid",
		Cookie: "session=x",
		Preset: "nope",
	}, testLogger(), false); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected unknown preset error, got %v", err)
	}
}

func TestTwoStepCustomHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.php":
			fmt.Fprint(w, `<html><body>请签到</body></html>`)
		case "/signin.php":
			if got := r.Header.Get("X-Requested-With"); got != "XMLHttpRequest" {
				t.Errorf("X-Requested-With = %q", got)
			}
			fmt.Fprint(w, `<html><body>签到成功</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	site, err := NewSite(SiteConfig{
		URL:    srv.URL,
		Cookie: "session=x",
		Signin: SigninConfig{
			Path: "/index.php",
			TwoStep: &TwoStepConfig{
				Path:    "/signin.php",
				Method:  "GET",
				Headers: map[string]string{"X-Requested-With": "XMLHttpRequest"},
			},
		},
	}, testLogger(), false)
	if err != nil {
		t.Fatal(err)
	}
	res := site.CheckIn(context.Background())
	if !res.OK() {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
}

func TestPresetsLoad(t *testing.T) {
	names := PresetNames()
	if len(names) != 4 {
		t.Fatalf("presets = %v", names)
	}
	for _, name := range names {
		site, err := NewSite(SiteConfig{
			URL:    "https://" + name + ".invalid",
			Cookie: "session=x",
			Preset: name,
		}, testLogger(), false)
		if err != nil {
			t.Fatalf("preset %s: %v", name, err)
		}
		if site.Config().Preset != name {
			t.Fatalf("preset %s not applied: %q", name, site.Config().Preset)
		}
	}
}

func TestPresetBTSchool(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		site, err := NewSite(SiteConfig{URL: "https://pt.btschool.club", Cookie: "session=x"}, testLogger(), false)
		if err != nil {
			t.Fatal(err)
		}
		cfg := site.Config()
		if cfg.Preset != "btschool" || cfg.Signin.Path != "/index.php" {
			t.Fatalf("btschool preset = %+v", cfg.Signin)
		}
		if len(cfg.Signin.AbsentKeywords) != 1 || cfg.Signin.AbsentKeywords[0] != "每日签到" {
			t.Fatalf("absent keywords = %v", cfg.Signin.AbsentKeywords)
		}
		if cfg.Signin.TwoStep == nil || cfg.Signin.TwoStep.Path != "/index.php?action=addbonus" {
			t.Fatalf("two step = %+v", cfg.Signin.TwoStep)
		}
	})

	t.Run("signed in", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `<html><body>欢迎回来</body></html>`)
		}))
		defer srv.Close()
		site, err := NewSite(SiteConfig{URL: srv.URL, Cookie: "session=x", Preset: "btschool"}, testLogger(), false)
		if err != nil {
			t.Fatal(err)
		}
		res := site.CheckIn(context.Background())
		if res.Status != checkin.StatusAlready {
			t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
		}
	})

	t.Run("claims via addbonus", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/index.php":
				if r.URL.Query().Get("action") == "addbonus" {
					fmt.Fprint(w, `<html><body>签到成功，您获得 10 点魔力值</body></html>`)
					return
				}
				fmt.Fprint(w, `<html><body><form action="/index.php?action=addbonus" method="get">
					<input type="submit" name="submit" value="每日签到"></form></body></html>`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		site, err := NewSite(SiteConfig{URL: srv.URL, Cookie: "session=x", Preset: "btschool"}, testLogger(), false)
		if err != nil {
			t.Fatal(err)
		}
		res := site.CheckIn(context.Background())
		if !res.OK() || !strings.Contains(res.Detail, "10 魔力值") {
			t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
		}
	})
}

func TestPresetCarPTAndPTZone(t *testing.T) {
	cases := map[string]string{
		"https://carpt.net":  "carpt",
		"https://ptzone.xyz": "ptzone",
		"https://cspt.top":   "cspt",
	}
	for raw, want := range cases {
		site, err := NewSite(SiteConfig{URL: raw, Cookie: "session=x"}, testLogger(), false)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		cfg := site.Config()
		if cfg.Preset != want || cfg.Signin.Path != "/attendance.php" {
			t.Fatalf("%s preset = %+v", raw, cfg.Signin)
		}
	}
}

func TestCheckInMaintenance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>站点维护中</title></head><body>系统维护，请稍后访问</body></html>`)
	}))
	defer srv.Close()

	res := newTestSite(t, srv, nil).CheckIn(context.Background())
	if res.Status != checkin.StatusFailed || !strings.Contains(res.Detail, "维护") {
		t.Fatalf("status = %s, detail = %s", res.Status, res.Detail)
	}
}
