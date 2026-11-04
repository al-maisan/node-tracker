package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/httpx"
	"github.com/al-maisan/node-tracker/internal/service"
	"github.com/al-maisan/node-tracker/internal/source/bitcoind"
)

// turn is one prompt of a setup test and its answer. secret answers must
// be read with the echo off. do, if set, runs instead of answering.
type turn struct {
	prompt, answer string
	secret         bool
	do             func()
}

// fakeTerm is the terminal of a setup test. It answers each prompt of its
// script once setup has printed it, and keeps a transcript: what setup
// printed, plus the answers as a terminal would echo them.
type fakeTerm struct {
	t       *testing.T
	mu      sync.Mutex
	cond    *sync.Cond
	out     strings.Builder
	pos     int // of the transcript, up to the last prompt answered
	script  []turn
	echo    bool
	pending []byte
	closed  bool
}

func newFakeTerm(t *testing.T, script ...turn) *fakeTerm {
	f := &fakeTerm{t: t, script: script, echo: true}
	f.cond = sync.NewCond(&f.mu)
	end := func() {
		f.mu.Lock()
		f.closed = true
		f.cond.Broadcast()
		f.mu.Unlock()
	}
	timer := time.AfterFunc(30*time.Second, end) // a stuck test ends with EOF
	t.Cleanup(func() { timer.Stop(); end() })
	return f
}

func (f *fakeTerm) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out.Write(p)
	f.cond.Broadcast()
	return len(p), nil
}

func (f *fakeTerm) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.pending) == 0 {
		if f.closed {
			return 0, io.EOF
		}
		if len(f.script) > 0 && strings.Contains(f.out.String()[f.pos:], f.script[0].prompt) {
			s := f.script[0]
			f.script = f.script[1:]
			f.pos = f.out.Len()
			if s.do != nil {
				s.do()
				continue
			}
			if s.secret == f.echo {
				f.t.Errorf("answer to %q read with echo=%v", s.prompt, f.echo)
			}
			if f.echo {
				f.out.WriteString(s.answer + "\n")
				f.pos = f.out.Len()
			}
			f.pending = []byte(s.answer + "\n")
			continue
		}
		f.cond.Wait()
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

func (f *fakeTerm) noEcho() (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.echo = false
	return func() {
		f.mu.Lock()
		f.echo = true
		f.mu.Unlock()
	}, nil
}

func (f *fakeTerm) transcript() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.out.String()
}

// done fails the test if a prompt was not reached or the echo is still off.
func (f *fakeTerm) done() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.script) > 0 {
		f.t.Errorf("prompt %q not reached", f.script[0].prompt)
	}
	if !f.echo {
		f.t.Error("echo left off")
	}
}

// fakeTG is the Telegram Bot API of setup tests, for the token "123:TOKEN".
type fakeTG struct {
	srv     *httptest.Server
	mu      sync.Mutex
	updates func(ctx context.Context, offset int64) (status int, body string)
	offsets []int64
	sent    []string // chat_id: text
}

const tgToken = "123:TOKEN"

