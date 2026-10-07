package checkin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newV2EXTestSite(t *testing.T, handler http.HandlerFunc) (*V2EXSite, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewV2EXSite("A2=secret; A2O=twofactor", srv.URL, 5*time.Second), srv
}

func TestV2EXSiteAlreadySigned(t *testing.T) {
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>每日登录奖励已领取，获得 18 铜币</html>`))
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusAlready {
		t.Fatalf("status = %q, want already (detail=%q)", res.Status, res.Detail)
	}
	if !res.OK() || res.Site != "V2EX" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(res.Detail, "18") {
		t.Fatalf("reward missing from detail: %q", res.Detail)
	}
}

func TestV2EXSiteSignsIn(t *testing.T) {
	claimed := false
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mission/daily" && !claimed:
			w.Write([]byte(`<a href="/mission/daily/redeem?once=98765">领取</a>`))
		case r.URL.Path == "/mission/daily/redeem":
			if got := r.URL.Query().Get("once"); got != "98765" {
				t.Errorf("once = %q, want 98765", got)
			}
			claimed = true
			w.Write([]byte(`<html>已成功领取每日登录奖励 50 铜币</html>`))
		case r.URL.Path == "/mission/daily":
			if !strings.Contains(r.Header.Get("Referer"), "/mission/daily") {
				t.Errorf("Referer missing: %q", r.Header.Get("Referer"))
			}
			w.Write([]byte(`<html>每日登录奖励已领取</html>`))
		default:
			http.NotFound(w, r)
		}
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusSuccess {
		t.Fatalf("status = %q, want success (detail=%q)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "50") {
		t.Fatalf("reward missing from detail: %q", res.Detail)
	}
}

func TestV2EXSiteExpiredCookie(t *testing.T) {
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mission/daily" {
			http.Redirect(w, r, "/signin?next=%2Fmission%2Fdaily", http.StatusFound)
			return
		}
		w.Write([]byte(`<html>You need to sign in</html>`))
	})

	res := site.SignIn(context.Background())
	if res.OK() {
		t.Fatalf("expired cookie must fail, got %+v", res)
	}
	if !strings.Contains(res.Detail, "Cookie") {
		t.Fatalf("detail should mention the cookie: %q", res.Detail)
	}
}

func TestV2EXSiteMissingToken(t *testing.T) {
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>签到页没有 once 链接</html>`))
	})

	res := site.SignIn(context.Background())
	if res.OK() {
		t.Fatalf("missing token must fail, got %+v", res)
	}
	if !strings.Contains(res.Detail, "token") {
		t.Fatalf("unexpected detail: %q", res.Detail)
	}
}

func TestV2EXSiteStaleToken(t *testing.T) {
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mission/daily" {
			w.Write([]byte(`<a href="/mission/daily/redeem?once=1">领取</a>`))
			return
		}
		w.Write([]byte(`<html>请重新点击一次以领取每日登录奖励</html>`))
	})

	res := site.SignIn(context.Background())
	if res.OK() {
		t.Fatalf("stale token must fail, got %+v", res)
	}
	if !strings.Contains(res.Detail, "重新点击") {
		t.Fatalf("unexpected detail: %q", res.Detail)
	}
}

func TestV2EXSiteRewardFallsBackToLedger(t *testing.T) {
	// The real claim page's flash may omit the amount; the /balance ledger always
	// records it, so it must win over an empty flash.
	claimed := false
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mission/daily" && !claimed:
			w.Write([]byte(`<a href="/mission/daily/redeem?once=98765">领取</a>`))
		case r.URL.Path == "/mission/daily/redeem":
			claimed = true
			w.Write([]byte(`<html><div class="message">已成功领取每日登录奖励</div></html>`))
		case r.URL.Path == "/mission/daily":
			w.Write([]byte(`<html>每日登录奖励已领取</html>`))
		case r.URL.Path == "/balance":
			w.Write([]byte(`<div id="money"><a href="/balance">29 <img src="/static/img/silver@2x.png" alt="S" /> 77 <img src="/static/img/bronze@2x.png" alt="B" /></a></div>
				<tr><td class="d"><span class="gray">20261007 的每日登录奖励 8 铜币</span></td></tr>`))
		default:
			http.NotFound(w, r)
		}
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusSuccess {
		t.Fatalf("status = %q, want success (detail=%q)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "获得 8 铜币") {
		t.Fatalf("reward should come from the ledger: %q", res.Detail)
	}
	if !strings.Contains(res.Detail, "余额 29 银币 77 铜币") {
		t.Fatalf("balance missing: %q", res.Detail)
	}
}

