package checkin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURLV2EX is the web origin (not the API base). The daily-bonus
// mission is a normal web page that authenticates with the A2 session cookie;
// the API token used for notifications does not work here.
const DefaultBaseURLV2EX = "https://www.v2ex.com"

var (
	// onceRe captures the one-time token embedded in the mission page.
	// Example: <a href="/mission/daily/redeem?once=123456">领取</a>
	onceRe = regexp.MustCompile(`/mission/daily/redeem\?once=(\d+)`)
	// rewardRe matches the explicit confirmation shown right after claiming.
	rewardRe = regexp.MustCompile(`已成功领取每日登录奖励\s*(\d+)\s*(铜币|银币|金币)`)
	// anyRewardRe is the loose fallback used on the claim pages when the
	// explicit confirmation is missing or malformed.
	anyRewardRe = regexp.MustCompile(`(\d+)\s*(铜币|银币|金币)`)
	// coinImgRe reads the balance widget: a number followed by a coin icon.
	// alt B = 铜币, S = 银币, G = 金币 (1 金币 = 100 银币 = 10000 铜币).
	coinImgRe = regexp.MustCompile(`(\d+)\s*<img[^>]*alt="([BSG])"`)
	// ledgerRewardRe matches the /balance ledger row V2EX writes for the daily
	// bonus, e.g. "20261007 的每日登录奖励 8 铜币". The ledger is written
	// server-side, so it is a more reliable source for the day's reward than the
	// claim page's flash message. The leading date is captured so a row from a
	// previous day is never mistaken for today's.
	ledgerRewardRe = regexp.MustCompile(`(\d{8})\s*的每日登录奖励\s*(\d+)\s*(铜币|银币|金币)`)
	// missionDayRe captures the mission day key V2EX stamps into the daily page,
	// e.g. "20261009" from "v2ex:member:477522:20261009". The mission day runs
	// 08:00-08:00 UTC+8, so before 08:00 the key is still yesterday's date.
	missionDayRe = regexp.MustCompile(`v2ex:member:\d+:(\d{8})`)
	// titleRe reads the page title, which is the cheapest way to name a page
	// the claim flow did not expect (a login or challenge screen).
	titleRe = regexp.MustCompile(`(?is)<title>\s*(.*?)\s*</title>`)
)

const (
	// v2exWindowHour is when V2EX opens a new mission day: 08:00 UTC+8, i.e.
	// 00:00 UTC. Nothing can be claimed before it.
	v2exWindowHour = 8
	// v2exRetryInterval backs off when the mission day has still not rolled over
	// after its window (V2EX is sometimes late).
	v2exRetryInterval = 30 * time.Minute
	// v2exClaimAttempts bounds how often one run asks V2EX to pay the bonus.
	// The redeem endpoint answers 2xx even when it credits nothing, and a token
	// mismatch is answered with "请重新点击一次", so a single attempt can lose
	// the day over something a repeat fixes. Repeated claims are safe: the
	// bonus is once per mission day.
	v2exClaimAttempts = 3
)

// V2EXSite claims the V2EX daily login bonus.
//
// Flow (see https://www.v2ex.com/mission/daily):
//
//	GET /mission/daily            -> already claimed, or a once token
//	GET /mission/daily/redeem?once=<token>
//	GET /mission/daily            -> "每日登录奖励已领取" confirms success
type V2EXSite struct {
	baseURL string
	cookie  string
	hc      *http.Client
}

// NewV2EXSite builds a V2EX signer. cookie is the full Cookie header value,
// normally "A2=..."; accounts with 2FA enabled must include A2O as well.
//
// The cookie is loaded into a jar so everyday cookies V2EX sets while browsing
// (PB3_SESSION in particular) are carried into the claim request, as a browser
// would. A claim issued with a bare A2 has been observed to 302 without
// crediting, while the same account signed in via a browser works.
//
// PB3_SESSION is deliberately taken from the site, not from the config: V2EX
// issues it with a five-day expiry and only when the request does not already
// carry one, so a value copied from a browser shadows the fresh cookie and
// leaves the claim running on a session the site no longer hands out.
func NewV2EXSite(cookie, baseURL string, timeout time.Duration) *V2EXSite {
	if baseURL == "" {
		baseURL = DefaultBaseURLV2EX
	}
	s := &V2EXSite{
		baseURL: trimBaseURL(baseURL),
		cookie:  strings.TrimSpace(cookie),
		hc:      newHTTPClient(timeout),
	}
	if s.cookie != "" {
		if u, err := url.Parse(s.baseURL); err == nil {
			jar, _ := cookiejar.New(nil)
			jar.SetCookies(u, sessionCookies(s.cookie))
			s.hc.Jar = jar
		}
	}
	return s
}

