package feishu

import (
	"context"
	"sync"

	"github.com/Sunyin0818/v2ex-notifier/internal/v2ex"
)

// TopicResolver fetches a topic by ID. Implemented by the V2EX client.
type TopicResolver interface {
	Topic(ctx context.Context, id int) (*v2ex.Topic, error)
}

// Bot turns notifications into Feishu messages. It implements the app's
// Notifier interface.
type Bot struct {
	client *Client
	topics TopicResolver

	mu     sync.Mutex
	titles map[int]string
}

// NewBot builds a bot. topics may be nil, in which case topic titles are
// simply omitted from cards.
func NewBot(client *Client, topics TopicResolver) *Bot {
	return &Bot{client: client, topics: topics, titles: make(map[int]string)}
}

// Notify pushes the given notifications as a single aggregated card.
func (b *Bot) Notify(ctx context.Context, items []v2ex.Notification) error {
	if len(items) == 0 {
		return nil
	}
	return b.client.SendCard(ctx, BuildCard(items, b.resolveTitles(ctx, items)))
}

// Alert sends an operational warning (token expired, proxy down, ...).
func (b *Bot) Alert(ctx context.Context, text string) error {
	return b.client.SendText(ctx, text)
}

// resolveTitles lazily looks up topic titles, tolerating failures: a missing
// title is not worth failing the whole notification.
func (b *Bot) resolveTitles(ctx context.Context, items []v2ex.Notification) map[int]string {
	if b.topics == nil {
		return nil
	}
	out := make(map[int]string, len(items))
	for _, it := range items {
		id := TopicID(it)
		if id <= 0 {
			continue
		}
		if t, ok := b.cached(id); ok {
			out[id] = t
			continue
		}
		topic, err := b.topics.Topic(ctx, id)
		if err != nil || topic == nil || topic.Title == "" {
			continue
		}
		b.store(id, topic.Title)
		out[id] = topic.Title
	}
	return out
}

func (b *Bot) cached(id int) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.titles[id]
	return t, ok
}

func (b *Bot) store(id int, title string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Bound the cache: titles never change meaningfully during a run.
	if len(b.titles) > 500 {
		b.titles = make(map[int]string)
	}
	b.titles[id] = title
}
