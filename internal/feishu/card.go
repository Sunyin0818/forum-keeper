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

// Notification kinds, also used by FILTER_TYPES.
const (
	KindReply   = "reply"
	KindMention = "mention"
	KindThanks  = "thanks"
	KindOther   = "other"
)

// KindOf classifies a notification by its human readable text.
func KindOf(text string) string {
	switch {
	case strings.Contains(text, "感谢"):
		return KindThanks
	case strings.Contains(text, "提到"):
		return KindMention
	case strings.Contains(text, "回复"):
		return KindReply
	default:
		return KindOther
	}
}

type kindStyle struct {
	emoji    string
	label    string
	noun     string
	template string
}

func styleFor(kind string) kindStyle {
	switch kind {
	case KindReply:
		return kindStyle{emoji: "💬", label: "回复了你", noun: "回复", template: "blue"}
	case KindMention:
		return kindStyle{emoji: "📣", label: "提到了你", noun: "提及", template: "orange"}
	case KindThanks:
		return kindStyle{emoji: "🙏", label: "感谢了你", noun: "感谢", template: "green"}
	default:
		return kindStyle{emoji: "🔔", label: "新提醒", noun: "提醒", template: "grey"}
	}
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
		label := items[0].Text
		if strings.TrimSpace(label) == "" {
			label = style.label
		}
		title = fmt.Sprintf("%s V2EX · %s", style.emoji, label)
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
	label := it.Text
	if strings.TrimSpace(label) == "" {
		label = styleFor(KindOf(it.Text)).label
	}
	sb.WriteString(label)

	topicID := TopicID(it)
	if topicID > 0 {
		if t := strings.TrimSpace(titles[topicID]); t != "" {
			sb.WriteString("\n📄 ")
			sb.WriteString(escapeMarkdown(t))
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

// --- helpers ---------------------------------------------------------------

var (
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	topicRe = regexp.MustCompile(`/(?:t|topic|topics)/(\d+)`)
)

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
func Snippet(it v2ex.Notification) string {
	for _, raw := range []string{it.PayloadRendered, it.Payload} {
		if s := StripHTML(raw); s != "" {
			return truncate(s, 140)
		}
	}
	return ""
}

// StripHTML removes tags and collapses whitespace.
func StripHTML(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
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
