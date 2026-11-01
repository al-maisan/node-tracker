package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/al-maisan/node-tracker/internal/health"
	"github.com/al-maisan/node-tracker/internal/notify"
	"github.com/al-maisan/node-tracker/internal/source"
	"github.com/al-maisan/node-tracker/internal/state"
)

// script yields one height (or error) per tick.
type script struct {
	name  string
	steps []any // uint64 or error
	tick  *int
}

func (s *script) Name() string { return s.name }
func (s *script) Height(context.Context) (source.Height, error) {
	switch v := s.steps[min(*s.tick, len(s.steps)-1)].(type) {
	case error:
		return source.Height{}, v
	default:
		h := v.(int)
		return source.Height{Blocks: uint64(h), Headers: uint64(h)}, nil
	}
}

type recorder struct {
	mu   sync.Mutex
	got  []string
	fail int // fail this many calls first
}

func (r *recorder) Name() string { return "rec" }
func (r *recorder) Send(_ context.Context, m notify.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail > 0 {
		r.fail--
		return errors.New("down")
	}
	r.got = append(r.got, m.Subject)
	return nil
}

var errDown = errors.New("connection refused")

// Node falls 5 behind, catches up, then goes down.
var (
	nodeSteps = []any{100, 100, 100, 100, 105, 105, errDown, errDown}
	refSteps  = []any{100, 105, 105, 105, 105, 105, 105, 105}
	wantAlert = []string{"🔴 btc is lagging", "🟢 btc is in sync", "⚫ btc: node unreachable"}
)

func newMonitor(tick *int, rec *recorder) *Monitor {
	return &Monitor{
		Targets: []Target{{
			Name:   "btc",
			Node:   &script{name: "node", steps: nodeSteps, tick: tick},
			Refs:   []source.Source{&script{name: "ref", steps: refSteps, tick: tick}},
			Rule:   health.Rule{Threshold: 3, Recover: 1, For: 2},
			Notify: []string{"rec"},
		}},
		Notifiers:   map[string]notify.Notifier{"rec": rec},
		Timeout:     time.Second,
		Retry:       notify.Retry{Attempts: 1},
		OutboxLimit: 10,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:         func() time.Time { return time.Date(2026, 11, 1, 0, 0, *tick, 0, time.UTC) },
	}
}

func TestDaemonAndOnceAgree(t *testing.T) {
	// Daemon: one Data in memory, N ticks.
	var tick int
	daemon := &recorder{}
	m := newMonitor(&tick, daemon)
	d := &state.Data{Targets: map[string]health.Target{}}
	for tick = range len(nodeSteps) {
		if _, _, err := m.Tick(context.Background(), d, time.Second); err != nil {
			t.Fatal(err)
		}
	}

	// once: a fresh process per tick, state only via the file.
	path := filepath.Join(t.TempDir(), "state.json")
	once := &recorder{}
	for tick = range len(nodeSteps) {
		s, err := state.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		od, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := newMonitor(&tick, once).Tick(context.Background(), od, time.Second); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(od); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}

	if !reflect.DeepEqual(daemon.got, wantAlert) {
		t.Errorf("daemon alerts = %v, want %v", daemon.got, wantAlert)
	}
	if !reflect.DeepEqual(once.got, wantAlert) {
		t.Errorf("once alerts = %v, want %v", once.got, wantAlert)
	}
}

func TestFailedDeliveryIsRetriedInOrder(t *testing.T) {
	var tick int
	rec := &recorder{fail: 3} // down for the first three sends
	m := newMonitor(&tick, rec)
	d := &state.Data{Targets: map[string]health.Target{}}
	for tick = range len(nodeSteps) + 2 { // two extra ticks to flush
		m.Tick(context.Background(), d, time.Second)
	}
	if !reflect.DeepEqual(rec.got, wantAlert) {
		t.Fatalf("alerts = %v, want %v", rec.got, wantAlert)
	}
	if len(d.Outbox) != 0 {
		t.Fatalf("outbox not drained: %+v", d.Outbox)
	}
}

