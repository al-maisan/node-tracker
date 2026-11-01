//go:build unix

// Package state persists target states and the alert outbox between ticks,
// so a cron-driven `once` and the `run` daemon behave the same.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/al-maisan/node-tracker/internal/health"
	"github.com/al-maisan/node-tracker/internal/notify"
)

const version = 1

// ErrLocked means another node-tracker holds the state file.
var ErrLocked = errors.New("state file is locked by another node-tracker process")

// Data is the content of the state file.
type Data struct {
	Version int                      `json:"version"`
	Targets map[string]health.Target `json:"targets"`
	Outbox  []Pending                `json:"outbox"`
}

// Pending is an event that some notifiers still have to deliver.
type Pending struct {
	Event     notify.Event `json:"event"`
	Notifiers []string     `json:"notifiers"`
}

// Enqueue adds e for the given notifiers. Each notifier keeps at most limit
// pending events; the oldest are dropped first. It returns how many
// deliveries were dropped.
func (d *Data) Enqueue(e notify.Event, notifiers []string, limit int) (dropped int) {
	if len(notifiers) == 0 {
		return 0
	}
	d.Outbox = append(d.Outbox, Pending{Event: e, Notifiers: slices.Clone(notifiers)})
	for _, n := range notifiers {
		count := 0
		for _, p := range d.Outbox {
			if slices.Contains(p.Notifiers, n) {
				count++
			}
		}
		for i := 0; count > limit && i < len(d.Outbox); i++ {
			if j := slices.Index(d.Outbox[i].Notifiers, n); j >= 0 {
				d.Outbox[i].Notifiers = slices.Delete(d.Outbox[i].Notifiers, j, j+1)
				count--
				dropped++
			}
		}
	}
	d.Compact()
	return dropped
}

// Compact removes fully delivered events.
func (d *Data) Compact() {
	d.Outbox = slices.DeleteFunc(d.Outbox, func(p Pending) bool { return len(p.Notifiers) == 0 })
}

// Store is an exclusively locked state file.
type Store struct {
	path string
	lock *os.File
}

// Open creates the state directory (0700) if needed and takes an exclusive,
// non-blocking lock on "<path>.lock". It returns ErrLocked if another process
// holds it. The lock is released by Close or when the process exits.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock state file: %w", err)
	}
	return &Store{path: path, lock: f}, nil
}

// Path returns the state file path.
func (s *Store) Path() string { return s.path }

// Close releases the lock.
func (s *Store) Close() error { return s.lock.Close() }

// Load reads the state. A missing file yields empty state; a corrupt or
// unknown-version file is an error, never silently reset, because a reset
// could repeat or swallow alerts.
func (s *Store) Load() (*Data, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Data{Version: version, Targets: map[string]health.Target{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var d Data
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("state file %s is corrupt (fix or delete it): %w", s.path, err)
	}
	if d.Version != version {
		return nil, fmt.Errorf("state file %s has version %d, want %d", s.path, d.Version, version)
	}
	if d.Targets == nil {
		d.Targets = map[string]health.Target{}
	}
	return &d, nil
}

// Save writes the state atomically: temp file, fsync, rename, fsync dir.
func (s *Store) Save(d *Data) (err error) {
	d.Version = version
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp") // created 0600
	if err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(append(b, '\n')); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	if err = os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	if df, derr := os.Open(dir); derr == nil {
		derr = df.Sync()
		df.Close()
		if derr != nil {
			return fmt.Errorf("save state: sync dir: %w", derr)
		}
	}
	return nil
}