func newFakeTG(t *testing.T) *fakeTG {
	f := &fakeTG{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		method, ok := strings.CutPrefix(r.URL.Path, "/bot"+tgToken+"/")
		if !ok {
			rw.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(rw, `{"ok":false,"description":"Unauthorized"}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case "getMe":
			fmt.Fprint(rw, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"nt_test_bot"}}`)
		case "getUpdates":
			off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
			f.offsets = append(f.offsets, off)
			status, body := f.updates(r.Context(), off)
			rw.WriteHeader(status)
			fmt.Fprint(rw, body)
		case "sendMessage":
			var p struct {
				ChatID string `json:"chat_id"`
				Text   string
			}
			json.NewDecoder(r.Body).Decode(&p)
			if p.ChatID == "666" {
				rw.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(rw, `{"ok":false,"description":"Bad Request: chat not found"}`)
				return
			}
			f.sent = append(f.sent, p.ChatID+": "+p.Text)
			fmt.Fprint(rw, `{"ok":true}`)
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTG) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// fakeSetup replaces the machine of setup; installs collects the
// install-service calls.
func fakeSetup(t *testing.T, tg *fakeTG, ft *fakeTerm) (h *setupHost, installs *[]service.Options) {
	installs = new([]service.Options)
	h = &setupHost{
		in:     ft,
		tty:    true,
		noEcho: ft.noEcho,
		euid:   1000,
		goos:   "linux",
		etcDir: filepath.Join(t.TempDir(), "etc"),
		group:  func() (int, error) { return 0, fmt.Errorf("no group") },
		install: func(_ context.Context, _ io.Writer, o service.Options) error {
			*installs = append(*installs, o)
			return nil
		},
		cookies:     func(*config.Config) error { return nil },
		hostname:    func() (string, error) { return "orendil.example.org", nil },
		userNodes:   func() []userNode { return nil },
		telegramAPI: tg.srv.URL,
		torProxy:    config.DefaultTorProxy,
		poll:        time.Second,
		waitChat:    20 * time.Second,
	}
	if ft == nil {
		h.in, h.tty, h.noEcho = strings.NewReader(""), false, func() (func(), error) { return nil, fmt.Errorf("no terminal") }
	}
	saved := newSetupHost
	newSetupHost = func() setupHost { return *h }
	t.Cleanup(func() { newSetupHost = saved })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return h, installs
}

func runSetup(ctx context.Context, out io.Writer, args ...string) (code int, stderr string) {
	var errb bytes.Buffer
	code = setupCmd(ctx, args[1:], out, &errb) // args[0] is "setup"
	return code, errb.String()
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var nonceRE = regexp.MustCompile(`\?start=([A-Za-z0-9_-]{22})\n`)

// The local node is detected; the chat id comes from the /start link, and
// old updates and other users' /start are ignored.
func TestSetupLocalNode(t *testing.T) {
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	home := t.TempDir()
	conf, cookie := filepath.Join(home, ".bitcoin", "bitcoin.conf"), filepath.Join(home, ".bitcoin", ".cookie")
	os.MkdirAll(filepath.Dir(conf), 0o700)
	os.WriteFile(conf, []byte("server=1\nrpcport="+w.srv.URL[strings.LastIndex(w.srv.URL, ":")+1:]+"\n"), 0o600)
	os.WriteFile(cookie, []byte("__cookie__:x"), 0o600)
	saved := detectEnv
	t.Cleanup(func() { detectEnv = saved })
	detectEnv = config.Env{Root: t.TempDir(), Home: home, Getenv: func(string) string { return "" }}

	tg := newFakeTG(t)
	ft := newFakeTerm(t,
		turn{prompt: "Watch this node? [Y/n] "},
		turn{prompt: "Name of this node in alerts [orendil]: "},
		turn{prompt: "References: Esplora API URLs separated by spaces, or none [https://mempool.space/api https://blockstream.info/api]: ", answer: w.srv.URL + "/api"},
		turn{prompt: "Preset [2]: "},
		turn{prompt: "Name of this tracker in front of every alert, to tell several trackers apart (none for no name) [orendil]: "},
		turn{prompt: "Do you already have a Telegram bot for the alerts? [y/N] "},
		turn{prompt: "Bot token (input hidden): ", secret: true},
		turn{prompt: "Bot token (input hidden): ", answer: "123:WRONG", secret: true},
		turn{prompt: "Bot token (input hidden): ", answer: tgToken, secret: true},
		turn{prompt: "Choice [1]: "},
		turn{prompt: "Install and start the systemd service now (node-tracker install-service -user)? [Y/n] "},
	)
	tg.updates = func(_ context.Context, offset int64) (int, string) {
		m := nonceRE.FindStringSubmatch(ft.transcript())
		if m == nil {
			t.Error("getUpdates before the link was shown")
			return 500, ""
		}
		if offset == 0 { // left over from before, and someone else's /start
			return 200, `{"ok":true,"result":[{"update_id":5,"message":{"text":"/start","chat":{"id":7,"type":"private"}}},` +
				`{"update_id":6,"message":{"text":"/start AAAAAAAAAAAAAAAAAAAAAA","chat":{"id":666,"type":"private"}}},` +
				`{"update_id":7,"message":{"text":"/start ` + m[1] + `x","chat":{"id":667,"type":"private"}}}]}`
		}
		return 200, `{"ok":true,"result":[{"update_id":8,"message":{"text":"/start ` + m[1] + `","chat":{"id":42,"type":"private","username":"me\u001b[2J"}}}]}`
	}
	_, installs := fakeSetup(t, tg, ft)

	code, errs := runSetup(context.Background(), ft, "setup")
	out := ft.transcript()
	t.Logf("transcript:\n%s", out)
	ft.done()
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, errs)
	}
	if strings.Contains(out, "reached over Tor") {
		t.Error("down_for explained for a local node")
	}
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "node-tracker")
	path, tok := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "telegram-token")
	for _, s := range []string{
		"Found bitcoind at http://127.0.0.1:",
		": chain main, height 938,266, cookie " + cookie + "\n",
		"telegram rejected the token; copy only the token (e.g. 123456789:AAH…) again from @BotFather\n",
		"No token entered: paste it and press Enter; the input stays hidden.\n",
		"Bot: @nt_test_bot (token …OKEN)\n",
		"Found: you (@me?[2J), chat id 42\n",
		"Sending a test message … sent.",
		"wrote " + tok + "\n",
		"wrote " + path + "\n",
		"orendil  OK     938266",
		"notifier telegram  token valid (@nt_test_bot)",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("no %q in the transcript", s)
		}
	}
	if strings.Contains(out, tgToken) || strings.Contains(out, "WRONG") {
		t.Error("a token shows in the transcript")
	}
	if got := tg.messages(); len(got) != 1 || !strings.HasPrefix(got[0], "42: ") {
		t.Errorf("sent %q", got)
	}
	if tg.offsets[0] != 0 || tg.offsets[1] != 8 {
		t.Errorf("getUpdates offsets %v, want 0, 8", tg.offsets)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Errorf("files left in %s: %v", dir, ents)
	}
	if mode(t, dir) != 0o700 || mode(t, path) != 0o600 || mode(t, tok) != 0o600 || read(t, tok) != tgToken+"\n" {
		t.Errorf("modes %v %v %v, token %q", mode(t, dir), mode(t, path), mode(t, tok), read(t, tok))
	}
	cfg, _, err := config.LoadEnv(path, detectEnv)
	if err != nil {
		t.Fatal(err)
	}
	if tg := cfg.Targets[0]; tg.Name != "orendil" || tg.Node.CookieFile != cookie || len(tg.References) != 1 || *tg.Alert.Threshold != 3 || *tg.Alert.RecoverThreshold != 1 || tg.Alert.For != 2 || tg.Alert.DownFor != 2 || cfg.Name != "orendil" {
		t.Errorf("target %+v", tg)
	}
	if n := cfg.Notifiers[0]; n.ChatID != "42" || n.BotTokenFile != tok || cfg.Interval != 30*time.Second {
		t.Errorf("notifier %+v", n)
	}
	if len(*installs) != 1 || (*installs)[0] != (service.Options{}) {
		t.Errorf("install-service %+v", *installs)
	}
}

