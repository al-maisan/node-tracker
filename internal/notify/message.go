package notify

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/al-maisan/node-tracker/internal/health"
)

var subjects = map[health.State]string{
	health.OK:          "🟢 %s is in sync",
	health.Lagging:     "🔴 %s is lagging",
	health.NodeDown:    "⚫ %s: node unreachable",
	health.NoReference: "🟡 %s: no reference reachable",
}

// Render turns a state change into a message.
func Render(e Event) Message {
	var b strings.Builder
	if e.To != health.NodeDown {
		fmt.Fprintf(&b, "Node height: %s\n", Num(e.Node))
		fmt.Fprintf(&b, "Network height: %s%s\n", Num(e.Tip), via(e.TipFrom))
		fmt.Fprintf(&b, "Lag: %d blocks\n", e.Lag)
	}
	if e.Err != "" {
		fmt.Fprintf(&b, "Error: %s\n", e.Err)
	}
	if e.From != health.Unknown {
		fmt.Fprintf(&b, "Was: %s\n", e.From)
	}
	fmt.Fprintf(&b, "At: %s", stamp(e.At))
	return Message{Subject: fmt.Sprintf(subjects[e.To], e.Target), Body: b.String()}
}

// Row is one target's line in a status report.
type Row struct {
	Target string
	health.Observation
}

// Report renders a status report of all targets, sent regardless of state.
func Report(at time.Time, rows []Row) Message {
	var b strings.Builder
	bad := 0
	for _, r := range rows {
		fmt.Fprintf(&b, "%s\n", r.Target)
		switch r.State {
		case health.NodeDown:
			bad++
			fmt.Fprintf(&b, "✗ node unreachable: %s\n\n", r.Err)
			continue
		case health.NoReference:
			bad++
			fmt.Fprintf(&b, "Network height: unknown, all references failed: %s\n", r.Err)
		default:
			fmt.Fprintf(&b, "Network height: %s%s\n", Num(r.Tip), via(r.TipFrom))
		}
		mark := "✓"
		if r.State == health.Lagging {
			bad++
		}
		if r.State != health.OK {
			mark = "✗"
		}
		fmt.Fprintf(&b, "%s %s: %s (%d behind)\n\n", mark, r.NodeSource, Num(r.Node), r.Lag)
	}
	if bad == 0 {
		b.WriteString("✅ All nodes in sync")
	} else {
		fmt.Fprintf(&b, "⚠️ %d of %d nodes need attention", bad, len(rows))
	}
	fmt.Fprintf(&b, "\nAt: %s", stamp(at))
	return Message{Subject: "🔗 Node sync status", Body: b.String()}
}

// Test is the message sent by `node-tracker test-notify`.
func Test(at time.Time) Message {
	return Message{Subject: "🔔 node-tracker test message", Body: "If you can read this, alerts will reach you.\nAt: " + stamp(at)}
}

// Num formats n with thousands separators: 938266 -> "938,266".
func Num(n uint64) string {
	s := strconv.FormatUint(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func via(src string) string {
	if src == "" {
		return ""
	}
	return " (" + src + ")"
}

func stamp(t time.Time) string { return t.UTC().Format(time.DateTime + " UTC") }
