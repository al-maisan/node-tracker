// Package health holds the pure decision logic: it turns one sample of a
// target into a health state and debounces state changes. It does no I/O.
package health

import (
	"errors"
	"time"
)

// MaxErrLen caps error text taken from sources (it can come from untrusted
// nodes) before it reaches the state file, logs and alerts.
const MaxErrLen = 300

// State is the health of one target.
type State string

const (
	Unknown     State = ""             // not established yet (first run)
	OK          State = "OK"           // lag <= threshold
	Lagging     State = "LAGGING"      // lag > threshold
	NodeDown    State = "NODE_DOWN"    // the node could not be queried
	NoReference State = "NO_REFERENCE" // node is up, every reference failed
)

// Rule configures how a target is judged.
type Rule struct {
	Threshold uint64 // LAGGING when lag > Threshold
	Recover   uint64 // back to OK when lag <= Recover (Recover <= Threshold)
	For       int    // a new state must be seen For samples in a row (>= 1)
}

// Reading is the result of querying one source.
type Reading struct {
	Source  string
	Blocks  uint64
	Headers uint64
	Err     error
}

// Tip is the highest height the reading knows of.
func (r Reading) Tip() uint64 { return max(r.Blocks, r.Headers) }

// Sample is one round of readings for a target.
type Sample struct {
	Node Reading
	Refs []Reading
}

// Observation is how a single sample looks, before debouncing.
type Observation struct {
	State      State
	Lag        uint64
	NodeSource string // name of the node source
	Node       uint64 // node height (validated blocks)
	Tip        uint64
	TipFrom    string // source that set the tip
	Err        string // why the state is NODE_DOWN / NO_REFERENCE
}

// Observe classifies a sample. lagging tells whether the target's last
// lag verdict was LAGGING; it selects the threshold (hysteresis: a lagging
// target recovers only once lag <= r.Recover, even across a NODE_DOWN).
//
// Precedence: NODE_DOWN, then LAGGING (also when the lag is only known from
// the node's own headers), then NO_REFERENCE, then OK.
func Observe(s Sample, r Rule, lagging bool) Observation {
	if s.Node.Err != nil {
		return Observation{State: NodeDown, NodeSource: s.Node.Source, Err: clip(s.Node.Err.Error())}
	}
	o := Observation{NodeSource: s.Node.Source, Node: s.Node.Blocks, Tip: s.Node.Tip(), TipFrom: s.Node.Source}
	var errs []error
	for _, ref := range s.Refs {
		if ref.Err != nil {
			errs = append(errs, ref.Err)
			continue
		}
		if ref.Tip() > o.Tip {
			o.Tip, o.TipFrom = ref.Tip(), ref.Source
		}
	}
	if o.Tip > o.Node {
		o.Lag = o.Tip - o.Node
	}
	limit := r.Threshold
	if lagging {
		limit = r.Recover
	}
	if len(s.Refs) > 0 && len(errs) == len(s.Refs) {
		o.Err = clip(errors.Join(errs...).Error())
		o.State = NoReference
	}
	if o.Lag > limit {
		o.State = Lagging
	} else if o.State == "" {
		o.State = OK
	}
	return o
}

func clip(s string) string {
	r := []rune(s)
	if len(r) <= MaxErrLen {
		return s
	}
	return string(r[:MaxErrLen-1]) + "…"
}

// Target is the persisted, debounced state of one target.
type Target struct {
	Current   State     `json:"current"`
	Since     time.Time `json:"since,omitzero"`
	Candidate State     `json:"candidate,omitempty"`
	Seen      int       `json:"seen,omitempty"`
	LastLag   State     `json:"last_lag,omitempty"` // last debounced OK/LAGGING verdict, for hysteresis
}

// Lagging reports whether the last lag verdict was LAGGING.
func (t Target) Lagging() bool { return t.LastLag == Lagging }

// Step feeds one observed state into the debouncer. It returns the new
// target state and whether a change must be alerted. The very first state of
// a target is set silently when it is OK.
func Step(t Target, observed State, r Rule, now time.Time) (next Target, alert bool) {
	if observed == t.Current {
		return Target{Current: t.Current, Since: t.Since, LastLag: t.LastLag}, false
	}
	if observed == t.Candidate {
		t.Seen++
	} else {
		t.Candidate, t.Seen = observed, 1
	}
	if t.Seen < max(r.For, 1) {
		return t, false
	}
	alert = !(t.Current == Unknown && observed == OK)
	next = Target{Current: observed, Since: now, LastLag: t.LastLag}
	if observed == OK || observed == Lagging {
		next.LastLag = observed
	}
	return next, alert
}