// A remote node over Tor: the proxy is checked, the rpcauth line is made
// for a new password, the webhook conflict and a wrong chat id are
// explained, and the old config is kept as .bak.
func TestSetupOnionNode(t *testing.T) {
	const onion = "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion"
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	socks := newSOCKS5(t, w.srv.Listener.Addr().String())
	tg := newFakeTG(t)
	tg.updates = func(context.Context, int64) (int, string) {
		return 409, `{"ok":false,"description":"Conflict: can't use getUpdates method while webhook is active"}`
	}
	ft := newFakeTerm(t,
		turn{prompt: "Node RPC URL [http://127.0.0.1:8332]: ", answer: "http://" + onion + ":8332"},
		turn{prompt: "Tor SOCKS proxy [socks5://127.0.0.1:9050]: ", answer: "socks5://127.0.0.1:1"},
		turn{prompt: "Tor SOCKS proxy [socks5://127.0.0.1:9050]: ", answer: "socks5://" + socks.addr},
		turn{prompt: "Do you already have the node's RPC user and password? (Umbrel, myNode, RaspiBlitz, StartOS: yes) [Y/n] ", answer: "n"},
		turn{prompt: "RPC user [nodetracker]: "},
		turn{prompt: "Generate a new password and the rpcauth line for the node's bitcoin.conf? [y/N] ", answer: "y"},
		turn{prompt: "Press Enter when bitcoind has restarted: "},
		turn{prompt: "Name of this node in alerts [node]: ", answer: "home"},
		turn{prompt: "[https://mempool.space/api https://blockstream.info/api]: ", answer: "none"},
		turn{prompt: "Preset [2]: ", answer: "quiet"},
		turn{prompt: "Name of this tracker in front of every alert, to tell several trackers apart (none for no name) [orendil]: ", answer: "my tracker"},
		turn{prompt: "Name of this tracker in front of every alert, to tell several trackers apart (none for no name) [orendil]: ", answer: "shb"},
		turn{prompt: "Do you already have a Telegram bot for the alerts? [y/N] "},
		turn{prompt: "Bot token (input hidden): ", answer: tgToken, secret: true},
		turn{prompt: "Choice [1]: "},
		turn{prompt: "Choice [1]: ", answer: "4"},
		turn{prompt: "Chat id (Enter: back): ", answer: "666"},
		turn{prompt: "Choice [1]: ", answer: "3"},
		turn{prompt: "Channel's @name (Enter: back): ", answer: "@mychannel"},
		turn{prompt: "Replace them (the old config is kept as .bak, old secrets are not)? [y/N] ", answer: "y"},
		turn{prompt: "Install and start the systemd service now (node-tracker install-service -user -config ", answer: "n"},
	)
	_, installs := fakeSetup(t, tg, ft)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("old\n"), 0o600)

	code, errs := runSetup(context.Background(), ft, "setup", "-config", path)
	out := ft.transcript()
	t.Logf("transcript:\n%s", out)
	ft.done()
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, errs)
	}
	pwFile := filepath.Join(dir, "home-rpc-password")
	pw := strings.TrimSpace(read(t, pwFile))
	m := regexp.MustCompile(`\n    rpcauth=(nodetracker:([0-9a-f]{32})\$[0-9a-f]{64})\n    rpcwhitelist=nodetracker:getblockchaininfo\n    rpcwhitelistdefault=0\n`).FindStringSubmatch(out)
	if m == nil || bitcoind.RPCAuth("nodetracker", pw, m[2]) != m[1] || strings.Contains(out, pw) {
		t.Errorf("rpcauth line %q does not match the password file, or the password shows", m)
	}
	for _, s := range []string{
		"No local bitcoind answered at http://127.0.0.1:8332",
		"Tor does not answer at socks5://127.0.0.1:1: dial tcp 127.0.0.1:1: connect: connection refused. Install and start Tor",
		"Testing the node … ok: chain main, height 938,266\n",
		"Telegram refuses to hand out the bot's messages: the bot has a webhook",
		"The Telegram settings of " + path + " can't be kept: ",
		"failed: HTTP 400: Bad Request: chat not found (wrong chat id, or the bot was never started in that chat)\n",
		"backed up " + path + " to " + path + ".bak\n",
		`"node unreachable" is sent only after 5 checks in a row (about 5m0s; alert.down_for)`,
		"Start it later with: node-tracker install-service -user -config " + path,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("no %q in the transcript", s)
		}
	}
	if read(t, path+".bak") != "old\n" || mode(t, pwFile) != 0o600 || len(*installs) != 0 {
		t.Errorf("bak %q, password mode %v, installs %v", read(t, path+".bak"), mode(t, pwFile), *installs)
	}
	cfg, _, err := config.LoadEnv(path, detectEnv)
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Targets[0].Node
	if n.URL != "http://"+onion+":8332" || n.Proxy != "socks5://"+socks.addr || n.Username != "nodetracker" || n.PasswordFile != pwFile || string(n.Password) != pw ||
		len(cfg.Targets[0].References) != 0 || *cfg.Targets[0].Alert.Threshold != 6 || cfg.Targets[0].Alert.DownFor != 5 || cfg.Interval != time.Minute || cfg.Notifiers[0].ChatID != "@mychannel" || cfg.Name != "shb" {
		t.Errorf("config %+v", cfg)
	}
	if !strings.Contains(out, "the tracker name must match") {
		t.Error("an invalid tracker name was not refused")
	}
	if got := tg.messages(); len(got) != 1 || !strings.HasPrefix(got[0], "@mychannel: ") || !strings.Contains(got[0], "[shb] ") {
		t.Errorf("sent %q", got)
	}
}

// A node box on this host: its RPC user and password are typed, nothing is
// generated. Telegram does not answer, so the token is kept untested and
// the chat id typed.
func TestSetupExistingUserTelegramDown(t *testing.T) {
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	ft := newFakeTerm(t,
		turn{prompt: "Node RPC URL [http://127.0.0.1:8332]: ", answer: w.srv.URL + "/rpc"},
		turn{prompt: "Use bitcoind's cookie file for auth? [Y/n] ", answer: "n"},
		turn{prompt: "Do you already have the node's RPC user and password? (Umbrel, myNode, RaspiBlitz, StartOS: yes) [y/N] ", answer: "y"},
		turn{prompt: "RPC user: ", answer: "umbrel"},
		turn{prompt: "RPC password (input hidden): ", answer: "boxPW", secret: true},
		turn{prompt: "Name of this node in alerts [orendil]: "},
		turn{prompt: "References", answer: "none"},
		turn{prompt: "Preset [2]: "},
		turn{prompt: "Name of this tracker in front of every alert, to tell several trackers apart (none for no name) [orendil]: ", answer: "none"},
		turn{prompt: "Do you already have a Telegram bot for the alerts? [y/N] "},
		turn{prompt: "Bot token (input hidden): ", answer: tgToken, secret: true},
		turn{prompt: "[r]etry, [k]eep the token untested, or [t]ry through Tor (socks5://127.0.0.1:1)? [r]: "},
		turn{prompt: "[r]etry, [k]eep the token untested, or [t]ry through Tor (socks5://127.0.0.1:1)? [r]: ", answer: "t"},
		turn{prompt: "[r]etry, [k]eep the token untested, or [t]ry through Tor (socks5://127.0.0.1:1)? [r]: ", answer: "k"},
		turn{prompt: "Chat id or @channel: ", answer: "42"},
		turn{prompt: "Install and start the systemd service now", answer: "n"},
	)
	h, _ := fakeSetup(t, newFakeTG(t), ft)
	h.telegramAPI, h.torProxy = "http://127.0.0.1:1", "socks5://127.0.0.1:1" // Tor is down too
	code, errs := runSetup(context.Background(), ft, "setup")
	out := ft.transcript()
	t.Logf("transcript:\n%s", out)
	ft.done()
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, errs)
	}
	for _, s := range []string{
		"Testing the node … ok: chain main, height 938,266\n",
		"could not check the token with Telegram: Get \"http://127.0.0.1:1\": dial tcp 127.0.0.1:1: connect: connection refused\n",
		"Tor does not answer at socks5://127.0.0.1:1: dial tcp 127.0.0.1:1: connect: connection refused. Install and start Tor",
		"Token …OKEN kept untested.",
		"Once Telegram answers, send a test message: node-tracker test-notify\n",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("no %q in the transcript", s)
		}
	}
	if n := strings.Count(out, "could not check the token"); n != 2 {
		t.Errorf("token checked %d times, want 2: once, and on retry", n)
	}
	if strings.Contains(out, "rpcauth") || strings.Contains(out, "boxPW") || strings.Contains(out, tgToken) {
		t.Error("rpcauth offered, or a secret shows")
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "node-tracker", "config.yaml")
	cfg, _, err := config.LoadEnv(path, detectEnv)
	if err != nil {
		t.Fatal(err)
	}
	if n, tn := cfg.Targets[0].Node, cfg.Notifiers[0]; n.Username != "umbrel" || n.Password != "boxPW" || tn.ChatID != "42" || tn.Proxy != "" {
		t.Errorf("config %+v", cfg)
	}
}

