package ptsite

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sunyin0818/forum-keeper/internal/checkin"
)

const (
	defaultSuccessPattern = `(签到成功|簽到成功|成功签到|签到完成|簽到完成|打卡成功|领取成功|已领取|签到已成功)`
	defaultAlreadyPattern = `(今日已签|今天已签|已经签到|已签到过|已签到|已簽到|签到已得|已领取过|不要重复签到|无需重复签到|already|signed in today)`
	defaultLoginPattern   = `(用户登录|會員登入|请先登录|请先登入|请登录|login\.php\?returnto|takelogin\.php|session expired)`
	challengePattern      = `(just a moment|cf-chl|cf_chl_opt|challenge-platform|challenges\.cloudflare\.com|turnstile|正在进行安全检测|雷池\s*WAF|safeline)`
	// Do not treat challenge-platform / cf-chl-widget as an interstitial:
	// normal Turnstile widgets carry those too. Only the interstitial markers
	// mean the request never reached the real page.
	hardChallengePattern = `(just a moment|_cf_chl_opt|chl_page|正在进行安全检测|雷池\s*WAF|safeline)`
	captchaPattern       = `(captcha|imagestring|imagehash|验证码|驗證碼)`
	maintenancePattern   = `(站点维护|站點維護|系统维护|系統維護|维护中|維護中|maintenance|under maintenance|暂时关闭|暫停開放)`
	stateHintPattern     = `(已签到|已簽到|今日已签|今天已签|已领取|已領取|领取成功|領取成功|签到成功|簽到成功|成功签到|签到完成|本次签到|签到获得|已签到过|不要重复签到|无需重复签到)`
	signinActionPattern  = `(立即签到|点击签到|點擊簽到|去签到|前往签到|马上签到|开始签到|每日签到|打卡)`
	rewardPattern        = `(?:(?:获得|獲得|獲取|签到已得|簽到已得)\s*([\d,]+(?:\.\d+)?)\s*(?:个|粒|枚|点|個|顆)?\s*(魔力值|爆米花|积分|金币|憨豆|金贝|鲸币|奖励))`
)

var (
	commonSigninPaths = []string{
		"/attendance.php",
		"/signin.php",
		"/sign_in.php",
		"/checkin.php",
	}
	jsonStateRe = regexp.MustCompile(`(?i)"(?:state|status)"\s*:\s*"?(success|ok|true|false|fail|failed|error)`)
)

// CheckIn implements checkin.Site.
func (s *Site) CheckIn(ctx context.Context) checkin.Result {
	started := s.now()
	switch s.cfg.Signin.Mode {
	case "off":
		return checkin.Result{Site: s.Name(), Status: checkin.StatusAlready, Detail: "未启用签到", At: started}
	case "login":
		return s.loginOnly(ctx, started)
	default:
		return s.checkin(ctx, started)
	}
}

func (s *Site) loginOnly(ctx context.Context, started time.Time) checkin.Result {
	p, err := s.get(ctx, DefaultIndexPath)
	if err != nil {
		return s.failure(started, "登录检查失败："+httpReason(err))
	}
	res, _ := s.judge(p)
	if res.Status == checkin.StatusFailed {
		return res
	}
	if res.OK() {
		res.Detail = "登录有效"
		return res
	}
	text := visibleText(p.Text)
	if s.isLoginPage(p, text) {
		return s.failure(started, "Cookie 已失效或未登录")
	}
	if s.isChallenge(text) {
		return s.failure(started, "站点返回 Cloudflare/WAF 验证页（需要浏览器或更新 cf_clearance）")
	}
	return checkin.Result{Site: s.Name(), Status: checkin.StatusSuccess, Detail: "登录有效", At: started}
}

