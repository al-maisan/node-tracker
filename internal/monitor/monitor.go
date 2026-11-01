// Package monitor runs ticks: sample every target, update the debounced
// state, queue alerts in the outbox and deliver them.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/al-maisan/node-tracker/internal/health"
	"github.com/al-maisan/node-tracker/internal/notify"
	"github.com/al-maisan/node-tracker/internal/source"
	"github.com/al-maisan/node-tracker/internal/state"
)

// Target is one watched node.
type Target struct {
	Name   string
	Node   source.Source
	Refs   []source.Source
	Rule   health.Rule
	Notify []string // notifier names
}

// Monitor holds the wiring. All fields are required except Now.
type Monitor struct {
	Targets     []Target
	Notifiers   map[string]notify.Notifier
	Timeout     time.Duration // per source call, unless set with source.WithTimeout
	Retry       notify.Retry
	OutboxLimit int // pending events per notifier
	Log         *slog.Logger
	Now         func() time.Time // defaults to time.Now
}

// Result is what one tick saw for a target.
type Result = notify.Row

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Observe samples every target in parallel and classifies the samples
// against the states in d (nil: no history). It changes nothing. It returns
// ctx's error if ctx ended while sampling: such samples are meaningless.
func (m *Monitor) Observe(ctx context.Context, d *state.Data) ([]Result, error) {
	samples, err := m.Sample(ctx)
	if err != nil {
		return nil, err
	}
	return m.Classify(samples, d), nil
}

