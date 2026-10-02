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

var (
	// checkResultRe extracts the playful "签到勤勉检定" line the site returns.
	checkResultRe = regexp.MustCompile(`签到勤勉检定[^。]*。`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]*>`)
)

// LibraSite signs in to 2libra.com.
//
// The endpoint is POST /api/sign and accepts either the browser cookie
// (access_token=...) or an Authorization: Bearer token. Cloudflare sits in
// front of it, so the browser-like headers matter.
type LibraSite struct {
	baseURL string
	cookie  string
	token   string
	hc      *http.Client
}

// NewLibraSite builds a 2libra signer. Exactly one of cookie / token is
// normally set; if both are present the cookie wins, matching the browser.
func NewLibraSite(cookie, token, baseURL string, timeout time.Duration) *LibraSite {
	if baseURL == "" {
		baseURL = DefaultBaseURLLibra
	}
	return &LibraSite{
		baseURL: trimBaseURL(baseURL),
		cookie:  strings.TrimSpace(cookie),
		token:   strings.TrimSpace(token),
		hc:      newHTTPClient(timeout),
	}
}

// Name implements Site.
func (s *LibraSite) Name() string { return "2libra" }

// SignIn implements Site.
func (s *LibraSite) SignIn(ctx context.Context) Result {
	body, status, err := s.post(ctx, s.baseURL+"/api/sign")
	if err != nil {
		return failure(s.Name(), httpError(err))
	}
	text := libraText(body)

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return failure(s.Name(), fmt.Sprintf("Cookie 已失效或无权限（HTTP %d）", status))
	case status >= 500:
		return failure(s.Name(), fmt.Sprintf("站点错误（HTTP %d）", status))
	}

	switch {
	case strings.Contains(text, "已经签到") || strings.Contains(text, "已签到"):
		return Result{Site: s.Name(), Status: StatusAlready, Detail: firstNonEmpty(checkResult(text), "今日已签过"), At: time.Now()}
	case status == http.StatusCreated,
		strings.Contains(text, "签到成功"),
		strings.Contains(text, "签到勤勉检定"):
		return Result{Site: s.Name(), Status: StatusSuccess, Detail: firstNonEmpty(checkResult(text), "签到成功"), At: time.Now()}
	case status == http.StatusOK && !containsAny(text, "失败", "错误", "异常", "无效", "未登录", "Unauthorized") && strings.Contains(text, "签到"):
		// Fallback for a reworded success message: anything that talks about
		// 签到 without a failure word.
		return Result{Site: s.Name(), Status: StatusSuccess, Detail: firstNonEmpty(checkResult(text), "签到成功"), At: time.Now()}
	default:
		return failure(s.Name(), libraFailureDetail(status, text))
	}
}

// post sends the check-in request with whichever credential is configured.
func (s *LibraSite) post(ctx context.Context, rawURL string) (body string, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Origin", s.baseURL)
	req.Header.Set("Referer", s.baseURL+"/")
	if s.cookie != "" {
		req.Header.Set("Cookie", s.cookie)
	} else if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.hc.Do(req)
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

// libraText flattens the envelope into searchable text. `d` is usually an HTML
// fragment carrying the human readable result.
func libraText(body string) string {
	var envelope libraResp
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return stripTags(body)
	}

	parts := make([]string, 0, 2)
	if envelope.M != "" {
		parts = append(parts, stripTags(envelope.M))
	}
	parts = append(parts, stripTags(rawToString(envelope.D)))
	return strings.TrimSpace(strings.Join(parts, " "))
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
