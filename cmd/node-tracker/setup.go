package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/al-maisan/node-tracker/internal/app"
	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/explain"
	"github.com/al-maisan/node-tracker/internal/fsx"
	"github.com/al-maisan/node-tracker/internal/httpx"
	"github.com/al-maisan/node-tracker/internal/notify"
	"github.com/al-maisan/node-tracker/internal/notify/telegram"
	"github.com/al-maisan/node-tracker/internal/service"
	"github.com/al-maisan/node-tracker/internal/source"
	"github.com/al-maisan/node-tracker/internal/source/bitcoind"
	"github.com/al-maisan/node-tracker/internal/term"
)

// presets are the alert presets of docs/configuration.md.
var presets = []struct {
	name               string
	threshold, recover uint64
	ticks              int
	interval           time.Duration
}{
	{"fast", 2, 0, 2, 30 * time.Second},
	{"balanced", 3, 1, 2, 30 * time.Second},
	{"quiet", 6, 2, 3, 60 * time.Second},
}

const (
	onionDownWait = 5 * time.Minute // how long a .onion node must be unreachable for NODE_DOWN
	defaultPreset = 1               // balanced
	notifierName  = "telegram"
	maxSecret     = 4 << 10
)

var (
	rpcUserRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

	errInputEnded   = errors.New("input ended")
	errDeclined     = errors.New("nothing written")
	errRunAsUser    = errors.New("stopped to run setup as the user of bitcoind")
	errTelegramDown = errors.New("could not check the token with Telegram")
)

// setupHost is the machine setup works on; tests fake it.
type setupHost struct {
	in          io.Reader
	tty         bool                               // in is a terminal
	noEcho      func() (restore func(), err error) // turns off the echo of in
	euid        int
	goos        string
	etcDir      string                      // config and secrets of the system service
	group       func() (gid int, err error) // of service.Account
	install     func(ctx context.Context, out io.Writer, o service.Options) error
	cookies     func(cfg *config.Config) error // service.CheckCookies
	hostname    func() (string, error)
	userNodes   func() []userNode // bitcoinds run by users, see userNodes
	telegramAPI string
	torProxy    string        // offered when Telegram does not answer
	poll        time.Duration // one getUpdates long poll
	waitChat    time.Duration // for the tap on the /start link
}

var newSetupHost = func() setupHost {
	fd := os.Stdin.Fd()
	sh := service.NewHost(os.Stdout, os.Stderr)
	return setupHost{
		in:     os.Stdin,
		tty:    term.IsTerminal(fd),
		noEcho: func() (func(), error) { return term.NoEcho(fd) },
		euid:   os.Geteuid(),
		goos:   runtime.GOOS,
		etcDir: sh.EtcDir,
		group:  func() (int, error) { return sh.LookupGroup(service.Account) },
		install: func(ctx context.Context, out io.Writer, o service.Options) error {
			return service.Install(ctx, service.NewHost(out, out), o)
		},
		cookies:     func(cfg *config.Config) error { return service.CheckCookies(sh, cfg) },
		hostname:    os.Hostname,
		userNodes:   func() []userNode { return userNodes("/proc", user.LookupId) },
		telegramAPI: config.DefaultTelegramAPI,
		torProxy:    config.DefaultTorProxy,
		poll:        30 * time.Second,
		waitChat:    5 * time.Minute,
	}
}

// setupOptions are the setup flags. With -non-interactive they are the
// answers.
type setupOptions struct {
	config, name, nodeURL, cookieFile, rpcUser, rpcPasswordFile string
	proxy, references, preset, tokenFile, chatID, telegramProxy string
	nonInteractive, allowInsecure, installService, force        bool
	downFor                                                     int // 0: see answers.downFor
	trackerName                                                 string
}

func setupCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var o setupOptions
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: node-tracker setup [-config file] [-non-interactive flags]\n\n"+
			"Asks for the node, the references, the alert preset and the Telegram bot,\n"+
			"tests each answer, writes the config and its secrets, runs check and offers\n"+
			"install-service. With -non-interactive, the answers come from the flags.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	fs.StringVar(&o.config, "config", "", "config file to write (default $XDG_CONFIG_HOME/node-tracker/config.yaml; as root /etc/node-tracker/config.yaml); the secrets go next to it")
	fs.BoolVar(&o.nonInteractive, "non-interactive", false, "ask nothing: take the answers from the other flags")
	fs.StringVar(&o.trackerName, "tracker-name", "", "name of this tracker in front of every message, or none (default: the host name)")
	fs.StringVar(&o.name, "name", "", "target name in alerts (default: the host name for a local node, else node)")
	fs.StringVar(&o.nodeURL, "node-url", "", "bitcoind RPC URL (default: the detected local bitcoind)")
	fs.StringVar(&o.cookieFile, "cookie-file", "", "bitcoind's .cookie (default for the detected node: its cookie)")
	fs.StringVar(&o.rpcUser, "rpc-user", "", "RPC username, instead of the cookie")
	fs.StringVar(&o.rpcPasswordFile, "rpc-password-file", "", "file holding the password of -rpc-user")
	fs.StringVar(&o.proxy, "proxy", "", "proxy for the node, e.g. socks5://127.0.0.1:9150 (default: from the environment; for .onion "+config.DefaultTorProxy+")")
	fs.BoolVar(&o.allowInsecure, "allow-insecure", false, "allow plain http to a node that is not loopback or .onion (trusted LAN)")
	fs.StringVar(&o.references, "references", "", "Esplora API URLs, comma-separated, or none (default: the public ones of the node's chain)")
	fs.StringVar(&o.preset, "preset", presets[defaultPreset].name, "alert preset: fast, balanced or quiet")
	fs.StringVar(&o.tokenFile, "token-file", "", "file holding the Telegram bot token (required)")
	fs.StringVar(&o.chatID, "chat-id", "", "Telegram chat id or @channel (required)")
	fs.IntVar(&o.downFor, "down-for", 0, fmt.Sprintf("ticks a node must be unreachable before NODE_DOWN, 1-%d (default: as for the preset; for a .onion node about %s)", config.MaxFor, onionDownWait))
	fs.StringVar(&o.telegramProxy, "telegram-proxy", "", "proxy for Telegram, e.g. "+config.DefaultTorProxy+" (default: from the environment)")
	fs.BoolVar(&o.installService, "install-service", false, "then run install-service (-user, or -system as root)")
	fs.BoolVar(&o.force, "force", false, "replace existing files, keeping the old config as .bak")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "setup: unexpected arguments %v\n", fs.Args())
		return exitUsage
	}
	var answers []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "config" && f.Name != "non-interactive" {
			answers = append(answers, "-"+f.Name)
		}
	})
	if !o.nonInteractive && len(answers) > 0 {
		fmt.Fprintf(stderr, "setup: %s need -non-interactive\n", strings.Join(answers, ", "))
		return exitUsage
	}
	for _, p := range []*string{&o.config, &o.cookieFile, &o.rpcPasswordFile, &o.tokenFile} {
		if *p != "" {
			abs, err := filepath.Abs(*p)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return exitUsage
			}
			*p = abs
		}
	}
	h := newSetupHost()
	if !o.nonInteractive && !h.tty {
		fmt.Fprintln(stderr, "setup: standard input is not a terminal. Run it in a terminal, or with -non-interactive and the answers as flags (node-tracker setup -h)")
		return exitUsage
	}
	log, _ := newLogger(stderr, "text", "warn")
	w := &wizard{setupHost: h, o: o, out: stdout, log: log, hc: httpx.NewClient(), done: make(chan struct{})}
	defer close(w.done)
	code, err := w.run(ctx)
	if err != nil {
		switch {
		case errors.Is(err, errInputEnded), errors.Is(err, context.Canceled):
			fmt.Fprintf(stderr, "\nsetup aborted (%v)", err)
			if !w.wrote {
				fmt.Fprint(stderr, "; nothing written")
			}
			fmt.Fprintln(stderr)
			code = exitFailure
		case errors.Is(err, errRunAsUser):
			fmt.Fprintln(stdout, "Stopped; nothing written.")
			code = exitOK
		default:
			fmt.Fprintf(stderr, "node-tracker: setup: %v\n", err)
		}
	}
	return code
}

