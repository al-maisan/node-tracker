//go:build unix

package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/al-maisan/node-tracker/internal/health"
	"github.com/al-maisan/node-tracker/internal/notify"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.Load()
	if err != nil || len(d.Targets) != 0 {
		t.Fatalf("empty load: %+v %v", d, err)
	}
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	d.Targets["a"] = health.Target{Current: health.Lagging, Since: at, Candidate: health.OK, Seen: 1}
	d.Enqueue(notify.Event{Target: "a", To: health.Lagging, At: at}, []string{"tg"}, 10)
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("round trip:\n got %+v\nwant %+v\n%v", got, d, err)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v (%v)", p, fi.Mode().Perm(), want, err)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".state-*")); len(m) != 0 {
		t.Errorf("temp files left behind: %v", m)
	}
}

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open: %v, want ErrLocked", err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	s2.Close()
}

func TestCorruptIsAnError(t *testing.T) {
	for name, content := range map[string]string{
		"junk":    "{not json",
		"version": `{"version":99}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			os.WriteFile(path, []byte(content), 0o600)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.Load(); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestEnqueueCapsPerNotifier(t *testing.T) {
	var d Data
	ev := func(i int) notify.Event { return notify.Event{Target: "t", Lag: uint64(i)} }
	d.Enqueue(ev(1), []string{"a", "b"}, 2)
	d.Enqueue(ev(2), []string{"a"}, 2)
	if dropped := d.Enqueue(ev(3), []string{"a"}, 2); dropped != 1 {
		t.Fatalf("dropped = %d", dropped)
	}
	// ev1 must survive for b only; a keeps ev2 and ev3.
	want := []Pending{
		{Event: ev(1), Notifiers: []string{"b"}},
		{Event: ev(2), Notifiers: []string{"a"}},
		{Event: ev(3), Notifiers: []string{"a"}},
	}
	if !reflect.DeepEqual(d.Outbox, want) {
		t.Fatalf("outbox = %+v", d.Outbox)
	}
	d.Outbox[0].Notifiers = nil
	d.Compact()
	if len(d.Outbox) != 2 {
		t.Fatalf("compact: %+v", d.Outbox)
	}
}
