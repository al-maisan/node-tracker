// Command node-tracker watches how far blockchain nodes are behind the chain
// tip and alerts when a threshold is crossed in either direction.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/al-maisan/node-tracker/internal/app"
	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/health"
	"github.com/al-maisan/node-tracker/internal/monitor"
	"github.com/al-maisan/node-tracker/internal/notify"
	"github.com/al-maisan/node-tracker/internal/service"
	"github.com/al-maisan/node-tracker/internal/state"
)

// Set at build time, see Makefile.
var (
	version   = "dev"
	commit    = ""
	buildTime = ""
)

// Exit codes.
const (
	exitOK        = 0
	exitFailure   = 1
	exitUsage     = 2
	exitLocked    = 3
	exitNotInSync = 4
)

const usage = `Usage: node-tracker <command> [flags]

Commands:
  setup            interactive wizard: write, test and install the config
  run              run as a daemon, one tick every interval
  once             run one tick and exit (for cron or a systemd timer)
  check            dry run: sample all targets, print a table and a verdict; no state, no alerts
  report           send a status report of all targets to every notifier
  test-notify      send a test message, to one notifier [name] or all
  install-service  write and enable the systemd unit (-user or -system)
  version          print version information

Run 'node-tracker <command> -h' for the command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// detectEnv is where the local bitcoind is looked for; tests fake it.
var detectEnv = config.SystemEnv()

type options struct {
	config, state, logFormat, logLevel string
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, versionString())
		return exitOK
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "install-service":
		return installService(ctx, args, stdout, stderr)
	case "setup":
		return setupCmd(ctx, args, stdout, stderr)
	case "run", "once", "check", "report", "test-notify":
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}

	var o options
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.config, "config", "", "config file (default $XDG_CONFIG_HOME/node-tracker/config.yaml, else ~/.config/node-tracker/config.yaml)")
	if cmd == "run" || cmd == "once" {
		fs.StringVar(&o.state, "state", "", "state file (default: state_file from config, else $XDG_STATE_HOME/node-tracker/state.json)")
	}
	fs.StringVar(&o.logFormat, "log-format", "text", "log format: text or json")
	fs.StringVar(&o.logLevel, "log-level", "info", "log level: debug, info, warn or error")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	for _, p := range []*string{&o.config, &o.state} {
		if *p != "" {
			abs, err := filepath.Abs(*p)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return exitUsage
			}
			*p = abs
		}
	}
	if cmd != "test-notify" && fs.NArg() > 0 || fs.NArg() > 1 {
		fmt.Fprintf(stderr, "%s: unexpected arguments %v\n", cmd, fs.Args())
		return exitUsage
	}

	log, err := newLogger(stderr, o.logFormat, o.logLevel)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	// once, report and test-notify run from cron and timers: keep it quiet.
	detectLevel := slog.LevelDebug
	if cmd == "run" || cmd == "check" {
		detectLevel = slog.LevelInfo
	}
	cfg, m, code := setup(o, log, detectLevel, stderr)
	if m == nil {
		return code
	}

	switch cmd {
	case "check":
		return check(ctx, m, cfg, stdout, log)
	case "report":
		return report(ctx, m, cfg, log)
	case "test-notify":
		return testNotify(ctx, m, cfg, fs.Arg(0), log)
	default:
		return tick(ctx, cmd, m, cfg, o, log)
	}
}

func installService(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var o service.Options
	var user bool
	fs := flag.NewFlagSet("install-service", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&user, "user", false, "user service: ~/.config/systemd/user, no root needed")
	fs.BoolVar(&o.System, "system", false, "system service: /etc/systemd/system, needs root")
	fs.BoolVar(&o.Timer, "timer", false, "install node-tracker-once.timer instead of the daemon")
	fs.BoolVar(&o.DryRun, "dry-run", false, "print the files and commands, change nothing")
	fs.BoolVar(&o.Force, "force", false, "replace a changed unit or config, keeping a .bak")
	fs.StringVar(&o.Config, "config", "", "-user: config for the unit (default $XDG_CONFIG_HOME/node-tracker/config.yaml); -system: config to install to /etc/node-tracker")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if user == o.System || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "install-service: give one of -user or -system, and no arguments")
		return exitUsage
	}
	if o.Config != "" {
		abs, err := filepath.Abs(o.Config)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		o.Config = abs
	}
	if err := service.Install(ctx, service.NewHost(stdout, stderr), o); err != nil {
		fmt.Fprintf(stderr, "node-tracker: install-service: %v\n", err)
		if errors.As(err, new(service.UsageError)) {
			return exitUsage
		}
		return exitFailure
	}
	return exitOK
}

// setup loads the config and builds the monitor. Config errors go to stderr
// as plain multi-line text, which reads better than a log line.
func setup(o options, log *slog.Logger, detectLevel slog.Level, stderr io.Writer) (*config.Config, *monitor.Monitor, int) {
	path := o.config
	if path == "" {
		var err error
		if path, err = config.DefaultConfigPath(); err != nil {
			log.Error("no config path", "err", err)
			return nil, nil, exitUsage
		}
	}
	cfg, warns, err := config.LoadEnv(path, detectEnv)
	for _, w := range warns {
		log.Warn("config", "warning", w)
	}
	if err != nil {
		fmt.Fprintf(stderr, "node-tracker: %v\n", err)
		return nil, nil, exitUsage
	}
	if d := cfg.Detected; d != nil {
		log.Log(context.Background(), detectLevel, "detected local bitcoind", "conf", cmp.Or(d.Conf, "none"), "chain", d.Chain, "url", d.URL, "cookie_file", d.CookieFile)
	}
	for _, t := range cfg.Targets {
		if t.DefaultRefs && len(t.References) > 0 {
			var urls []string
			for _, r := range t.References {
				urls = append(urls, r.URL)
			}
			log.Log(context.Background(), detectLevel, "using public references; set references to choose others, or [] for none", "target", t.Name, "references", strings.Join(urls, ","))
		}
	}
	m, err := app.Build(cfg, log)
	if err != nil {
		log.Error("building monitor failed", "err", err)
		return nil, nil, exitUsage
	}
	return cfg, m, exitOK
}

// tick runs `run` or `once`.
func tick(ctx context.Context, cmd string, m *monitor.Monitor, cfg *config.Config, o options, log *slog.Logger) int {
	path := o.state
	if path == "" {
		path = cfg.StateFile
	}
	if path == "" {
		var err error
		if path, err = config.DefaultStatePath(); err != nil {
			log.Error("no state path", "err", err)
			return exitUsage
		}
	}
	s, err := state.Open(path)
	if errors.Is(err, state.ErrLocked) {
		log.Warn("another node-tracker is running on this state file, exiting", "state", path)
		return exitLocked
	}
	if err != nil {
		log.Error("opening state failed", "err", err)
		return exitFailure
	}
	defer s.Close()

	if cmd == "run" {
		log.Info("node-tracker started", "version", versionString(), "targets", len(m.Targets),
			"interval", cfg.Interval, "state", path)
		if err := m.Run(ctx, s, cfg.Interval, cfg.Interval/2); err != nil {
			log.Error("daemon failed", "err", err)
			return exitFailure
		}
		log.Info("node-tracker stopped")
		return exitOK
	}

	d, err := s.Load()
	if err != nil {
		log.Error("loading state failed", "err", err)
		return exitFailure
	}
	res, pending, err := m.Tick(ctx, d, 3*cfg.Timeout)
	if err != nil {
		log.Warn("interrupted", "err", err)
		return exitFailure
	}
	if err := s.Save(d); err != nil {
		log.Error("saving state failed", "err", err)
		return exitFailure
	}
	if pending > 0 {
		// Kept in the outbox for the next run, but make cron/systemd notice.
		log.Error("alerts not delivered yet", "pending", pending)
		return exitFailure
	}
	return syncCode(res)
}

func report(ctx context.Context, m *monitor.Monitor, cfg *config.Config, log *slog.Logger) int {
	res, err := m.Observe(ctx, nil)
	if err != nil {
		log.Warn("interrupted", "err", err)
		return exitFailure
	}
	sctx, cancel := context.WithTimeout(ctx, 3*cfg.Timeout)
	defer cancel()
	if err := m.Broadcast(sctx, notify.Report(time.Now(), res)); err != nil {
		log.Error("sending report failed", "err", err)
		return exitFailure
	}
	return syncCode(res)
}

func testNotify(ctx context.Context, m *monitor.Monitor, cfg *config.Config, name string, log *slog.Logger) int {
	if name != "" {
		n, ok := m.Notifiers[name]
		if !ok {
			log.Error("unknown notifier", "name", name)
			return exitUsage
		}
		m.Notifiers = map[string]notify.Notifier{name: n}
	}
	sctx, cancel := context.WithTimeout(ctx, 3*cfg.Timeout)
	defer cancel()
	if err := m.Broadcast(sctx, notify.Test(time.Now())); err != nil {
		log.Error("test message failed", "err", err)
		return exitFailure
	}
	return exitOK
}

// syncCode is exitNotInSync if any target was observed in a state other
// than OK, so cron and scripts can react.
func syncCode(res []monitor.Result) int {
	for _, r := range res {
		if r.State != health.OK {
			return exitNotInSync
		}
	}
	return exitOK
}

func newLogger(w io.Writer, format, level string) (*slog.Logger, error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid -log-level %q", level)
	}
	opts := &slog.HandlerOptions{Level: lv}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("invalid -log-format %q", format)
}

// versionString is e.g. "node-tracker v1.0.0 (a1b2c3d, built 2026-11-01T08:00:00Z, go1.26.10)"
// for `make build`, which injects version, commit and build time.
func versionString() string {
	bi, _ := debug.ReadBuildInfo()
	return formatVersion(version, commit, buildTime, bi, runtime.Version())
}

// pseudoRev matches the commit hash at the end of a pseudo-version such as
// v0.0.0-20261101080000-a1b2c3d4e5f6.
var pseudoRev = regexp.MustCompile(`[-.]\d{14}-([0-9a-f]{12})(?:\+[0-9A-Za-z.-]+)?$`)

// formatVersion fills what the ldflags left empty from the build info (bi may
// be nil): the module version (go install …@vX.Y.Z), the VCS revision of a
// checkout, or the hash in a pseudo-version. Unknown parts are left out.
func formatVersion(v, c, bt string, bi *debug.BuildInfo, goVersion string) string {
	var goInstall bool
	if bi != nil {
		if v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
		if c == "" {
			var dirty bool
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					c = s.Value[:min(7, len(s.Value))]
				case "vcs.modified":
					dirty = s.Value == "true"
				}
			}
			if dirty && c != "" {
				c += "-dirty"
			}
			// No VCS data: built from the module cache, i.e. go install.
			if c == "" && v == bi.Main.Version {
				goInstall = true
				if m := pseudoRev.FindStringSubmatch(v); m != nil {
					c = m[1][:7]
				}
			}
		}
	}
	var parts []string
	if c = strings.TrimSpace(c); c != "" {
		parts = append(parts, c)
	}
	if bt = strings.TrimSpace(bt); bt != "" {
		parts = append(parts, "built "+bt)
	}
	if goInstall {
		parts = append(parts, "go install")
	}
	return fmt.Sprintf("node-tracker %s (%s)", v, strings.Join(append(parts, goVersion), ", "))
}
