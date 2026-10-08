package checkin

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURLLibra is the 2libra forum origin.
const DefaultBaseURLLibra = "https://2libra.com"

// libraLedgerPath is the API behind https://2libra.com/coins: the day's coin
// transactions, including the check-in reward.
const libraLedgerPath = "/api/coins/today-transaction"

var (
	// checkResultRe extracts the playful "签到勤勉检定" line the site returns.
	checkResultRe = regexp.MustCompile(`签到勤勉检定[^。]*。`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]*>`)
)

// LibraSite signs in to 2libra.com.
//
// The endpoint is POST /api/sign. It accepts either the browser cookie
// (access_token=...) or an Authorization: Bearer token, so a single credential
// string is enough: anything without '=' or ';' is treated as a raw bearer
// token, everything else as a Cookie header.
//
// Cloudflare sits in front of it, so the browser-like headers matter.
type LibraSite struct {
	baseURL string
	cookie  string
	token   string
	hc      *http.Client
}

// NewLibraSite builds a 2libra signer from a cookie string or a raw bearer
// token.
func NewLibraSite(credential, baseURL string, timeout time.Duration) *LibraSite {
	if baseURL == "" {
		baseURL = DefaultBaseURLLibra
	}
	site := &LibraSite{
		baseURL: trimBaseURL(baseURL),
		hc:      newHTTPClient(timeout),
	}
	credential = strings.TrimSpace(credential)
	switch {
	case credential == "":
	case strings.ContainsAny(credential, "=;"):
		site.cookie = credential
	default:
		site.token = credential
	}
	return site
}

// Name implements Site.
func (s *LibraSite) Name() string { return "2libra" }

// SignIn implements Site.
func (s *LibraSite) SignIn(ctx context.Context) Result {
	body, status, err := s.post(ctx, s.baseURL+"/api/sign")
	if err != nil {
		return failure(s.Name(), httpError(err))
	}
	env, text := parseLibra(body)

	// Cloudflare sits in front of the API. A challenge looks nothing like a
	// normal JSON error, so call it out instead of reporting a bland failure.
	if isCloudflareChallenge(status, text) {
		return failure(s.Name(), "被 Cloudflare 拦截（需要浏览器指纹，或换网络）")
	}

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || env.C == 401 || env.C == 403:
		return failure(s.Name(), fmt.Sprintf("Cookie 已失效或无权限（HTTP %d）", status))
	case status == http.StatusTooManyRequests:
		return failure(s.Name(), "请求过于频繁（HTTP 429），稍后重试")
	case status >= 500:
		return failure(s.Name(), fmt.Sprintf("站点错误（HTTP %d）", status))
	}

	// Preferred path: the API's structured result. Real response:
	//   {"c":200,"m":"请求成功","d":{"ok":true,"alreadySigned":true,
	//     "checkData":null,"streak":5,"coins":0,"balance":134034}}
	if data, ok := parseLibraData(env.D); ok {
		if !data.OK {
			return failure(s.Name(), firstNonEmpty(stripTags(env.M), "签到失败"))
		}
		// The sign endpoint only reports `coins` on a fresh sign-in; when the
		// day was already signed it sends 0. Read today's ledger so an
		// "already signed" run still shows what today earned - the same trick
		// the V2EX site uses for the /balance reward line.
		if data.Coins <= 0 {
			if coins, ok := s.todayCheckinCoins(ctx); ok {
				data.Coins = coins
			}
		}
		if data.AlreadySigned {
			return Result{Site: s.Name(), Status: StatusAlready, Detail: libraDetail("今日已签过", data), At: time.Now()}
		}
		return Result{Site: s.Name(), Status: StatusSuccess, Detail: libraDetail("签到成功", data), At: time.Now()}
	}

	// Fallback for non-JSON bodies or a changed shape.
	var res Result
	switch {
	case strings.Contains(text, "已经签到") || strings.Contains(text, "已签到"):
		res = Result{Site: s.Name(), Status: StatusAlready, Detail: firstNonEmpty(checkResult(text), "今日已签过"), At: time.Now()}
	case env.C == http.StatusCreated,
		status == http.StatusCreated,
		strings.Contains(text, "签到成功"),
		strings.Contains(text, "签到勤勉检定"):
		res = Result{Site: s.Name(), Status: StatusSuccess, Detail: firstNonEmpty(checkResult(text), "签到成功"), At: time.Now()}
	case (status == http.StatusOK || env.C == http.StatusOK) &&
		!containsAny(text, "失败", "错误", "异常", "无效", "未登录", "Unauthorized") &&
		strings.Contains(text, "签到"):
		// Anything that talks about 签到 without a failure word.
		res = Result{Site: s.Name(), Status: StatusSuccess, Detail: firstNonEmpty(checkResult(text), "签到成功"), At: time.Now()}
	default:
		return failure(s.Name(), libraFailureDetail(status, text))
	}
	if coins, ok := s.todayCheckinCoins(ctx); ok && coins > 0 {
		res.Detail = withReward(res.Detail, fmt.Sprintf("%d 金币", coins))
	}
	return res
}

// todayCheckinCoins reads the day's coin ledger (/api/coins/today-transaction,
// the data behind https://2libra.com/coins) and returns the amount today's
// check-in credited. It reports false when the request fails or the day has no
// check-in entry. Best-effort: callers only use it to enrich the card.
func (s *LibraSite) todayCheckinCoins(ctx context.Context) (int, bool) {
	body, status, err := s.get(ctx, s.baseURL+libraLedgerPath)
	if err != nil || status != http.StatusOK {
		return 0, false
	}
	var env struct {
		D []libraCoinTx `json:"d"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return 0, false
	}
	for _, tx := range env.D {
		if tx.Reason == "checkin" {
			return tx.Amount, true
		}
	}
	return 0, false
}

