package checkin

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
	// anyRewardRe is the loose fallback used on the balance/mission pages.
	anyRewardRe = regexp.MustCompile(`(\d+)\s*(铜币|银币|金币)`)
	// coinImgRe reads the balance widget: a number followed by a coin icon.
	// alt B = 铜币, S = 银币, G = 金币 (1 金币 = 100 银币 = 10000 铜币).
	coinImgRe = regexp.MustCompile(`(\d+)\s*<img[^>]*alt="([BSG])"`)
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
func NewV2EXSite(cookie, baseURL string, timeout time.Duration) *V2EXSite {
	if baseURL == "" {
		baseURL = DefaultBaseURLV2EX
	}
	return &V2EXSite{
		baseURL: trimBaseURL(baseURL),
		cookie:  strings.TrimSpace(cookie),
		hc:      newHTTPClient(timeout),
	}
}

// Name implements Site.
func (s *V2EXSite) Name() string { return "V2EX" }

// SignIn implements Site. On success it also reads the account balance from
// /balance so the card can show the current coins.
func (s *V2EXSite) SignIn(ctx context.Context) Result {
	res := s.signInOnce(ctx)
	if res.OK() {
		if bal := s.balance(ctx); bal != "" {
			res.Detail = joinDetail(res.Detail, "余额 "+bal)
		}
	}
	return res
}

// signInOnce performs the mission flow without the extra balance lookup.
func (s *V2EXSite) signInOnce(ctx context.Context) Result {
	daily, finalURL, status, err := s.get(ctx, s.baseURL+"/mission/daily")
	if err != nil {
		return failure(s.Name(), httpError(err))
	}
	if isLoginRedirect(finalURL) || strings.Contains(daily, "You need to sign in") || strings.Contains(daily, "请先登录") {
		return failure(s.Name(), "Cookie 已失效或未登录（需要 A2，2FA 账号还需 A2O）")
	}
	if status != http.StatusOK {
		return failure(s.Name(), fmt.Sprintf("签到页返回 HTTP %d", status))
	}

	if strings.Contains(daily, "每日登录奖励已领取") {
		return Result{
			Site:   s.Name(),
			Status: StatusAlready,
			Detail: withReward("今日已签过", daily),
			At:     time.Now(),
		}
	}

	m := onceRe.FindStringSubmatch(daily)
	if m == nil {
		return failure(s.Name(), "未找到签到 token，页面结构可能已变化")
	}

	redeemURL := fmt.Sprintf("%s/mission/daily/redeem?once=%s", s.baseURL, url.QueryEscape(m[1]))
	redeem, _, redeemStatus, err := s.get(ctx, redeemURL)
	if err != nil {
		return failure(s.Name(), httpError(err))
	}
	if redeemStatus != http.StatusOK {
		return failure(s.Name(), fmt.Sprintf("领取奖励返回 HTTP %d", redeemStatus))
	}
	// A stale token is reported with HTTP 200, so it has to be sniffed out of
	// the body before the success fallback below.
	if strings.Contains(redeem, "请重新点击一次") {
		return failure(s.Name(), "token 已失效（请重新点击一次以领取每日登录奖励）")
	}

	// The "已成功领取" hint only exists on the redeem response, so check both.
	again, _, _, againErr := s.get(ctx, s.baseURL+"/mission/daily")
	pages := []string{redeem}
	if againErr == nil {
		pages = append(pages, again)
	}
	for _, page := range pages {
		if strings.Contains(page, "每日登录奖励已领取") || rewardRe.MatchString(page) {
			return Result{Site: s.Name(), Status: StatusSuccess, Detail: withReward("签到成功", page), At: time.Now()}
		}
	}
	// A 200 on the redeem endpoint is the site's success signal; the reward text
	// may simply be rendered by JavaScript we do not execute.
	return Result{
		Site:   s.Name(),
		Status: StatusSuccess,
		Detail: withReward("签到成功（奖励信息未识别）", pages...),
		At:     time.Now(),
	}
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
		if s.cookie != "" {
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

// balance reads the current coins from /balance. It is best-effort: any failure
// just omits the line from the card.
func (s *V2EXSite) balance(ctx context.Context) string {
	body, _, status, err := s.get(ctx, s.baseURL+"/balance")
	if err != nil || status != http.StatusOK {
		return ""
	}
	return parseCoinBalance(body)
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

// withReward prefixes detail with the extracted reward, when one is found.
func withReward(prefix string, pages ...string) string {
	if reward := extractReward(pages...); reward != "" {
		return fmt.Sprintf("%s（%s）", prefix, reward)
	}
	return prefix
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