func (s *Site) checkin(ctx context.Context, started time.Time) checkin.Result {
	paths := []string{s.cfg.Signin.Path}
	discovered := false
	tried := make(map[string]bool)

	for i := 0; i < len(paths) && len(tried) < 4; i++ {
		path := paths[i]
		if path == "" || tried[path] {
			continue
		}
		tried[path] = true

		p, err := s.signinRequest(ctx, path)
		if err != nil {
			// A transport error is worth reporting directly. 404/405 come back
			// as a page, not an error, and are handled below.
			return s.failure(started, "签到请求失败："+httpReason(err))
		}

		res, missing := s.judge(p)
		if res.OK() {
			return stamp(res, started)
		}
		if res.Status == checkin.StatusFailed && isTerminalFailure(res.Detail) {
			return stamp(res, started)
		}

		// Two-step sites first: the token lives on the page we just fetched.
		if !missing && s.cfg.Signin.TwoStep != nil {
			if out := s.twoStep(ctx, p, started); out != nil {
				if out.OK() || isTerminalFailure(out.Detail) {
					return *out
				}
				res = *out
			}
		}

		if form := findSigninForm(p.Text, path, s.cfg.Signin.SubmitText); form != nil {
			out := s.submitAndConfirm(ctx, form, started)
			if out.OK() || isTerminalFailure(out.Detail) {
				return out
			}
			res = out
		}

		if !missing {
			// The page was reachable but did not look like a result and had no
			// usable form. That is "we do not know", not a hard failure.
			if res.Status == checkin.StatusUncertain || res.Status == checkin.StatusFailed {
				return s.uncertain(started, res.Detail)
			}
			return stamp(res, started)
		}

		if discovered || i > 0 {
			continue
		}
		discovered = true
		paths = append(paths, s.discoverPaths(ctx)...)
		paths = append(paths, commonSigninPaths...)
	}

	if s.dryRun {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusSuccess, Detail: "dry-run：未提交签到", At: started}
	}
	return s.failure(started, "没找到可用的签到入口（已试过 "+strings.Join(keys(tried), "、")+"）")
}

func (s *Site) signinRequest(ctx context.Context, path string) (page, error) {
	values := url.Values{}
	for k, v := range s.cfg.Signin.Data {
		values.Set(k, v)
	}
	if s.cfg.Signin.Method == "POST" {
		return s.postForm(ctx, path, values)
	}
	return s.get(ctx, appendQuery(path, values))
}

func appendQuery(path string, values url.Values) string {
	if len(values) == 0 {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + values.Encode()
}

func (s *Site) submitAndConfirm(ctx context.Context, form *signinForm, started time.Time) checkin.Result {
	if s.dryRun {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusSuccess, Detail: "dry-run：未提交签到表单", At: started}
	}
	var posted page
	var err error
	if form.Method == "GET" {
		posted, err = s.get(ctx, appendQuery(form.Action, form.Values))
	} else {
		posted, err = s.postForm(ctx, form.Action, form.Values)
	}
	if err != nil {
		return s.failure(started, "提交签到表单失败："+httpReason(err))
	}
	res, _ := s.judge(posted)
	if res.OK() {
		res.Detail = withSuffix(res.Detail, "表单提交")
		return stamp(res, started)
	}
	if res.Status == checkin.StatusFailed && isTerminalFailure(res.Detail) {
		return stamp(res, started)
	}

	// Many NexusPHP themes render the button on GET and only update the page
	// after a redirect. Re-read the page once before calling it unknown.
	if s.verifyDelay > 0 {
		select {
		case <-ctx.Done():
			return s.failure(started, ctx.Err().Error())
		case <-time.After(s.verifyDelay):
		}
	}
	verifyPath := s.cfg.Signin.VerifyPath
	if verifyPath == "" {
		verifyPath = s.cfg.Signin.Path
	}
	if verified, err := s.get(ctx, verifyPath); err == nil {
		if vres, _ := s.judge(verified); vres.OK() {
			vres.Detail = withSuffix(vres.Detail, "回读确认")
			return stamp(vres, started)
		}
	}
	return s.uncertain(started, "提交后未看到签到成功标志："+res.Detail)
}

