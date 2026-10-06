package feishu

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/Sunyin0818/v2ex-notifier/internal/v2ex"
)

// maxCardItems caps how many notifications are rendered in a single card.
const maxCardItems = 10

// Notification kinds, also used by V2EX_FILTER_TYPES.
const (
	KindReply   = "reply"
	KindMention = "mention"
	KindThanks  = "thanks"
	KindOther   = "other"
)

// KindOf classifies a notification by its human readable text. The text field
// is an HTML fragment (e.g. `<a ...>alice</a> 在 <a ...>标题</a> 里回复了你`),
// so it is stripped before matching.
func KindOf(text string) string {
	t := StripHTML(text)
	switch {
	case strings.Contains(t, "感谢"):
		return KindThanks
	case strings.Contains(t, "提到"):
		return KindMention
	case strings.Contains(t, "回复"):
		return KindReply
	default:
		return KindOther
	}
}

type kindStyle struct {
	emoji    string
	noun     string // aggregate title: "12 条回复"
	action   string // single title / body line: "回复了你"
	template string
}

func styleFor(kind string) kindStyle {
	switch kind {
	case KindReply:
		return kindStyle{emoji: "💬", noun: "回复", action: "回复了你", template: "blue"}
	case KindMention:
		return kindStyle{emoji: "📣", noun: "提及", action: "在回复中提到了你", template: "orange"}
	case KindThanks:
		return kindStyle{emoji: "🙏", noun: "感谢", action: "感谢了你的主题", template: "green"}
	default:
		return kindStyle{emoji: "🔔", noun: "提醒", action: "", template: "grey"}
	}
}

// actionLabel is a short description of what happened. It never contains the
// raw `text` HTML: unknown kinds fall back to the stripped text.
func actionLabel(it v2ex.Notification) string {
	if a := styleFor(KindOf(it.Text)).action; a != "" {
		return a
	}
	if s := StripHTML(it.Text); s != "" {
		return truncate(s, 60)
	}
	return "有新的提醒"
}

// --- card schema -----------------------------------------------------------

type Card struct {
	Config   CardConfig  `json:"config"`
	Header   *CardHeader `json:"header,omitempty"`
	Elements []any       `json:"elements"`
}

type CardConfig struct {
	WideScreenMode bool `json:"wide_screen_mode"`
}

type CardHeader struct {
	Template string    `json:"template"`
	Title    *TextNode `json:"title"`
}

type TextNode struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type DivElement struct {
	Tag   string    `json:"tag"`
	Text  *TextNode `json:"text,omitempty"`
	Extra *Button   `json:"extra,omitempty"`
}

type ActionElement struct {
	Tag     string   `json:"tag"`
	Actions []Button `json:"actions"`
}

type Button struct {
	Tag  string    `json:"tag"`
	Text *TextNode `json:"text"`
	Type string    `json:"type,omitempty"`
	URL  string    `json:"url,omitempty"`
}

type NoteElement struct {
	Tag      string     `json:"tag"`
	Elements []TextNode `json:"elements"`
}

// CheckinEntry is one site's result, as rendered into the daily check-in card.
// It deliberately carries no markup: Detail is shown as plain_text.
type CheckinEntry struct {
	Site   string
	OK     bool
	Detail string
}

func div(content string) *DivElement {
	return &DivElement{
		Tag:  "div",
		Text: &TextNode{Tag: "lark_md", Content: content},
	}
}

func hr() map[string]string {
	return map[string]string{"tag": "hr"}
}

func plain(s string) *TextNode {
	return &TextNode{Tag: "plain_text", Content: s}
}

// BuildCard renders one aggregated interactive card for the given
// notifications. titles maps topic IDs to their human readable titles; a
// missing entry simply omits the title line.
func BuildCard(items []v2ex.Notification, titles map[int]string) Card {
	card := Card{Config: CardConfig{WideScreenMode: true}}

	sameKind := true
	first := KindOf(items[0].Text)
	for _, it := range items[1:] {
		if KindOf(it.Text) != first {
			sameKind = false
			break
		}
	}

	style := styleFor(first)
	template := "blue"
	title := fmt.Sprintf("🔔 V2EX · %d 条新提醒", len(items))
	if len(items) == 1 {
		title = fmt.Sprintf("%s V2EX · %s", style.emoji, actionLabel(items[0]))
	} else if sameKind {
		title = fmt.Sprintf("%s V2EX · %d 条%s", style.emoji, len(items), style.noun)
	}
	if sameKind {
		template = style.template
	}

	card.Header = &CardHeader{Template: template, Title: plain(title)}

	shown := items
	truncated := 0
	if len(shown) > maxCardItems {
		truncated = len(shown) - maxCardItems
		shown = shown[:maxCardItems]
	}

	for i, it := range shown {
		if i > 0 {
			card.Elements = append(card.Elements, hr())
		}
		card.Elements = append(card.Elements, notificationElement(it, titles))
	}

	note := fmt.Sprintf("来自 V2EX · %d 条", len(items))
	if truncated > 0 {
		note = fmt.Sprintf("来自 V2EX · 还有 %d 条未显示", truncated)
	}
	card.Elements = append(card.Elements, &NoteElement{
		Tag:      "note",
		Elements: []TextNode{{Tag: "plain_text", Content: note}},
	})
	return card
}

// BuildStartupCard renders the "service started" card shown once when the
// service comes up.
func BuildStartupCard(lines []string, at time.Time) Card {
	card := Card{
		Config: CardConfig{WideScreenMode: true},
		Header: &CardHeader{
			Template: "green",
			Title:    plain("🟢 V2EX 通知服务已启动"),
		},
	}
	card.Elements = append(card.Elements, &DivElement{
		Tag: "div",
		// plain_text avoids having to escape paths and other values for lark_md.
		Text: &TextNode{Tag: "plain_text", Content: strings.Join(lines, "\n")},
	})
	card.Elements = append(card.Elements, &NoteElement{
		Tag:      "note",
		Elements: []TextNode{{Tag: "plain_text", Content: "启动时间 " + at.Local().Format("2006-01-02 15:04:05")}},
	})
	return card
}