// A node not on this host: generating the rpcauth line defaults to no.
// Telegram is reached only through Tor, which then goes into the config.
func TestSetupRemoteNodeTelegramTor(t *testing.T) {
	const onion = "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion"
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	nodeSOCKS := newSOCKS5(t, w.srv.Listener.Addr().String())
	tg := newFakeTG(t)
	tg.updates = func(ctx context.Context, _ int64) (int, string) {
		// Short polls: the fake SOCKS server does not pass on the client's close.
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		return 200, `{"ok":true,"result":[]}`
	}
	tor := "socks5://" + newSOCKS5(t, tg.srv.Listener.Addr().String()).addr
	ft := newFakeTerm(t,
		turn{prompt: "Node RPC URL [http://127.0.0.1:8332]: ", answer: "http://" + onion + ":8332"},
		turn{prompt: "Tor SOCKS proxy [socks5://127.0.0.1:9050]: ", answer: "socks5://" + nodeSOCKS.addr},
		turn{prompt: "Do you already have the node's RPC user and password? (Umbrel, myNode, RaspiBlitz, StartOS: yes) [Y/n] ", answer: "n"},
		turn{prompt: "RPC user [nodetracker]: "},
		turn{prompt: "Generate a new password and the rpcauth line for the node's bitcoin.conf? [y/N] "},
		turn{prompt: "RPC password (input hidden): ", answer: "s3cret", secret: true},
		turn{prompt: "Name of this node in alerts [node]: "},
		turn{prompt: "References", answer: "none"},
		turn{prompt: "Preset [2]: "},
		turn{prompt: "Name of this tracker in front of every alert, to tell several trackers apart (none for no name) [orendil]: ", answer: "none"},
		turn{prompt: "Do you already have a Telegram bot for the alerts? [y/N] "},
		turn{prompt: "Bot token (input hidden): ", answer: tgToken, secret: true},
		turn{prompt: "or [t]ry through Tor (" + tor + ")? [r]: ", answer: "t"},
		turn{prompt: "Choice [1]: ", answer: "4"},
		turn{prompt: "Chat id (Enter: back): ", answer: "42"},
		turn{prompt: "Install and start the systemd service now", answer: "n"},
	)
	h, _ := fakeSetup(t, tg, ft)
	h.telegramAPI, h.torProxy = "http://127.0.0.1:1", tor
	path := filepath.Join(t.TempDir(), "config.yaml")
	code, errs := runSetup(context.Background(), ft, "setup", "-config", path)
	out := ft.transcript()
	t.Logf("transcript:\n%s", out)
	ft.done()
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, errs)
	}
	for _, s := range []string{"Bot: @nt_test_bot (token …OKEN)\n", "Sending a test message … sent.", "notifier telegram  token valid (@nt_test_bot)"} {
		if !strings.Contains(out, s) {
			t.Errorf("no %q in the transcript", s)
		}
	}
	if strings.Contains(out, "rpcauth=") || strings.Contains(out, "s3cret") {
		t.Error("rpcauth generated, or the password shows")
	}
	if got := tg.messages(); len(got) != 1 || !strings.HasPrefix(got[0], "42: ") {
		t.Errorf("sent %q", got)
	}
	cfg, _, err := config.LoadEnv(path, detectEnv)
	if err != nil {
		t.Fatal(err)
	}
	if n := cfg.Notifiers[0]; n.Proxy != tor || n.APIURL != "http://127.0.0.1:1" || cfg.Targets[0].Node.Password != "s3cret" {
		t.Errorf("config %+v", cfg)
	}
}

