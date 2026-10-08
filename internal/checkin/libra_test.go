package checkin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newLibraTestSite(t *testing.T, credential string, handler http.HandlerFunc) *LibraSite {
	t.Helper()
	// Default ledger: today has no check-in entry. Tests that need one use
	// newLibraTestSiteWithLedger.
	return newLibraTestSiteWithLedger(t, credential, handler, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"c":0,"m":"请求成功","d":[]}`))
	})
}

func newLibraTestSiteWithLedger(t *testing.T, credential string, sign, ledger http.HandlerFunc) *LibraSite {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == libraLedgerPath {
			ledger(w, r)
			return
		}
		sign(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewLibraSite(credential, srv.URL, 5*time.Second)
}

func TestLibraSiteSuccessCreated(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"c":201,"m":"签到成功","d":"<p>签到勤勉检定 +2。</p>"}`))
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusSuccess || res.Site != "2libra" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(res.Detail, "签到勤勉检定") {
		t.Fatalf("check result missing from detail: %q", res.Detail)
	}
}

func TestLibraSiteSuccessOK(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"c":200,"m":"签到成功","d":null}`))
	})

	if res := site.SignIn(context.Background()); res.Status != StatusSuccess {
		t.Fatalf("status = %q, want success (detail=%q)", res.Status, res.Detail)
	}
}

func TestLibraSiteAlreadySigned(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"c":200,"m":"你今天已经签到过了，签到时间为：2026-10-01 06:00:00","d":null}`))
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusAlready || !res.OK() {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestLibraSiteUnauthorized(t *testing.T) {
	site := newLibraTestSite(t, "access_token=stale", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"c":401,"m":"Unauthorized","d":null}`))
	})

	res := site.SignIn(context.Background())
	if res.OK() {
		t.Fatalf("401 must fail, got %+v", res)
	}
	if !strings.Contains(res.Detail, "Cookie") {
		t.Fatalf("detail should mention the cookie: %q", res.Detail)
	}
}

func TestLibraSiteServerError(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	res := site.SignIn(context.Background())
	if res.OK() {
		t.Fatalf("5xx must fail, got %+v", res)
	}
}

func TestLibraSiteLogicalFailure(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"c":400,"m":"签到失败，请稍后再试","d":null}`))
	})

	res := site.SignIn(context.Background())
	if res.OK() {
		t.Fatalf("logical failure must fail, got %+v", res)
	}
}

func TestLibraSiteAutoDetectsCredential(t *testing.T) {
	var cookie, auth string
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	})
	site.SignIn(context.Background())
	if cookie != "access_token=jwt" {
		t.Fatalf("Cookie = %q", cookie)
	}
	if auth != "" {
		t.Fatalf("Authorization should be omitted for a cookie credential, got %q", auth)
	}

	// A raw token has no '=' or ';', so it is sent as a bearer token instead.
	var bearer, bearerCookie string
	bearerSite := newLibraTestSite(t, "eyJhbGciOiJI.raw.jwt", func(w http.ResponseWriter, r *http.Request) {
		bearer = r.Header.Get("Authorization")
		bearerCookie = r.Header.Get("Cookie")
		w.WriteHeader(http.StatusCreated)
	})
	bearerSite.SignIn(context.Background())
	if bearer != "Bearer eyJhbGciOiJI.raw.jwt" {
		t.Fatalf("Authorization = %q", bearer)
	}
	if bearerCookie != "" {
		t.Fatalf("Cookie should be omitted for a bearer credential, got %q", bearerCookie)
	}
}

func TestLibraSiteRealAlreadyShape(t *testing.T) {
	// Captured from the live API: `d` is an object, success is signalled by ok/alreadySigned.
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"c":200,"m":"请求成功","d":{"ok":true,"alreadySigned":true,"checkData":null,"streak":5,"coins":0,"balance":134034}}`))
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusAlready || !res.OK() {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(res.Detail, "连续 5") || !strings.Contains(res.Detail, "134034") {
		t.Fatalf("detail should carry streak and balance: %q", res.Detail)
	}
}

func TestLibraSiteRealFreshShape(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"c":200,"m":"请求成功","d":{"ok":true,"alreadySigned":false,"checkData":null,"streak":6,"coins":2,"balance":134036}}`))
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusSuccess {
		t.Fatalf("status = %q, want success (detail=%q)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "+2 金币") || !strings.Contains(res.Detail, "连续 6") {
		t.Fatalf("detail should carry coins and streak: %q", res.Detail)
	}
}

