// Package notify defines alert events, the Notifier interface and delivery
// with retries.
package notify

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/al-maisan/node-tracker/internal/health"
)

// Event is a debounced state change of a target. It is plain data so it can
// be stored in the outbox as JSON; it is rendered only when sent.
type Event struct {
	Target  string       `json:"target"`
	From    health.State `json:"from"`
	To      health.State `json:"to"`
	Lag     uint64       `json:"lag"`
	Node    uint64       `json:"node"`
	Tip     uint64       `json:"tip"`
	TipFrom string       `json:"tip_from,omitempty"`
	Err     string       `json:"err,omitempty"` // already free of secrets
	At      time.Time    `json:"at"`
}

// Message is what a notifier delivers: a one-line subject and a plain-text
// body. Notifiers decide how to present it (bold title, email subject, ...).
type Message struct {
	Subject string
	Body    string
}

// Notifier delivers messages to humans. Implementations must honour ctx.
type Notifier interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// RetryAfter is returned by notifiers whose service asked to back off.
type RetryAfter struct {
	After time.Duration
	Err   error
}

func (e *RetryAfter) Error() string { return fmt.Sprintf("%v (retry after %s)", e.Err, e.After) }
func (e *RetryAfter) Unwrap() error { return e.Err }

type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// Permanent marks err as not worth retrying (e.g. a rejected bot token).
func Permanent(err error) error { return permanent{err} }

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool { return errors.As(err, new(permanent)) }

// Retry is a bounded exponential backoff policy.
type Retry struct {
	Attempts int           // total tries, >= 1
	Base     time.Duration // first backoff
	Max      time.Duration // backoff cap
}

// DefaultRetry suits chat APIs: 4 tries within ~7s of backoff.
var DefaultRetry = Retry{Attempts: 4, Base: time.Second, Max: 4 * time.Second}

// Send delivers m, retrying transient failures. It stops early when ctx ends,
// when an error is permanent, or when a requested back-off would outlive ctx.
func Send(ctx context.Context, n Notifier, m Message, r Retry) error {
	var err error
	tries := max(r.Attempts, 1)
	for attempt := range tries {
		if err = n.Send(ctx, m); err == nil || IsPermanent(err) {
			return err
		}
		if attempt == tries-1 {
			break
		}
		wait := min(r.Base<<attempt, r.Max)
		wait = wait/2 + rand.N(wait/2+1) // jitter: [wait/2, wait]
		if ra := (*RetryAfter)(nil); errors.As(err, &ra) {
			wait = ra.After
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
			break
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(wait):
		}
	}
	return err
}
