package feishu

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Sunyin0818/forum-keeper/internal/v2ex"
)

// Realistic `text` values: V2EX sends an HTML fragment here, not a plain phrase.
const (
	replyText   = `<a href="/member/alice" target="_blank"><strong>alice</strong></a> 在 <a href="/t/555#reply28" class="topic-link">关于 xxx 的讨论</a> 里回复了你`
	mentionText = `<a href="/member/bob" target="_blank"><strong>bob</strong></a> 在 <a href="/t/555#reply9" class="topic-link">关于 xxx 的讨论</a> 里提到了你`
	thanksText  = `<a href="/member/carol" target="_blank"><strong>carol</strong></a> 在 <a href="/t/555#reply3" class="topic-link">关于 xxx 的讨论</a> 感谢了你的主题`
)

func TestKindOf(t *testing.T) {
	cases := map[string]string{
		"回复了你的主题":        KindReply,
		"在回复中提到了你":       KindMention,
		"感谢了你的主题":        KindThanks,
		"关注了你":           KindOther,
		"something else": KindOther,
		// The real payloads are HTML fragments.
		replyText:   KindReply,
		mentionText: KindMention,
		thanksText:  KindThanks,
	}
	for text, want := range cases {
		if got := KindOf(text); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestTopicTitleFromText(t *testing.T) {
	for _, text := range []string{replyText, mentionText, thanksText} {
		if got := TopicTitleFromText(text); got != "关于 xxx 的讨论" {
			t.Errorf("TopicTitleFromText = %q, want %q", got, "关于 xxx 的讨论")
		}
	}
	// No topic anchor (e.g. a thanks without one) must yield "", so the caller
	// falls back to the API instead of rendering an empty line.
	if got := TopicTitleFromText("<strong>x</strong> 感谢了你的主题"); got != "" {
		t.Fatalf("expected empty title, got %q", got)
	}
}

func TestActionLabelNeverLeaksHTML(t *testing.T) {
	for _, text := range []string{replyText, mentionText, thanksText, "<b>weird</b> thing", ""} {
		got := actionLabel(v2ex.Notification{Text: text})
		if strings.ContainsAny(got, "<>") {
			t.Errorf("actionLabel(%q) leaked markup: %q", text, got)
		}
	}
}

func TestBuildCardWithRealisticTextHasNoHTML(t *testing.T) {
	items := []v2ex.Notification{{
		ID:              1,
		Text:            replyText,
		Payload:         "/t/555#reply28",
		PayloadRendered: `@<a href="/member/Sunyin">Sunyin</a> 我也遇到过同样的问题`,
		Member:          v2ex.Member{Username: "alice"},
	}}

	// No titles map: the title must come out of the notification's own text.
	card := BuildCard(items, nil)
	if card.Header.Title.Content != "💬 V2EX · 回复了你" {
		t.Fatalf("unexpected title %q", card.Header.Title.Content)
	}

	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(b)
	for _, bad := range []string{"<a ", "</a>", "<strong>", "class=", "target="} {
		if strings.Contains(body, bad) {
			t.Fatalf("card leaked %q:\n%s", bad, body)
		}
	}
	if !strings.Contains(body, "关于 xxx 的讨论") {
		t.Fatalf("topic title was not extracted from text:\n%s", body)
	}
	if !strings.Contains(body, "@alice") {
		t.Fatalf("member missing:\n%s", body)
	}
}

func TestTopicIDAndSnippet(t *testing.T) {
	it := v2ex.Notification{
		Payload:         "/t/123456#reply7",
		PayloadRendered: `<a href="/member/alice">@alice</a> 我也遇到了同样的问题 &amp; 已经解决了`,
	}
	if got := TopicID(it); got != 123456 {
		t.Fatalf("TopicID = %d, want 123456", got)
	}
	got := Snippet(it)
	if !strings.Contains(got, "@alice") || !strings.Contains(got, "& 已经解决了") {
		t.Fatalf("Snippet = %q", got)
	}
	if strings.Contains(got, "<a") {
		t.Fatalf("Snippet still contains HTML: %q", got)
	}
}

func TestSnippetPrefersPlainPayload(t *testing.T) {
	it := v2ex.Notification{
		Payload:         "@Sunyin 按的时候屏幕是不是会闪一下？",
		PayloadRendered: `@<a href="/member/Sunyin">Sunyin</a> 按的时候屏幕是不是会闪一下？`,
	}
	got := Snippet(it)
	if got != "@Sunyin 按的时候屏幕是不是会闪一下？" {
		t.Fatalf("Snippet = %q; want the plain payload without the stray space", got)
	}

	// A payload that is just a path must fall back to the rendered body.
	it2 := v2ex.Notification{Payload: "/t/123#reply1", PayloadRendered: `@<a href="/member/x">x</a> 正文`}
	if got := Snippet(it2); got != "@ x 正文" {
		t.Fatalf("Snippet fallback = %q", got)
	}
}

func TestTopicIDFromFullURL(t *testing.T) {
	it := v2ex.Notification{Payload: "https://www.v2ex.com/t/999"}
	if got := TopicID(it); got != 999 {
		t.Fatalf("TopicID = %d, want 999", got)
	}
	if got := TopicID(v2ex.Notification{Text: "回复了你的主题"}); got != 0 {
		t.Fatalf("TopicID without link = %d, want 0", got)
	}
}

func TestBuildCardSingle(t *testing.T) {
	items := []v2ex.Notification{{
		ID:              1,
		Text:            replyText,
		Payload:         "/t/555#reply1",
		PayloadRendered: "内容内容",
		Member:          v2ex.Member{Username: "somebody"},
	}}

	card := BuildCard(items, map[int]string{555: "关于 xxx 的讨论"})
	if card.Header == nil {
		t.Fatal("missing header")
	}
	if card.Header.Title.Content != "💬 V2EX · 回复了你" {
		t.Fatalf("unexpected title %q", card.Header.Title.Content)
	}
	if card.Header.Template != "blue" {
		t.Fatalf("unexpected template %q", card.Header.Template)
	}
	if len(card.Elements) != 2 {
		t.Fatalf("expected div + note, got %d elements", len(card.Elements))
	}

	div, ok := card.Elements[0].(*DivElement)
	if !ok {
		t.Fatalf("first element is %T, want *DivElement", card.Elements[0])
	}
	if div.Text == nil || !strings.Contains(div.Text.Content, "@somebody") {
		t.Fatalf("unexpected div content: %+v", div.Text)
	}
	if !strings.Contains(div.Text.Content, "关于 xxx 的讨论") {
		t.Fatalf("topic title missing: %q", div.Text.Content)
	}
	if div.Extra == nil || div.Extra.URL != "https://www.v2ex.com/t/555" {
		t.Fatalf("unexpected button: %+v", div.Extra)
	}
}

func TestBuildCardAggregatesAndTruncates(t *testing.T) {
	items := make([]v2ex.Notification, 0, 12)
	for i := 0; i < 12; i++ {
		items = append(items, v2ex.Notification{
			ID:     i + 1,
			Text:   replyText,
			Member: v2ex.Member{Username: "u"},
		})
	}
	card := BuildCard(items, nil)
	if card.Header.Title.Content != "💬 V2EX · 12 条回复" {
		t.Fatalf("unexpected title %q", card.Header.Title.Content)
	}

	// maxCardItems divs + (maxCardItems-1) hrs + 1 note
	wantElements := maxCardItems + (maxCardItems - 1) + 1
	if len(card.Elements) != wantElements {
		t.Fatalf("expected %d elements, got %d", wantElements, len(card.Elements))
	}
	note, ok := card.Elements[len(card.Elements)-1].(*NoteElement)
	if !ok {
		t.Fatalf("last element is %T, want *NoteElement", card.Elements[len(card.Elements)-1])
	}
	if !strings.Contains(note.Elements[0].Content, "还有 2 条未显示") {
		t.Fatalf("unexpected note %q", note.Elements[0].Content)
	}
}

func TestCardMarshalsToValidJSON(t *testing.T) {
	card := BuildCard([]v2ex.Notification{{
		ID:     1,
		Text:   mentionText,
		Member: v2ex.Member{Username: "bob"},
	}}, nil)
	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded["header"].(map[string]any)["template"] != "orange" {
		t.Fatalf("unexpected card JSON: %s", b)
	}
}

func TestBuildCheckinCard(t *testing.T) {
	at := time.Date(2026, 10, 2, 6, 0, 5, 0, time.Local)
	entries := []CheckinEntry{
		{Site: "V2EX", OK: true, Detail: "获得 18 铜币"},
		{Site: "2libra", OK: false, Detail: "Cookie 已失效（HTTP 401）"},
	}

	card := BuildCheckinCard(entries, at)
	if card.Header == nil || card.Header.Template != "orange" {
		t.Fatalf("unexpected header: %+v", card.Header)
	}
	if !strings.Contains(card.Header.Title.Content, "1/2") {
		t.Fatalf("unexpected title %q", card.Header.Title.Content)
	}

	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(b)
	for _, want := range []string{"V2EX", "获得 18 铜币", "2libra", "HTTP 401", `"tag":"column_set"`, "站点", "结果", "签到时间 2026-10-02 06:00:05"} {
		if !strings.Contains(body, want) {
			t.Errorf("card missing %q:\n%s", want, body)
		}
	}
}

func TestBuildCheckinCardAllSuccess(t *testing.T) {
	entries := []CheckinEntry{
		{Site: "V2EX", OK: true, Detail: "今日已签过"},
		{Site: "2libra", OK: true, Detail: "签到成功"},
	}
	card := BuildCheckinCard(entries, time.Now())
	if card.Header.Template != "green" || !strings.Contains(card.Header.Title.Content, "全部成功") {
		t.Fatalf("unexpected header: %+v", card.Header)
	}
}

func TestBuildCheckinCardSingleSite(t *testing.T) {
	card := BuildCheckinCard([]CheckinEntry{{Site: "V2EX", OK: true, Detail: ""}}, time.Now())
	if card.Header.Title.Content != "✅ 每日签到 · V2EX" {
		t.Fatalf("unexpected title %q", card.Header.Title.Content)
	}
	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), "无详情") {
		t.Fatalf("empty detail should fall back: %s", b)
	}
}

