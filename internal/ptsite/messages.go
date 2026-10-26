package ptsite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var (
	unreadIDRe = regexp.MustCompile(`(?i)(?:viewmessage[^"']*?[?&](?:amp;)?id=|messages\.php\?action=viewmessage&(?:amp;)?id=)(\d+)`)
	dateRe     = regexp.MustCompile(`\d{4}[-/.]\d{1,2}[-/.]\d{1,2}(?:[ T]\d{1,2}:\d{2}(?::\d{2})?)?`)
)

// Message is one tracker inbox entry. Body is filled only in "full" mode.
type Message struct {
	Site   string `json:"site"`
	Host   string `json:"host"`
	ID     string `json:"id"`
	Title  string `json:"title"`
	Date   string `json:"date"`
	Body   string `json:"body,omitempty"`
	URL    string `json:"url"`
	Digest string `json:"digest,omitempty"`
}

// Unread returns the unread inbox rows without fetching bodies. Bodies are
// fetched separately so a message already seen locally is never opened (and
// therefore never marked read on NexusPHP) again.
func (s *Site) Unread(ctx context.Context) ([]Message, error) {
	if !s.cfg.Messages.enabled() {
		return nil, nil
	}
	if n, known, err := s.unreadCount(ctx); err == nil && known && n == 0 {
		return nil, nil
	}

	var out []Message
	seen := make(map[string]bool)
	var firstErr error

	for _, box := range []string{s.cfg.Messages.InboxPath, s.cfg.Messages.SystemPath} {
		if strings.TrimSpace(box) == "" {
			continue
		}
		msgs, err := s.readBox(ctx, box)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, m := range msgs {
			key := m.ID
			if key == "" {
				key = m.URL
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, m)
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// FetchMessage fills the title/date/body of a listed message. For NexusPHP
// this opens the detail page, which marks the message read on the site.
func (s *Site) FetchMessage(ctx context.Context, msg Message) (Message, error) {
	if strings.TrimSpace(msg.URL) == "" {
		return msg, fmt.Errorf("ptsite: message %s has no URL", msg.ID)
	}
	p, err := s.get(ctx, msg.URL)
	if err != nil {
		return msg, err
	}
	if p.Status >= 400 {
		return msg, fmt.Errorf("ptsite: message %s returned HTTP %d", msg.ID, p.Status)
	}
	text := visibleText(p.Text)
	if s.isChallenge(text) {
		return msg, fmt.Errorf("ptsite: message %s hit a challenge page", msg.ID)
	}
	if s.isMaintenance(text) {
		return msg, fmt.Errorf("ptsite: message %s is unavailable (site maintenance)", msg.ID)
	}
	if s.isLoginPage(p, text) {
		return msg, fmt.Errorf("ptsite: message %s requires login", msg.ID)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(p.Text))
	if err != nil {
		return msg, fmt.Errorf("ptsite: parse message %s: %w", msg.ID, err)
	}
	sel := s.cfg.Messages.Selectors
	title := ""
	if sel != nil && sel.Title != "" {
		title = strings.TrimSpace(doc.Find(sel.Title).First().Text())
	}
	title = firstNonEmpty(
		title,
		strings.TrimSpace(doc.Find("h1").First().Text()),
		strings.TrimSpace(doc.Find(".layui-card-header span").First().Text()),
		msg.Title,
	)
	if title == "" {
		title = "（无标题）"
	}
	date := firstNonEmpty(msg.Date, dateRe.FindString(text))
	if sel != nil && sel.Date != "" {
		date = firstNonEmpty(strings.TrimSpace(doc.Find(sel.Date).First().Text()), date)
	}
	body := s.messageBody(doc)
	if body == "" {
		body = visibleText(p.Text)
	}
	msg.Title = truncateRunes(title, 200)
	msg.Date = date
	msg.Body = truncateRunes(body, 4000)
	if msg.ID == "" {
		msg.Digest = digestOf(msg)
	}
	return msg, nil
}

func (s *Site) readBox(ctx context.Context, path string) ([]Message, error) {
	var out []Message
	next := path
	for page := 0; page < s.cfg.Messages.MaxPages && next != ""; page++ {
		p, err := s.get(ctx, next)
		if err != nil {
			return out, err
		}
		if p.Status == 404 || p.Status == 405 {
			// The system box (box=-2) does not exist on every theme; an empty
			// system inbox is not an error.
			if strings.Contains(path, "box=-2") {
				return out, nil
			}
			return out, fmt.Errorf("ptsite: inbox %s returned HTTP %d", next, p.Status)
		}
		text := visibleText(p.Text)
		if s.isChallenge(text) {
			return out, fmt.Errorf("ptsite: inbox hit a challenge page")
		}
		if s.isMaintenance(text) {
			return out, fmt.Errorf("ptsite: inbox is unavailable (site maintenance)")
		}
		if s.isLoginPage(p, text) {
			return out, fmt.Errorf("ptsite: inbox requires login")
		}
		msgs, nextPage := s.parseMessageList(p.Text, next)
		out = append(out, msgs...)
		if nextPage == "" || nextPage == next {
			break
		}
		next = nextPage
	}
	return out, nil
}

func (s *Site) parseMessageList(html, currentPath string) ([]Message, string) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, ""
	}
	sel := s.cfg.Messages.Selectors
	linkSel := "a[href]"
	if sel != nil && sel.Link != "" {
		linkSel = sel.Link
	}
	var out []Message
	seen := make(map[string]bool)
	doc.Find(linkSel).Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		if href == "" {
			return
		}
		if (sel == nil || sel.Link == "") && !strings.Contains(strings.ToLower(href), "viewmessage") {
			return
		}
		row := a.Closest("tr")
		if row.Length() == 0 {
			row = a.Closest("div")
		}
		if row.Length() == 0 {
			return
		}
		if !isUnreadRow(row, sel) {
			return
		}
		id := messageID(href)
		key := id
		if key == "" {
			key = href
		}
		if seen[key] {
			return
		}
		seen[key] = true
		title := strings.TrimSpace(a.Text())
		if sel != nil && sel.Title != "" {
			title = strings.TrimSpace(a.Find(sel.Title).First().Text())
		}
		if title == "" {
			title = strings.TrimSpace(a.AttrOr("title", ""))
		}
		date := dateRe.FindString(normalizeSpace(row.Text()))
		if sel != nil && sel.Date != "" {
			date = strings.TrimSpace(row.Find(sel.Date).First().Text())
		}
		msg := Message{
			Site:  s.Name(),
			Host:  s.Host(),
			ID:    id,
			Title: title,
			Date:  date,
			URL:   absoluteString(s.base, currentPath, href),
		}
		if msg.ID == "" {
			msg.Digest = digestOf(msg)
		}
		out = append(out, msg)
	})

	var nextPage string
	doc.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		text := strings.TrimSpace(a.Text())
		if strings.Contains(text, "下一页") || strings.Contains(text, "下一頁") {
			href, _ := a.Attr("href")
			if href != "" {
				nextPage = absoluteString(s.base, currentPath, href)
			}
			return false
		}
		return true
	})
	return out, nextPage
}