func TestCanceledTickChangesNothing(t *testing.T) {
	var tick int
	m := newMonitor(&tick, &recorder{})
	d := &state.Data{Targets: map[string]health.Target{"btc": {Current: health.OK}}}
	before := *d
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.Tick(ctx, d, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(*d, before) {
		t.Fatalf("state changed: %+v", d)
	}
}

func TestPrune(t *testing.T) {
	var tick int
	m := newMonitor(&tick, &recorder{})
	d := &state.Data{
		Targets: map[string]health.Target{"btc": {}, "gone": {}},
		Outbox: []state.Pending{
			{Event: notify.Event{Target: "btc"}, Notifiers: []string{"rec", "old"}},
			{Event: notify.Event{Target: "btc"}, Notifiers: []string{"old"}},
		},
	}
	m.Prune(d)
	if _, ok := d.Targets["gone"]; ok || len(d.Targets) != 1 {
		t.Errorf("targets = %v", d.Targets)
	}
	if len(d.Outbox) != 1 || !reflect.DeepEqual(d.Outbox[0].Notifiers, []string{"rec"}) {
		t.Errorf("outbox = %+v", d.Outbox)
	}
}

func TestObserveIsReadOnly(t *testing.T) {
	var tick int
	m := newMonitor(&tick, &recorder{})
	tick = 1 // ref 105, node 100: lagging
	res, err := m.Observe(context.Background(), nil)
	if err != nil || len(res) != 1 || res[0].State != health.Lagging || res[0].Lag != 5 || res[0].TipFrom != "ref" {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

func TestBroadcast(t *testing.T) {
	var tick int
	ok, bad := &recorder{}, &recorder{fail: 1}
	m := newMonitor(&tick, ok)
	m.Notifiers = map[string]notify.Notifier{"ok": ok, "bad": bad}
	err := m.Broadcast(context.Background(), notify.Message{Subject: "hi"})
	if err == nil || len(ok.got) != 1 || len(bad.got) != 0 {
		t.Fatalf("err=%v ok=%v bad=%v", err, ok.got, bad.got)
	}
}

// rejecter fails permanently for subjects in bad; everything else depends on down.
type rejecter struct {
	bad  map[string]bool
	down bool
	got  []string
}

func (r *rejecter) Name() string { return "rej" }
func (r *rejecter) Send(_ context.Context, m notify.Message) error {
	if r.down || r.bad[m.Subject] {
		return notify.Permanent(errors.New("400"))
	}
	r.got = append(r.got, m.Subject)
	return nil
}

func TestDeliverDropsOnlyMessagesTheNotifierRejects(t *testing.T) {
	var tick int
	rej := &rejecter{bad: map[string]bool{"🔴 a is lagging": true}}
	m := newMonitor(&tick, nil)
	m.Notifiers = map[string]notify.Notifier{"rej": rej}
	ev := func(target string, to health.State) notify.Event { return notify.Event{Target: target, To: to} }
	d := &state.Data{}
	for _, e := range []notify.Event{ev("a", health.Lagging), ev("b", health.Lagging), ev("c", health.OK)} {
		d.Enqueue(e, []string{"rej"}, 10)
	}

	// Channel broken: every send fails permanently -> keep everything.
	rej.down = true
	if pending := m.Deliver(context.Background(), d); pending != 3 {
		t.Fatalf("channel down: pending = %d, want 3", pending)
	}
	// Channel fine, one message rejected -> it is dropped, the rest delivered.
	rej.down = false
	if pending := m.Deliver(context.Background(), d); pending != 0 || len(d.Outbox) != 0 {
		t.Fatalf("pending = %d, outbox = %+v", pending, d.Outbox)
	}
	if !reflect.DeepEqual(rej.got, []string{"🔴 b is lagging", "🟢 c is in sync"}) {
		t.Fatalf("got %v", rej.got)
	}
}

func TestTickReportsPending(t *testing.T) {
	var tick int
	m := newMonitor(&tick, &recorder{fail: 100})
	d := &state.Data{Targets: map[string]health.Target{}}
	var pending int
	for tick = range 4 { // node falls behind at tick 1, alert at tick 2
		_, pending, _ = m.Tick(context.Background(), d, time.Second)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
}
