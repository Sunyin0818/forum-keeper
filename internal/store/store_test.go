package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilterNewMarkAndCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	got, err := s.FilterNew([]int{1, 2, 3})
	if err != nil {
		t.Fatalf("FilterNew: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 new ids, got %v", got)
	}

	if err := s.Mark([]int{1, 3}); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	got, err = s.FilterNew([]int{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("FilterNew: %v", err)
	}
	want := []int{2, 4}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, got)
	}

	n, err := s.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected count 2, got %d", n)
	}

	has, err := s.Has(3)
	if err != nil || !has {
		t.Fatalf("Has(3) = %v, %v; want true, nil", has, err)
	}
	if has, err = s.Has(9); err != nil || has {
		t.Fatalf("Has(9) = %v, %v; want false, nil", has, err)
	}
}

func TestOpenCreatesParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open with missing parents: %v", err)
	}
	defer s.Close()

	if err := s.Mark([]int{1}); err != nil {
		t.Fatalf("Mark: %v", err)
	}
}

func TestOpenReadIsSideEffectFree(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nested", "state.db")

	s, err := OpenRead(missing)
	if err != nil {
		t.Fatalf("OpenRead on missing file: %v", err)
	}
	defer s.Close()

	// Nothing must be created on disk.
	if _, err := os.Stat(filepath.Join(dir, "nested")); !os.IsNotExist(err) {
		t.Fatalf("OpenRead created directories: %v", err)
	}

	// An empty read-only store reports everything as new and ignores Mark.
	got, err := s.FilterNew([]int{1, 2})
	if err != nil || len(got) != 2 {
		t.Fatalf("FilterNew = %v, %v; want all new", got, err)
	}
	if err := s.Mark([]int{1, 2}); err != nil {
		t.Fatalf("Mark on empty store: %v", err)
	}
	if n, err := s.Count(); err != nil || n != 0 {
		t.Fatalf("Count = %d, %v; want 0", n, err)
	}
}

func TestOpenReadSeesExistingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Mark([]int{1}); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := OpenRead(path)
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	defer r.Close()

	got, err := r.FilterNew([]int{1, 2})
	if err != nil {
		t.Fatalf("FilterNew: %v", err)
	}
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("expected [2], got %v", got)
	}
}

func TestStateSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Mark([]int{42}); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, err := s2.FilterNew([]int{42, 43})
	if err != nil {
		t.Fatalf("FilterNew: %v", err)
	}
	if len(got) != 1 || got[0] != 43 {
		t.Fatalf("expected [43] after reopen, got %v", got)
	}
}

func TestStartupTimestampRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if got, err := s.LastStartup(); err != nil || !got.IsZero() {
		t.Fatalf("LastStartup = %v, %v; want zero", got, err)
	}

	want := time.Unix(1759000000, 0)
	if err := s.SetStartup(want); err != nil {
		t.Fatalf("SetStartup: %v", err)
	}

	// The startup marker must not be counted as a seen notification, otherwise
	// it would break first-run detection.
	if n, err := s.Count(); err != nil || n != 0 {
		t.Fatalf("Count = %d, %v; want 0", n, err)
	}

	got, err := s.LastStartup()
	if err != nil {
		t.Fatalf("LastStartup: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("LastStartup = %v, want %v", got, want)
	}
}

func TestSetStartupOnEmptyReadOnlyStoreIsNoop(t *testing.T) {
	s, err := OpenRead(filepath.Join(t.TempDir(), "absent", "state.db"))
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	defer s.Close()

	if err := s.SetStartup(time.Now()); err != nil {
		t.Fatalf("SetStartup on read-only store: %v", err)
	}
	if got, err := s.LastStartup(); err != nil || !got.IsZero() {
		t.Fatalf("LastStartup = %v, %v; want zero", got, err)
	}
}
