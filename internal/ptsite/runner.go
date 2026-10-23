package ptsite

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Sunyin0818/forum-keeper/internal/checkin"
)

// CheckinState is the per-site, per-day sign-in record. It is persisted so a
// restart does not re-run a site that already succeeded, and so a failure is
// retried later instead of being marked done.
type CheckinState struct {
	Day      string    `json:"day"`
	Host     string    `json:"host"`
	Status   string    `json:"status"`
	Detail   string    `json:"detail"`
	Attempts int       `json:"attempts"`
	NextAt   time.Time `json:"next_at,omitempty"`
	Terminal bool      `json:"terminal"`
	At       time.Time `json:"at"`
}

// State is the persistence surface the runner needs. internal/store owns the
// bbolt implementation; tests can use an in-memory fake.
type State interface {
	LoadCheckin(day, host string) (CheckinState, bool, error)
	SaveCheckin(day, host string, st CheckinState) error
	AllCheckinTerminal(day string, hosts []string) (bool, error)

	FirstRunDone() (bool, error)
	SetFirstRunDone() error

	MessageSeen(host, id string) (bool, error)
	MarkMessage(host, id, digest string) error
	SavePending(msg Message) error
	PendingMessages() ([]Message, error)
	DeletePending(host, id string) error
}

// Runner drives sign-in and inbox checks for a set of sites.
type Runner struct {
	sites       []*Site
	state       State
	log         *slog.Logger
	now         func() time.Time
	maxAttempts int
	concurrency int
	firstRun    string
	dryRun      bool
}

func NewRunner(sites []*Site, state State, log *slog.Logger, firstRun string, maxAttempts, concurrency int, dryRun bool) *Runner {
	if log == nil {
		log = slog.Default()
	}
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 16 {
		concurrency = 16
	}
	if firstRun == "" {
		firstRun = FirstRunSkip
	}
	if state == nil {
		state = NewMemoryState()
	}
	return &Runner{
		sites:       sites,
		state:       state,
		log:         log,
		now:         time.Now,
		maxAttempts: maxAttempts,
		concurrency: concurrency,
		firstRun:    firstRun,
		dryRun:      dryRun,
	}
}

// SetNow is used by tests.
func (r *Runner) SetNow(now func() time.Time) { r.now = now }

// Sites returns the configured sites.
func (r *Runner) Sites() []*Site { return r.sites }

// RunDue signs in to every site that is not already terminal/pending today.
// It never aborts early: one broken site must not hide the others.
func (r *Runner) RunDue(ctx context.Context) []checkin.Result {
	now := r.now()
	day := now.Format("2006-01-02")

	var due []*Site
	for _, site := range r.sites {
		if site.cfg.Signin.Mode == "off" {
			continue
		}
		st, ok, err := r.state.LoadCheckin(day, site.Host())
		if err != nil {
			r.log.Warn("pt: load check-in state", "site", site.Name(), "err", err)
		}
		if ok {
			if st.Terminal {
				continue
			}
			if !st.NextAt.IsZero() && st.NextAt.After(now) {
				continue
			}
			if st.Attempts >= r.maxAttempts {
				// Tolerate a state written by an older run: do not keep the
				// site "due forever" after its retry budget is spent.
				st.Terminal = true
				st.NextAt = time.Time{}
				if err := r.state.SaveCheckin(day, site.Host(), st); err != nil {
					r.log.Warn("pt: save exhausted check-in state", "site", site.Name(), "err", err)
				}
				continue
			}
		}
		due = append(due, site)
	}
	if len(due) == 0 {
		return nil
	}

	results := make([]checkin.Result, len(due))
	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup
	for i, site := range due {
		wg.Add(1)
		go func(i int, site *Site) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = site.CheckIn(ctx)
		}(i, site)
	}
	wg.Wait()

	for i, site := range due {
		if results[i].Site == "" {
			results[i].Site = site.Name()
		}
		r.record(day, site, &results[i], now)
	}
	return results
}

func (r *Runner) record(day string, site *Site, res *checkin.Result, now time.Time) {
	if r.dryRun {
		return
	}
	st, _, err := r.state.LoadCheckin(day, site.Host())
	if err != nil {
		r.log.Warn("pt: load check-in state", "site", site.Name(), "err", err)
	}
	st.Day = day
	st.Host = site.Host()
	st.Status = string(res.Status)
	st.Detail = res.Detail
	st.At = res.At
	if st.At.IsZero() {
		st.At = now
	}

	if res.OK() {
		st.Terminal = true
		st.Attempts = 0
		st.NextAt = time.Time{}
	} else {
		st.Attempts++
		if st.Attempts >= r.maxAttempts {
			st.Terminal = true
			st.NextAt = time.Time{}
		} else {
			st.Terminal = false
			st.NextAt = now.Add(backoffFor(st.Attempts))
			res.RetryAfter = st.NextAt
		}
	}
	if err := r.state.SaveCheckin(day, site.Host(), st); err != nil {
		r.log.Warn("pt: save check-in state", "site", site.Name(), "err", err)
	}
}

func backoffFor(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 10 * time.Minute
	case 2:
		return 30 * time.Minute
	default:
		return 2 * time.Hour
	}
}

