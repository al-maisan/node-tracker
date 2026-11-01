package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/al-maisan/node-tracker/internal/health"
)

type flaky struct {
	errs  []error // returned in order; nil once exhausted
	calls int
}

func (f *flaky) Name() string { return "flaky" }
func (f *flaky) Send(context.Context, Message) error {
	f.calls++
	if f.calls <= len(f.errs) {
		return f.errs[f.calls-1]
	}
	return nil
}

var fast = Retry{Attempts: 3, Base: time.Millisecond, Max: time.Millisecond}

func TestSend(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name      string
		errs      []error
		wantCalls int
		wantErr   bool
	}{
		{"first try", nil, 1, false},
		{"recovers", []error{boom, boom}, 3, false},
		{"gives up", []error{boom, boom, boom, boom}, 3, true},
		{"permanent stops", []error{Permanent(boom)}, 1, true},
		{"retry-after honoured", []error{&RetryAfter{After: time.Millisecond, Err: boom}}, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &flaky{errs: tt.errs}
			err := Send(context.Background(), f, Message{}, fast)
			if f.calls != tt.wantCalls || (err != nil) != tt.wantErr {
				t.Fatalf("calls=%d err=%v; want calls=%d err=%v", f.calls, err, tt.wantCalls, tt.wantErr)
			}
		})
	}
}

func TestSendRespectsDeadline(t *testing.T) {
	f := &flaky{errs: []error{&RetryAfter{After: time.Hour, Err: errors.New("429")}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := Send(ctx, f, Message{}, fast); err == nil || f.calls != 1 {
		t.Fatalf("calls=%d err=%v", f.calls, err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("Send waited although the back-off outlives the deadline")
	}
}

func TestRender(t *testing.T) {
	at := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)
	tests := []struct {
		e       Event
		subject string
		body    []string
		notBody string
	}{
		{Event{Target: "orendil", From: health.OK, To: health.Lagging, Lag: 5, Node: 938261, Tip: 938266, TipFrom: "esplora:mempool.space", At: at},
			"🔴 orendil is lagging",
			[]string{"Node height: 938,261", "Network height: 938,266 (esplora:mempool.space)", "Lag: 5 blocks", "Was: in sync\n", "At: 2026-11-01 08:30:00 UTC"}, ""},
		{Event{Target: "orendil", From: health.OK, To: health.NodeDown, At: at,
			Err: `Post "http://127.0.0.1:1": dial tcp 127.0.0.1:1: connect: connection refused`},
			"⚫ orendil: node unreachable", []string{"Error: nothing answers at 127.0.0.1:1\nDetails: Post \"http://127.0.0.1:1\": dial tcp 127.0.0.1:1: connect: connection refused\n"}, "Lag"},
		{Event{Target: "orendil", To: health.NodeDown, Err: "rpc error -1: odd", At: at},
			"⚫ orendil: node unreachable", []string{"Error: rpc error -1: odd\nAt: "}, "Details"},
		{Event{Target: "orendil", From: health.NodeDown, To: health.NoReference, Node: 7, Tip: 7, Err: "esplora:mempool.space: HTTP 503", At: at},
			"🟡 orendil: no reference reachable", []string{"Error: no independent reference answered: ", "Details: esplora:mempool.space: HTTP 503\n", "Was: node unreachable\n"}, "NODE_DOWN"},
		{Event{Target: "orendil", To: health.OK, From: health.Lagging, At: at},
			"🟢 orendil is in sync", []string{"Was: lagging\n"}, "LAGGING"},
		{Event{Target: "orendil", To: health.Lagging, From: health.NoReference, At: at},
			"🔴 orendil is lagging", []string{"Was: no independent reference answered\n"}, "NO_REFERENCE"},
	}
	for _, tt := range tests {
		m := Render(tt.e)
		if m.Subject != tt.subject {
			t.Errorf("subject = %q, want %q", m.Subject, tt.subject)
		}
		for _, w := range tt.body {
			if !strings.Contains(m.Body, w) {
				t.Errorf("body %q missing %q", m.Body, w)
			}
		}
		if tt.notBody != "" && strings.Contains(m.Body, tt.notBody) {
			t.Errorf("body %q must not contain %q", m.Body, tt.notBody)
		}
	}
}

func TestReport(t *testing.T) {
	at := time.Date(2026, 11, 1, 13, 5, 0, 0, time.UTC)
	ok := Row{Target: "orendil", Observation: health.Observation{State: health.OK, NodeSource: "bitcoind:127.0.0.1:8332", Node: 938266, Tip: 938266, TipFrom: "esplora:mempool.space"}}
	m := Report(at, []Row{ok})
	for _, w := range []string{"orendil\n", "Network height: 938,266 (esplora:mempool.space)", "✓ bitcoind:127.0.0.1:8332: 938,266 (0 behind)", "✅ All nodes in sync"} {
		if !strings.Contains(m.Body, w) {
			t.Errorf("body %q missing %q", m.Body, w)
		}
	}
	lag := Row{Target: "b", Observation: health.Observation{State: health.Lagging, NodeSource: "n", Node: 10, Tip: 15, Lag: 5}}
	down := Row{Target: "c", Observation: health.Observation{State: health.NodeDown, Err: "refused"}}
	noref := Row{Target: "d", Observation: health.Observation{State: health.NoReference, NodeSource: "m", Node: 7, Tip: 7, Err: "timeout"}}
	m = Report(at, []Row{ok, lag, down, noref})
	for _, w := range []string{"✗ n: 10 (5 behind)", "✗ node unreachable: refused", "✗ m: 7 (0 behind)", "⚠️ 3 of 4 nodes need attention"} {
		if !strings.Contains(m.Body, w) {
			t.Errorf("body %q missing %q", m.Body, w)
		}
	}
}

func TestNum(t *testing.T) {
	for n, want := range map[uint64]string{0: "0", 999: "999", 1000: "1,000", 938266: "938,266", 1234567890: "1,234,567,890"} {
		if got := Num(n); got != want {
			t.Errorf("Num(%d) = %q, want %q", n, got, want)
		}
	}
}