// -non-interactive as root: /etc/node-tracker with the service's group,
// install-service -system, and -force to replace files.
func TestSetupNonInteractive(t *testing.T) {
	w := newWorld(t)
	w.node.Store(938260)
	w.tip.Store(938266)
	tg := newFakeTG(t)
	h, installs := fakeSetup(t, tg, nil)
	h.euid = 0
	h.group = func() (int, error) { return os.Getgid(), nil }
	src := t.TempDir()
	pwSrc, tokSrc := filepath.Join(src, "pw"), filepath.Join(src, "tok")
	os.WriteFile(pwSrc, []byte("s3cret\n"), 0o600)
	os.WriteFile(tokSrc, []byte(tgToken+"\n"), 0o600)
	args := []string{"setup", "-non-interactive", "-node-url", w.srv.URL + "/rpc", "-rpc-user", "tracker", "-rpc-password-file", pwSrc,
		"-token-file", tokSrc, "-chat-id", "42", "-references", w.srv.URL + "/api", "-preset", "fast", "-name", "btc", "-down-for", "7", "-install-service"}

	var out bytes.Buffer
	code, errs := runSetup(context.Background(), &out, args...)
	if code != exitOK {
		t.Fatalf("code = %d\n%s\n%s", code, out.String(), errs)
	}
	path := filepath.Join(h.etcDir, "config.yaml")
	if mode(t, h.etcDir) != 0o750 || mode(t, path) != 0o640 || mode(t, filepath.Join(h.etcDir, "telegram-token")) != 0o640 ||
		read(t, filepath.Join(h.etcDir, "btc-rpc-password")) != "s3cret\n" {
		t.Errorf("modes or password file wrong")
	}
	for _, s := range []string{"bot: @nt_test_bot (token …OKEN)\n", "test message sent\n", "btc     LAGGING  938260  938266", "LAGGING by 6 is the node's current state"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("no %q in\n%s", s, out.String())
		}
	}
	if strings.Contains(out.String(), "s3cret") || strings.Contains(out.String(), tgToken) {
		t.Error("a secret shows in the output")
	}
	if len(*installs) != 1 || (*installs)[0] != (service.Options{System: true, Config: path}) {
		t.Errorf("install-service %+v", *installs)
	}
	cfg, _, err := config.LoadEnv(path, detectEnv)
	if err != nil || *cfg.Targets[0].Alert.Threshold != 2 || *cfg.Targets[0].Alert.RecoverThreshold != 0 || cfg.Targets[0].Node.Username != "tracker" || cfg.Targets[0].Alert.DownFor != 7 || cfg.Name != "orendil" {
		t.Fatalf("config %+v, %v", cfg, err)
	}

	before := read(t, path)
	args[len(args)-1] = "-force=false"
	args[slicesIndex(args, "42")] = "43"
	if code, errs := runSetup(context.Background(), io.Discard, args...); code != exitUsage || !strings.Contains(errs, "exist and differ; rerun with -force") || read(t, path) != before {
		t.Errorf("changed config without -force: code=%d\n%s", code, errs)
	}
	tok := filepath.Join(h.etcDir, "telegram-token")
	os.WriteFile(tok, []byte("123:OLD-LEAKED\n"), 0o640)
	if code, errs := runSetup(context.Background(), io.Discard, append(args, "-force")...); code != exitOK || read(t, path+".bak") != before || !strings.Contains(read(t, path), `chat_id: "43"`) {
		t.Errorf("-force: code=%d\n%s", code, errs)
	}
	// A replaced secret is not kept: it may be the reason for the change.
	if _, err := os.Lstat(tok + ".bak"); !errors.Is(err, fs.ErrNotExist) || read(t, tok) != tgToken+"\n" {
		t.Errorf("token .bak: %v, token %q", err, read(t, tok))
	}

	os.WriteFile(tokSrc, []byte("123:WRONG\n"), 0o600)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{append(args, "-force"), "-token-file: telegram rejected the token"},
		{[]string{"setup", "-non-interactive", "-chat-id", "42"}, "needs -token-file and -chat-id"},
		{[]string{"setup", "-non-interactive", "-token-file", tokSrc, "-chat-id", "42", "-node-url", "http://127.0.0.1:1"}, "-node-url needs -cookie-file"},
		{[]string{"setup", "-non-interactive", "-token-file", tokSrc, "-chat-id", "42", "-node-url", "http://10.0.0.2:8332", "-cookie-file", "/c"}, "plain http is only allowed"},
		{[]string{"setup", "-non-interactive", "-token-file", tokSrc, "-chat-id", "42", "-preset", "slow"}, `-preset "slow"`},
		{[]string{"setup", "-non-interactive", "-token-file", tokSrc, "-chat-id", "42", "-down-for", "101"}, "-down-for 101: must be between 1 and 100"},
		{[]string{"setup", "-non-interactive", "-token-file", tokSrc, "-chat-id", "42", "-tracker-name", "my tracker"}, "-tracker-name: the tracker name must match"},
		{[]string{"setup", "-non-interactive", "-token-file", tokSrc, "-chat-id", "42", "-down-for", "-1"}, "-down-for -1: must be between 1 and 100"},
	} {
		if code, errs := runSetup(context.Background(), io.Discard, tc.args...); code != exitUsage || !strings.Contains(errs, tc.want) {
			t.Errorf("%v: code=%d, want %q in\n%s", tc.args, code, tc.want, errs)
		}
	}
	if !strings.Contains(read(t, path), `chat_id: "43"`) {
		t.Error("a failed run changed the config")
	}
}

// As root, the config and its secrets must go in /etc/node-tracker: that is
// the only place install-service -system takes secrets from.
func TestSetupRootConfigOutsideEtc(t *testing.T) {
	h, _ := fakeSetup(t, newFakeTG(t), nil)
	h.euid = 0
	tok := filepath.Join(t.TempDir(), "tok")
	os.WriteFile(tok, []byte(tgToken+"\n"), 0o600)
	elsewhere := filepath.Join(t.TempDir(), "config.yaml")
	code, errs := runSetup(context.Background(), io.Discard, "setup", "-non-interactive", "-config", elsewhere, "-token-file", tok, "-chat-id", "42")
	if code != exitUsage || !strings.Contains(errs, "as root, setup writes the system service's config, which goes in "+h.etcDir) {
		t.Fatalf("code=%d\n%s", code, errs)
	}
	if _, err := os.Stat(elsewhere); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("wrote %s: %v", elsewhere, err)
	}
	// In the service's directory it is fine, under any name.
	h2, _ := fakeSetup(t, newFakeTG(t), nil)
	h2.euid = 0
	if code, errs := runSetup(context.Background(), io.Discard, "setup", "-non-interactive", "-config", filepath.Join(h2.etcDir, "other.yaml"), "-token-file", tok, "-chat-id", "42"); strings.Contains(errs, "as root, setup writes") {
		t.Errorf("code=%d\n%s", code, errs)
	}
}

func slicesIndex(s []string, v string) int {
	for i, e := range s {
		if e == v {
			return i
		}
	}
	return -1
}

// Without a terminal, setup refuses to ask, so no secret is read from a
// pipe by mistake; answer flags need -non-interactive.
func TestSetupRefusals(t *testing.T) {
	fakeSetup(t, newFakeTG(t), nil)
	if code, errs := runSetup(context.Background(), io.Discard, "setup"); code != exitUsage || !strings.Contains(errs, "standard input is not a terminal") {
		t.Errorf("no tty: code=%d %q", code, errs)
	}
	if code, errs := runSetup(context.Background(), io.Discard, "setup", "-chat-id", "42"); code != exitUsage || !strings.Contains(errs, "-chat-id need -non-interactive") {
		t.Errorf("flag without -non-interactive: code=%d %q", code, errs)
	}
}

// Ctrl-C at the hidden token prompt turns the echo back on and writes
// nothing.
func TestSetupInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ft := newFakeTerm(t,
		turn{prompt: "Node RPC URL [http://127.0.0.1:8332]: "},
		turn{prompt: "Use bitcoind's cookie file for auth? [Y/n] "},
		turn{prompt: "Cookie file [/nonexistent-node-tracker-test/var/lib/bitcoind/.cookie]: "},
		turn{prompt: "[r]etry, [c]hange the settings, or [k]eep them untested? [r]: ", answer: "k"},
		turn{prompt: "Name of this node in alerts [orendil]: "},
		turn{prompt: "References", answer: "none"},
		turn{prompt: "Preset [2]: "},
		turn{prompt: "Name of this tracker in front of every alert, to tell several trackers apart (none for no name) [orendil]: ", answer: "none"},
		turn{prompt: "Do you already have a Telegram bot for the alerts? [y/N] "},
		turn{prompt: "Bot token (input hidden): ", do: cancel},
	)
	fakeSetup(t, newFakeTG(t), ft)
	code, errs := runSetup(ctx, ft, "setup")
	ft.done()
	if code == exitOK || !strings.Contains(errs, "setup aborted (context canceled); nothing written") {
		t.Fatalf("code=%d %q\n%s", code, errs, ft.transcript())
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "node-tracker")); err == nil {
		t.Error("files written")
	}
}