// TerminalResults returns today's already-final results. Manual check-in
// (force) uses it so the card still lists sites that finished earlier today
// instead of showing only the two or three sites that happened to be due.
func (r *Runner) TerminalResults(day string) []checkin.Result {
	var out []checkin.Result
	for _, site := range r.sites {
		if site.cfg.Signin.Mode == "off" {
			continue
		}
		st, ok, err := r.state.LoadCheckin(day, site.Host())
		if err != nil || !ok || !st.Terminal || st.Day != day {
			continue
		}
		out = append(out, checkin.Result{
			Site:   site.Name(),
			Status: checkin.Status(st.Status),
			Detail: st.Detail,
			At:     st.At,
		})
	}
	return out
}

// NextRetry returns the earliest retry time for a site that is neither
// terminal nor waiting. Zero means nothing is scheduled.
func (r *Runner) NextRetry() time.Time {
	day := r.now().Format("2006-01-02")
	var earliest time.Time
	for _, site := range r.sites {
		if site.cfg.Signin.Mode == "off" {
			continue
		}
		st, ok, err := r.state.LoadCheckin(day, site.Host())
		if err != nil || !ok || st.Terminal || st.NextAt.IsZero() {
			continue
		}
		if earliest.IsZero() || st.NextAt.Before(earliest) {
			earliest = st.NextAt
		}
	}
	return earliest
}

// AllTerminal reports whether every sign-in site has reached a final state
// for the given day. It is used by startup catch-up.
func (r *Runner) AllTerminal(day string) bool {
	if r.dryRun {
		return true
	}
	hosts := make([]string, 0, len(r.sites))
	for _, site := range r.sites {
		if site.cfg.Signin.Mode != "off" {
			hosts = append(hosts, site.Host())
		}
	}
	if len(hosts) == 0 {
		return true
	}
	ok, err := r.state.AllCheckinTerminal(day, hosts)
	if err != nil {
		r.log.Warn("pt: check terminal state", "err", err)
		return false
	}
	return ok
}

// Pending returns messages that were fetched but not yet delivered.
func (r *Runner) Pending() ([]Message, error) {
	return r.state.PendingMessages()
}

// FetchNew returns messages that are new locally. With first_run=skip the
// first call records the existing unread inbox without returning anything.
func (r *Runner) FetchNew(ctx context.Context) ([]Message, error) {
	firstDone, err := r.state.FirstRunDone()
	if err != nil {
		return nil, err
	}
	if !firstDone && r.firstRun == FirstRunSkip {
		if err := r.recordFirstRun(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}

	const maxNewPerPoll = 20
	var out []Message
	for _, site := range r.sites {
		if !site.cfg.Messages.enabled() {
			continue
		}
		msgs, err := site.Unread(ctx)
		if err != nil {
			r.log.Warn("pt: read inbox", "site", site.Name(), "err", err)
			continue
		}
		fetched := 0
		for _, msg := range msgs {
			if fetched >= maxNewPerPoll {
				r.log.Info("pt: per-poll message cap reached", "site", site.Name(), "cap", maxNewPerPoll)
				break
			}
			seen, err := r.state.MessageSeen(msg.Host, msg.Key())
			if err != nil {
				r.log.Warn("pt: check seen message", "site", site.Name(), "err", err)
				continue
			}
			if seen {
				continue
			}
			if site.cfg.Messages.Mode == "full" {
				// Save the list row first: if the detail request succeeds and
				// the process dies before the card is sent, the pending row
				// still lets us report the message later.
				if err := r.state.SavePending(msg); err != nil {
					r.log.Warn("pt: save pending message", "site", site.Name(), "err", err)
					continue
				}
				full, err := site.FetchMessage(ctx, msg)
				if err != nil {
					r.log.Warn("pt: fetch message body", "site", site.Name(), "id", msg.ID, "err", err)
					msg.Body = ""
				} else {
					msg = full
				}
			}
			if err := r.state.SavePending(msg); err != nil {
				r.log.Warn("pt: save pending message", "site", site.Name(), "err", err)
				continue
			}
			fetched++
			out = append(out, msg)
		}
	}
	if !firstDone {
		if err := r.state.SetFirstRunDone(); err != nil {
			r.log.Warn("pt: record first run", "err", err)
		}
	}
	return out, nil
}

func (r *Runner) recordFirstRun(ctx context.Context) error {
	for _, site := range r.sites {
		if !site.cfg.Messages.enabled() {
			continue
		}
		msgs, err := site.Unread(ctx)
		if err != nil {
			return fmt.Errorf("first run %s: %w", site.Name(), err)
		}
		for _, msg := range msgs {
			if err := r.state.MarkMessage(msg.Host, msg.Key(), msg.Digest); err != nil {
				return err
			}
		}
		r.log.Info("pt: first run recorded existing unread", "site", site.Name(), "count", len(msgs))
	}
	if err := r.state.SetFirstRunDone(); err != nil {
		return err
	}
	return nil
}

// MarkSent records delivered messages and drops their pending rows.
func (r *Runner) MarkSent(msgs []Message) {
	for _, msg := range msgs {
		if err := r.state.MarkMessage(msg.Host, msg.Key(), msg.Digest); err != nil {
			r.log.Warn("pt: mark message seen", "site", msg.Site, "id", msg.ID, "err", err)
		}
		if err := r.state.DeletePending(msg.Host, msg.Key()); err != nil {
			r.log.Warn("pt: delete pending message", "site", msg.Site, "id", msg.ID, "err", err)
		}
	}
}