func (s *Site) twoStep(ctx context.Context, first page, started time.Time) *checkin.Result {
	ts := s.cfg.Signin.TwoStep
	if ts == nil || ts.Path == "" {
		return nil
	}
	data := url.Values{}
	for k, v := range ts.Data {
		data.Set(k, v)
	}
	for field, pattern := range ts.Tokens {
		re, err := regexp.Compile(pattern)
		if err != nil {
			out := s.failure(started, fmt.Sprintf("two_step token %s 正则无效：%v", field, err))
			return &out
		}
		m := re.FindStringSubmatch(first.Text)
		if m == nil {
			out := s.failure(started, fmt.Sprintf("two_step 未找到 %s", field))
			return &out
		}
		if len(m) > 1 {
			data.Set(field, m[1])
		} else {
			data.Set(field, m[0])
		}
	}

	var second page
	var err error
	switch ts.Method {
	case "GET":
		second, err = s.getWithHeaders(ctx, appendQuery(ts.Path, data), ts.Headers)
	default:
		second, err = s.postFormWithHeaders(ctx, ts.Path, data, ts.Headers)
	}
	if err != nil {
		out := s.failure(started, "two_step 请求失败："+httpReason(err))
		return &out
	}
	res, _ := s.judge(second)
	if res.OK() {
		res.Detail = withSuffix(res.Detail, "二段式")
		stamped := stamp(res, started)
		return &stamped
	}
	if res.Status == checkin.StatusFailed && isTerminalFailure(res.Detail) {
		stamped := stamp(res, started)
		return &stamped
	}
	out := s.uncertain(started, "二段式提交后未确认："+res.Detail)
	return &out
}

// judge classifies a page. missingPath is true for 404/405 so the caller can
// try a discovered sign-in endpoint.
func (s *Site) judge(p page) (checkin.Result, bool) {
	now := s.now()
	if p.Status == 404 || p.Status == 405 {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusFailed, Detail: fmt.Sprintf("签到入口返回 HTTP %d", p.Status), At: now}, true
	}
	text := visibleText(p.Text)
	if s.isHardChallenge(text) {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusFailed, Detail: "站点返回 Cloudflare/WAF 验证页（需要浏览器或更新 cf_clearance）", At: now}, false
	}
	if s.isMaintenance(text) {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusFailed, Detail: "站点维护中，稍后重试", At: now}, false
	}
	if s.isLoginPage(p, text) {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusFailed, Detail: "Cookie 已失效或未登录", At: now}, false
	}

	// Success wins over "already": many NexusPHP success pages also say
	// "已签到 N 天", and checking already first would misreport a fresh claim.
	if s.match(text, s.cfg.Signin.Success, defaultSuccessPattern) {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusSuccess, Detail: withReward("签到成功", s.reward(p.Text)), At: now}, false
	}
	if s.match(text, s.cfg.Signin.Already, defaultAlreadyPattern) {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusAlready, Detail: withReward("今日已签到", s.reward(p.Text)), At: now}, false
	}
	if m := jsonStateRe.FindStringSubmatch(text); m != nil {
		switch strings.ToLower(m[1]) {
		case "success", "ok", "true":
			return checkin.Result{Site: s.Name(), Status: checkin.StatusSuccess, Detail: withReward("签到成功", s.reward(p.Text)), At: now}, false
		case "false", "fail", "failed", "error":
			return checkin.Result{Site: s.Name(), Status: checkin.StatusFailed, Detail: "站点返回签到失败", At: now}, false
		}
	}
	if s.isTurnstileOrCaptcha(p.Text) {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusFailed, Detail: "页面要求验证码 / Turnstile（需要浏览器或人工处理）", At: now}, false
	}

	// Silent success: the sign-in entry disappears after a successful claim.
	if len(s.cfg.Signin.AbsentKeywords) > 0 {
		control := controlText(p.Text)
		absent := false
		for _, word := range s.cfg.Signin.AbsentKeywords {
			word = strings.TrimSpace(word)
			if word == "" {
				continue
			}
			if strings.Contains(text, word) || strings.Contains(control, word) {
				absent = false
				break
			}
			absent = true
		}
		if absent {
			return checkin.Result{Site: s.Name(), Status: checkin.StatusAlready, Detail: withReward("今日已签到", s.reward(p.Text)), At: now}, false
		}
	}

	// Fallback: a post-sign-in phrase is visible and no action button remains.
	if !hasSigninAction(p.Text) && regexp.MustCompile(ci(stateHintPattern)).MatchString(text) {
		return checkin.Result{Site: s.Name(), Status: checkin.StatusAlready, Detail: withReward("今日已签到", s.reward(p.Text)), At: now}, false
	}

	return checkin.Result{Site: s.Name(), Status: checkin.StatusUncertain, Detail: "未匹配到签到结果", At: now}, false
}