// Typing while waiting does not stop the wait; the typed menu choices
// refuse the bot itself, go back on Enter and check the id.
func TestAskChat(t *testing.T) {
	tg := newFakeTG(t)
	var ft *fakeTerm
	tg.updates = func(ctx context.Context, _ int64) (int, string) {
		if !strings.Contains(ft.transcript(), "Still waiting") {
			select { // a long poll that sees nothing
			case <-ctx.Done():
			case <-time.After(50 * time.Millisecond):
			}
			return 200, `{"ok":true,"result":[]}`
		}
		m := regexp.MustCompile(`\?startgroup=([A-Za-z0-9_-]{22})\n`).FindStringSubmatch(ft.transcript())
		return 200, `{"ok":true,"result":[{"update_id":1,"message":{"text":"/start@nt_test_bot ` + m[1] + `","chat":{"id":-100,"type":"group","title":"ops"}}}]}`
	}
	ft = newFakeTerm(t,
		turn{prompt: "Choice [1]: ", answer: "116947131"},
		turn{prompt: "Choice [1]: ", answer: "4"},
		turn{prompt: "Chat id (Enter: back): ", answer: "@NT_Test_Bot"},
		turn{prompt: "Chat id (Enter: back): ", answer: "x"},
		turn{prompt: "Chat id (Enter: back): "},
		turn{prompt: "Choice [1]: ", answer: "2"},
		turn{prompt: "there is nothing to type here (Ctrl-C aborts) …\n", answer: "@nt_test_bot"},
	)
	h, _ := fakeSetup(t, tg, ft)
	w := &wizard{setupHost: *h, out: ft, hc: httpx.NewClient(), done: make(chan struct{})}
	defer close(w.done)
	id, err := w.askChat(context.Background(), tgToken, "nt_test_bot")
	ft.done()
	out := ft.transcript()
	for _, s := range []string{
		"@nt_test_bot is the bot itself, it can't send to itself. For a chat with you, go back (Enter) and pick 1.\n",
		"The chat id must be a numeric chat id or @channelname\n",
		"If nothing happens (e.g. the bot is in the group already), send this message in the group:\n  /start@nt_test_bot ",
		"Still waiting: open the link above in Telegram, or send the message above.",
		"Found: group \"ops\", chat id -100\n",
		"Pick 1, 2, 3 or 4.\n",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("no %q in\n%s", s, out)
		}
	}
	if id != "-100" || err != nil {
		t.Fatalf("askChat = %q, %v\n%s", id, err, out)
	}
}

// A rerun keeps the bot and chat of the config, with its api_url and proxy;
// if the test message fails, the token stays and only the chat is asked.
func TestKeepTelegram(t *testing.T) {
	for _, chat := range []string{"42", "666"} {
		tg := newFakeTG(t)
		proxy := "socks5://" + newSOCKS5(t, tg.srv.Listener.Addr().String()).addr
		dir := t.TempDir()
		path, tok := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "telegram-token")
		os.WriteFile(tok, []byte(tgToken+"\n"), 0o600)
		os.WriteFile(path, []byte("targets:\n  - name: btc\n    references: []\nnotifiers:\n  - name: telegram\n    kind: telegram\n"+
			"    bot_token_file: "+tok+"\n    chat_id: \""+chat+"\"\n    api_url: "+tg.srv.URL+"\n    proxy: "+proxy+"\n"), 0o600)
		script := []turn{{prompt: "Keep the Telegram bot and chat of " + path + " (chat " + chat + ", token …OKEN)? [Y/n] "}}
		if chat == "666" {
			script = append(script, turn{prompt: "Choice [1]: ", answer: "4"}, turn{prompt: "Chat id (Enter: back): ", answer: "43"})
		}
		ft := newFakeTerm(t, script...)
		h, _ := fakeSetup(t, tg, ft)
		h.telegramAPI = "http://127.0.0.1:1" // the config's api_url is used
		w := &wizard{setupHost: *h, out: ft, hc: httpx.NewClient(), done: make(chan struct{})}
		a := &answers{}
		err := w.askTelegram(context.Background(), a, path)
		close(w.done)
		ft.done()
		want := map[string]string{"42": "42", "666": "43"}[chat]
		if err != nil || a.token != tgToken || a.chatID != want || !a.chatTested || a.tgProxy != proxy || w.telegramAPI != tg.srv.URL {
			t.Errorf("chat %s: err %v, answers %+v, api %s\n%s", chat, err, a, w.telegramAPI, ft.transcript())
		}
		if strings.Contains(ft.transcript(), "Bot token") || strings.Contains(ft.transcript(), "Do you already have a Telegram bot") {
			t.Errorf("chat %s: token asked again\n%s", chat, ft.transcript())
		}
	}
}

func TestIsStart(t *testing.T) {
	const n = "Zm9vYmFyYmF6cXV4cXV1eA"
	for text, want := range map[string]bool{
		"/start " + n:                  true,
		"/start@NT_Test_Bot " + n:      true,
		"  /start   " + n + "  ":       true,
		"/start@other_bot " + n:        false,
		"/start":                       false,
		"/start " + n + "x":            false,
		"/start " + n + " extra":       false,
		"/startgroup " + n:             false,
		"hello " + n:                   false,
		"/start " + strings.ToLower(n): false,
	} {
		if got := isStart(text, "nt_test_bot", n); got != want {
			t.Errorf("isStart(%q) = %v, want %v", text, got, want)
		}
	}
}