func TestBuildStartupCard(t *testing.T) {
	at := time.Date(2026, 9, 29, 15, 30, 0, 0, time.Local)
	card := BuildStartupCard([]string{"版本: 1.2.3", "账号: @tester (id 1)"}, at)

	if card.Header == nil || card.Header.Template != "green" {
		t.Fatalf("unexpected header: %+v", card.Header)
	}
	if card.Header.Title.Content != "🟢 forum-keeper 已启动" {
		t.Fatalf("unexpected title %q", card.Header.Title.Content)
	}
	if len(card.Elements) != 2 {
		t.Fatalf("expected body + note, got %d elements", len(card.Elements))
	}
	div, ok := card.Elements[0].(*DivElement)
	if !ok {
		t.Fatalf("first element is %T", card.Elements[0])
	}
	// plain_text keeps paths and usernames safe from lark_md parsing.
	if div.Text.Tag != "plain_text" {
		t.Fatalf("startup body should be plain_text, got %q", div.Text.Tag)
	}
	if !strings.Contains(div.Text.Content, "版本: 1.2.3") || !strings.Contains(div.Text.Content, "@tester") {
		t.Fatalf("unexpected body %q", div.Text.Content)
	}

	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), "启动时间 2026-09-29 15:30:00") {
		t.Fatalf("unexpected card JSON: %s", b)
	}
}