func (s *Site) discoverPaths(ctx context.Context) []string {
	p, err := s.get(ctx, DefaultIndexPath)
	if err != nil {
		return nil
	}
	target := discoverSigninTarget(p.Text)
	if target == "" {
		return nil
	}
	return []string{target}
}

func ci(pattern string) string {
	if strings.HasPrefix(pattern, "(?i)") {
		return pattern
	}
	return "(?i)" + pattern
}

func (s *Site) isLoginPage(p page, text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "logout") || strings.Contains(lower, "退出") || strings.Contains(lower, "登出") {
		return false
	}
	if p.FinalURL != nil {
		path := strings.ToLower(p.FinalURL.Path)
		if strings.Contains(path, "login") || strings.Contains(path, "takelogin") {
			return true
		}
	}
	pattern := s.cfg.Signin.LoginRequired
	if pattern == "" {
		pattern = defaultLoginPattern
	}
	re, err := regexp.Compile(ci(pattern))
	return err == nil && re.MatchString(text)
}

func (s *Site) isChallenge(text string) bool {
	return regexp.MustCompile(ci(challengePattern)).MatchString(text)
}

func (s *Site) isHardChallenge(text string) bool {
	return regexp.MustCompile(ci(hardChallengePattern)).MatchString(text)
}

func (s *Site) isMaintenance(text string) bool {
	return regexp.MustCompile(ci(maintenancePattern)).MatchString(text)
}

func (s *Site) isTurnstileOrCaptcha(html string) bool {
	return regexp.MustCompile(`(?i)(cf-turnstile|turnstile/v0|turnstile\.min\.js|turnstile\.render)`).MatchString(html) ||
		regexp.MustCompile(ci(captchaPattern)).MatchString(html)
}

func (s *Site) match(text, custom, fallback string) bool {
	pattern := custom
	if pattern == "" {
		pattern = fallback
	}
	re, err := regexp.Compile(ci(pattern))
	return err == nil && re.MatchString(text)
}

func (s *Site) reward(html string) string {
	if s.cfg.Signin.Bonus != "" {
		re, err := regexp.Compile(ci(s.cfg.Signin.Bonus))
		if err == nil {
			if m := re.FindStringSubmatch(visibleText(html)); m != nil {
				if len(m) > 1 {
					return "获得 " + strings.TrimSpace(m[1])
				}
				return strings.TrimSpace(m[0])
			}
		}
	}
	if m := regexp.MustCompile(ci(rewardPattern)).FindStringSubmatch(visibleText(html)); m != nil {
		return "获得 " + strings.TrimSpace(m[1]) + " " + strings.TrimSpace(m[2])
	}
	return ""
}

func (s *Site) failure(started time.Time, detail string) checkin.Result {
	return checkin.Result{Site: s.Name(), Status: checkin.StatusFailed, Detail: detail, At: started}
}

func (s *Site) uncertain(started time.Time, detail string) checkin.Result {
	return checkin.Result{
		Site:       s.Name(),
		Status:     checkin.StatusUncertain,
		Detail:     detail,
		At:         started,
		RetryAfter: started.Add(30 * time.Minute),
	}
}

func stamp(res checkin.Result, started time.Time) checkin.Result {
	if res.At.IsZero() {
		res.At = started
	}
	return res
}

func isTerminalFailure(detail string) bool {
	for _, marker := range []string{"Cookie 已失效", "Cloudflare", "WAF", "验证码", "站点维护中", "HTTP 401", "HTTP 403"} {
		if strings.Contains(detail, marker) {
			return true
		}
	}
	return false
}

func withReward(prefix, reward string) string {
	if reward == "" {
		return prefix
	}
	return prefix + "（" + reward + "）"
}

func withSuffix(s, suffix string) string {
	if s == "" {
		return suffix
	}
	return s + "（" + suffix + "）"
}

