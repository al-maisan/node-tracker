package health

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var rule = Rule{Threshold: 3, Recover: 1, For: 2, Outlier: 6}

func TestObserve(t *testing.T) {
	boom := errors.New("boom")
	node := func(b, h uint64) Reading { return Reading{Source: "node", Blocks: b, Headers: h} }
	ref := func(name string, h uint64) Reading { return Reading{Source: name, Blocks: h} }
	local := func(name string, h uint64) Reading { return Reading{Source: name, Blocks: h, Local: true} }
	tests := []struct {
		name    string
		s       Sample
		lagging bool
		want    Observation
	}{
		{
			name: "in sync",
			s:    Sample{Node: node(100, 100), Refs: []Reading{ref("a", 100)}},
			want: Observation{NodeSource: "node", State: OK, Node: 100, Tip: 100, TipFrom: "node"},
		},
		{
			name: "max reference wins",
			s:    Sample{Node: node(100, 100), Refs: []Reading{ref("a", 102), ref("b", 105), ref("c", 90)}},
			want: Observation{NodeSource: "node", State: Lagging, Lag: 5, Node: 100, Tip: 105, TipFrom: "b"},
		},
		{
			name: "lag equal to threshold is OK",
			s:    Sample{Node: node(100, 100), Refs: []Reading{ref("a", 103)}},
			want: Observation{NodeSource: "node", State: OK, Lag: 3, Node: 100, Tip: 103, TipFrom: "a"},
		},
		{
			name:    "hysteresis keeps LAGGING above recover threshold",
			s:       Sample{Node: node(100, 100), Refs: []Reading{ref("a", 102)}},
			lagging: true,
			want:    Observation{NodeSource: "node", State: Lagging, Lag: 2, Node: 100, Tip: 102, TipFrom: "a"},
		},
		{
			name:    "recovers at recover threshold",
			s:       Sample{Node: node(100, 100), Refs: []Reading{ref("a", 101)}},
			lagging: true,
			want:    Observation{NodeSource: "node", State: OK, Lag: 1, Node: 100, Tip: 101, TipFrom: "a"},
		},
		{
			name: "node headers catch lag without references",
			s:    Sample{Node: node(100, 110)},
			want: Observation{NodeSource: "node", State: Lagging, Lag: 10, Node: 100, Tip: 110, TipFrom: "node"},
		},
		{
			name: "node ahead of references is lag 0",
			s:    Sample{Node: node(100, 100), Refs: []Reading{ref("a", 95)}},
			want: Observation{NodeSource: "node", State: OK, Node: 100, Tip: 100, TipFrom: "node"},
		},
		{
			name: "two references that disagree: the higher one wins",
			s:    Sample{Node: node(100, 100), Refs: []Reading{ref("mempool", 150), ref("own-electrs", 100)}},
			want: Observation{NodeSource: "node", State: Lagging, Lag: 50, Node: 100, Tip: 150, TipFrom: "mempool"},
		},
		{
			name: "only a reference on the node's host answers: no independent view",
			s:    Sample{Node: node(100, 100), Refs: []Reading{{Source: "mempool", Err: boom}, local("own-electrs", 100)}},
			want: Observation{NodeSource: "node", State: NoReference, Node: 100, Tip: 100, TipFrom: "node", Err: "only references on the node's host answered; boom"},
		},
		{
			name: "a local reference that shows lag still makes it LAGGING",
			s:    Sample{Node: node(100, 100), Refs: []Reading{{Source: "mempool", Err: boom}, local("own-electrs", 110)}},
			want: Observation{NodeSource: "node", State: Lagging, Lag: 10, Node: 100, Tip: 110, TipFrom: "own-electrs", Err: "only references on the node's host answered; boom"},
		},
		{
			name: "an independent reference answers: OK",
			s:    Sample{Node: node(100, 100), Refs: []Reading{ref("mempool", 100), local("own-electrs", 100)}},
			want: Observation{NodeSource: "node", State: OK, Node: 100, Tip: 100, TipFrom: "node"},
		},
		{
			name: "local references don't outvote an independent one that is ahead",
			s:    Sample{Node: node(100, 100), Refs: []Reading{local("electrs", 100), local("box-mempool", 100), ref("mempool", 110)}},
			want: Observation{NodeSource: "node", State: Lagging, Lag: 10, Node: 100, Tip: 110, TipFrom: "mempool"},
		},
		{
			name: "only local references configured: trusted as before",
			s:    Sample{Node: node(100, 100), Refs: []Reading{local("own-electrs", 100)}},
			want: Observation{NodeSource: "node", State: OK, Node: 100, Tip: 100, TipFrom: "node"},
		},
		{
			name: "reference far above the others is ignored",
			s:    Sample{Node: node(100, 100), Refs: []Reading{ref("liar", 999999), ref("a", 101), ref("b", 100)}},
			want: Observation{NodeSource: "node", State: OK, Lag: 1, Node: 100, Tip: 101, TipFrom: "a", Outliers: "liar"},
		},
		{
			name: "single reference is trusted, even far ahead (isolated node)",
			s:    Sample{Node: node(100, 100), Refs: []Reading{ref("a", 150), {Source: "b", Err: boom}}},
			want: Observation{NodeSource: "node", State: Lagging, Lag: 50, Node: 100, Tip: 150, TipFrom: "a"},
		},
		{
			name: "references that agree may be far above the node",
			s:    Sample{Node: node(100, 100), Refs: []Reading{ref("a", 150), ref("b", 149)}},
			want: Observation{NodeSource: "node", State: Lagging, Lag: 50, Node: 100, Tip: 150, TipFrom: "a"},
		},
		{
			name: "one failed reference is ignored",
			s:    Sample{Node: node(100, 100), Refs: []Reading{{Source: "a", Err: boom}, ref("b", 100)}},
			want: Observation{NodeSource: "node", State: OK, Node: 100, Tip: 100, TipFrom: "node"},
		},
		{
			name: "own headers show lag although all references failed",
			s:    Sample{Node: node(100, 110), Refs: []Reading{{Source: "a", Err: boom}}},
			want: Observation{NodeSource: "node", State: Lagging, Lag: 10, Node: 100, Tip: 110, TipFrom: "node", Err: "boom"},
		},
		{
			name: "all references failed",
			s:    Sample{Node: node(100, 102), Refs: []Reading{{Source: "a", Err: boom}}},
			want: Observation{NodeSource: "node", State: NoReference, Lag: 2, Node: 100, Tip: 102, TipFrom: "node", Err: "boom"},
		},
		{
			name: "reference on another chain counts as failed",
			s:    Sample{Node: Reading{Source: "node", Blocks: 100, Headers: 100, Chain: "main"}, Refs: []Reading{{Source: "a", Blocks: 150, Headers: 150, Chain: "signet"}}},
			want: Observation{NodeSource: "node", State: NoReference, Node: 100, Tip: 100, TipFrom: "node", Err: "a is on chain signet, the node on main"},
		},
		{
			name: "an unknown chain is no mismatch",
			s:    Sample{Node: Reading{Source: "node", Blocks: 100, Headers: 100}, Refs: []Reading{{Source: "a", Blocks: 150, Headers: 150, Chain: "signet"}}},
			want: Observation{NodeSource: "node", State: Lagging, Lag: 50, Node: 100, Tip: 150, TipFrom: "a"},
		},
		{
			name: "node down wins",
			s:    Sample{Node: Reading{Source: "node", Err: boom}, Refs: []Reading{ref("a", 100)}},
			want: Observation{State: NodeDown, NodeSource: "node", Err: "boom"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Observe(tt.s, rule, tt.lagging); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStep(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	type step struct {
		observed State
		alert    bool
		current  State
	}
	tests := []struct {
		name    string
		start   Target
		For     int
		DownFor int
		steps   []step
	}{
		{
			name: "first OK is silent",
			For:  2,
			steps: []step{
				{OK, false, Unknown},
				{OK, false, OK},
				{OK, false, OK},
			},
		},
		{
			name: "first non-OK alerts",
			For:  2,
			steps: []step{
				{Lagging, false, Unknown},
				{Lagging, true, Lagging},
			},
		},
		{
			name:  "debounce: blip is ignored",
			start: Target{Current: OK},
			For:   2,
			steps: []step{
				{Lagging, false, OK},
				{OK, false, OK},
				{Lagging, false, OK},
				{OK, false, OK},
			},
		},
		{
			name:  "crossing up then down",
			start: Target{Current: OK},
			For:   2,
			steps: []step{
				{Lagging, false, OK},
				{Lagging, true, Lagging},
				{Lagging, false, Lagging},
				{OK, false, Lagging},
				{OK, true, OK},
			},
		},
		{
			name:  "alternating bad states still leave OK, with the latest state",
			start: Target{Current: OK},
			For:   2,
			steps: []step{
				{Lagging, false, OK},
				{NodeDown, true, NodeDown},
			},
		},
		{
			name:  "alternating bad states never stall",
			start: Target{Current: OK},
			For:   3,
			steps: []step{
				{Lagging, false, OK},
				{NodeDown, false, OK},
				{Lagging, true, Lagging},
			},
		},
		{
			name:  "one OK sample after trouble is not recovery",
			start: Target{Current: Lagging},
			For:   2,
			steps: []step{
				{NodeDown, false, Lagging},
				{NodeDown, true, NodeDown},
				{OK, false, NodeDown},
				{Lagging, false, NodeDown},
				{OK, false, NodeDown},
				{OK, true, OK},
			},
		},
		{
			name:  "bad sample interrupts recovery",
			start: Target{Current: NodeDown},
			For:   2,
			steps: []step{
				{OK, false, NodeDown},
				{Lagging, false, NodeDown},
				{Lagging, true, Lagging},
			},
		},
		{
			name:  "For 1 changes at once",
			start: Target{Current: OK},
			For:   1,
			steps: []step{
				{NoReference, true, NoReference},
				{OK, true, OK},
			},
		},
		{
			name:    "down_for: a short outage is not NODE_DOWN",
			start:   Target{Current: OK},
			For:     2,
			DownFor: 4,
			steps: []step{
				{NodeDown, false, OK},
				{NodeDown, false, OK},
				{NodeDown, false, OK},
				{OK, false, OK},
				{NodeDown, false, OK},
				{NodeDown, false, OK},
				{NodeDown, false, OK},
				{NodeDown, true, NodeDown},
			},
		},
		{
			name:    "down_for: a node that answers behind is LAGGING after For",
			start:   Target{Current: OK},
			For:     2,
			DownFor: 4,
			steps: []step{
				{NodeDown, false, OK},
				{Lagging, true, Lagging},
			},
		},
		{
			name:    "down_for: LAGGING, then unreachable, waits down_for",
			start:   Target{Current: Lagging},
			For:     2,
			DownFor: 3,
			steps: []step{
				{NodeDown, false, Lagging},
				{NodeDown, false, Lagging},
				{NodeDown, true, NodeDown},
			},
		},
		{
			name:    "down_for: recovery takes For",
			start:   Target{Current: NodeDown},
			For:     2,
			DownFor: 10,
			steps: []step{
				{OK, false, NodeDown},
				{OK, true, OK},
			},
		},
		{
			name:  "For 0 behaves like 1",
			start: Target{Current: OK},
			steps: []step{{NodeDown, true, NodeDown}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur := tt.start
			for i, s := range tt.steps {
				now := t0.Add(time.Duration(i) * time.Minute)
				next, alert := Step(cur, s.observed, Rule{For: tt.For, DownFor: tt.DownFor}, now)
				if alert != s.alert || next.Current != s.current {
					t.Fatalf("step %d (%s): got alert=%v current=%q, want alert=%v current=%q",
						i, s.observed, alert, next.Current, s.alert, s.current)
				}
				if next.Current != cur.Current && !next.Since.Equal(now) {
					t.Fatalf("step %d: Since not updated on change", i)
				}
				cur = next
			}
		})
	}
}

func TestErrorsAreClipped(t *testing.T) {
	long := errors.New(strings.Repeat("x", 5000))
	o := Observe(Sample{Node: Reading{Err: long}}, rule, false)
	if n := len([]rune(o.Err)); n != MaxErrLen || !strings.HasSuffix(o.Err, "…") {
		t.Fatalf("len = %d", n)
	}
}

// down_for keeps the last lag verdict, as NODE_DOWN always does.
func TestDownForKeepsHysteresis(t *testing.T) {
	r := Rule{Threshold: 6, Recover: 1, For: 1, DownFor: 2}
	now := time.Now()
	tgt, _ := Step(Target{Current: OK, LastLag: OK}, Lagging, r, now)
	for range 2 {
		tgt, _ = Step(tgt, NodeDown, r, now)
	}
	if tgt.Current != NodeDown || !tgt.Lagging() {
		t.Fatalf("got %+v, want NODE_DOWN with the lagging verdict kept", tgt)
	}
}

// A lagging node that restarts must not be reported in sync while its lag
// is still above the recover threshold.
func TestHysteresisSurvivesNodeDown(t *testing.T) {
	r := Rule{Threshold: 6, Recover: 1, For: 1}
	now := time.Now()
	tgt, _ := Step(Target{Current: OK, LastLag: OK}, Lagging, r, now)
	tgt, _ = Step(tgt, NodeDown, r, now)
	if !tgt.Lagging() {
		t.Fatal("NODE_DOWN must keep the last lag verdict")
	}
	o := Observe(Sample{Node: Reading{Blocks: 100, Headers: 100}, Refs: []Reading{{Source: "a", Blocks: 104}}}, r, tgt.Lagging())
	if o.State != Lagging {
		t.Fatalf("lag 4 > recover 1 after restart: got %s", o.State)
	}
	tgt, _ = Step(tgt, OK, r, now)
	if tgt.Lagging() {
		t.Fatal("OK must reset the lag verdict")
	}
}

func TestErrorTextIsSanitized(t *testing.T) {
	err := errors.Join(errors.New("a\x1b[31mred"), errors.New("b\r\nc"))
	o := Observe(Sample{Node: Reading{Err: err}}, rule, false)
	if o.Err != "a[31mred; b; c" {
		t.Fatalf("Err = %q", o.Err)
	}
}