func TestBuildSiteMessagesCard(t *testing.T) {
	messages := []SiteMessage{{
		Site:  "HDSky",
		Title: "账号安全提醒",
		Date:  "2026-10-23 09:00:00",
		Body:  "第一行\n第二行 <b>不是标签</b>",
		URL:   "https://hdsky.me/messages.php?action=viewmessage&id=42",
	}}
	card := BuildSiteMessagesCard(messages, time.Now())
	if card.Header == nil || !strings.Contains(card.Header.Title.Content, "HDSky") {
		t.Fatalf("header = %+v", card.Header)
	}
	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(b)
	if !strings.Contains(body, "账号安全提醒") || !strings.Contains(body, "第一行") {
		t.Fatalf("message missing: %s", body)
	}
	// The body is plain_text; angle brackets are data, not markup.
	if !strings.Contains(body, "不是标签") {
		t.Fatalf("body missing: %s", body)
	}
	if strings.Contains(body, `"tag":"lark_md"`) {
		t.Fatalf("message body must not use lark_md: %s", body)
	}
}

func TestCleanCheckinDetail(t *testing.T) {
	cases := map[string]string{
		`请求失败：Get "https://www.v2ex.com/mission/daily": dial tcp [2a03:2880::1]:443: i/o timeout`: "请求失败：网络超时",
		`签到请求失败：dial tcp 1.2.3.4:443: i/o timeout`:                                                "签到请求失败：网络超时",
		`dial tcp 1.2.3.4:443: connect: connection refused`:                                       "连接被拒绝",
		`dial tcp: lookup example.com: no such host`:                                              "域名解析失败",
		`Cookie 已失效或无权限（HTTP 401）`:                                                                "Cookie 已失效或无权限（HTTP 401）",
	}
	for in, want := range cases {
		if got := cleanCheckinDetail(in); got != want {
			t.Errorf("cleanCheckinDetail(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanCheckinDetail(strings.Repeat("长", 200)); len([]rune(got)) > 101 {
		t.Fatalf("detail was not truncated: %d runes", len([]rune(got)))
	}
}

func TestBuildCheckinCardCompact(t *testing.T) {
	entries := []CheckinEntry{
		{Site: "V2EX", OK: false, Detail: `请求失败：Get "https://www.v2ex.com/mission/daily": dial tcp [2a03:2880:f10d:183:face:b00c:0:25de]:443: i/o timeout`},
		{Site: "学校", OK: true, Detail: "今日已签到"},
		{Site: "CarPT", OK: true, Detail: "签到成功（获得 10 魔力值）"},
	}
	card := BuildCheckinCard(entries, time.Now())
	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(b)
	if strings.Contains(body, "https://") || strings.Contains(body, "2a03:") {
		t.Fatalf("raw transport error leaked into card:\n%s", body)
	}
	for _, want := range []string{"V2EX", "请求失败：网络超时", "学校", "今日已签到", "CarPT", "签到成功"} {
		if !strings.Contains(body, want) {
			t.Fatalf("card missing %q:\n%s", want, body)
		}
	}
	if strings.Count(body, `"tag":"column_set"`) < 4 {
		t.Fatalf("expected header + one row per site:\n%s", body)
	}
	if !strings.Contains(body, "签到成功（获得 10 魔力值）") {
		t.Fatalf("full detail was clipped:\n%s", body)
	}
}

// A reply's text is written by another V2EX user. lark_md would render an
// injected link or <at id=all> inside the group card, so notifications must be
// plain_text.
func TestBuildCardRendersNotificationAsPlainText(t *testing.T) {
	it := v2ex.Notification{
		ID:      1,
		Text:    replyText,
		Member:  v2ex.Member{Username: "alice"},
		Payload: "[点我](https://evil.example) <at id=all></at> **加粗**",
	}
	card := BuildCard([]v2ex.Notification{it}, nil)
	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(b)
	if strings.Contains(body, `"tag":"lark_md"`) {
		t.Fatalf("notification card must not use lark_md: %s", body)
	}
	if !strings.Contains(body, "[点我](https://evil.example)") {
		t.Fatalf("plain text content should be preserved verbatim: %s", body)
	}
	el, ok := card.Elements[0].(*DivElement)
	if !ok {
		t.Fatalf("first element is %T, want *DivElement", card.Elements[0])
	}
	if el.Text.Tag != "plain_text" {
		t.Fatalf("notification body is %q, want plain_text", el.Text.Tag)
	}
}