type wizard struct {
	setupHost
	o     setupOptions
	out   io.Writer
	log   *slog.Logger
	hc    *http.Client
	tg    *http.Client  // for Telegram, if not hc: see useTelegramProxy
	lines chan string   // the lines of in, see input
	done  chan struct{} // closed when setup ends
	wrote bool          // a file was written
	root  bool          // writing for the system service
	noSvc bool          // the system service can't use the node yet: no install offered
}

// answers is what goes into the config.
type answers struct {
	name       string
	node       config.Endpoint // as written; for username, without password_file
	password   config.Secret   // of node.Username
	refs       []config.Endpoint
	preset     int
	token      config.Secret
	chatID     string
	chatTested bool   // the test message went through
	tgProxy    string // of the notifier
	downFor    int    // -down-for; 0: see nodeDownFor
	tracker    string // config name: in front of every message; "" for none
}

func (w *wizard) run(ctx context.Context) (int, error) {
	root := w.euid == 0 && w.goos != "darwin"
	w.root = root
	path := w.o.config
	// install-service -system takes secrets only from etcDir, and setup
	// puts them next to the config.
	if root && path != "" && filepath.Dir(path) != filepath.Clean(w.etcDir) {
		return exitUsage, fmt.Errorf("-config %s: as root, setup writes the system service's config, which goes in %s; leave out -config, or run setup as your user", path, w.etcDir)
	}
	if path == "" {
		path = filepath.Join(w.etcDir, "config.yaml")
		if !root {
			var err error
			if path, err = config.DefaultConfigPath(); err != nil {
				return exitUsage, err
			}
		}
	}
	var a *answers
	var err error
	if w.o.nonInteractive {
		a, err = w.fromFlags(ctx)
	} else {
		a, err = w.interview(ctx, path)
	}
	if err != nil {
		return exitUsage, err
	}
	return w.finish(ctx, a, path, root)
}

// input is the lines of w.in, read ahead by one goroutine. It is closed at
// the end of the input.
func (w *wizard) input() <-chan string {
	if w.lines == nil {
		w.lines = make(chan string)
		go func() {
			defer close(w.lines)
			r := bufio.NewReader(w.in)
			for {
				l, err := r.ReadString('\n')
				if err != nil && l == "" {
					return
				}
				select {
				case w.lines <- strings.TrimSpace(l):
				case <-w.done:
					return
				}
			}
		}()
	}
	return w.lines
}

