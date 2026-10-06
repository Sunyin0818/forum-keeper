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
	srv := httptest.NewServer(handler)
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