// sessionCookies parses the configured Cookie header and drops PB3_SESSION,
// which V2EX issues itself on the first response of a run.
func sessionCookies(header string) []*http.Cookie {
	cookies := parseCookieHeader(header)
	kept := cookies[:0]
	for _, c := range cookies {
		if strings.EqualFold(c.Name, "PB3_SESSION") {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}

// parseCookieHeader turns a Cookie request header into cookies, stripping the
// surrounding quotes V2EX uses around its signed values.
func parseCookieHeader(header string) []*http.Cookie {
	var out []*http.Cookie
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, &http.Cookie{Name: name, Value: strings.Trim(strings.TrimSpace(value), `"`)})
	}
	return out
}

// Name implements Site.
func (s *V2EXSite) Name() string { return "V2EX" }

// SignIn implements Site. After a claim it also reads /balance once: the widget
// supplies the current coins, and the ledger row "YYYYMMDD 的每日登录奖励 N 铜币"
// supplies the reward the site actually paid today. The claim page only surfaces
// that reward in a flash message, so the ledger wins whenever both are present -
// it also lets an "already signed" run report what today's sign-in earned.
//
// A fresh claim is only a success when it can be seen landing: V2EX answers the
// redeem endpoint with 302 even when it credits nothing, so the optimistic
// "200 means success" shortcut would report a sign-in that never happened. The
// claim is therefore repeated a few times before the ledger is consulted, and
// the claim page is named in the failure detail when nothing explains it.
func (s *V2EXSite) SignIn(ctx context.Context) Result {
	out := s.claim(ctx)
	res, reward, confirmed := out.res, out.reward, out.confirmed
	if !res.OK() {
		return res
	}

	page, hasBalance := s.balancePage(ctx)
	todayReward := ""
	if hasBalance {
		todayReward = parseTodayLedgerReward(page, time.Now())
		if todayReward != "" {
			reward = todayReward
		}
	}

	switch {
	case res.Status == StatusSuccess && !confirmed && todayReward == "":
		detail := "签到未生效（签到页仍可领取，余额没有今天的奖励流水）"
		if out.hint != "" {
			detail += "；领取页：" + out.hint
		}
		res = failure(s.Name(), detail)
	case reward != "":
		res.Detail = withReward(res.Detail, reward)
	case res.Status == StatusSuccess:
		// Claimed, but neither the flash nor the ledger named an amount.
		res.Detail += "（奖励信息未识别）"
	}
	if hasBalance {
		if bal := parseCoinBalance(page); bal != "" {
			res.Detail = joinDetail(res.Detail, "余额 "+bal)
		}
	}
	return res
}

// claimOutcome is one run's claim evidence. hint names the claim page when it
// showed no usable confirmation, so an unexplained failure stays diagnosable.
type claimOutcome struct {
	res       Result
	reward    string
	confirmed bool
	hint      string
	// retry marks an attempt worth repeating: either V2EX answered the redeem
	// without confirming it, or it asked for the token to be sent again.
	retry bool
}

// claim runs the mission flow until an attempt is confirmed or conclusive.
func (s *V2EXSite) claim(ctx context.Context) claimOutcome {
	var out claimOutcome
	for attempt := 1; attempt <= v2exClaimAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				// The previous attempt may still have landed; the ledger is
				// the only judge, so hand its evidence back unchanged.
				return out
			case <-time.After(retryBase):
			}
		}
		out = s.signInOnce(ctx)
		if !out.retry {
			return out
		}
	}
	return out
}

