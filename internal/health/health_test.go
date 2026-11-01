package health

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var rule = Rule{Threshold: 3, Recover: 1, For: 2}

func TestObserve(t *testing.T) {
	boom := errors.New("boom")
	node := func(b, h uint64) Reading { return Reading{Source: "node", Blocks: b, Headers: h} }
	ref := func(name string, h uint64) Reading { return Reading{Source: name, Blocks: h} }
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
		name  string
		start Target
		For   int
		steps []step
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
			name:  "candidate switch resets the counter",
			start: Target{Current: OK},
			For:   2,
			steps: []step{
				{Lagging, false, OK},
				{NodeDown, false, OK},
				{NodeDown, true, NodeDown},
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
				next, alert := Step(cur, s.observed, Rule{For: tt.For}, now)
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