// -proxy is written for a .onion node, so the service uses it too.
func TestSetupNonInteractiveProxy(t *testing.T) {
	const onion = "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion"
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	socks := newSOCKS5(t, w.srv.Listener.Addr().String())
	src := t.TempDir()
	pwSrc, tokSrc := filepath.Join(src, "pw"), filepath.Join(src, "tok")
	os.WriteFile(pwSrc, []byte("s3cret\n"), 0o600)
	os.WriteFile(tokSrc, []byte(tgToken+"\n"), 0o600)
	tg := newFakeTG(t)
	tgSOCKS := "socks5://" + newSOCKS5(t, tg.srv.Listener.Addr().String()).addr
	h, _ := fakeSetup(t, tg, nil)
	h.telegramAPI = "http://127.0.0.1:1" // reached only through -telegram-proxy
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer
	args := []string{"setup", "-non-interactive", "-config", path, "-node-url", "http://" + onion + ":8332", "-proxy", "socks5://" + socks.addr,
		"-rpc-user", "tracker", "-rpc-password-file", pwSrc, "-token-file", tokSrc, "-chat-id", "42", "-references", "none"}
	code, errs := runSetup(context.Background(), &out, append(args, "-telegram-proxy", tgSOCKS)...)
	if code != exitOK || !strings.Contains(out.String(), "node http://"+onion+":8332: chain main") || !strings.Contains(out.String(), "test message sent\n") {
		t.Fatalf("code = %d\n%s\n%s", code, out.String(), errs)
	}
	if code, errs := runSetup(context.Background(), io.Discard, append(args, "-telegram-proxy", "tor://x")...); code != exitUsage ||
		!strings.Contains(errs, `-telegram-proxy "tor://x": scheme must be one of`) {
		t.Errorf("bad -telegram-proxy: code=%d\n%s", code, errs)
	}
	cfg, _, err := config.LoadEnv(path, detectEnv)
	if err != nil || cfg.Targets[0].Node.Proxy != "socks5://"+socks.addr || cfg.Notifiers[0].Proxy != tgSOCKS {
		t.Fatalf("config %+v, %v", cfg, err)
	}
}

// As root, ~ is /root, which the system service can't read.
func TestCookiePathErr(t *testing.T) {
	for _, tt := range []struct {
		root bool
		c    string
		ok   bool
	}{
		{false, "/var/lib/bitcoind/.cookie", true},
		{false, "~/.bitcoin/.cookie", true},
		{false, ".bitcoin/.cookie", false},
		{true, "/var/lib/bitcoind/.cookie", true},
		{true, "~/.bitcoin/.cookie", false},
	} {
		if err := (&wizard{root: tt.root}).cookiePathErr(tt.c); (err == nil) != tt.ok {
			t.Errorf("root=%v %q: %v", tt.root, tt.c, err)
		}
	}
}

// down_for: -down-for wins for any node; else about 5 minutes for a .onion
// node, else the preset's for (left out of the config).
func TestNodeDownFor(t *testing.T) {
	const onion = "http://2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion:8332"
	for _, tt := range []struct {
		url             string
		preset, downFor int
		want            int
	}{
		{"http://127.0.0.1:8332", 1, 0, 2},
		{onion, 1, 0, 10}, // balanced: 30s
		{onion, 2, 0, 5},  // quiet: 60s
		{onion, 1, 4, 4},  // -down-for
		{"http://127.0.0.1:8332", 0, 7, 7},
	} {
		a := &answers{node: config.Endpoint{URL: tt.url}, preset: tt.preset, downFor: tt.downFor}
		if got := a.nodeDownFor(); got != tt.want {
			t.Errorf("%s preset %d -down-for %d: %d, want %d", tt.url, tt.preset, tt.downFor, got, tt.want)
		}
	}
}

// As root, with no system bitcoind, setup points at the bitcoinds run by
// users and offers to stop, so that setup runs as that user.
func TestSetupUserNodeHint(t *testing.T) {
	const hint = "No system bitcoind answered at http://127.0.0.1:8332.\n" +
		"bitcoind runs as user \"alice\", with the cookie /home/alice/.bitcoin/.cookie.\n" +
		"bitcoind runs as user \"bob\", with the cookie /mnt/ssd/bitcoin/.cookie.\n" +
		"Best fit: a user service. Run 'node-tracker setup' as that user, without sudo.\n"
	const notes = "The system service can't read /home/alice/.bitcoin/.cookie: it is in the home of \"alice\".\n" +
		"For the system service, give the cookie /mnt/ssd/bitcoin/.cookie; the node-tracker user must be able to read it.\n"
	nodes := []userNode{{"alice", "http://127.0.0.1:8332", "/home/alice/.bitcoin/.cookie", true}, {"bob", "http://127.0.0.1:38332", "/mnt/ssd/bitcoin/.cookie", false},
		{"carol", "http://127.0.0.1:8332", "/nonexistent-node-tracker-test/var/lib/bitcoind/.cookie", false}} // the one tried
	for _, tt := range []struct {
		name  string
		euid  int
		turns func(cancel func()) []turn
		code  int
		shown []string
	}{
		{"stop", 0, func(func()) []turn { return []turn{{prompt: "Stop here? [Y/n] "}} },
			exitOK, []string{hint, "Stop here? [Y/n] \nStopped; nothing written.\n"}},
		// The node with a cookie outside a home is the default.
		{"go on", 0, func(cancel func()) []turn {
			return []turn{{prompt: "Stop here? [Y/n] ", answer: "n"}, {prompt: "Node RPC URL [http://127.0.0.1:38332]: "},
				{prompt: "Use bitcoind's cookie file for auth? [Y/n] "}, {prompt: "Cookie file [/mnt/ssd/bitcoin/.cookie]: ", do: cancel}}
		}, exitFailure, []string{hint, notes}},
		{"not root", 1000, func(cancel func()) []turn {
			return []turn{{prompt: "Node RPC URL [http://127.0.0.1:8332]: ", do: cancel}}
		}, exitFailure, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ft := newFakeTerm(t, tt.turns(cancel)...)
			h, _ := fakeSetup(t, newFakeTG(t), ft)
			h.euid = tt.euid
			h.userNodes = func() []userNode { return nodes }
			code, _ := runSetup(ctx, ft, "setup")
			ft.done()
			out := ft.transcript()
			if code != tt.code {
				t.Errorf("exit code %d, want %d\n%s", code, tt.code, out)
			}
			for _, s := range tt.shown {
				if !strings.Contains(out, s) {
					t.Errorf("no %q in\n%s", s, out)
				}
			}
			if strings.Contains(out, "carol") || tt.euid != 0 && strings.Contains(out, "bitcoind runs as") {
				t.Errorf("wrong hints in\n%s", out)
			}
		})
	}
}