// signInOnce performs the mission flow and returns the reward it could read
// from the claim pages (a flash message), if any, plus whether the claim was
// positively confirmed by those pages. The caller resolves the reward against
// the /balance ledger and decides the final status.
func (s *V2EXSite) signInOnce(ctx context.Context) claimOutcome {
	daily, finalURL, status, err := s.get(ctx, s.baseURL+"/mission/daily")
	if err != nil {
		return claimOutcome{res: failure(s.Name(), httpError(err))}
	}
	if isLoginRedirect(finalURL) || strings.Contains(daily, "You need to sign in") || strings.Contains(daily, "请先登录") {
		return claimOutcome{res: failure(s.Name(), "Cookie 已失效或未登录（需要 A2，2FA 账号还需 A2O）")}
	}
	if status != http.StatusOK {
		return claimOutcome{res: failure(s.Name(), fmt.Sprintf("签到页返回 HTTP %d", status))}
	}

	// Before 08:00 UTC+8 the page still carries the previous mission day and,
	// when that day was already claimed, looks exactly like "today is done".
	// Claiming then would target the wrong day, and reporting it as a success
	// would mark the local day as finished - so wait for the window instead.
	if day := missionDay(daily); day != "" && day != time.Now().Format("20060102") {
		return claimOutcome{res: Result{
			Site:       s.Name(),
			Status:     StatusPending,
			Detail:     fmt.Sprintf("V2EX 当日奖励 08:00 后开放（当前任务日 %s）", day),
			At:         time.Now(),
			RetryAfter: nextV2EXWindow(time.Now()),
		}}
	}

	if strings.Contains(daily, "每日登录奖励已领取") {
		return claimOutcome{
			res: Result{
				Site:   s.Name(),
				Status: StatusAlready,
				Detail: "今日已签过",
				At:     time.Now(),
			},
			reward:    extractReward(daily),
			confirmed: true,
		}
	}

	m := onceRe.FindStringSubmatch(daily)
	if m == nil {
		return claimOutcome{res: failure(s.Name(), "未找到签到 token，页面结构可能已变化")}
	}

	redeemURL := fmt.Sprintf("%s/mission/daily/redeem?once=%s", s.baseURL, url.QueryEscape(m[1]))
	redeem, redeemURLFinal, redeemStatus, err := s.get(ctx, redeemURL)
	if err != nil {
		return claimOutcome{res: failure(s.Name(), httpError(err))}
	}
	if isLoginRedirect(redeemURLFinal) {
		return claimOutcome{res: failure(s.Name(), "领取时被要求登录（Cookie 已失效，请重新取 A2 与 A2O）")}
	}
	if redeemStatus != http.StatusOK {
		return claimOutcome{res: failure(s.Name(), fmt.Sprintf("领取奖励返回 HTTP %d", redeemStatus))}
	}
	// A stale token is reported with HTTP 200 plus "请重新点击一次", which is
	// V2EX telling us to repeat the claim with the token it now hands out.
	if strings.Contains(redeem, "请重新点击一次") {
		return claimOutcome{
			res:   failure(s.Name(), "token 已失效（请重新点击一次以领取每日登录奖励）"),
			hint:  pageHint(redeem),
			retry: true,
		}
	}

	// The "已成功领取" hint only exists on the redeem response, so check both.
	again, _, _, againErr := s.get(ctx, s.baseURL+"/mission/daily")
	pages := []string{redeem}
	if againErr == nil {
		pages = append(pages, again)
	}
	for _, page := range pages {
		if strings.Contains(page, "每日登录奖励已领取") || rewardRe.MatchString(page) {
			return claimOutcome{
				res:       Result{Site: s.Name(), Status: StatusSuccess, Detail: "签到成功", At: time.Now()},
				reward:    extractReward(pages...),
				confirmed: true,
			}
		}
	}
	// The redeem endpoint answered 2xx but the pages show no confirmation. This
	// is what a no-op redeem looks like: repeat it, then let the ledger reject
	// the claim when today's reward is still missing.
	return claimOutcome{
		res:    Result{Site: s.Name(), Status: StatusSuccess, Detail: "签到成功", At: time.Now()},
		reward: extractReward(pages...),
		hint:   pageHint(pages...),
		retry:  true,
	}
}

// pageHint names a page by its title, e.g. `标题「V2EX › Sign In」`, so a claim
// that ends on an unexpected screen says which one. It returns "" when no page
// carries a title.
func pageHint(pages ...string) string {
	for _, page := range pages {
		if m := titleRe.FindStringSubmatch(page); m != nil {
			if title := strings.TrimSpace(m[1]); title != "" {
				return fmt.Sprintf("标题「%s」", title)
			}
		}
	}
	return ""
}