func httpReason(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- form discovery --------------------------------------------------------

type signinForm struct {
	Action string
	Method string
	Values url.Values
}

func findSigninForm(html, currentPath, submitText string) *signinForm {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	wanted := strings.TrimSpace(submitText)
	var found *signinForm
	doc.Find("form").EachWithBreak(func(_ int, form *goquery.Selection) bool {
		control, label := submitControl(form, wanted)
		if control == nil {
			return true
		}
		action, _ := form.Attr("action")
		action = strings.TrimSpace(action)
		if action == "" {
			action = currentPath
		}
		if !matchesSigninLabel(label, wanted) && !looksLikeSigninAction(action) {
			return true
		}
		values := url.Values{}
		form.Find("input, select, textarea").Each(func(_ int, input *goquery.Selection) {
			name, _ := input.Attr("name")
			name = strings.TrimSpace(name)
			if name == "" {
				return
			}
			kind, _ := input.Attr("type")
			kind = strings.ToLower(strings.TrimSpace(kind))
			switch kind {
			case "submit", "button", "image", "file", "reset", "password":
				return
			case "checkbox", "radio":
				if _, ok := input.Attr("checked"); !ok {
					return
				}
			}
			value, _ := input.Attr("value")
			values.Set(name, value)
		})
		if n, ok := control.Attr("name"); ok && strings.TrimSpace(n) != "" {
			v, _ := control.Attr("value")
			values.Set(strings.TrimSpace(n), v)
		}
		method, _ := form.Attr("method")
		method = strings.ToUpper(strings.TrimSpace(method))
		if method != "GET" {
			method = "POST"
		}
		found = &signinForm{Action: action, Method: method, Values: values}
		return false
	})
	return found
}

func submitControl(form *goquery.Selection, wanted string) (*goquery.Selection, string) {
	var first *goquery.Selection
	var firstLabel string
	var match *goquery.Selection
	var matchLabel string
	form.Find("input[type=submit], input[type=button], input[type=image], button").Each(func(_ int, s *goquery.Selection) {
		label := strings.TrimSpace(s.AttrOr("value", ""))
		if label == "" {
			label = strings.TrimSpace(s.Text())
		}
		if label == "" {
			label = strings.TrimSpace(s.AttrOr("title", ""))
		}
		if first == nil {
			first, firstLabel = s, label
		}
		if match == nil && matchesSigninLabel(label, wanted) {
			match, matchLabel = s, label
		}
	})
	if match != nil {
		return match, matchLabel
	}
	return first, firstLabel
}

func matchesSigninLabel(label, wanted string) bool {
	label = strings.TrimSpace(label)
	if wanted != "" {
		return strings.Contains(label, wanted)
	}
	lower := strings.ToLower(label)
	for _, word := range []string{"签到", "簽到", "打卡", "领取", "領取", "每日签到", "sign in", "check in", "attendance"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func looksLikeSigninAction(action string) bool {
	lower := strings.ToLower(action)
	for _, word := range []string{"attendance", "signin", "sign_in", "checkin", "check-in", "addbonus", "showup", "bakatest"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func discoverSigninTarget(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return ""
	}
	var target string
	doc.Find("a").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		href, _ := a.Attr("href")
		text := strings.TrimSpace(a.Text())
		if !matchesSigninLabel(text, "") && !looksLikeSigninAction(href) {
			return true
		}
		if href == "" || isNoiseLink(href) {
			return true
		}
		target = href
		return false
	})
	if target != "" {
		return target
	}
	// Some themes render the daily check-in as a form on the index page.
	doc.Find("form").EachWithBreak(func(_ int, form *goquery.Selection) bool {
		action, _ := form.Attr("action")
		_, label := submitControl(form, "")
		if !matchesSigninLabel(label, "") && !looksLikeSigninAction(action) {
			return true
		}
		if action == "" || isNoiseLink(action) {
			return true
		}
		target = action
		return false
	})
	return target
}

func isNoiseLink(href string) bool {
	lower := strings.ToLower(href)
	for _, word := range []string{"messages.php", "sendmessage", "logout", "userdetails", "javascript:", "mailto:"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func controlText(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return ""
	}
	var parts []string
	doc.Find("input, button, a").Each(func(_ int, s *goquery.Selection) {
		if v := strings.TrimSpace(s.AttrOr("value", "")); v != "" {
			parts = append(parts, v)
		}
		if t := strings.TrimSpace(s.Text()); t != "" {
			parts = append(parts, t)
		}
	})
	return normalizeSpace(strings.Join(parts, " "))
}

func hasSigninAction(html string) bool {
	return regexp.MustCompile(ci(signinActionPattern)).MatchString(controlText(html))
}