func TestUserNodes(t *testing.T) {
	proc, homes, data := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(homes, "bob"), 0o755)
	proc1 := func(pid, comm, uid string, args ...string) {
		d := filepath.Join(proc, pid)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "status"), []byte("Name:\t"+comm+"\nUid:\t"+uid+"\t"+uid+"\t"+uid+"\t"+uid+"\nGid:\t0\t0\t0\t0\n"), 0o644)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(append([]string{"/usr/bin/" + comm}, args...), "\x00")+"\x00"), 0o644)
	}
	cookie := func(p string) string {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("__cookie__:x\n"), 0o600)
		return p
	}
	// A cookie counts only if it is a regular file of the process's owner:
	// me, as the test can't make files of other users.
	me, other := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getuid()+1)
	alice := cookie(filepath.Join(homes, "alice", ".bitcoin", ".cookie"))
	os.WriteFile(filepath.Join(homes, "alice", ".bitcoin", "bitcoin.conf"), []byte("server=1\n"), 0o600)
	signet := cookie(filepath.Join(data, "signet", ".cookie"))
	proc1("10", "bitcoind", me, "-conf="+filepath.Join(homes, "alice", ".bitcoin", "bitcoin.conf"), "-rpcpassword=s3cret") // found
	proc1("11", "bitcoin-qt", me)                                                                                          // the same: once
	proc1("12", "bitcoind", "0")                                                                                           // root: the system's
	proc1("13", "bash", me)                                                                                                // not bitcoind
	proc1("14", "bitcoind", other)                                                                                         // bob: no cookie in his home
	proc1("15", "bitcoind", other, "-rpccookiefile="+alice)                                                                // bob: not his file
	proc1("16", "bitcoind", "4242")                                                                                        // unknown uid
	proc1("17", "bitcoind", me, "-datadir="+data, "-signet")                                                               // outside the home
	proc1("18", "bitcoind", me, "-rpccookiefile="+data)                                                                    // a directory
	proc1("self", "bitcoind", me)                                                                                          // not a pid
	users := map[string]user.User{me: {Username: "alice", HomeDir: filepath.Join(homes, "alice")}, other: {Username: "bob", HomeDir: filepath.Join(homes, "bob")}}
	lookup := func(uid string) (*user.User, error) {
		u, ok := users[uid]
		if !ok {
			return nil, user.UnknownUserIdError(4242)
		}
		return &u, nil
	}
	want := []userNode{{"alice", "http://127.0.0.1:8332", alice, true}, {"alice", "http://127.0.0.1:38332", signet, false}}
	if got := userNodes(proc, lookup); !slices.Equal(got, want) {
		t.Errorf("userNodes = %v, want %v", got, want)
	}
	// A service account with the home "/": only its arguments count.
	users[me] = user.User{Username: "svc", HomeDir: "/"}
	if got, want := userNodes(proc, lookup), []userNode{{"svc", "http://127.0.0.1:38332", signet, false}}; !slices.Equal(got, want) {
		t.Errorf("home /: userNodes = %v, want %v", got, want)
	}
	if got := userNodes(filepath.Join(proc, "missing"), lookup); got != nil {
		t.Errorf("no proc: %v", got)
	}
}

// As root, the service's cookie check runs before anything is written: if
// it fails, the config is written only if wanted, and no install is offered.
func TestSetupCheckServiceFirst(t *testing.T) {
	refused := service.UsageError{Err: errors.New("the service can't read bitcoind's cookie /data/bitcoin/.cookie.\nTo fix it:\n  1. sudo usermod -aG user node-tracker")}
	for _, ans := range []string{"", "y"} {
		ft := newFakeTerm(t, turn{prompt: "Write the config anyway, and install the service once that is fixed? [y/N] ", answer: ans})
		h, _ := fakeSetup(t, newFakeTG(t), ft)
		w := &wizard{setupHost: *h, out: ft, root: true, done: make(chan struct{})}
		w.cookies = func(*config.Config) error { return refused }
		err := w.checkService(context.Background(), &answers{name: "orendil", node: config.Endpoint{Kind: config.KindBitcoind, CookieFile: "/data/bitcoin/.cookie"}})
		close(w.done)
		ft.done()
		if want := map[string]error{"": errDeclined, "y": nil}[ans]; !errors.Is(err, want) && err != want || w.noSvc != (ans == "y") {
			t.Errorf("answer %q: err %v, noSvc %v", ans, err, w.noSvc)
		}
		if !strings.Contains(ft.transcript(), "Before anything is written: The service can't read bitcoind's cookie /data/bitcoin/.cookie.\nTo fix it:\n") {
			t.Errorf("no refusal shown:\n%s", ft.transcript())
		}
	}

	// -non-interactive: with -install-service nothing is written; without,
	// the config is written and the install is left for later.
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	src := t.TempDir()
	pwSrc, tokSrc := filepath.Join(src, "pw"), filepath.Join(src, "tok")
	os.WriteFile(pwSrc, []byte("s3cret\n"), 0o600)
	os.WriteFile(tokSrc, []byte(tgToken+"\n"), 0o600)
	args := []string{"setup", "-non-interactive", "-node-url", w.srv.URL + "/rpc", "-rpc-user", "tracker", "-rpc-password-file", pwSrc,
		"-token-file", tokSrc, "-chat-id", "42", "-references", w.srv.URL + "/api"}
	for _, install := range []bool{true, false} {
		h, installs := fakeSetup(t, newFakeTG(t), nil)
		h.euid = 0
		h.group = func() (int, error) { return os.Getgid(), nil }
		h.cookies = func(*config.Config) error { return refused }
		var out bytes.Buffer
		code, errs := runSetup(context.Background(), &out, append(args, "-install-service="+strconv.FormatBool(install))...)
		_, statErr := os.Stat(filepath.Join(h.etcDir, "config.yaml"))
		switch {
		case install && (code != exitUsage || !strings.Contains(errs, "-install-service: the service can't read bitcoind's cookie") || statErr == nil):
			t.Errorf("-install-service: code %d, config written %v\n%s", code, statErr == nil, errs)
		case !install && (code != exitOK || statErr != nil || !strings.Contains(out.String(), "Once the service can read the cookie (see above), install it: sudo node-tracker install-service -system\n")):
			t.Errorf("no -install-service: code %d, config %v\n%s\n%s", code, statErr, out.String(), errs)
		}
		if len(*installs) != 0 {
			t.Errorf("install-service ran: %v", *installs)
		}
	}
}

// readSecret reads like config.Load: bounded, regular files only, trimmed.
func TestReadSecret(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"ok": " s3cret\n", "empty": "\n", "big": strings.Repeat("x", maxSecret+1)}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if s, err := readSecret(filepath.Join(dir, "ok")); err != nil || s != "s3cret" {
		t.Errorf("ok: %q, %v", s, err)
	}
	for path, want := range map[string]string{
		filepath.Join(dir, "empty"): "is empty",
		filepath.Join(dir, "big"):   "larger than 4096 bytes",
		"/dev/zero":                 "/dev/zero: not a regular file",
	} {
		if _, err := readSecret(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v; want %q", path, err, want)
		}
	}
}