func notificationElement(it v2ex.Notification, titles map[int]string) *DivElement {
	var sb strings.Builder
	user := it.Member.Username
	if user == "" {
		user = "someone"
	}
	sb.WriteString("**@")
	sb.WriteString(user)
	sb.WriteString("** ")
	sb.WriteString(actionLabel(it))

	topicID := TopicID(it)
	if topicID > 0 {
		// Prefer the title already embedded in the notification; fall back to
		// the one fetched via the API.
		title := TopicTitleFromText(it.Text)
		if title == "" {
			title = strings.TrimSpace(titles[topicID])
		}
		if title != "" {
			sb.WriteString("\n📄 ")
			sb.WriteString(escapeMarkdown(title))
		}
	}
	if s := Snippet(it); s != "" {
		sb.WriteString("\n> ")
		sb.WriteString(escapeMarkdown(s))
	}

	el := div(sb.String())
	if topicID > 0 {
		el.Extra = &Button{
			Tag:  "button",
			Text: plain("查看"),
			Type: "default",
			URL:  fmt.Sprintf("https://www.v2ex.com/t/%d", topicID),
		}
	}
	return el
}

// BuildCheckinCard renders the daily check-in summary.
//
// The body is plain_text on purpose: site details can contain URLs, angle
// brackets and asterisks, all of which lark_md would happily mangle.
func BuildCheckinCard(entries []CheckinEntry, at time.Time) Card {
	okCount := 0
	for _, e := range entries {
		if e.OK {
			okCount++
		}
	}

	template := "green"
	title := fmt.Sprintf("✅ 每日签到 · %d 个站点全部成功", len(entries))
	if okCount < len(entries) {
		template = "orange"
		title = fmt.Sprintf("⚠️ 每日签到 · %d/%d 成功", okCount, len(entries))
	}
	if len(entries) == 1 {
		icon := "✅"
		if !entries[0].OK {
			icon = "❌"
		}
		title = fmt.Sprintf("%s 每日签到 · %s", icon, entries[0].Site)
	}

	card := Card{
		Config: CardConfig{WideScreenMode: true},
		Header: &CardHeader{Template: template, Title: plain(title)},
	}

	var sb strings.Builder
	for i, e := range entries {
		if i > 0 {
			sb.WriteString("\n")
		}
		icon := "✅"
		if !e.OK {
			icon = "❌"
		}
		sb.WriteString(icon)
		sb.WriteString(" ")
		sb.WriteString(e.Site)
		sb.WriteString("\n")
		sb.WriteString("    └ ")
		sb.WriteString(firstLineOr(e.Detail, "无详情"))
	}

	card.Elements = append(card.Elements,
		&DivElement{Tag: "div", Text: &TextNode{Tag: "plain_text", Content: sb.String()}},
		&NoteElement{Tag: "note", Elements: []TextNode{{Tag: "plain_text", Content: "签到时间 " + at.Local().Format("2006-01-02 15:04:05")}}},
	)
	return card
}

// firstLineOr collapses whitespace and falls back when the detail is empty.
func firstLineOr(s, fallback string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return fallback
	}
	return s
}

// --- helpers ---------------------------------------------------------------

var (
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	topicRe = regexp.MustCompile(`/(?:t|topic|topics)/(\d+)`)
	linkRe  = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
)

// TopicTitleFromText extracts the topic title from a notification's `text`
// field, which embeds it as `<a ... class="topic-link">TITLE</a>`.
//
// V2EX already sends the title here, so this avoids a GET /topics/:id round
// trip (and its rate limit cost) for the common case.
func TopicTitleFromText(text string) string {
	for _, m := range linkRe.FindAllStringSubmatch(text, -1) {
		if !strings.Contains(m[1], "topic-link") {
			continue
		}
		if title := StripHTML(m[2]); title != "" {
			return title
		}
	}
	return ""
}

// TopicID extracts the topic ID referenced by a notification, or 0.
func TopicID(it v2ex.Notification) int {
	for _, s := range []string{it.Payload, it.PayloadRendered, it.Text} {
		if m := topicRe.FindStringSubmatch(s); m != nil {
			var id int
			if _, err := fmt.Sscanf(m[1], "%d", &id); err == nil && id > 0 {
				return id
			}
		}
	}
	return 0
}

// Snippet returns a plain-text excerpt of the notification body.
//
// `payload` is already plain text and reads best; `payload_rendered` is HTML
// whose tags would leave stray spaces (`@<a ...>Sunyin</a>` -> `@ Sunyin`).
// Prefer the former, unless it is just a link/path rather than content.
func Snippet(it v2ex.Notification) string {
	if p := normalizeText(it.Payload); p != "" && !looksLikeLink(p) {
		return truncate(p, 140)
	}
	if s := StripHTML(it.PayloadRendered); s != "" {
		return truncate(s, 140)
	}
	return ""
}

func looksLikeLink(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// StripHTML removes tags and collapses whitespace.
func StripHTML(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return normalizeText(tagRe.ReplaceAllString(s, " "))
}

// normalizeText unescapes entities and collapses all whitespace runs.
func normalizeText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// escapeMarkdown neutralises lark_md syntax that could break card rendering.
func escapeMarkdown(s string) string {
	r := strings.NewReplacer("**", "*", "__", "_", "~~", "~")
	return r.Replace(s)
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max])) + "…"
}