func (s *Site) unreadCount(ctx context.Context) (int, bool, error) {
	p, err := s.get(ctx, DefaultIndexPath)
	if err != nil {
		return 0, false, err
	}
	text := visibleText(p.Text)
	if s.isChallenge(text) {
		return 0, false, fmt.Errorf("index hit a challenge page")
	}
	if s.isMaintenance(text) {
		return 0, false, fmt.Errorf("index is unavailable (site maintenance)")
	}
	if s.isLoginPage(p, text) {
		return 0, false, fmt.Errorf("index requires login")
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(p.Text))
	if err != nil {
		return 0, false, err
	}
	found := false
	numbered := false
	count := 0
	doc.Find("a[href*='messages.php']").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		found = true
		parent := a.Parent()
		label := a.Text() + " " + parent.Text()
		for _, m := range regexp.MustCompile(`\d+`).FindAllString(label, -1) {
			if n, err := parseInt(m); err == nil {
				count = n
				numbered = true
				return false
			}
		}
		return true
	})
	// A link without a number (e.g. an unread icon) means "there is unread
	// mail, but we do not know how many"; force the caller to read the list.
	return count, found && numbered, nil
}

func isUnreadRow(row *goquery.Selection, sel *MessageSelectors) bool {
	if sel != nil && sel.UnreadRow != "" {
		return row.Is(sel.UnreadRow) || row.Find(sel.UnreadRow).Length() > 0
	}
	if row.Find("img.unreadpm").Length() > 0 ||
		row.Find("img[alt='Unread'], img[title*='未读'], img[title*='Unread']").Length() > 0 {
		return true
	}
	if row.Find("img[alt='Read'], img[title='Read'], img[title='已读']").Length() > 0 {
		return false
	}
	class, _ := row.Attr("class")
	if strings.Contains(strings.ToLower(class), "unread") {
		return true
	}
	return strings.Contains(normalizeSpace(row.Text()), "未读")
}