// Sample reads every target's sources in parallel, in the order of
// m.Targets. It returns ctx's error if ctx ended while sampling.
func (m *Monitor) Sample(ctx context.Context) ([]health.Sample, error) {
	samples := make([]health.Sample, len(m.Targets))
	var wg sync.WaitGroup
	for i, t := range m.Targets {
		wg.Go(func() { samples[i] = m.sample(ctx, t) })
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

// Classify judges the samples from Sample against the states in d (nil: no
// history).
func (m *Monitor) Classify(samples []health.Sample, d *state.Data) []Result {
	res := make([]Result, len(m.Targets))
	for i, t := range m.Targets {
		lagging := d != nil && d.Targets[t.Name].Lagging()
		o := health.Observe(samples[i], t.Rule, lagging)
		res[i] = Result{Target: t.Name, Observation: o}
		if o.Outliers != "" {
			m.Log.Warn("ignoring implausibly high reference", "target", t.Name, "references", o.Outliers)
		}
		m.Log.Debug("sample", "target", t.Name, "observed", o.State, "lag", o.Lag,
			"node", o.Node, "tip", o.Tip, "tip_from", o.TipFrom)
	}
	return res
}

func (m *Monitor) sample(ctx context.Context, t Target) health.Sample {
	read := func(s source.Source) health.Reading {
		cctx, cancel := context.WithTimeout(ctx, source.Timeout(s, m.Timeout))
		defer cancel()
		h, err := s.Height(cctx)
		if err != nil && ctx.Err() == nil { // don't log failures caused by shutdown
			m.Log.Warn("source failed", "target", t.Name, "source", s.Name(), "err", err)
		}
		return health.Reading{Source: s.Name(), Blocks: h.Blocks, Headers: h.Headers, Err: err}
	}
	s := health.Sample{Refs: make([]health.Reading, len(t.Refs))}
	var wg sync.WaitGroup
	wg.Go(func() { s.Node = read(t.Node) })
	for i, r := range t.Refs {
		wg.Go(func() { s.Refs[i] = read(r) })
	}
	wg.Wait()
	return s
}

// Tick runs one full cycle on d: observe, step the debouncer, queue alerts,
// then deliver the outbox within deliverBy. It returns what was observed and
// how many deliveries are still pending. If ctx ends while sampling, d is
// left untouched and ctx's error is returned.
func (m *Monitor) Tick(ctx context.Context, d *state.Data, deliverBy time.Duration) (res []Result, pending int, err error) {
	m.Prune(d)
	if res, err = m.Observe(ctx, d); err != nil {
		return nil, 0, err
	}
	now := m.now()
	for i, t := range m.Targets {
		o := res[i].Observation
		prev := d.Targets[t.Name]
		next, alert := health.Step(prev, o.State, t.Rule, now)
		d.Targets[t.Name] = next
		if next.Current == prev.Current {
			continue
		}
		m.Log.Info("state changed", "target", t.Name, "from", prev.Current, "to", next.Current,
			"lag", o.Lag, "node", o.Node, "tip", o.Tip, "tip_from", o.TipFrom, "err", o.Err)
		if !alert {
			continue
		}
		e := notify.Event{Target: t.Name, From: prev.Current, To: next.Current, Lag: o.Lag,
			Node: o.Node, Tip: o.Tip, TipFrom: o.TipFrom, Err: o.Err, At: now}
		if n := d.Enqueue(e, t.Notify, m.OutboxLimit); n > 0 {
			m.Log.Error("outbox full, dropped oldest undelivered alerts", "dropped", n)
		}
	}
	dctx, cancel := context.WithTimeout(ctx, deliverBy)
	defer cancel()
	return res, m.Deliver(dctx, d), nil
}

// Deliver sends pending events and returns how many deliveries are still
// pending. Notifiers run in parallel; each sends its events in order and
// stops at the first transient failure, so order is preserved and the rest
// is retried next tick.
//
// A permanent failure is either about the channel (bad token: every message
// fails) or about one message (rejected content). To tell them apart without
// guessing from error texts, the next event is tried: if it is delivered,
// the failed one was the message's fault and is dropped, so it can't block
// the queue forever; if it fails too, the channel is broken and everything
// is kept.
func (m *Monitor) Deliver(ctx context.Context, d *state.Data) (pending int) {
	type done struct {
		notifier string
		idx      []int // delivered or dropped
	}
	results := make(chan done, len(m.Notifiers))
	var wg sync.WaitGroup
	for name, n := range m.Notifiers {
		wg.Go(func() {
			var ok []int
			defer func() { results <- done{name, ok} }()
			rejected := -1 // index of an event that failed permanently
			for i, p := range d.Outbox {
				if !slices.Contains(p.Notifiers, name) {
					continue
				}
				err := notify.Send(ctx, n, notify.Render(p.Event), m.Retry)
				switch {
				case err == nil:
					m.Log.Info("alert delivered", "notifier", name, "target", p.Event.Target, "to", p.Event.To)
					ok = append(ok, i)
					if rejected >= 0 {
						r := d.Outbox[rejected].Event
						m.Log.Error("dropping alert the notifier rejects", "notifier", name,
							"target", r.Target, "to", r.To, "at", r.At)
						ok, rejected = append(ok, rejected), -1
					}
				case notify.IsPermanent(err) && rejected < 0:
					m.Log.Error("alert rejected, trying the next one", "notifier", name,
						"target", p.Event.Target, "to", p.Event.To, "err", err)
					rejected = i
				default:
					m.Log.Error("alert delivery failed, will retry next tick", "notifier", name,
						"target", p.Event.Target, "to", p.Event.To, "err", err)
					return
				}
			}
		})
	}
	wg.Wait()
	close(results)
	for r := range results {
		for _, i := range r.idx {
			p := &d.Outbox[i]
			p.Notifiers = slices.DeleteFunc(p.Notifiers, func(s string) bool { return s == r.notifier })
		}
	}
	d.Compact()
	for _, p := range d.Outbox {
		pending += len(p.Notifiers)
	}
	return pending
}

// Broadcast sends msg through every notifier now, bypassing the outbox. It is
// used for status reports and test messages, which are worthless if late.
func (m *Monitor) Broadcast(ctx context.Context, msg notify.Message) error {
	errs := make([]error, 0, len(m.Notifiers))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, n := range m.Notifiers {
		wg.Go(func() {
			if err := notify.Send(ctx, n, msg, m.Retry); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				mu.Unlock()
				return
			}
			m.Log.Info("message delivered", "notifier", name, "subject", msg.Subject)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Prune drops state of targets and outbox entries of notifiers that are no
// longer configured.
func (m *Monitor) Prune(d *state.Data) {
	for name := range d.Targets {
		if !slices.ContainsFunc(m.Targets, func(t Target) bool { return t.Name == name }) {
			m.Log.Info("dropping state of removed target", "target", name)
			delete(d.Targets, name)
		}
	}
	for i := range d.Outbox {
		d.Outbox[i].Notifiers = slices.DeleteFunc(d.Outbox[i].Notifiers, func(n string) bool {
			if _, ok := m.Notifiers[n]; ok {
				return false
			}
			m.Log.Warn("dropping alert for removed notifier", "notifier", n, "target", d.Outbox[i].Event.Target)
			return true
		})
	}
	d.Compact()
}

// Run is the daemon: a tick right away, then one every interval, saving the
// state after each. It returns nil when ctx ends.
func (m *Monitor) Run(ctx context.Context, s *state.Store, interval, deliverBy time.Duration) error {
	d, err := s.Load()
	if err != nil {
		return err
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		// Tick fails only when ctx ended mid-sample; d is then unchanged.
		if _, _, err := m.Tick(ctx, d, deliverBy); err == nil {
			if err := s.Save(d); err != nil {
				// Keep going: in-memory state stays correct; the next save may work.
				m.Log.Error("saving state failed", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
		}
	}
}