func TestV2EXSiteAlreadySignedReportsLedgerReward(t *testing.T) {
	// A repeat run still knows what today's sign-in earned, thanks to the ledger.
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mission/daily":
			w.Write([]byte(`<html>每日登录奖励已领取</html>`))
		case "/balance":
			w.Write([]byte(`<div id="money"><a href="/balance">29 <img src="/static/img/silver@2x.png" alt="S" /> 77 <img src="/static/img/bronze@2x.png" alt="B" /></a></div>
				<span class="gray">20261007 的每日登录奖励 8 铜币</span>`))
		default:
			http.NotFound(w, r)
		}
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusAlready {
		t.Fatalf("status = %q, want already (detail=%q)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "今日已签过（获得 8 铜币）") {
		t.Fatalf("already-signed detail should carry today's reward: %q", res.Detail)
	}
}

func TestV2EXSiteRewardUnidentified(t *testing.T) {
	// Claim succeeds but neither the flash nor /balance names an amount.
	claimed := false
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mission/daily" && !claimed:
			w.Write([]byte(`<a href="/mission/daily/redeem?once=98765">领取</a>`))
		case r.URL.Path == "/mission/daily/redeem":
			claimed = true
			w.Write([]byte(`<html>已成功领取每日登录奖励</html>`))
		case r.URL.Path == "/mission/daily":
			w.Write([]byte(`<html>每日登录奖励已领取</html>`))
		default:
			http.NotFound(w, r)
		}
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusSuccess {
		t.Fatalf("status = %q, want success (detail=%q)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "奖励信息未识别") {
		t.Fatalf("detail should flag the unknown reward: %q", res.Detail)
	}
}

func TestParseCoinBalance(t *testing.T) {
	page := `<div class="header"><div id="money"><a href="/balance" class="balance_area" style="">29 <img src="/static/img/silver@2x.png" height="16" alt="S" border="0" /> 69 <img src="/static/img/bronze@2x.png" height="16" alt="B" border="0" /></a></div>&nbsp;<a href="/">V2EX</a></div>`

	if got := parseCoinBalance(page); got != "29 银币 69 铜币" {
		t.Fatalf("parseCoinBalance = %q, want %q", got, "29 银币 69 铜币")
	}

	withGold := `<div id="money"><a href="/balance" class="balance_area">5 <img src="/static/img/gold@2x.png" alt="G" /> 29 <img src="/static/img/silver@2x.png" alt="S" /> 69 <img src="/static/img/bronze@2x.png" alt="B" /></a></div>`
	if got := parseCoinBalance(withGold); got != "5 金币 29 银币 69 铜币" {
		t.Fatalf("parseCoinBalance = %q", got)
	}

	if got := parseCoinBalance("<html>no balance widget</html>"); got != "" {
		t.Fatalf("parseCoinBalance = %q, want empty", got)
	}
}

func TestParseLedgerReward(t *testing.T) {
	page := `<tr><td class="d">每日登录奖励</td><td class="d"><span class="gray">20261007 的每日登录奖励 8 铜币</span></td></tr>
	<tr><td class="d">每日登录奖励</td><td class="d"><span class="gray">20261006 的每日登录奖励 15 铜币</span></td></tr>`

	if got := parseLedgerReward(page); got != "获得 8 铜币" {
		t.Fatalf("parseLedgerReward = %q, want the newest row", got)
	}
	if got := parseLedgerReward("<html>no ledger here</html>"); got != "" {
		t.Fatalf("parseLedgerReward = %q, want empty", got)
	}
}

func TestV2EXSiteSignInIncludesBalance(t *testing.T) {
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mission/daily":
			w.Write([]byte(`<html>每日登录奖励已领取</html>`))
		case "/balance":
			w.Write([]byte(`<div id="money"><a href="/balance">29 <img src="/static/img/silver@2x.png" alt="S" /> 69 <img src="/static/img/bronze@2x.png" alt="B" /></a></div>`))
		default:
			http.NotFound(w, r)
		}
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusAlready {
		t.Fatalf("status = %q, want already", res.Status)
	}
	if !strings.Contains(res.Detail, "余额 29 银币 69 铜币") {
		t.Fatalf("detail should include the balance: %q", res.Detail)
	}
}

func TestV2EXSiteSendsCookie(t *testing.T) {
	var got string
	site, _ := newV2EXTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Cookie")
		w.Write([]byte(`每日登录奖励已领取`))
	})

	site.SignIn(context.Background())
	if got != "A2=secret; A2O=twofactor" {
		t.Fatalf("Cookie header = %q", got)
	}
}
