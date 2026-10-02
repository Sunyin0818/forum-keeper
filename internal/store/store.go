// Package store persists the set of notification IDs that have already been
// processed, so a restart never re-pushes old reminders.
package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	seenBucket = []byte("seen")
	metaBucket = []byte("meta")
)

const (
	startupKey = "startup_at"
	checkinKey = "checkin_at"
)

// Store is a bbolt-backed set of seen notification IDs.
type Store struct {
	db *bolt.DB
}

// Open opens (or creates) the state database at path, creating parent
// directories as needed.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create state directory %s: %w", dir, err)
		}
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, e := tx.CreateBucketIfNotExists(seenBucket); e != nil {
			return e
		}
		_, e := tx.CreateBucketIfNotExists(metaBucket)
		return e
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: init %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// OpenRead opens an existing state database read-only and never creates
// anything. If path does not exist it returns an empty, in-memory store, so
// read-only callers (dry runs) are free of side effects.
func OpenRead(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Store{}, nil
		}
		return nil, fmt.Errorf("store: stat %s: %w", path, err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: open %s read-only: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// FilterNew returns the subset of ids that have not been seen yet, preserving
// input order. All lookups happen in a single read transaction.
func (s *Store) FilterNew(ids []int) ([]int, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if s.db == nil { // empty, read-only store
		out := make([]int, len(ids))
		copy(out, ids)
		return out, nil
	}
	var out []int
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(seenBucket)
		for _, id := range ids {
			if b.Get(key(id)) == nil {
				out = append(out, id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: filter: %w", err)
	}
	return out, nil
}

// Has reports whether a single id has been seen.
func (s *Store) Has(id int) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(seenBucket).Get(key(id)) != nil
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("store: has: %w", err)
	}
	return found, nil
}

// Mark records ids as seen in a single write transaction. It is a no-op on a
// read-only (empty) store.
func (s *Store) Mark(ids []int) error {
	if s.db == nil || len(ids) == 0 {
		return nil
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(seenBucket)
		for _, id := range ids {
			if err := b.Put(key(id), []byte{1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("store: mark: %w", err)
	}
	return nil
}

// Count returns how many ids are recorded. Zero means "never ran before".
func (s *Store) Count() (int, error) {
	if s.db == nil {
		return 0, nil
	}
	var n int
	if err := s.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(seenBucket).Stats().KeyN
		return nil
	}); err != nil {
		return 0, fmt.Errorf("store: count: %w", err)
	}
	return n, nil
}

func key(id int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(id))
	return b
}

// LastStartup returns when the startup message was last sent, or the zero time
// if it never was.
func (s *Store) LastStartup() (time.Time, error) {
	if s.db == nil {
		return time.Time{}, nil
	}
	var t time.Time
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(metaBucket)
		if b == nil {
			return nil
		}
		v := b.Get([]byte(startupKey))
		if v == nil {
			return nil
		}
		sec, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return nil // tolerate a corrupt value rather than failing startup
		}
		t = time.Unix(sec, 0)
		return nil
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("store: last startup: %w", err)
	}
	return t, nil
}

// SetStartup records when the startup message was sent. It is a no-op on a
// read-only (empty) store.
func (s *Store) SetStartup(at time.Time) error {
	if s.db == nil {
		return nil
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(metaBucket)
		if err != nil {
			return err
		}
		return b.Put([]byte(startupKey), []byte(strconv.FormatInt(at.Unix(), 10)))
	}); err != nil {
		return fmt.Errorf("store: set startup: %w", err)
	}
	return nil
}

// LastCheckin returns when the daily check-in last ran, or the zero time if it
// never did. It is used to keep CHECKIN_ON_START from re-running on every
// container restart within the same day.
func (s *Store) LastCheckin() (time.Time, error) {
	return s.metaTime(checkinKey, "last check-in")
}

// SetCheckin records when the daily check-in last ran. It is a no-op on a
// read-only (empty) store.
func (s *Store) SetCheckin(at time.Time) error {
	if s.db == nil {
		return nil
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(metaBucket)
		if err != nil {
			return err
		}
		return b.Put([]byte(checkinKey), []byte(strconv.FormatInt(at.Unix(), 10)))
	}); err != nil {
		return fmt.Errorf("store: set check-in: %w", err)
	}
	return nil
}

func (s *Store) metaTime(key, label string) (time.Time, error) {
	if s.db == nil {
		return time.Time{}, nil
	}
	var t time.Time
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(metaBucket)
		if b == nil {
			return nil
		}
		v := b.Get([]byte(key))
		if v == nil {
			return nil
		}
		sec, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return nil // tolerate a corrupt value rather than failing
		}
		t = time.Unix(sec, 0)
		return nil
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("store: %s: %w", label, err)
	}
	return t, nil
}