func TestLibraSiteOKFalseIsFailure(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"c":200,"m":"操作失败","d":{"ok":false}}`))
	})

	res := site.SignIn(context.Background())
	if res.OK() {
		t.Fatalf("ok=false must fail, got %+v", res)
	}
}

// TestLibraSiteAlreadySignedUsesLedger covers the reason for the /coins ledger
// fetch: on an already-signed day the sign API reports coins=0, so the amount
// actually paid has to come from /api/coins/today-transaction.
func TestLibraSiteAlreadySignedUsesLedger(t *testing.T) {
	site := newLibraTestSiteWithLedger(t, "access_token=jwt",
		func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"c":200,"m":"请求成功","d":{"ok":true,"alreadySigned":true,"streak":5,"coins":0,"balance":134444}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("ledger method = %s, want GET", r.Method)
			}
			w.Write([]byte(`{"c":0,"m":"请求成功","d":[{"id":1,"amount":110,"reason":"checkin"},{"id":2,"amount":-20,"reason":"comment"}]}`))
		})

	res := site.SignIn(context.Background())
	if res.Status != StatusAlready || !res.OK() {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(res.Detail, "+110 金币") {
		t.Fatalf("detail should carry today's check-in coins from the ledger: %q", res.Detail)
	}
	if strings.Contains(res.Detail, "-20") {
		t.Fatalf("detail should ignore non-check-in entries: %q", res.Detail)
	}
}

// TestLibraSiteFreshSignSkipsLedger: when the sign response already names the
// coins, no extra request is needed.
func TestLibraSiteFreshSignSkipsLedger(t *testing.T) {
	var ledgerCalls int
	site := newLibraTestSiteWithLedger(t, "access_token=jwt",
		func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"c":200,"m":"请求成功","d":{"ok":true,"alreadySigned":false,"streak":6,"coins":2,"balance":134446}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			ledgerCalls++
			w.Write([]byte(`{"c":0,"m":"请求成功","d":[]}`))
		})

	res := site.SignIn(context.Background())
	if !strings.Contains(res.Detail, "+2 金币") {
		t.Fatalf("detail should carry the sign response coins: %q", res.Detail)
	}
	if ledgerCalls != 0 {
		t.Fatalf("ledger should not be fetched when the sign response has coins; calls = %d", ledgerCalls)
	}
}

// TestLibraSiteLedgerFailureIsBestEffort: a broken ledger must not turn a good
// sign-in into a failure.
func TestLibraSiteLedgerFailureIsBestEffort(t *testing.T) {
	site := newLibraTestSiteWithLedger(t, "access_token=jwt",
		func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"c":200,"m":"请求成功","d":{"ok":true,"alreadySigned":true,"streak":5,"coins":0,"balance":134444}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

	res := site.SignIn(context.Background())
	if !res.OK() {
		t.Fatalf("ledger failure must not fail the sign-in, got %+v", res)
	}
	if strings.Contains(res.Detail, "金币") {
		t.Fatalf("no coins expected, got %q", res.Detail)
	}
}

func TestLibraSiteNonJSONBodyStillDetected(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`<html><b>签到成功</b></html>`))
	})

	if res := site.SignIn(context.Background()); res.Status != StatusSuccess {
		t.Fatalf("status = %q, want success (detail=%q)", res.Status, res.Detail)
	}
}

func TestLibraSiteEnvelopeCode201WithHTTP200(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"c":201,"m":"签到成功","d":null}`))
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusSuccess {
		t.Fatalf("status = %q, want success (detail=%q)", res.Status, res.Detail)
	}
}

func TestLibraSiteRetriesTransientFailures(t *testing.T) {
	var calls int
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})

	res := site.SignIn(context.Background())
	if res.Status != StatusSuccess {
		t.Fatalf("status = %q, want success after retries (detail=%q)", res.Status, res.Detail)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestLibraSiteCloudflareChallenge(t *testing.T) {
	site := newLibraTestSite(t, "access_token=jwt", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`<html><title>Just a moment...</title>Attention Required! | Cloudflare</html>`))
	})

	res := site.SignIn(context.Background())
	if res.OK() {
		t.Fatalf("challenge must fail, got %+v", res)
	}
	if !strings.Contains(res.Detail, "Cloudflare") {
		t.Fatalf("detail should name Cloudflare: %q", res.Detail)
	}
}
