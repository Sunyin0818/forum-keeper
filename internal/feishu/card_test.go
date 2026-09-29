package feishu

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Sunyin0818/v2ex-notifier/internal/v2ex"
)

func TestKindOf(t *testing.T) {
	cases := map[string]string{
		"回复了你的主题":        KindReply,
		"在回复中提到了你":       KindMention,
		"感谢了你的主题":        KindThanks,
		"关注了你":           KindOther,
		"something else": KindOther,
	}
	for text, want := range cases {
		if got := KindOf(text); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", text, got, want)
		}
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
		Text:            "回复了你的主题",
		Payload:         "/t/555#reply1",
		PayloadRendered: "内容内容",
		Member:          v2ex.Member{Username: "somebody"},
	}}

	card := BuildCard(items, map[int]string{555: "关于 xxx 的讨论"})
	if card.Header == nil {
		t.Fatal("missing header")
	}
	if card.Header.Title.Content != "💬 V2EX · 回复了你的主题" {
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
			Text:   "回复了你的主题",
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
		Text:   "在回复中提到了你",
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

func TestBuildStartupCard(t *testing.T) {
	at := time.Date(2026, 9, 29, 15, 30, 0, 0, time.Local)
	card := BuildStartupCard([]string{"版本: 1.2.3", "账号: @tester (id 1)"}, at)

	if card.Header == nil || card.Header.Template != "green" {
		t.Fatalf("unexpected header: %+v", card.Header)
	}
	if card.Header.Title.Content != "🟢 V2EX 通知服务已启动" {
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
