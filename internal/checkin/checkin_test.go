package checkin

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestMain shrinks the retry backoff so retry-path tests stay fast.
func TestMain(m *testing.M) {
	retryBase = time.Millisecond
	os.Exit(m.Run())
}

func TestUntilNext(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	cases := []struct {
		name         string
		now          time.Time
		hour, minute int
		want         time.Duration
	}{
		{
			name: "before the target time today",
			now:  time.Date(2026, 10, 2, 5, 0, 0, 0, shanghai),
			hour: 6, minute: 0,
			want: time.Hour,
		},
		{
			name: "exactly at the target rolls to tomorrow",
			now:  time.Date(2026, 10, 2, 6, 0, 0, 0, shanghai),
			hour: 6, minute: 0,
			want: 24 * time.Hour,
		},
		{
			name: "after the target rolls to tomorrow",
			now:  time.Date(2026, 10, 2, 10, 30, 0, 0, shanghai),
			hour: 6, minute: 0,
			want: 19*time.Hour + 30*time.Minute,
		},
		{
			name: "minute precision",
			now:  time.Date(2026, 10, 2, 5, 45, 0, 0, shanghai),
			hour: 6, minute: 0,
			want: 15 * time.Minute,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UntilNext(tc.now, tc.hour, tc.minute, shanghai); got != tc.want {
				t.Fatalf("UntilNext = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestUntilNextNilLocationUsesLocal(t *testing.T) {
	now := time.Now()
	if got := UntilNext(now, 0, 0, nil); got <= 0 || got > 24*time.Hour {
		t.Fatalf("UntilNext = %s, want within (0, 24h]", got)
	}
}

func TestSameLocalDay(t *testing.T) {
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	morning := time.Date(2026, 10, 2, 6, 0, 0, 0, shanghai)
	evening := time.Date(2026, 10, 2, 23, 0, 0, 0, shanghai)
	nextDay := time.Date(2026, 10, 3, 0, 30, 0, 0, shanghai)

	if !SameLocalDay(morning, evening, shanghai) {
		t.Fatal("same day should compare equal")
	}
	if SameLocalDay(evening, nextDay, shanghai) {
		t.Fatal("different days must not compare equal")
	}
}

type stubSite struct {
	name   string
	result Result
	calls  int
}

func (s *stubSite) Name() string { return s.name }

func (s *stubSite) SignIn(context.Context) Result {
	s.calls++
	res := s.result
	if res.Site == "" {
		res.Site = s.name
	}
	return res
}

func TestRunVisitsEverySiteEvenAfterFailure(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := &stubSite{name: "V2EX", result: Result{Status: StatusFailed, Detail: "boom"}}
	second := &stubSite{name: "2libra", result: Result{Status: StatusSuccess, Detail: "ok"}}

	got := Run(context.Background(), log, []Site{first, second})
	if len(got) != 2 {
		t.Fatalf("results = %d, want 2", len(got))
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("both sites must run: %d, %d", first.calls, second.calls)
	}
	if got[0].OK() || !got[1].OK() {
		t.Fatalf("OK flags wrong: %+v", got)
	}
	if got[0].At.IsZero() {
		t.Fatal("Run should stamp a time when the site did not")
	}
}
