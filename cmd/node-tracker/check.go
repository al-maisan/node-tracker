package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/al-maisan/node-tracker/internal/app"
	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/health"
	"github.com/al-maisan/node-tracker/internal/monitor"
	"github.com/al-maisan/node-tracker/internal/notify/telegram"
	"github.com/al-maisan/node-tracker/internal/source/bitcoind"
)

// tokenChecker is a notifier that can validate its credentials without
// sending anything.
type tokenChecker interface {
	CheckToken(ctx context.Context) (string, error)
}

// check prints the observed state of every target with hints for node
// errors, the proxies if any endpoint uses one (or at debug level), and a
// verdict on the setup.
func check(ctx context.Context, m *monitor.Monitor, cfg *config.Config, w io.Writer, log *slog.Logger) int {
	samples, err := m.Sample(ctx)
	if err != nil {
		log.Warn("interrupted", "err", err)
		return exitFailure
	}
	res := m.Classify(samples, nil)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tSTATE\tNODE\tTIP\tLAG\tTIP FROM\tERROR")
	for i, r := range res {
		msg := r.Err
		if r.State == health.NodeDown {
			if h := hint(cfg.Targets[i].Node, samples[i].Node.Err); h != "" {
				msg += " (" + h + ")"
			}
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
	fmt.Fprintln(tw, "config\tok")
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
		if ref.Err == nil {
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
		return strings.TrimPrefix(err.Error(), "telegram: ") + " · check bot_token_file / bot_token_env"
	case err != nil:
		return "token not checked: " + err.Error()
	}
	return "token valid (@" + user + ") · chat not tested " + run
}

// hint suggests a likely cause of a node error, or returns "".
func hint(e config.Endpoint, err error) string {
	var auth *bitcoind.AuthError
	var rpc *bitcoind.RPCError
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED) && app.Proxy(e) != "none":
		return "is the proxy running? for .onion: is Tor running?"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "is bitcoind running with server=1? RPC ports: main 8332, testnet3 18332, testnet4 48332, signet 38332, regtest 18443"
	case errors.As(err, &auth) && auth.Status == 401:
		return "wrong username/password, or a stale cookie"
	case errors.As(err, &auth):
		return "not allowed: check rpcallowip, and rpcwhitelist for getblockchaininfo"
	case errors.As(err, &rpc) && rpc.Code == bitcoind.ErrWarmup:
		return "bitcoind is starting; try again when it has loaded"
	case (errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()) && isOnion(e.URL):
		return "is Tor running and bootstrapped? .onion calls can be slow"
	}
	return ""
}

func isOnion(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Hostname()), ".onion")
}