// get performs one GET with the session cookie, following redirects. It
// returns the body, the final URL (to detect a redirect to /signin) and the
// final status code.
func (s *V2EXSite) get(ctx context.Context, rawURL string) (body, finalURL string, status int, err error) {
	resp, err := doWithRetry(ctx, s.hc, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", browserUA)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
		req.Header.Set("Referer", s.baseURL+"/mission/daily")
		// Match a top-level browser navigation: V2EX rejected a claim that only
		// carried A2/A2O, and these hints are cheap to mirror.
		req.Header.Set("Upgrade-Insecure-Requests", "1")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-User", "?1")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("sec-ch-ua", `"Chromium";v="137", "Not/A)Brand";v="24"`)
		req.Header.Set("sec-ch-ua-mobile", "?0")
		req.Header.Set("sec-ch-ua-platform", `"macOS"`)
		// The jar already carries the configured cookies once seeded; fall back
		// to the raw header only when it could not be built.
		if s.hc.Jar == nil && s.cookie != "" {
			req.Header.Set("Cookie", s.cookie)
		}
		return req, nil
	})
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", resp.Request.URL.String(), resp.StatusCode, err
	}
	return string(b), resp.Request.URL.String(), resp.StatusCode, nil
}

// balancePage fetches /balance, which carries both the current coins widget and
// the reward ledger. It is best-effort: any failure just omits those lines from
// the card.
func (s *V2EXSite) balancePage(ctx context.Context) (string, bool) {
	body, _, status, err := s.get(ctx, s.baseURL+"/balance")
	if err != nil || status != http.StatusOK {
		return "", false
	}
	return body, true
}

// parseTodayLedgerReward extracts "获得 8 铜币" from the daily-bonus row of the
// /balance ledger dated today, or "" when there is none. The ledger is newest
// first, but the date is checked explicitly so a previous day's reward is never
// reported as today's. now is interpreted in its own location, which is the
// container's TZ (Asia/Shanghai).
func parseTodayLedgerReward(html string, now time.Time) string {
	day := now.Format("20060102")
	for _, m := range ledgerRewardRe.FindAllStringSubmatch(html, -1) {
		if m[1] == day {
			return fmt.Sprintf("获得 %s %s", m[2], m[3])
		}
	}
	return ""
}

// missionDay returns the mission day key V2EX stamps into the daily page, e.g.
// "20261009" from "v2ex:member:477522:20261009". It returns "" when the page
// carries no key, in which case the caller keeps the previous behaviour.
func missionDay(html string) string {
	if m := missionDayRe.FindStringSubmatch(html); m != nil {
		return m[1]
	}
	return ""
}

// nextV2EXWindow returns when the current mission day may be claimed: 08:01
// local on the same day, or a short backoff when that window has already
// passed (V2EX rolls the day over late often enough to be worth another try).
func nextV2EXWindow(now time.Time) time.Time {
	w := time.Date(now.Year(), now.Month(), now.Day(), v2exWindowHour, 1, 0, 0, now.Location())
	if w.After(now) {
		return w
	}
	return now.Add(v2exRetryInterval)
}

// parseCoinBalance extracts "29 银币 69 铜币" from the balance widget.
func parseCoinBalance(html string) string {
	idx := strings.Index(html, `id="money"`)
	if idx < 0 {
		return ""
	}
	block := html[idx:]
	if end := strings.Index(block, "</div>"); end >= 0 {
		block = block[:end]
	}

	amounts := make(map[string]string)
	for _, m := range coinImgRe.FindAllStringSubmatch(block, -1) {
		if _, seen := amounts[m[2]]; !seen {
			amounts[m[2]] = m[1]
		}
	}

	parts := make([]string, 0, 3)
	for _, coin := range []struct{ alt, name string }{{"G", "金币"}, {"S", "银币"}, {"B", "铜币"}} {
		if v, ok := amounts[coin.alt]; ok {
			parts = append(parts, v+" "+coin.name)
		}
	}
	return strings.Join(parts, " ")
}

// joinDetail appends detail b to a with a separator, skipping empty parts.
func joinDetail(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "，" + b
	}
}

func isLoginRedirect(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.HasPrefix(u.Path, "/signin")
}

// withReward renders detail with the reward as a parenthesised suffix, e.g.
// "签到成功（获得 8 铜币）". An empty reward leaves the detail untouched.
func withReward(prefix, reward string) string {
	if reward == "" {
		return prefix
	}
	return fmt.Sprintf("%s（%s）", prefix, reward)
}

func extractReward(pages ...string) string {
	for _, p := range pages {
		if m := rewardRe.FindStringSubmatch(p); m != nil {
			return fmt.Sprintf("获得 %s %s", m[1], m[2])
		}
	}
	for _, p := range pages {
		if m := anyRewardRe.FindStringSubmatch(p); m != nil {
			return fmt.Sprintf("获得 %s %s", m[1], m[2])
		}
	}
	return ""
}
