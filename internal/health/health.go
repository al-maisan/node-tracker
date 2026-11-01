// Package health holds the pure decision logic: it turns one sample of a
// target into a health state and debounces state changes. It does no I/O.
package health

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
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
	NoReference State = "NO_REFERENCE" // node is up, no independent reference answered
)

// Rule configures how a target is judged.
type Rule struct {
	Threshold uint64 // LAGGING when lag > Threshold
	Recover   uint64 // back to OK when lag <= Recover (Recover <= Threshold)
	For       int    // a change must be seen For samples in a row (>= 1)
	DownFor   int    // a change to NODE_DOWN needs this many instead; 0 = For
	Outlier   uint64 // with 3+ references, ignore one more than this many blocks above all others; 0 = off
}

// Reading is the result of querying one source.
type Reading struct {
	Source  string
	Blocks  uint64
	Headers uint64
	Chain   string
	Err     error
	Local   bool // a reference on the node's host, likely fed by the node: no independent view
}

// MismatchedChain tells whether ref is known to be on another chain than
// node. Its heights mean nothing for node, so it counts as failed.
func MismatchedChain(node, ref Reading) bool {
	return node.Chain != "" && ref.Chain != "" && node.Chain != ref.Chain
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
	Err        string // why the state is NODE_DOWN / NO_REFERENCE; clipped, no control characters
	Outliers   string // references ignored as implausibly high, comma-separated
}

// Observe classifies a sample. lagging tells whether the target's last
// lag verdict was LAGGING; it selects the threshold (hysteresis: a lagging
// target recovers only once lag <= r.Recover, even across a NODE_DOWN).
//
// Precedence: NODE_DOWN, then LAGGING (also when the lag is only known from
// the node's own headers), then NO_REFERENCE, then OK. A reference on
// another chain than the node counts as failed. A Local reference may be
// stuck along with the node, so if the target has independent references,
// one of them must answer.
func Observe(s Sample, r Rule, lagging bool) Observation {
	if s.Node.Err != nil {
		return Observation{State: NodeDown, NodeSource: s.Node.Source, Err: clip(s.Node.Err.Error())}
	}
	o := Observation{NodeSource: s.Node.Source, Node: s.Node.Blocks, Tip: s.Node.Tip(), TipFrom: s.Node.Source}
	var errs []error
	var ok []Reading
	for _, ref := range s.Refs {
		if ref.Err != nil {
			errs = append(errs, ref.Err)
		} else if MismatchedChain(s.Node, ref) {
			errs = append(errs, fmt.Errorf("%s is on chain %s, the node on %s", ref.Source, ref.Chain, s.Node.Chain))
		} else {
			ok = append(ok, ref)
		}
	}
	// One broken or tampered reference must not pin the target in LAGGING:
	// a reference far above all other references is ignored. That needs at
	// least two others to outvote it; with two references that disagree,
	// the honest one can't be told apart (it may be stuck with the node, e.g.
	// a local electrs), so the higher one wins. The node's own headers are
	// not compared, since an isolated node's headers are behind.
	// Only independent references vote: Local ones may be stuck with the
	// node, and must not outvote an independent one that is ahead.
	byTip := func(a, b Reading) int { return cmp.Compare(b.Tip(), a.Tip()) }
	local := slices.DeleteFunc(slices.Clone(ok), func(r Reading) bool { return !r.Local })
	ok = slices.DeleteFunc(ok, func(r Reading) bool { return r.Local })
	slices.SortStableFunc(ok, byTip)
	var outliers []string
	for r.Outlier > 0 && len(ok) >= 3 && ok[0].Tip()-ok[1].Tip() > r.Outlier {
		outliers, ok = append(outliers, ok[0].Source), ok[1:]
	}
	o.Outliers = strings.Join(outliers, ",")
	ok = append(ok, local...)
	slices.SortStableFunc(ok, byTip)
	if len(ok) > 0 && ok[0].Tip() > o.Tip {
		o.Tip, o.TipFrom = ok[0].Tip(), ok[0].Source
	}
	if o.Tip > o.Node {
		o.Lag = o.Tip - o.Node
	}
	limit := r.Threshold
	if lagging {
		limit = r.Recover
	}
	independent := func(r Reading) bool { return !r.Local }
	switch {
	case len(s.Refs) > 0 && len(errs) == len(s.Refs):
		o.Err = clip(errors.Join(errs...).Error())
		o.State = NoReference
	case slices.ContainsFunc(s.Refs, independent) && !slices.ContainsFunc(ok, independent):
		o.Err = clip(errors.Join(append([]error{errors.New("only references on the node's host answered")}, errs...)...).Error())
		o.State = NoReference
	}
	if o.Lag > limit {
		o.State = Lagging
	} else if o.State == "" {
		o.State = OK
	}
	return o
}

// clip makes untrusted error text safe to store, log, print and send: line
// breaks become "; ", other control characters (terminal escapes) are
// dropped, and the length is capped.
func clip(s string) string {
	s = strings.NewReplacer("\r\n", "; ", "\n", "; ").Replace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > MaxErrLen {
		return string(r[:MaxErrLen-1]) + "…"
	}
	return s
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
//
// Samples count in two groups: OK, and "not OK" (LAGGING, NODE_DOWN,
// NO_REFERENCE). Bad states count together, so LAGGING, NODE_DOWN, LAGGING
// is as much "not OK" as LAGGING three times, and the target moves to the
// latest one. OK must be seen For times in a row on its own: one good
// sample after trouble is not recovery. A change to NODE_DOWN needs DownFor
// samples, if set: a node reached over Tor is unreachable for minutes at
// times.
func Step(t Target, observed State, r Rule, now time.Time) (next Target, alert bool) {
	if observed == t.Current {
		return Target{Current: t.Current, Since: t.Since, LastLag: t.LastLag}, false
	}
	if t.Seen > 0 && (observed == OK) == (t.Candidate == OK) {
		t.Seen++
	} else {
		t.Seen = 1
	}
	t.Candidate = observed
	need := max(r.For, 1)
	if observed == NodeDown && r.DownFor > 0 {
		need = r.DownFor
	}
	if t.Seen < need {
		return t, false
	}
	alert = !(t.Current == Unknown && observed == OK)
	next = Target{Current: observed, Since: now, LastLag: t.LastLag}
	if observed == OK || observed == Lagging {
		next.LastLag = observed
	}
	return next, alert
}
