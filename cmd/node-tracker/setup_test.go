package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
		hostname:    func() (string, error) { return "orendil.example.org", nil },
		telegramAPI: tg.srv.URL,
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
		turn{prompt: "Bot token (input hidden): ", answer: "123:WRONG", secret: true},
		turn{prompt: "Bot token (input hidden): ", answer: tgToken, secret: true},
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
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "node-tracker")
	path, tok := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "telegram-token")
	for _, s := range []string{
		"Found bitcoind at http://127.0.0.1:",
		": chain main, height 938,266, cookie " + cookie + "\n",
		"telegram rejected the token; copy it again from @BotFather\n",
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
	if tg := cfg.Targets[0]; tg.Name != "orendil" || tg.Node.CookieFile != cookie || len(tg.References) != 1 || *tg.Alert.Threshold != 3 || *tg.Alert.RecoverThreshold != 1 || tg.Alert.For != 2 {
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
		turn{prompt: "RPC user [nodetracker]: "},
		turn{prompt: "Generate a new password and the rpcauth line for the node's bitcoin.conf? [Y/n] "},
		turn{prompt: "Press Enter when bitcoind has restarted: "},
		turn{prompt: "Name of this node in alerts [node]: ", answer: "home"},
		turn{prompt: "[https://mempool.space/api https://blockstream.info/api]: ", answer: "none"},
		turn{prompt: "Preset [2]: ", answer: "quiet"},
		turn{prompt: "Bot token (input hidden): ", answer: tgToken, secret: true},
		turn{prompt: "Chat id or @channel: ", answer: "666"},
		turn{prompt: "Chat id or @channel: ", answer: "@mychannel"},
		turn{prompt: "Replace them, keeping each as .bak? [y/N] ", answer: "y"},
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
		"failed: telegram: HTTP 400: Bad Request: chat not found (wrong chat id, or the bot was never started in that chat)\n",
		"backed up " + path + " to " + path + ".bak\n",
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
		len(cfg.Targets[0].References) != 0 || *cfg.Targets[0].Alert.Threshold != 6 || cfg.Interval != time.Minute || cfg.Notifiers[0].ChatID != "@mychannel" {
		t.Errorf("config %+v", cfg)
	}
	if got := tg.messages(); len(got) != 1 || !strings.HasPrefix(got[0], "@mychannel: ") {
		t.Errorf("sent %q", got)
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
		"-token-file", tokSrc, "-chat-id", "42", "-references", w.srv.URL + "/api", "-preset", "fast", "-name", "btc", "-install-service"}

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
	if err != nil || *cfg.Targets[0].Alert.Threshold != 2 || *cfg.Targets[0].Alert.RecoverThreshold != 0 || cfg.Targets[0].Node.Username != "tracker" {
		t.Fatalf("config %+v, %v", cfg, err)
	}

	before := read(t, path)
	args[len(args)-1] = "-force=false"
	args[slicesIndex(args, "42")] = "43"
	if code, errs := runSetup(context.Background(), io.Discard, args...); code != exitUsage || !strings.Contains(errs, "exist and differ; rerun with -force") || read(t, path) != before {
		t.Errorf("changed config without -force: code=%d\n%s", code, errs)
	}
	if code, errs := runSetup(context.Background(), io.Discard, append(args, "-force")...); code != exitOK || read(t, path+".bak") != before || !strings.Contains(read(t, path), `chat_id: "43"`) {
		t.Errorf("-force: code=%d\n%s", code, errs)
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
	} {
		if code, errs := runSetup(context.Background(), io.Discard, tc.args...); code != exitUsage || !strings.Contains(errs, tc.want) {
			t.Errorf("%v: code=%d, want %q in\n%s", tc.args, code, tc.want, errs)
		}
	}
	if !strings.Contains(read(t, path), `chat_id: "43"`) {
		t.Error("a failed run changed the config")
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

// A chat id typed while waiting for the /start stops the wait.
func TestAskChatTyped(t *testing.T) {
	tg := newFakeTG(t)
	tg.updates = func(ctx context.Context, _ int64) (int, string) {
		<-ctx.Done() // a long poll that sees nothing
		return 200, `{"ok":true,"result":[]}`
	}
	ft := newFakeTerm(t, turn{prompt: "press Enter)\n", answer: "x"}, turn{prompt: "Chat id or @channel: ", answer: "-100123"})
	h, _ := fakeSetup(t, tg, ft)
	w := &wizard{setupHost: *h, out: ft, hc: httpx.NewClient(), done: make(chan struct{})}
	defer close(w.done)
	id, err := w.askChat(context.Background(), tgToken, "nt_test_bot")
	ft.done()
	if id != "-100123" || err != nil || !strings.Contains(ft.transcript(), "The chat id must be a numeric chat id or @channelname\n") {
		t.Fatalf("askChat = %q, %v\n%s", id, err, ft.transcript())
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

func TestPrintable(t *testing.T) {
	if got := printable("a\x1b[2Jb\nc\u202ed é"); got != "a?[2Jb?c?d é" {
		t.Errorf("printable = %q", got)
	}
}

// -proxy is written for a .onion node, so the service uses it too.
func TestSetupNonInteractiveProxy(t *testing.T) {
	const onion = "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion"
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	socks := newSOCKS5(t, w.srv.Listener.Addr().String())
	fakeSetup(t, newFakeTG(t), nil)
	src := t.TempDir()
	pwSrc, tokSrc := filepath.Join(src, "pw"), filepath.Join(src, "tok")
	os.WriteFile(pwSrc, []byte("s3cret\n"), 0o600)
	os.WriteFile(tokSrc, []byte(tgToken+"\n"), 0o600)
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer
	code, errs := runSetup(context.Background(), &out, "setup", "-non-interactive", "-config", path, "-node-url", "http://"+onion+":8332",
		"-proxy", "socks5://"+socks.addr, "-rpc-user", "tracker", "-rpc-password-file", pwSrc, "-token-file", tokSrc, "-chat-id", "42", "-references", "none")
	if code != exitOK || !strings.Contains(out.String(), "node http://"+onion+":8332: chain main") {
		t.Fatalf("code = %d\n%s\n%s", code, out.String(), errs)
	}
	cfg, _, err := config.LoadEnv(path, detectEnv)
	if err != nil || cfg.Targets[0].Node.Proxy != "socks5://"+socks.addr {
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