// post sends the check-in request with whichever credential is configured.
func (s *LibraSite) post(ctx context.Context, rawURL string) (body string, status int, err error) {
	return s.do(ctx, http.MethodPost, rawURL)
}

// get performs a GET on the API, e.g. the coin ledger behind /coins.
func (s *LibraSite) get(ctx context.Context, rawURL string) (body string, status int, err error) {
	return s.do(ctx, http.MethodGet, rawURL)
}

// do sends one authenticated API request. Cloudflare sits in front of the
// site, so the browser-like headers matter for both verbs.
func (s *LibraSite) do(ctx context.Context, method, rawURL string) (body string, status int, err error) {
	resp, err := doWithRetry(ctx, s.hc, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", browserUA)
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
		req.Header.Set("Origin", s.baseURL)
		req.Header.Set("Referer", s.baseURL+"/")
		if s.cookie != "" {
			req.Header.Set("Cookie", s.cookie)
		} else if s.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.token)
		}
		return req, nil
	})
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", resp.StatusCode, err
	}
	return string(b), resp.StatusCode, nil
}

// libraResp mirrors the API envelope {"c":<code>,"m":<message>,"d":<data>}.
type libraResp struct {
	C int             `json:"c"`
	M string          `json:"m"`
	D json.RawMessage `json:"d"`
}

// libraData is the `d` object of a sign-in response, e.g.
// {"ok":true,"alreadySigned":true,"checkData":null,"streak":5,"coins":0,"balance":134034}
type libraData struct {
	OK            bool `json:"ok"`
	AlreadySigned bool `json:"alreadySigned"`
	Streak        int  `json:"streak"`
	Coins         int  `json:"coins"`
	Balance       int  `json:"balance"`
	CheckData     any  `json:"checkData"`
}

// libraCoinTx is one row of /api/coins/today-transaction. Today's check-in
// appears as {"amount":110,"reason":"checkin",...}.
type libraCoinTx struct {
	Amount int    `json:"amount"`
	Reason string `json:"reason"`
}

// parseLibra extracts the envelope and a searchable text rendering of it.
func parseLibra(body string) (libraResp, string) {
	var env libraResp
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return libraResp{}, stripTags(body)
	}

	parts := make([]string, 0, 2)
	if env.M != "" {
		parts = append(parts, stripTags(env.M))
	}
	parts = append(parts, stripTags(rawToString(env.D)))
	return env, strings.TrimSpace(strings.Join(parts, " "))
}

// parseLibraData decodes `d` as the structured result. It reports false when
// `d` is null, a string or an array (older/other shapes), so the caller can
// fall back to text matching.
func parseLibraData(raw json.RawMessage) (libraData, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return libraData{}, false
	}
	var d libraData
	if err := json.Unmarshal(raw, &d); err != nil {
		return libraData{}, false
	}
	return d, true
}

// libraDetail renders "今日已签过（连续 5 天，余额 134034）"-style details.
func libraDetail(prefix string, d libraData) string {
	parts := make([]string, 0, 3)
	if d.Coins > 0 {
		parts = append(parts, fmt.Sprintf("+%d 金币", d.Coins))
	}
	if d.Streak > 0 {
		parts = append(parts, fmt.Sprintf("连续 %d 天", d.Streak))
	}
	if d.Balance > 0 {
		parts = append(parts, fmt.Sprintf("余额 %d", d.Balance))
	}
	if len(parts) == 0 {
		return prefix
	}
	return prefix + "（" + strings.Join(parts, "，") + "）"
}

// isCloudflareChallenge detects an interstitial page rather than an API reply.
func isCloudflareChallenge(status int, text string) bool {
	if status != http.StatusForbidden && status != http.StatusServiceUnavailable {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{"just a moment", "cf-chl", "attention required", "cloudflare", "enable javascript"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// rawToString renders a json.RawMessage that may be a string, object or null.
func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func stripTags(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

func checkResult(text string) string {
	if m := checkResultRe.FindString(text); m != "" {
		return strings.TrimSpace(m)
	}
	return ""
}

func libraFailureDetail(status int, text string) string {
	if text != "" {
		return fmt.Sprintf("签到失败：%s", truncate(text, 120))
	}
	return fmt.Sprintf("签到失败（HTTP %d）", status)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}
