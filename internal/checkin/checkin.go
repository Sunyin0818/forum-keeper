// Package checkin signs in to the forums the user reads daily.
//
// Two sites are supported today: v2ex.com (claim the daily login bonus) and
// 2libra.com (POST /api/sign). Each site is a Site implementation that turns a
// cookie into a Result; Run drives them in order.
//
// Sign-in is idempotent by nature - every site reports "already signed in" as a
// success - so retrying after a failure is always safe.
package checkin

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Status describes how a single site's check-in went.
type Status string

const (
	// StatusSuccess means a fresh sign-in was performed today.
	StatusSuccess Status = "success"
	// StatusAlready means the site reported today's sign-in was done earlier.
	// Treated as a success: re-running must not be an error.
	StatusAlready Status = "already"
	// StatusFailed means the credential was rejected or the request did not
	// produce a confirmed sign-in.
	StatusFailed Status = "failed"
)

// Result is the outcome of one site's sign-in.
type Result struct {
	Site   string
	Status Status
	// Detail is a single human readable line: the reward, "今日已签过", or the
	// failure reason. It is rendered verbatim into the Feishu card as
	// plain_text, so it must not contain markup.
	Detail string
	At     time.Time
}

// OK reports whether the sign-in counts as a success.
func (r Result) OK() bool {
	return r.Status == StatusSuccess || r.Status == StatusAlready
}

// Site is one forum that can be checked in to.
type Site interface {
	// Name is the label shown in logs and the notification card.
	Name() string
	// SignIn performs one check-in. It must not return an error: transient
	// failures are reported as StatusFailed so the caller can still notify.
	SignIn(ctx context.Context) Result
}

// Run signs in to every site in order and returns their results. It never
// aborts early: one broken site must not hide the others.
func Run(ctx context.Context, log *slog.Logger, sites []Site) []Result {
	if log == nil {
		log = slog.Default()
	}
	out := make([]Result, 0, len(sites))
	for _, s := range sites {
		started := time.Now()
		res := s.SignIn(ctx)
		if res.At.IsZero() {
			res.At = time.Now()
		}
		if res.Site == "" {
			res.Site = s.Name()
		}
		out = append(out, res)

		level := slog.LevelInfo
		if !res.OK() {
			level = slog.LevelWarn
		}
		log.Log(ctx, level, "check-in result",
			"site", res.Site,
			"status", string(res.Status),
			"detail", res.Detail,
			"elapsed", time.Since(started).Round(time.Millisecond),
		)
	}
	return out
}

// UntilNext returns how long to wait before the next local hh:mm.
//
// hh:mm earlier than (or equal to) now rolls over to tomorrow, which makes the
// loop restart-safe: a service that comes up at 10:00 waits until 06:00 the
// next day instead of firing an immediate duplicate.
func UntilNext(now time.Time, hour, minute int, loc *time.Location) time.Duration {
	if loc == nil {
		loc = time.Local
	}
	n := now.In(loc)
	next := time.Date(n.Year(), n.Month(), n.Day(), hour, minute, 0, 0, loc)
	if !next.After(n) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(n)
}

// SameLocalDay reports whether a and b fall on the same calendar day in loc.
func SameLocalDay(a, b time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.Local
	}
	x, y := a.In(loc), b.In(loc)
	return x.Year() == y.Year() && x.YearDay() == y.YearDay()
}

// browserUA is sent by both sites: Cloudflare in front of 2libra rejects
// obviously scripted clients, and V2EX behaves the same way.
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"

// newHTTPClient builds a client that honours HTTPS_PROXY / NO_PROXY, matching
// the V2EX API client: a proxy that makes v2ex.com reachable is also the right
// route for the two forums.
func newHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// failure builds a StatusFailed result.
func failure(site, detail string) Result {
	return Result{Site: site, Status: StatusFailed, Detail: detail, At: time.Now()}
}

// httpError renders a short, non-secret description of a failed request.
func httpError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("请求失败：%s", err)
}

func trimBaseURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}