func (s *Site) messageBody(doc *goquery.Document) string {
	if sel := s.cfg.Messages.Selectors; sel != nil && sel.Body != "" {
		if body := doc.Find(sel.Body).First(); body.Length() > 0 {
			return nodeText(body)
		}
	}
	// layui-based themes.
	if body := doc.Find(".layui-card-body").First(); body.Length() > 0 {
		return nodeText(body)
	}
	// Standard NexusPHP: the detail table follows the <h1>, but some themes
	// put the inbox navigation table in between. Pick the table that actually
	// holds the message header instead of the first one after the heading.
	table, ok := messageTable(doc)
	if !ok {
		return ""
	}
	title := normalizeSpace(doc.Find("h1").First().Text())
	var parts []string
	table.Find("tr").Each(func(i int, row *goquery.Selection) {
		// Row 0 is the "自 / 日期" header, row 1 is sender/date.
		if i < 2 || isToolbarRow(row) {
			return
		}
		text := cleanBlock(nodeText(row))
		// The subject is repeated under the <h1>; the body starts after it.
		if text == "" || normalizeSpace(text) == title {
			return
		}
		parts = append(parts, text)
	})
	return strings.Join(parts, "\n")
}

func messageTable(doc *goquery.Document) (*goquery.Selection, bool) {
	// The message table carries the "自 / 日期" header cells. Prefer it over
	// any navigation table elsewhere on the page.
	var found *goquery.Selection
	doc.Find("table").EachWithBreak(func(_ int, table *goquery.Selection) bool {
		if table.Find("td.colhead").Length() >= 2 {
			found = table
			return false
		}
		return true
	})
	if found != nil {
		return found, true
	}
	// Themes without colhead: fall back to the first substantial table after
	// the heading.
	doc.Find("h1").First().NextAllFiltered("table").EachWithBreak(func(_ int, table *goquery.Selection) bool {
		if table.Find("tr").Length() >= 3 {
			found = table
			return false
		}
		return true
	})
	return found, found != nil
}

// isToolbarRow reports whether a row is the delete/reply/forward controls.
// Their labels are links, not message content, and must never reach a card.
func isToolbarRow(row *goquery.Selection) bool {
	return row.Find("form input[name='action'], a[href*='deletemessage'], a[href*='replymessage'], a[href*='forwardmessage']").Length() > 0
}

func nodeText(sel *goquery.Selection) string {
	raw, err := sel.Html()
	if err != nil {
		return cleanBlock(sel.Text())
	}
	return cleanBlock(htmlToText(raw))
}

func cleanBlock(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func absoluteString(base *url.URL, current, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		if cur, err := url.Parse(current); err == nil && cur.IsAbs() {
			u = cur.ResolveReference(u)
		} else {
			u = base.ResolveReference(u)
		}
	}
	return u.String()
}

func messageID(href string) string {
	if m := unreadIDRe.FindStringSubmatch(href); m != nil {
		return m[1]
	}
	if m := regexp.MustCompile(`[?&](?:amp;)?id=(\d+)`).FindStringSubmatch(href); m != nil {
		return m[1]
	}
	return ""
}

func digestOf(m Message) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{m.Host, m.Title, m.Date, m.Body, m.URL}, "|")))
	return hex.EncodeToString(sum[:8])
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}

// Key returns the per-message state key. Messages without a site-provided ID
// fall back to a content digest, so a re-rendered list row does not look new.
func (m Message) Key() string {
	if m.ID != "" {
		return m.ID
	}
	if m.Digest != "" {
		return "d:" + m.Digest
	}
	return ""
}
