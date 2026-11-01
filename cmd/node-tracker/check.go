package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"text/tabwriter"

	"github.com/al-maisan/node-tracker/internal/app"
	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/explain"
	"github.com/al-maisan/node-tracker/internal/health"
	"github.com/al-maisan/node-tracker/internal/monitor"
	"github.com/al-maisan/node-tracker/internal/notify/telegram"
)

// tokenChecker is a notifier that can validate its credentials without
// sending anything.
type tokenChecker interface {
	CheckToken(ctx context.Context) (string, error)
}

// check is checkWarns without config warnings, for setup, which prints them
// itself.
func check(ctx context.Context, m *monitor.Monitor, cfg *config.Config, w io.Writer, log *slog.Logger) int {
	return checkWarns(ctx, m, cfg, nil, w, log)
}

// checkWarns prints the observed state of every target with hints for node
// and reference errors, the proxies if any endpoint uses one (or at debug
// level), and a verdict on the setup that lists the config warnings. It
// warns about references on another chain than the node's.
func checkWarns(ctx context.Context, m *monitor.Monitor, cfg *config.Config, warns []string, w io.Writer, log *slog.Logger) int {
	samples, err := m.Sample(ctx)
	if err != nil {
		log.Warn("interrupted", "err", err)
		return exitFailure
	}
	res := m.Classify(samples, nil)
	for i, s := range samples {
		for _, r := range s.Refs {
			if health.MismatchedChain(s.Node, r) {
				log.Warn("reference is on another chain than the node, so it is ignored; set references for the node's chain (see docs/configuration.md)",
					"target", cfg.Targets[i].Name, "node_chain", s.Node.Chain, "reference", r.Source, "reference_chain", r.Chain)
			}
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tSTATE\tNODE\tTIP\tLAG\tTIP FROM\tERROR")
	for i, r := range res {
		msg := r.Err
		switch r.State {
		case health.NodeDown:
			e := cfg.Targets[i].Node // r.Err is clipped: keep it
			if h := explain.Hint(e, app.Proxy(e) != "none", samples[i].Node.Err); h != "" {
				msg += " (" + h + ")"
			}
		case health.NoReference:
			msg += " (" + explain.References(r.Err) + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n", r.Target, r.State, r.Node, r.Tip, r.Lag, r.TipFrom, msg)
	}
	tw.Flush()

	var proxies [][3]string
	anyProxy := log.Enabled(ctx, slog.LevelDebug)
	for _, t := range cfg.Targets {
		for _, e := range append([]config.Endpoint{t.Node}, t.References...) {
			p := app.Proxy(e)
			anyProxy = anyProxy || p != "none"
			proxies = append(proxies, [3]string{t.Name, e.Name, p})
		}
	}
	if anyProxy {
		fmt.Fprintln(w)
		fmt.Fprintln(tw, "TARGET\tENDPOINT\tPROXY")
		for _, p := range proxies {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", p[0], p[1], p[2])
		}
		tw.Flush()
	}

	fmt.Fprintln(w)
	switch len(warns) {
	case 0:
		fmt.Fprintln(tw, "config\tok")
	case 1:
		fmt.Fprintf(tw, "config\tok, 1 warning: %s\n", warns[0])
	default:
		fmt.Fprintf(tw, "config\tok, %d warnings:\n", len(warns))
		for _, wn := range warns {
			fmt.Fprintf(tw, "\t- %s\n", wn)
		}
	}
	for i, t := range cfg.Targets {
		fmt.Fprintf(tw, "target %s\t%s\n", t.Name, targetVerdict(t, samples[i], res[i]))
	}
	for _, n := range cfg.Notifiers {
		fmt.Fprintf(tw, "notifier %s\t%s\n", n.Name, notifierVerdict(ctx, m, cfg, n.Name))
	}
	tw.Flush()
	return syncCode(res)
}

func targetVerdict(t config.Target, s health.Sample, r monitor.Result) string {
	node := "node reachable"
	switch {
	case s.Node.Err != nil:
		node = "node not reachable (see ERROR)"
	case t.Node.CookieFile != "":
		node += " (cookie)"
	case t.Node.Username != "":
		node += " (username/password)"
	}
	ok := 0
	for _, ref := range s.Refs {
		if ref.Err == nil && !health.MismatchedChain(s.Node, ref) {
			ok++
		}
	}
	v := fmt.Sprintf("%s · references %d/%d", node, ok, len(s.Refs))
	switch r.State {
	case health.Lagging:
		v += fmt.Sprintf(" · LAGGING by %d is the node's current state, not a setup error", r.Lag)
	case health.OK:
		v += " · OK is the node's current state"
	}
	return v
}

func notifierVerdict(ctx context.Context, m *monitor.Monitor, cfg *config.Config, name string) string {
	run := "→ run 'node-tracker test-notify " + name + "'"
	tc, ok := m.Notifiers[name].(tokenChecker)
	if !ok {
		return "not tested " + run
	}
	cctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	user, err := tc.CheckToken(cctx)
	switch {
	case errors.Is(err, telegram.ErrBadToken):
		return explain.Printable(err.Error()) + " · check bot_token_file / bot_token_env"
	case err != nil:
		return "token not checked: " + explain.Telegram(err)
	case !explain.BotUser.MatchString(user):
		return "token not checked: unexpected bot username " + strconv.Quote(explain.Printable(user))
	}
	return "token valid (@" + user + ") · chat not tested " + run
}