func (w *wizard) readLine(ctx context.Context) (string, error) {
	select {
	case l, ok := <-w.input():
		if !ok {
			return "", errInputEnded
		}
		return l, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// ask returns the answer to q, or def for an empty one.
func (w *wizard) ask(ctx context.Context, q, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(w.out, "%s [%s]: ", q, def)
	} else {
		fmt.Fprintf(w.out, "%s: ", q)
	}
	l, err := w.readLine(ctx)
	return cmp.Or(l, def), err
}

func (w *wizard) yes(ctx context.Context, q string, def bool) (bool, error) {
	opts := map[bool]string{true: "[Y/n]", false: "[y/N]"}[def]
	for {
		fmt.Fprintf(w.out, "%s %s ", q, opts)
		l, err := w.readLine(ctx)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(l) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// choose asks until the answer is one of the letters of opts; Enter is the
// first.
func (w *wizard) choose(ctx context.Context, q, opts string) (byte, error) {
	for {
		l, err := w.ask(ctx, q, opts[:1])
		if err != nil {
			return 0, err
		}
		if l = strings.ToLower(l); len(l) == 1 && strings.Contains(opts, l) {
			return l[0], nil
		}
	}
}

// secret reads a line with the terminal's echo off. Echo goes off before
// the prompt, so nothing typed after it shows.
func (w *wizard) secret(ctx context.Context, q string) (config.Secret, error) {
	restore, err := w.noEcho()
	if err != nil {
		return "", fmt.Errorf("turning off the terminal echo: %w", err)
	}
	fmt.Fprintf(w.out, "%s (input hidden): ", q)
	l, err := w.readLine(ctx)
	restore()
	fmt.Fprintln(w.out)
	return config.Secret(l), err
}

func (w *wizard) interview(ctx context.Context, path string) (*answers, error) {
	fmt.Fprintln(w.out, "node-tracker setup: a few questions, each answer is tested right away. Enter takes the [default].\nNothing is written before the end; Ctrl-C aborts.")
	a := &answers{preset: defaultPreset}

	fmt.Fprintln(w.out, "\n1. The node")
	chain, local, err := w.askNode(ctx, a)
	if err != nil {
		return nil, err
	}
	for def := defaultName(local, w.hostname); ; {
		if a.name, err = w.ask(ctx, "Name of this node in alerts", def); err != nil {
			return nil, err
		}
		err := config.CheckName(a.name)
		if err == nil {
			break
		}
		fmt.Fprintf(w.out, "The name %v\n", err)
	}

	fmt.Fprintln(w.out, "\n2. References: independent views of the chain tip")
	if a.refs, err = w.askRefs(ctx, chain, a.node.URL); err != nil {
		return nil, err
	}

	fmt.Fprintln(w.out, "\n3. Alerts")
	if a.preset, err = w.askPreset(ctx); err != nil {
		return nil, err
	}
	if n := a.nodeDownFor(); n != presets[a.preset].ticks {
		fmt.Fprintf(w.out, "The node is reached over Tor, which stalls now and then: \"node unreachable\" is sent only after %d checks in a row (about %s; alert.down_for).\n",
			n, time.Duration(n)*presets[a.preset].interval)
	}
	for {
		l, err := w.ask(ctx, "Name of this tracker in front of every alert, to tell several trackers apart (none for no name)", cmp.Or(hostName(w.hostname), "none"))
		if err != nil {
			return nil, err
		}
		if a.tracker, err = trackerName(l); err == nil {
			break
		}
		fmt.Fprintln(w.out, err)
	}

	fmt.Fprintln(w.out, "\n4. Telegram")
	if err := w.askTelegram(ctx, a, path); err != nil {
		return nil, err
	}
	return a, nil
}

// askNode finds the node and its auth, and tests them. It returns the
// node's chain ("" if untested) and whether the node is on this host.
func (w *wizard) askNode(ctx context.Context, a *answers) (chain string, local bool, err error) {
	d := w.detect()
	e := config.Endpoint{Kind: config.KindBitcoind, URL: d.URL, CookieFile: d.CookieFile}
	h, ce, err := w.testNode(ctx, e, "")
	if err == nil {
		fmt.Fprintf(w.out, "Found bitcoind at %s: %s, cookie %s\n", d.URL, heightInfo(h), d.CookieFile)
		ok, err := w.yes(ctx, "Watch this node?", true)
		if err != nil || ok {
			a.node = e
			return h.Chain, true, err
		}
	} else if ns := w.otherUserNodes(d.CookieFile); len(ns) == 0 {
		fmt.Fprintf(w.out, "No local bitcoind answered at %s with the cookie %s: %s\n", d.URL, d.CookieFile, explain.Node(ce, app.Proxy(ce) != "none", err))
	} else {
		fmt.Fprintf(w.out, "No system bitcoind answered at %s.\n", d.URL)
		w.userNodeHint(ns)
		stop, err := w.yes(ctx, "Stop here?", true)
		if err != nil {
			return "", false, err
		}
		if stop {
			return "", false, errRunAsUser
		}
		found := false // the first node with a cookie outside a home is the default
		for _, n := range ns {
			if n.inHome {
				fmt.Fprintf(w.out, "The system service can't read %s: it is in the home of %q.\n", n.cookie, n.user)
				continue
			}
			fmt.Fprintf(w.out, "For the system service, give the cookie %s; the %s user must be able to read it.\n", n.cookie, service.Account)
			if !found {
				e.URL, e.CookieFile, found = n.url, n.cookie, true
			}
		}
	}
	for {
		var pw config.Secret
		if e, pw, err = w.askEndpoint(ctx, e, d.CookieFile); err != nil {
			return "", false, err
		}
		local = isLoopback(e.URL)
		for {
			fmt.Fprint(w.out, "Testing the node … ")
			h, ce, err := w.testNode(ctx, e, pw)
			if err == nil {
				fmt.Fprintf(w.out, "ok: %s\n", heightInfo(h))
				a.node, a.password = e, pw
				return h.Chain, local, nil
			}
			fmt.Fprintf(w.out, "failed: %s\n", explain.Node(ce, app.Proxy(ce) != "none", err))
			c, err := w.choose(ctx, "[r]etry, [c]hange the settings, or [k]eep them untested?", "rck")
			if err != nil {
				return "", false, err
			}
			if c == 'k' {
				a.node, a.password = e, pw
				return "", local, nil
			}
			if c == 'c' {
				break
			}
		}
	}
}

// askEndpoint asks for the node's URL, proxy and auth, with prev's as the
// defaults.
func (w *wizard) askEndpoint(ctx context.Context, prev config.Endpoint, cookie string) (config.Endpoint, config.Secret, error) {
	var e, ce config.Endpoint
	for {
		u, err := w.ask(ctx, "Node RPC URL", prev.URL)
		if err != nil {
			return e, "", err
		}
		e = config.Endpoint{Kind: config.KindBitcoind, URL: u, AllowInsecure: prev.AllowInsecure && u == prev.URL}
		// The URL alone: the auth is asked for next.
		probe := e
		probe.Username, probe.Password = "-", "-"
		if ce, _, err = config.CheckEndpoint(probe, "node", config.NodeKinds); err == nil {
			break
		}
		probe.AllowInsecure = true
		if _, _, err2 := config.CheckEndpoint(probe, "node", config.NodeKinds); err2 != nil {
			fmt.Fprintln(w.out, err)
			continue
		}
		ok, err := w.yes(ctx, "Plain http to this host sends the RPC password unencrypted. Is the network to the node trusted (LAN, VPN)?", false)
		if err != nil {
			return e, "", err
		}
		if ok {
			e.AllowInsecure = true
			if ce, _, err = config.CheckEndpoint(probe, "node", config.NodeKinds); err != nil {
				return e, "", err
			}
			break
		}
	}

	if explain.IsOnion(e.URL) {
		def := effectiveProxy(ce)
		for {
			p, err := w.ask(ctx, "Tor SOCKS proxy", def)
			if err != nil {
				return e, "", err
			}
			// The service may lack this shell's proxy variables.
			e.Proxy = ""
			if p != config.DefaultTorProxy {
				e.Proxy = p
			}
			probe := e
			probe.Username, probe.Password = "-", "-"
			if _, _, err := config.CheckEndpoint(probe, "node", config.NodeKinds); err != nil {
				fmt.Fprintln(w.out, err)
				continue
			}
			if err := dialProxy(ctx, p); err != nil {
				fmt.Fprintf(w.out, "%s Enter retries.\n", torDown(p, err))
				continue
			}
			break
		}
	}

	if isLoopback(e.URL) {
		ok, err := w.yes(ctx, "Use bitcoind's cookie file for auth?", true)
		if err != nil {
			return e, "", err
		}
		if ok {
			for {
				c, err := w.ask(ctx, "Cookie file", cmp.Or(prev.CookieFile, cookie))
				if err != nil {
					return e, "", err
				}
				err = w.cookiePathErr(c)
				if err == nil {
					e.CookieFile = c
					return e, "", nil
				}
				fmt.Fprintln(w.out, err)
			}
		}
	}
	// A node box has its own RPC user, and its bitcoin.conf is the box's.
	remote := !isLoopback(e.URL)
	have, err := w.yes(ctx, "Do you already have the node's RPC user and password? (Umbrel, myNode, RaspiBlitz, StartOS: yes)", remote)
	if err != nil {
		return e, "", err
	}
	def := prev.Username
	if !have {
		def = cmp.Or(def, "nodetracker")
	}
	for {
		u, err := w.ask(ctx, "RPC user", def)
		if err != nil {
			return e, "", err
		}
		if rpcUserRE.MatchString(u) {
			e.Username = u
			break
		}
		fmt.Fprintf(w.out, "The user must match %s\n", rpcUserRE)
	}
	gen := false
	if !have {
		gen, err = w.yes(ctx, "Generate a new password and the rpcauth line for the node's bitcoin.conf?", !remote)
	}
	if err != nil || !gen {
		for err == nil {
			var pw config.Secret
			if pw, err = w.secret(ctx, "RPC password"); err == nil && pw != "" {
				return e, pw, nil
			}
		}
		return e, "", err
	}
	pw, line := bitcoind.NewRPCAuth(e.Username)
	fmt.Fprintf(w.out, "\nOn the node, add these lines to bitcoin.conf, then restart bitcoind:\n\n    rpcauth=%s\n    rpcwhitelist=%s:getblockchaininfo\n    rpcwhitelistdefault=0\n\n", line, e.Username)
	fmt.Fprintln(w.out, "node-tracker may call getblockchaininfo only; rpcwhitelistdefault=0 keeps the other RPC users\n(the cookie, bitcoin-cli, …) unrestricted. The password is saved for node-tracker only.")
	_, err = w.ask(ctx, "Press Enter when bitcoind has restarted", "")
	return e, config.Secret(pw), err
}

// testNode calls getblockchaininfo on e with password pw. It also returns e
// as validated, for the hints.
func (w *wizard) testNode(ctx context.Context, e config.Endpoint, pw config.Secret) (source.Height, config.Endpoint, error) {
	e.Password = pw
	ce, _, err := config.CheckEndpoint(e, "node", config.NodeKinds)
	if err != nil {
		return source.Height{}, e, err
	}
	return w.height(ctx, ce)
}

func (w *wizard) height(ctx context.Context, ce config.Endpoint) (source.Height, config.Endpoint, error) {
	s, err := app.NewSource(ce, w.hc)
	if err != nil {
		return source.Height{}, ce, err
	}
	cctx, cancel := context.WithTimeout(ctx, source.Timeout(s, config.DefaultTimeout))
	defer cancel()
	h, err := s.Height(cctx)
	return h, ce, err
}

// askRefs asks for the references, the public ones of chain by default,
// and tests them.
func (w *wizard) askRefs(ctx context.Context, chain, nodeURL string) ([]config.Endpoint, error) {
	var urls []string
	for _, r := range config.DefaultReferences(cmp.Or(chain, config.ChainMain)) {
		urls = append(urls, r.URL)
	}
	def := cmp.Or(strings.Join(urls, " "), "none")
	for {
		l, err := w.ask(ctx, "References: Esplora API URLs separated by spaces, or none", def)
		if err != nil {
			return nil, err
		}
		refs, checked, err := parseRefs(l, nodeURL)
		if err != nil {
			fmt.Fprintln(w.out, err)
			continue
		}
		if w.testRefs(ctx, checked) {
			return refs, nil
		}
		if ok, err := w.yes(ctx, "Use them anyway?", false); err != nil || ok {
			return refs, err
		}
	}
}

// parseRefs reads references separated by spaces or commas, or "none".
func parseRefs(l, nodeURL string) (refs, checked []config.Endpoint, err error) {
	refs = []config.Endpoint{}
	if strings.EqualFold(strings.TrimSpace(l), "none") {
		return refs, nil, nil
	}
	seen := map[string]bool{nodeURL: true}
	for _, u := range strings.FieldsFunc(l, func(r rune) bool { return r == ' ' || r == ',' }) {
		r := config.Endpoint{Kind: config.KindEsplora, URL: u}
		c, _, err := config.CheckEndpoint(r, "reference", config.ReferenceKinds)
		if err != nil {
			return nil, nil, err
		}
		if seen[u] {
			return nil, nil, fmt.Errorf("reference %s: duplicate of the node or another reference", u)
		}
		seen[u] = true
		refs, checked = append(refs, r), append(checked, c)
	}
	if len(refs) == 0 {
		return nil, nil, errors.New("give at least one URL, or none")
	}
	return refs, checked, nil
}

// testRefs prints the height of each reference and reports whether all
// answered.
func (w *wizard) testRefs(ctx context.Context, refs []config.Endpoint) bool {
	ok := true
	for _, r := range refs {
		h, _, err := w.height(ctx, r)
		if err != nil {
			ok = false
			fmt.Fprintf(w.out, "  %s: failed: %s\n", r.Name, explain.Printable(err.Error()))
			continue
		}
		fmt.Fprintf(w.out, "  %s: height %s\n", r.Name, notify.Num(h.Blocks))
	}
	return ok
}

func (w *wizard) askPreset(ctx context.Context) (int, error) {
	fmt.Fprintln(w.out, "An alert is sent when the node is more than <behind> blocks behind for <checks> checks in a row,\nand another when it is back within <back> blocks. See docs/configuration.md#choosing-the-thresholds.")
	for i, p := range presets {
		fmt.Fprintf(w.out, "  %d  %-8s  behind %d, checks %d every %ds, back %d\n", i+1, p.name, p.threshold, p.ticks, int(p.interval.Seconds()), p.recover)
	}
	for {
		l, err := w.ask(ctx, "Preset", strconv.Itoa(defaultPreset+1))
		if err != nil {
			return 0, err
		}
		if i, ok := presetIndex(l); ok {
			return i, nil
		}
	}
}

// presetIndex finds a preset by number (from 1) or name.
func presetIndex(s string) (int, bool) {
	for i, p := range presets {
		if s == strconv.Itoa(i+1) || strings.EqualFold(s, p.name) {
			return i, true
		}
	}
	return 0, false
}

// askTelegram finds the bot and the chat: those of the config at path, if
// kept, else a token and a chat asked for.
func (w *wizard) askTelegram(ctx context.Context, a *answers, path string) error {
	user, done, err := w.keepTelegram(ctx, a, path)
	if err != nil || done {
		return err
	}
	if a.token == "" {
		had, err := w.yes(ctx, "Do you already have a Telegram bot for the alerts?", false)
		if err != nil {
			return err
		}
		if had {
			fmt.Fprintln(w.out, "In Telegram, open the chat with @BotFather (https://t.me/BotFather), not with your bot.\nSend /mybots there, pick your bot and tap API Token.")
		} else {
			fmt.Fprintln(w.out, "In Telegram, open the chat with @BotFather (https://t.me/BotFather).\nSend /newbot there and choose a name and a username ending in bot.")
		}
		fmt.Fprintln(w.out, "It shows the bot's token. Details: docs/telegram.md.")
	}
	for a.token == "" {
		tok, err := w.secret(ctx, "Bot token")
		if err != nil {
			return err
		}
		if tok == "" {
			fmt.Fprintln(w.out, "No token entered: paste it and press Enter; the input stays hidden.")
			continue
		}
		if user, err = w.testToken(ctx, a, tok); err != nil {
			return err
		}
	}
	if user == "" {
		fmt.Fprintf(w.out, "Token …%s kept untested. Without Telegram the chat can't be found or tested now: type its id.\n", tail(a.token))
		for a.chatID == "" {
			if a.chatID, err = w.askChatID(ctx, "Chat id or @channel", ""); err != nil {
				return err
			}
		}
		fmt.Fprintln(w.out, "Once Telegram answers, send a test message: node-tracker test-notify")
		return nil
	}
	fmt.Fprintf(w.out, "Bot: @%s (token …%s)\n", user, tail(a.token))
	for {
		id, err := w.askChat(ctx, a.token, user)
		if err != nil {
			return err
		}
		fmt.Fprint(w.out, "Sending a test message … ")
		if err := w.sendTest(ctx, a.token, id, a.tracker); err != nil {
			fmt.Fprintf(w.out, "failed: %s\n", err)
			continue
		}
		fmt.Fprintln(w.out, "sent. It should be in Telegram now.")
		a.chatID, a.chatTested = id, true
		return nil
	}
}

// keepTelegram offers the bot and chat of the config at path. Kept, they
// are tested: done if the test message went through, or if Telegram does
// not answer (kept untested). A rejected token is dropped; a valid one
// whose chat fails stays in a.token, and its bot is returned.
func (w *wizard) keepTelegram(ctx context.Context, a *answers, path string) (user string, done bool, err error) {
	n, ok := w.prevTelegram(path)
	if !ok {
		return "", false, nil
	}
	q := fmt.Sprintf("Keep the Telegram bot and chat of %s (chat %s, token …%s)?", path, n.ChatID, tail(n.BotToken))
	if ok, err := w.yes(ctx, q, true); err != nil || !ok {
		return "", false, err
	}
	api, hc := w.telegramAPI, w.tg
	if n.APIURL != "" {
		w.telegramAPI = n.APIURL
	}
	if err := w.useTelegramProxy(a, n.Proxy); err != nil {
		return "", false, err
	}
	user, err = w.checkToken(ctx, n.BotToken)
	switch {
	case errors.Is(err, errTelegramDown):
		fmt.Fprintf(w.out, "%v\nKept untested. Once Telegram answers, send a test message: node-tracker test-notify\n", err)
		a.token, a.chatID = n.BotToken, n.ChatID
		return "", true, nil
	case err != nil:
		fmt.Fprintln(w.out, err)
		w.telegramAPI, w.tg, a.tgProxy = api, hc, ""
		return "", false, nil
	}
	a.token = n.BotToken
	fmt.Fprintf(w.out, "Bot: @%s. Sending a test message … ", user)
	if err := w.sendTest(ctx, a.token, n.ChatID, a.tracker); err != nil {
		fmt.Fprintf(w.out, "failed: %s\n", err)
		return user, false, nil
	}
	fmt.Fprintln(w.out, "sent. It should be in Telegram now.")
	a.chatID, a.chatTested = n.ChatID, true
	return user, true, nil
}

// prevTelegram is the first Telegram notifier, with its token, of the
// config at path, if that loads.
func (w *wizard) prevTelegram(path string) (config.Notifier, bool) {
	if _, err := os.Stat(path); err != nil {
		return config.Notifier{}, false
	}
	cfg, _, err := config.LoadEnv(path, detectEnv)
	if err != nil {
		fmt.Fprintf(w.out, "The Telegram settings of %s can't be kept: %v\n", path, err)
		return config.Notifier{}, false
	}
	for _, n := range cfg.Notifiers {
		if n.Kind == config.KindTelegram && n.BotToken != "" {
			return n, true
		}
	}
	return config.Notifier{}, false
}

// testToken checks tok with Telegram. It sets a.token and returns the bot's
// username if tok is valid, or "" if it is kept untested; a.token stays
// empty if tok is rejected. If Telegram does not answer, it offers to retry,
// to keep tok untested, or to go through Tor (or back, without it).
func (w *wizard) testToken(ctx context.Context, a *answers, tok config.Secret) (string, error) {
	for check := true; ; {
		if check {
			user, err := w.checkToken(ctx, tok)
			if err == nil {
				a.token = tok
				return user, nil
			}
			fmt.Fprintln(w.out, err)
			if !errors.Is(err, errTelegramDown) {
				return "", nil
			}
		}
		q, opts := "[r]etry, [k]eep the token untested, or [t]ry through Tor ("+w.torProxy+")?", "rkt"
		if a.tgProxy != "" {
			q, opts = "[r]etry, [k]eep the token untested, or go [d]irect, without "+a.tgProxy+"?", "rkd"
		}
		c, err := w.choose(ctx, q, opts)
		if err != nil {
			return "", err
		}
		check = true
		switch c {
		case 'k':
			a.token = tok
			return "", nil
		case 't':
			if err := dialProxy(ctx, w.torProxy); err != nil {
				fmt.Fprintln(w.out, torDown(w.torProxy, err))
				check = false
				continue
			}
			err = w.useTelegramProxy(a, w.torProxy)
		case 'd':
			err = w.useTelegramProxy(a, "")
		}
		if err != nil {
			return "", err
		}
	}
}

// useTelegramProxy sends the Telegram calls through proxy, which must be
// valid, or without one for "".
func (w *wizard) useTelegramProxy(a *answers, proxy string) error {
	hc, err := app.Client(proxy, w.hc)
	if err != nil {
		return err
	}
	w.tg, a.tgProxy = hc, proxy
	return nil
}

func (w *wizard) bot(tok config.Secret, chatID string) *telegram.Bot {
	return telegram.New(notifierName, strings.TrimRight(w.telegramAPI, "/"), string(tok), chatID, cmp.Or(w.tg, w.hc))
}

// checkToken validates tok and returns the bot's username. Its errors say
// what to do.
func (w *wizard) checkToken(ctx context.Context, tok config.Secret) (string, error) {
	if tok == "" {
		return "", errors.New("no token entered")
	}
	if err := config.CheckBotToken(tok); err != nil {
		return "", fmt.Errorf("the token %v; copy it again from @BotFather", err)
	}
	cctx, cancel := context.WithTimeout(ctx, config.DefaultTimeout)
	defer cancel()
	user, err := w.bot(tok, "").CheckToken(cctx)
	switch {
	case errors.Is(err, telegram.ErrBadToken):
		return "", errors.New("telegram rejected the token; copy only the token (e.g. 123456789:AAH…) again from @BotFather")
	case err != nil:
		return "", fmt.Errorf("%w: %s", errTelegramDown, explain.Printable(err.Error()))
	case !explain.BotUser.MatchString(user):
		return "", fmt.Errorf("telegram: unexpected bot username %q", user)
	}
	return user, nil
}

// askChat asks where the alerts go and finds the chat id: from a /start
// link with a fresh nonce for a private chat or a group, else typed.
func (w *wizard) askChat(ctx context.Context, tok config.Secret, user string) (string, error) {
	for {
		fmt.Fprintln(w.out, "Where should the alerts go?\n"+
			"  1  to you, in a private chat with the bot\n"+
			"  2  a group\n"+
			"  3  a channel (the bot must be one of its admins)\n"+
			"  4  a chat id you already know")
		c, err := w.ask(ctx, "Choice", "1")
		if err != nil {
			return "", err
		}
		var id string
		switch c {
		case "1", "2":
			id, err = w.waitStart(ctx, tok, user, c == "2")
		case "3":
			id, err = w.askChatID(ctx, "Channel's @name (Enter: back)", user)
		case "4":
			id, err = w.askChatID(ctx, "Chat id (Enter: back)", user)
		default:
			fmt.Fprintln(w.out, "Pick 1, 2, 3 or 4.")
		}
		if err != nil || id != "" {
			return id, err
		}
	}
}

// waitStart shows the /start link of a private chat or a group and waits
// for its "/start <nonce>". Only that counts, so old updates and other
// users' messages are ignored. Lines typed meanwhile do not stop the wait.
// It returns "" if nothing arrived.
func (w *wizard) waitStart(ctx context.Context, tok config.Secret, user string, group bool) (string, error) {
	b := make([]byte, 16)
	rand.Read(b)
	nonce := base64.RawURLEncoding.EncodeToString(b)
	if group {
		fmt.Fprintf(w.out, "Open this link in Telegram, on any device, pick the group and add the bot:\n  https://t.me/%s?startgroup=%s\n"+
			"If nothing happens (e.g. the bot is in the group already), send this message in the group:\n  /start@%[1]s %[2]s\n", user, nonce)
	} else {
		fmt.Fprintf(w.out, "Open this link in Telegram, on any device, and press Start:\n  https://t.me/%s?start=%s\n"+
			"If nothing happens, send the bot this message:\n  /start %[2]s\n", user, nonce)
	}
	fmt.Fprintf(w.out, "Waiting up to %s; there is nothing to type here (Ctrl-C aborts) …\n", w.waitChat)
	pctx, cancel := context.WithTimeout(ctx, w.waitChat)
	defer cancel()
	type found struct {
		chat telegram.Chat
		err  error
	}
	res := make(chan found, 1)
	go func() {
		c, err := w.pollStart(pctx, w.bot(tok, ""), user, nonce)
		res <- found{c, err}
	}()
	for {
		select {
		case r := <-res:
			switch {
			case r.err == nil:
				id := strconv.FormatInt(r.chat.ID, 10)
				fmt.Fprintf(w.out, "Found: %s, chat id %s\n", chatName(r.chat), id)
				return id, nil
			case ctx.Err() != nil:
				return "", ctx.Err()
			case errors.Is(r.err, telegram.ErrConflict):
				fmt.Fprintln(w.out, "Telegram refuses to hand out the bot's messages: the bot has a webhook, or another program reads its updates.\nRemove the webhook (Bot API deleteWebhook) or stop that program, or pick 4 and type the chat id.")
			case pctx.Err() != nil:
				fmt.Fprintf(w.out, "Nothing arrived within %s.\n", w.waitChat)
			default:
				fmt.Fprintf(w.out, "Could not read the bot's messages: %s\n", explain.Printable(r.err.Error()))
			}
			return "", nil
		case _, ok := <-w.input():
			if !ok {
				return "", errInputEnded
			}
			fmt.Fprintln(w.out, "Still waiting: open the link above in Telegram, or send the message above. Ctrl-C aborts.")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// askChatID asks for a chat id until it is valid and not the bot itself,
// with q. With a bot, Enter returns "" (back); without one ("": untested
// token), Enter asks again.
func (w *wizard) askChatID(ctx context.Context, q, bot string) (string, error) {
	for {
		id, err := w.ask(ctx, q, "")
		switch {
		case err != nil:
			return "", err
		case id == "" && bot != "":
			return "", nil
		case id == "":
		case bot != "" && strings.EqualFold(strings.TrimPrefix(id, "@"), bot):
			fmt.Fprintf(w.out, "@%s is the bot itself, it can't send to itself. For a chat with you, go back (Enter) and pick 1.\n", bot)
		default:
			if err := config.CheckChatID(id); err != nil {
				fmt.Fprintf(w.out, "The chat id %v\n", err)
				continue
			}
			return id, nil
		}
	}
}

// pollStart long-polls the bot's updates until "/start <nonce>" arrives,
// and returns its chat.
func (w *wizard) pollStart(ctx context.Context, b *telegram.Bot, user, nonce string) (telegram.Chat, error) {
	var offset int64
	for {
		cctx, cancel := context.WithTimeout(ctx, w.poll+config.DefaultTimeout)
		us, err := b.Updates(cctx, offset, w.poll)
		cancel()
		if err != nil {
			return telegram.Chat{}, err
		}
		for _, u := range us {
			offset = max(offset, u.ID+1)
			if isStart(u.Text, user, nonce) {
				return u.Chat, nil
			}
		}
	}
}

// isStart: text is "/start <nonce>", or "/start@<bot> <nonce>" as in groups.
func isStart(text, bot, nonce string) bool {
	f := strings.Fields(text)
	return len(f) == 2 && f[1] == nonce && (f[0] == "/start" || strings.EqualFold(f[0], "/start@"+bot))
}

func chatName(c telegram.Chat) string {
	switch {
	case c.Type == "private" && c.Username != "":
		return "you (@" + explain.Printable(c.Username) + ")"
	case c.Type == "private":
		return "you (" + explain.Printable(c.FirstName) + ")"
	}
	return c.Type + " " + strconv.Quote(explain.Printable(c.Title))
}

// sendTest sends the test message of test-notify. Its errors say what to do.
func (w *wizard) sendTest(ctx context.Context, tok config.Secret, chatID, tracker string) error {
	cctx, cancel := context.WithTimeout(ctx, config.DefaultTimeout)
	defer cancel()
	err := w.bot(tok, chatID).Send(cctx, notify.Tag(tracker, notify.Test(time.Now())))
	if err == nil {
		return nil
	}
	return errors.New(explain.Telegram(err))
}

// fromFlags takes the answers from the flags, and tests them as the
// interview does. Only a rejected token is fatal of the tests: the node or
// Telegram may not be reachable yet, e.g. in a container.
func (w *wizard) fromFlags(ctx context.Context) (*answers, error) {
	o := w.o
	a := &answers{}
	var ok bool
	if a.preset, ok = presetIndex(o.preset); !ok {
		return nil, fmt.Errorf("-preset %q: must be fast, balanced or quiet", o.preset)
	}
	if o.downFor != 0 && (o.downFor < 1 || o.downFor > config.MaxFor) {
		return nil, fmt.Errorf("-down-for %d: must be between 1 and %d", o.downFor, config.MaxFor)
	}
	a.downFor = o.downFor
	if o.tokenFile == "" || o.chatID == "" {
		return nil, errors.New("-non-interactive needs -token-file and -chat-id")
	}
	if err := config.CheckChatID(o.chatID); err != nil {
		return nil, fmt.Errorf("-chat-id %q: %v", o.chatID, err)
	}
	a.chatID = o.chatID

	d := w.detect()
	a.node = config.Endpoint{Kind: config.KindBitcoind, URL: cmp.Or(o.nodeURL, d.URL), AllowInsecure: o.allowInsecure, Proxy: o.proxy}
	switch {
	case o.rpcUser != "" && o.cookieFile != "":
		return nil, errors.New("give -cookie-file or -rpc-user, not both")
	case o.rpcUser != "":
		if !rpcUserRE.MatchString(o.rpcUser) {
			return nil, fmt.Errorf("-rpc-user: must match %s", rpcUserRE)
		}
		if o.rpcPasswordFile == "" {
			return nil, errors.New("-rpc-user needs -rpc-password-file")
		}
		pw, err := readSecret(o.rpcPasswordFile)
		if err != nil {
			return nil, fmt.Errorf("-rpc-password-file: %w", err)
		}
		a.node.Username, a.password = o.rpcUser, pw
	case o.cookieFile != "":
		a.node.CookieFile = o.cookieFile
	case o.nodeURL == "":
		a.node.CookieFile = d.CookieFile
	default:
		return nil, errors.New("-node-url needs -cookie-file, or -rpc-user and -rpc-password-file")
	}
	e := a.node
	e.Password = a.password
	ce, _, err := config.CheckEndpoint(e, "node", config.NodeKinds)
	if err != nil {
		return nil, err
	}
	// As interviewed: the service may lack this shell's proxy variables.
	if p := effectiveProxy(ce); explain.IsOnion(ce.URL) && a.node.Proxy == "" && p != config.DefaultTorProxy {
		a.node.Proxy = p
	}
	h, _, err := w.height(ctx, ce)
	switch {
	case err != nil:
		fmt.Fprintf(w.out, "warning: the node did not answer: %s\n", explain.Node(ce, app.Proxy(ce) != "none", err))
		if o.nodeURL == "" && o.cookieFile == "" && o.rpcUser == "" {
			w.userNodeHint(w.otherUserNodes(d.CookieFile))
		}
	default:
		fmt.Fprintf(w.out, "node %s: %s\n", a.node.URL, heightInfo(h))
	}

	if a.tracker, err = trackerName(cmp.Or(o.trackerName, hostName(w.hostname), "none")); err != nil {
		return nil, fmt.Errorf("-tracker-name: %v", err)
	}
	a.name = cmp.Or(o.name, defaultName(isLoopback(a.node.URL), w.hostname))
	if err := config.CheckName(a.name); err != nil {
		return nil, fmt.Errorf("-name %q: %v", a.name, err)
	}
	refs := o.references
	if refs == "" {
		var urls []string
		for _, r := range config.DefaultReferences(cmp.Or(h.Chain, config.ChainMain)) {
			urls = append(urls, r.URL)
		}
		refs = cmp.Or(strings.Join(urls, ","), "none")
	}
	var checked []config.Endpoint
	if a.refs, checked, err = parseRefs(refs, a.node.URL); err != nil {
		return nil, fmt.Errorf("-references: %w", err)
	}
	if !w.testRefs(ctx, checked) {
		fmt.Fprintln(w.out, "warning: not every reference answered")
	}

	if o.telegramProxy != "" {
		if err := config.CheckProxy(o.telegramProxy, "-telegram-proxy"); err != nil {
			return nil, err
		}
		if err := w.useTelegramProxy(a, o.telegramProxy); err != nil {
			return nil, err
		}
	}
	if a.token, err = readSecret(o.tokenFile); err != nil {
		return nil, fmt.Errorf("-token-file: %w", err)
	}
	user, err := w.checkToken(ctx, a.token)
	switch {
	case errors.Is(err, errTelegramDown):
		fmt.Fprintf(w.out, "warning: %v\n", err)
	case err != nil:
		return nil, fmt.Errorf("-token-file: %w", err)
	default:
		fmt.Fprintf(w.out, "bot: @%s (token …%s)\n", user, tail(a.token))
	}
	if err := w.sendTest(ctx, a.token, a.chatID, a.tracker); err != nil {
		fmt.Fprintf(w.out, "warning: test message failed: %v\n", err)
	} else {
		fmt.Fprintln(w.out, "test message sent")
		a.chatTested = true
	}
	return a, nil
}

// readSecret reads a secret file as config.Load does: trimmed, not empty.
func readSecret(path string) (config.Secret, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSecret+1))
	switch s := strings.TrimSpace(string(b)); {
	case err != nil:
		return "", err
	case len(b) > maxSecret:
		return "", fmt.Errorf("%s: larger than %d bytes", path, maxSecret)
	case s == "":
		return "", fmt.Errorf("%s is empty", path)
	default:
		return config.Secret(s), nil
	}
}

// setupFile is a file finish writes.
type setupFile struct {
	path, what string
	data       []byte
	same, bak  bool
}

// finish writes the config and its secrets next to it, validated as Load
// does, then runs check and offers install-service.
func (w *wizard) finish(ctx context.Context, a *answers, path string, root bool) (int, error) {
	dir := filepath.Dir(path)
	mode, gid := fs.FileMode(0o600), -1
	if root {
		if g, err := w.group(); err == nil {
			mode, gid = 0o640, g
		}
	}
	tok, pw := filepath.Join(dir, "telegram-token"), filepath.Join(dir, a.name+"-rpc-password")
	files := []setupFile{{path: tok, data: []byte(string(a.token) + "\n"), what: "the bot token"}}
	if a.node.Username != "" {
		files = append(files, setupFile{path: pw, data: []byte(string(a.password) + "\n"), what: "the RPC password"})
	}
	cfgData, err := w.render(a, tok, pw)
	if err != nil {
		return exitFailure, err
	}
	files = append(files, setupFile{path: path, data: cfgData, what: "the config"})

	var exist []string
	for i := range files {
		f := &files[i]
		old, err := fsx.ReadRegular(f.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return exitFailure, err
		case bytes.Equal(old, f.data):
			f.same = true
		default:
			// Only the config is kept: an old secret may be one that leaked.
			f.bak = i == len(files)-1
			exist = append(exist, f.path)
		}
	}
	fmt.Fprintf(w.out, "\n5. The config\n%s:\n\n%s\n", path, indent(cfgData))
	for _, f := range files[:len(files)-1] {
		fmt.Fprintf(w.out, "%s goes to %s (mode %04o).\n", upper(f.what), f.path, mode)
	}
	if err := w.checkService(ctx, a); err != nil {
		if errors.Is(err, errDeclined) {
			return exitFailure, err
		}
		return exitUsage, err
	}
	if len(exist) > 0 {
		q := fmt.Sprintf("These files exist and differ: %s. Replace them (the old config is kept as .bak, old secrets are not)?", strings.Join(exist, ", "))
		if w.o.nonInteractive {
			if !w.o.force {
				return exitUsage, fmt.Errorf("%s exist and differ; rerun with -force to replace them (keeps the old config as .bak)", strings.Join(exist, ", "))
			}
		} else if ok, err := w.yes(ctx, q, false); err != nil || !ok {
			return exitFailure, cmp.Or(err, errDeclined)
		}
	}

	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, map[bool]fs.FileMode{true: 0o750, false: 0o700}[root]); err != nil {
			return exitFailure, err
		}
		if gid != -1 {
			if err := os.Chown(dir, -1, gid); err != nil {
				return exitFailure, err
			}
		}
	}
	// Validate the config as the commands will load it, with the secrets in
	// temporary files, before any file is replaced.
	var tmps []string
	defer func() {
		for _, t := range tmps {
			os.Remove(t)
		}
	}()
	for _, f := range files[:len(files)-1] {
		t, err := tmpFile(dir, f.data)
		tmps = append(tmps, t)
		if err != nil {
			return exitFailure, err
		}
	}
	tmps = append(tmps, "") // the password's, if none
	probe, err := w.render(a, tmps[0], tmps[1])
	if err != nil {
		return exitFailure, err
	}
	t, err := tmpFile(dir, probe)
	tmps = append(tmps, t)
	if err != nil {
		return exitFailure, err
	}
	_, warns, err := config.LoadEnv(t, detectEnv)
	if err != nil {
		return exitFailure, err
	}
	for _, wn := range warns {
		fmt.Fprintf(w.out, "config warning: %s\n", wn)
	}
	for _, f := range files {
		if err := w.write(f, mode, gid); err != nil {
			return exitFailure, err
		}
	}

	fmt.Fprintln(w.out, "\n6. Check")
	cfg, _, err := config.LoadEnv(path, detectEnv)
	if err != nil {
		return exitFailure, err
	}
	m, err := app.Build(cfg, w.log)
	if err != nil {
		return exitFailure, err
	}
	check(ctx, m, cfg, w.out, w.log)
	if a.chatTested {
		fmt.Fprintln(w.out, "The chat was tested above: setup sent the test message.")
	}
	return w.offerService(ctx, path, root, files[len(files)-1].bak)
}

// checkService runs, as root and before anything is written, the check of
// install-service -system that the service can read the node's cookie. If
// it can't, the config is written only if wanted, and the install is not
// offered; -non-interactive -install-service refuses.
func (w *wizard) checkService(ctx context.Context, a *answers) error {
	if !w.root || w.goos == "darwin" {
		return nil
	}
	err := w.cookies(&config.Config{Targets: []config.Target{{Name: a.name, Node: a.node, References: a.refs}}})
	var ue service.UsageError
	if !errors.As(err, &ue) {
		return err
	}
	fmt.Fprintf(w.out, "\nBefore anything is written: %s\n", upper(err.Error()))
	if w.o.nonInteractive {
		if w.o.installService {
			return fmt.Errorf("-install-service: %w", err)
		}
	} else if ok, err := w.yes(ctx, "Write the config anyway, and install the service once that is fixed?", false); err != nil || !ok {
		return cmp.Or(err, errDeclined)
	}
	w.noSvc = true
	return nil
}

// nodeDownFor is the alert.down_for of a: -down-for if given, else about
// onionDownWait for a .onion node (Tor stalls for minutes at times), else
// the preset's for.
func (a *answers) nodeDownFor() int {
	p := presets[a.preset]
	switch {
	case a.downFor > 0:
		return a.downFor
	case explain.IsOnion(a.node.URL):
		return max(p.ticks, int(onionDownWait/p.interval))
	}
	return p.ticks
}

// render is the config of a, with the bot token in tok and the RPC
// password, if any, in pw.
func (w *wizard) render(a *answers, tok, pw string) ([]byte, error) {
	p := presets[a.preset]
	t := config.Target{Name: a.name, Node: a.node, References: a.refs,
		Alert: config.Alert{Threshold: &p.threshold, RecoverThreshold: &p.recover, For: p.ticks}}
	if n := a.nodeDownFor(); n != p.ticks {
		t.Alert.DownFor = n
	}
	if a.node.Username != "" {
		t.Node.PasswordFile = pw
	}
	n := config.Notifier{Name: notifierName, Kind: config.KindTelegram, BotTokenFile: tok, ChatID: a.chatID, Proxy: a.tgProxy}
	if w.telegramAPI != config.DefaultTelegramAPI {
		n.APIURL = w.telegramAPI
	}
	var b bytes.Buffer
	b.WriteString("# Written by node-tracker setup. Reference: docs/configuration.md\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	err := enc.Encode(config.Config{Name: a.tracker, Interval: p.interval, Targets: []config.Target{t}, Notifiers: []config.Notifier{n}})
	return b.Bytes(), err
}

// tmpFile writes data to a new mode 0600 file in dir.
func tmpFile(dir string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".setup-*")
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return f.Name(), err
}

func (w *wizard) write(f setupFile, mode fs.FileMode, gid int) error {
	if f.same {
		fmt.Fprintf(w.out, "unchanged %s\n", f.path)
		return nil
	}
	w.wrote = true
	if err := fsx.WriteFile(f.path, f.data, mode, -1, gid, f.bak); err != nil {
		return fmt.Errorf("writing %s: %w", f.path, err)
	}
	if f.bak {
		fmt.Fprintf(w.out, "backed up %s to %[1]s.bak\n", f.path)
	}
	fmt.Fprintf(w.out, "wrote %s\n", f.path)
	return nil
}

// offerService runs install-service, if wanted. replaced: an old config
// was replaced, which a running service still uses.
func (w *wizard) offerService(ctx context.Context, path string, root, replaced bool) (int, error) {
	fmt.Fprintln(w.out, "\n7. The service")
	if w.goos == "darwin" {
		fmt.Fprintln(w.out, "To run node-tracker in the background: brew services start node-tracker (or node-tracker run in a terminal).")
		return exitOK, nil
	}
	o := service.Options{System: root}
	flagName, systemctl := "-user", "systemctl --user"
	if root {
		flagName, systemctl, o.Config = "-system", "systemctl", path
	} else if w.o.config != "" {
		o.Config = path
	}
	cmdline := "node-tracker install-service " + flagName
	if o.Config != "" && !root {
		cmdline += " -config " + path
	}
	later := cmdline // typed later, in a user's shell
	if root {
		later = "sudo " + cmdline
	}
	if w.noSvc {
		fmt.Fprintf(w.out, "Once the service can read the cookie (see above), install it: %s\n", later)
		return exitOK, nil
	}
	ok := w.o.installService
	if !w.o.nonInteractive {
		var err error
		if ok, err = w.yes(ctx, "Install and start the systemd service now ("+cmdline+")?", true); err != nil {
			return exitFailure, err
		}
	}
	if !ok {
		fmt.Fprintf(w.out, "Start it later with: %s (or node-tracker run in a terminal).\n", later)
		return exitOK, nil
	}
	if err := w.install(ctx, w.out, o); err != nil {
		return exitFailure, fmt.Errorf("install-service: %w\nThe config is written; nothing else was changed. Once that is fixed, run: %s", err, later)
	}
	if replaced {
		fmt.Fprintf(w.out, "If the service was running before, restart it to use the new config: %s restart node-tracker\n", systemctl)
	}
	return exitOK, nil
}

func heightInfo(h source.Height) string {
	s := fmt.Sprintf("chain %s, height %s", cmp.Or(h.Chain, "unknown"), notify.Num(h.Blocks))
	if h.Headers > h.Blocks {
		s += fmt.Sprintf(" (headers %s: still syncing)", notify.Num(h.Headers))
	}
	return s
}

// defaultName is this host's name for a local node, else "node".
func defaultName(local bool, hostname func() (string, error)) string {
	if h := hostName(hostname); local && h != "" {
		return h
	}
	return "node"
}

// hostName is this host's short name if it is a valid name, else "".
func hostName(hostname func() (string, error)) string {
	if h, err := hostname(); err == nil {
		if h, _, _ = strings.Cut(h, "."); config.CheckName(h) == nil {
			return h
		}
	}
	return ""
}

// trackerName is the config name of answer s: "" for none.
func trackerName(s string) (string, error) {
	if strings.EqualFold(s, "none") {
		return "", nil
	}
	if err := config.CheckName(s); err != nil {
		return "", fmt.Errorf("the tracker name %v, or none", err)
	}
	return s, nil
}

func isLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}

// detect finds the local bitcoind as the service will: the system service
// has no home and none of this user's environment.
func (w *wizard) detect() config.Detected {
	env := detectEnv
	if w.root {
		env.Home, env.Getenv = "", nil
	}
	return env.Detect()
}

// otherUserNodes are, as root, the bitcoinds run by users (see userNodes)
// but the one at the cookie tried.
func (w *wizard) otherUserNodes(tried string) []userNode {
	if !w.root {
		return nil
	}
	var ns []userNode
	for _, n := range w.userNodes() {
		if n.cookie != tried {
			ns = append(ns, n)
		}
	}
	return ns
}

// userNodeHint tells root of the bitcoinds of ns and suggests a service of
// that user's own: the system service runs as service.Account, which may
// not read the cookie (ProtectHome=yes for one in the home).
func (w *wizard) userNodeHint(ns []userNode) {
	if len(ns) == 0 {
		return
	}
	who := strconv.Quote(ns[0].user)
	for _, n := range ns {
		fmt.Fprintf(w.out, "bitcoind runs as %q, cookie %s.\n", n.user, n.cookie)
		if n.user != ns[0].user {
			who = "that user"
		}
	}
	fmt.Fprintf(w.out, "Best fit: run 'node-tracker setup' as %s, without sudo, for a service of that user's own.\n", who)
}

// userNode is a bitcoind run by a user other than root, and its cookie.
type userNode struct {
	user, url, cookie string // url: of its RPC, as detected
	inHome            bool   // the cookie is in the user's home
}

// userNodes finds them in proc (Linux's /proc): the processes named
// bitcoind or bitcoin-qt, their real uid, and the cookie that detection
// finds from their arguments and the user's home. lookup gives a uid's
// user. The arguments are not shown: they may hold -rpcpassword.
func userNodes(proc string, lookup func(uid string) (*user.User, error)) []userNode {
	ds, _ := os.ReadDir(proc)
	var ns []userNode
	for _, d := range ds {
		if _, err := strconv.Atoi(d.Name()); err != nil {
			continue
		}
		read := func(f string) string {
			b, _ := os.ReadFile(filepath.Join(proc, d.Name(), f))
			return string(b)
		}
		if c := strings.TrimSpace(read("comm")); c != "bitcoind" && c != "bitcoin-qt" {
			continue
		}
		var uid string
		for _, l := range strings.Split(read("status"), "\n") {
			if f := strings.Fields(l); len(f) > 1 && f[0] == "Uid:" {
				uid = f[1]
			}
		}
		if uid == "" || uid == "0" {
			continue
		}
		u, err := lookup(uid)
		if err != nil {
			continue
		}
		// A service account's home may be "/": then only the arguments count.
		home := u.HomeDir
		if !filepath.IsAbs(home) || filepath.Clean(home) == "/" {
			home = ""
		}
		var args []string
		if f := strings.Split(strings.TrimRight(read("cmdline"), "\x00"), "\x00"); len(f) > 1 {
			args = f[1:]
		}
		det := config.Env{Root: "/", Home: home, GOOS: runtime.GOOS, Args: args}.Detect()
		cookie := det.CookieFile
		n := userNode{u.Username, det.URL, cookie, home != "" && strings.HasPrefix(cookie, filepath.Clean(home)+string(filepath.Separator))}
		// Any process can be named bitcoind: only a regular file of its
		// owner is a cookie, not e.g. -rpccookiefile=/etc/shadow.
		fi, err := os.Stat(cookie)
		if err != nil || !fi.Mode().IsRegular() || slices.Contains(ns, n) {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && strconv.FormatUint(uint64(st.Uid), 10) == uid {
			ns = append(ns, n)
		}
	}
	return ns
}

// cookiePathErr is why c can't be the cookie_file: as root it must be
// absolute, as ~ is root's home, which the system service can't read.
func (w *wizard) cookiePathErr(c string) error {
	switch {
	case filepath.IsAbs(c):
		return nil
	case w.root:
		return errors.New("the cookie file must be an absolute path (as root, ~ is /root, which the system service can't read)")
	case strings.HasPrefix(c, "~/"):
		return nil
	}
	return errors.New("the cookie file must be an absolute path or start with ~/")
}

// effectiveProxy is the proxy the validated e uses: its own, else the
// environment's.
func effectiveProxy(e config.Endpoint) string {
	if e.Proxy != "" {
		return e.Proxy
	}
	u, _ := url.Parse(e.URL)
	if p, err := http.ProxyFromEnvironment(&http.Request{URL: u}); err == nil && p != nil {
		return p.String()
	}
	return config.DefaultTorProxy
}

// torDown says that Tor does not answer at proxy, and how to fix it.
func torDown(proxy string, err error) string {
	return fmt.Sprintf("Tor does not answer at %s: %s. Install and start Tor (Debian/Ubuntu: sudo apt install -y tor && sudo systemctl enable --now tor), see docs/operations.md#tor.", proxy, explain.Printable(err.Error()))
}

// dialProxy checks that something listens at the proxy's address.
func dialProxy(ctx context.Context, proxy string) error {
	u, err := url.Parse(proxy)
	if err != nil {
		return err
	}
	port := cmp.Or(u.Port(), map[string]string{"http": "80", "https": "443"}[u.Scheme], "1080")
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return err
	}
	return c.Close()
}

// tail shows the end of a token only, so a paste error is visible.
func tail(s config.Secret) string {
	r := []rune(string(s))
	return string(r[max(0, len(r)-4):])
}

func indent(b []byte) string {
	lines := strings.SplitAfter(strings.TrimSuffix(string(b), "\n"), "\n")
	return "    " + strings.Join(lines, "    ") + "\n"
}

func upper(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
