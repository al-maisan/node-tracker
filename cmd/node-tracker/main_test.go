package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/state"
)

// No test looks for the developer's own bitcoind.
func init() {
	detectEnv = config.Env{Root: "/nonexistent-node-tracker-test", Getenv: func(string) string { return "" }}
}

// world fakes bitcoind (at / and /rpc), an Esplora API (at …/api) and the
// Telegram Bot API.
type world struct {
	node, tip atomic.Int64
	tgDown    atomic.Bool
	rpcFail   atomic.Value // string: "" answers, else "401", "403" or "-28"
	mu        sync.Mutex
	chain     string   // the node's
	sent      []string // telegram message texts
	srv       *httptest.Server
}

func newWorld(t *testing.T) *world {
	w := &world{chain: "main"}
	w.rpcFail.Store("")
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rpc := r.URL.Path == "/rpc" || r.URL.Path == "/"
		switch {
		case rpc && w.rpcFail.Load() == "-28":
			http.Error(rw, `{"result":null,"error":{"code":-28,"message":"Loading block index…"}}`, http.StatusInternalServerError)
		case rpc && w.rpcFail.Load() == "401":
			rw.WriteHeader(http.StatusUnauthorized)
		case rpc && w.rpcFail.Load() == "403":
			rw.WriteHeader(http.StatusForbidden)
		case rpc:
			w.mu.Lock()
			chain := w.chain
			w.mu.Unlock()
			fmt.Fprintf(rw, `{"result":{"chain":%q,"blocks":%d,"headers":%d},"error":null}`, chain, w.node.Load(), w.node.Load())
		case strings.HasSuffix(r.URL.Path, "/api/blocks/tip/height"):
			fmt.Fprint(rw, w.tip.Load())
		case r.URL.Path == "/botTOKEN/getMe":
			fmt.Fprint(rw, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"nt_test_bot"}}`)
		case r.URL.Path == "/botTOKEN/sendMessage" && w.tgDown.Load():
			http.Error(rw, "down", http.StatusBadGateway)
		case r.URL.Path == "/botTOKEN/sendMessage":
			var p struct{ Text string }
			json.NewDecoder(r.Body).Decode(&p)
			w.mu.Lock()
			w.sent = append(w.sent, p.Text)
			w.mu.Unlock()
			fmt.Fprint(rw, `{"ok":true}`)
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *world) messages() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.sent...)
}

func (w *world) config(t *testing.T) (cfg, st string) {
	dir := t.TempDir()
	t.Setenv("NT_TEST_TOKEN", "TOKEN")
	t.Setenv("NT_TEST_PW", "pw")
	cfg = filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, []byte(fmt.Sprintf(`
timeout: 2s
targets:
  - name: btc
    node: {kind: bitcoind, url: %[1]s/rpc, username: u, password_env: NT_TEST_PW}
    references: [{kind: esplora, url: %[1]s/api}]
    alert: {threshold: 3, recover_threshold: 1, for: 2}
    notify: [tg]
notifiers:
  - {name: tg, kind: telegram, api_url: %[1]s, bot_token_env: NT_TEST_TOKEN, chat_id: "42"}
`, w.srv.URL)), 0o600)
	return cfg, filepath.Join(dir, "state", "state.json")
}

func exec(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestOnceCrossingUpAndDown(t *testing.T) {
	w := newWorld(t)
	cfg, st := w.config(t)
	steps := []struct {
		node, tip int64
		code      int
		sent      int
	}{
		{100, 100, exitOK, 0},        // first OK: silent
		{100, 100, exitOK, 0},        // established
		{100, 105, exitNotInSync, 0}, // lagging, debouncing
		{100, 105, exitNotInSync, 1}, // LAGGING alert
		{104, 105, exitOK, 1},        // lag 1 <= recover: observed OK, debouncing
		{105, 105, exitOK, 2},        // back in sync alert
	}
	for i, s := range steps {
		w.node.Store(s.node)
		w.tip.Store(s.tip)
		code, _, stderr := exec(t, "once", "-config", cfg, "-state", st)
		if code != s.code || len(w.messages()) != s.sent {
			t.Fatalf("step %d: code=%d sent=%d, want code=%d sent=%d\n%s", i, code, len(w.messages()), s.code, s.sent, stderr)
		}
	}
	msgs := w.messages()
	if !strings.Contains(msgs[0], "<b>🔴 btc is lagging</b>") || !strings.Contains(msgs[0], "Lag: 5 blocks") {
		t.Errorf("lagging alert = %q", msgs[0])
	}
	if !strings.Contains(msgs[1], "<b>🟢 btc is in sync</b>") {
		t.Errorf("recovery alert = %q", msgs[1])
	}
}

func TestCheckReportAndTestNotify(t *testing.T) {
	w := newWorld(t)
	cfg, st := w.config(t)
	w.node.Store(938266)
	w.tip.Store(938266)

	code, out, _ := exec(t, "check", "-config", cfg)
	if code != exitOK || !strings.Contains(out, "btc     OK     938266") {
		t.Fatalf("check: code=%d out=%q", code, out)
	}
	for _, want := range []string{
		"\nconfig       ok\n",
		"\ntarget btc   node reachable (username/password) · references 1/1 · OK is the node's current state\n",
		"\nnotifier tg  token valid (@nt_test_bot) · chat not tested → run 'node-tracker test-notify tg'\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "PROXY") || strings.Contains(out, "TOKEN") {
		t.Errorf("proxy table without a proxy, or the token:\n%s", out)
	}
	if _, out, _ := exec(t, "check", "-config", cfg, "-log-level", "debug"); !regexp.MustCompile(`btc +bitcoind:127\.0\.0\.1:\d+ +none\n`).MatchString(out) {
		t.Errorf("no proxy table at debug level:\n%s", out)
	}
	if _, err := os.Stat(st); err == nil || len(w.messages()) != 0 {
		t.Fatal("check must not write state or send alerts")
	}

	if code, _, errs := exec(t, "report", "-config", cfg); code != exitOK {
		t.Fatalf("report: code=%d %s", code, errs)
	}
	if code, _, errs := exec(t, "test-notify", "-config", cfg, "tg"); code != exitOK {
		t.Fatalf("test-notify: code=%d %s", code, errs)
	}
	msgs := w.messages()
	if len(msgs) != 2 || !strings.Contains(msgs[0], "938,266 (0 behind)") || !strings.Contains(msgs[1], "test message") {
		t.Fatalf("messages = %q", msgs)
	}
	if code, _, _ := exec(t, "test-notify", "-config", cfg, "nope"); code != exitUsage {
		t.Fatalf("unknown notifier: code=%d", code)
	}
}

func TestCheckHints(t *testing.T) {
	w := newWorld(t)
	cfg, _ := w.config(t)
	w.node.Store(100)
	w.tip.Store(106)
	code, out, _ := exec(t, "check", "-config", cfg)
	if code != exitNotInSync || !strings.Contains(out, " · LAGGING by 6 is the node's current state, not a setup error\n") {
		t.Errorf("lagging: code=%d\n%s", code, out)
	}
	for fail, want := range map[string]string{
		"401": "rpc auth failed (HTTP 401) (wrong username/password, or a stale cookie)",
		"403": "rpc auth failed (HTTP 403) (not allowed: check rpcallowip, and rpcwhitelist for getblockchaininfo)",
		"-28": "rpc error -28: Loading block index… (bitcoind is starting; try again when it has loaded)",
	} {
		w.rpcFail.Store(fail)
		code, out, _ := exec(t, "check", "-config", cfg)
		if code != exitNotInSync || !strings.Contains(out, want+"\n") ||
			!strings.Contains(out, "node not reachable (see ERROR) · references 1/1\n") {
			t.Errorf("%s: code=%d, want %q in\n%s", fail, code, want, out)
		}
	}

	// Nothing listens on a closed port; a wrong token fails getMe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	b, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(b), "url: "+w.srv.URL+"/rpc", "url: http://"+closed+"/rpc", 1)), 0o600)
	t.Setenv("NT_TEST_TOKEN", "WRONG")
	code, out, _ = exec(t, "check", "-config", cfg)
	for _, want := range []string{
		"connect: connection refused (is bitcoind running with server=1? RPC ports: main 8332",
		"\nnotifier tg  bot token rejected (HTTP 404) · check bot_token_file / bot_token_env\n",
	} {
		if code != exitNotInSync || !strings.Contains(out, want) {
			t.Errorf("code=%d, want %q in\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "WRONG") {
		t.Errorf("output shows the token:\n%s", out)
	}

	// A .onion node whose proxy (Tor) is not running.
	const onion = "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion"
	b, _ = os.ReadFile(cfg)
	node := fmt.Sprintf("url: http://%s:8332/rpc, proxy: socks5://%s", onion, closed)
	os.WriteFile(cfg, []byte(strings.Replace(string(b), "url: http://"+closed+"/rpc", node, 1)), 0o600)
	code, out, _ = exec(t, "check", "-config", cfg)
	if code != exitNotInSync || !strings.Contains(out, "connection refused (is the proxy running? for .onion: is Tor running?)\n") {
		t.Errorf("proxy down: code=%d\n%s", code, out)
	}
}

// socks5 is a minimal SOCKS5 server (no auth, CONNECT only). It records the
// requested addresses and connects every request to backend.
type socks5 struct {
	addr   string
	mu     sync.Mutex
	dialed []string
}

func newSOCKS5(t *testing.T, backend string) *socks5 {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &socks5{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c, backend)
		}
	}()
	return s
}

func (s *socks5) serve(c net.Conn, backend string) {
	defer c.Close()
	r := bufio.NewReader(c)
	read := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil
		}
		return b
	}
	// Greeting: VER NMETHODS METHODS; reply "no auth". Request: VER CMD RSV ATYP ADDR PORT.
	h := read(2)
	if h == nil || h[0] != 5 || read(int(h[1])) == nil {
		return
	}
	c.Write([]byte{5, 0})
	req := read(4)
	if req == nil || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1, 4:
		host = net.IP(read(map[byte]int{1: 4, 4: 16}[req[3]])).String()
	case 3:
		n, _ := r.ReadByte()
		host = string(read(int(n)))
	}
	port := read(2)
	if host == "" || port == nil {
		return
	}
	s.mu.Lock()
	s.dialed = append(s.dialed, net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))))
	s.mu.Unlock()
	up, err := net.Dial("tcp", backend)
	if err != nil {
		c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	go io.Copy(up, r)
	io.Copy(c, up)
}

// A .onion node needs no allow_insecure and goes through its proxy only;
// the reference next to it is reached directly.
func TestOnionNodeThroughProxy(t *testing.T) {
	const onion = "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion"
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	socks := newSOCKS5(t, w.srv.Listener.Addr().String())
	cfg, _ := w.config(t)
	b, _ := os.ReadFile(cfg)
	node := fmt.Sprintf("url: http://%s:8332/rpc, proxy: socks5://%s", onion, socks.addr)
	os.WriteFile(cfg, []byte(strings.Replace(string(b), "url: "+w.srv.URL+"/rpc", node, 1)), 0o600)

	code, out, errs := exec(t, "check", "-config", cfg)
	if code != exitOK || !strings.Contains(out, "btc     OK     938266") {
		t.Fatalf("check: code=%d out=%q\n%s", code, out, errs)
	}
	for _, re := range []string{
		`btc +bitcoind:` + onion + `:8332 +socks5://` + socks.addr + `\n`,
		`btc +esplora:127\.0\.0\.1:\d+ +none\n`,
	} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("check output lacks %s:\n%s", re, out)
		}
	}
	socks.mu.Lock()
	defer socks.mu.Unlock()
	if len(socks.dialed) != 1 || socks.dialed[0] != onion+":8332" {
		t.Fatalf("proxy dialed %q, want only the node", socks.dialed)
	}
}

const envChild = "NT_TEST_ENV_PROXY_CHECK"

// A .onion node without proxy goes through the environment's proxy, as
// before per-endpoint proxies existed. net/http reads the environment once
// per process, so `check` runs in a child process.
func TestOnionNodeThroughEnvProxy(t *testing.T) {
	if cfg := os.Getenv(envChild); cfg != "" {
		code, out, errs := exec(t, "check", "-config", cfg)
		fmt.Printf("CODE %d\n%s\n%s", code, out, errs)
		os.Exit(0)
	}
	const onion = "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion"
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	socks := newSOCKS5(t, w.srv.Listener.Addr().String())
	cfg, _ := w.config(t)
	b, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(b), "url: "+w.srv.URL+"/rpc", "url: http://"+onion+":8332/rpc", 1)), 0o600)

	cmd := osexec.Command(os.Args[0], "-test.run=^TestOnionNodeThroughEnvProxy$")
	cmd.Env = append(os.Environ(), "http_proxy=", "NO_PROXY=", "no_proxy=", "REQUEST_METHOD=",
		"HTTP_PROXY=socks5://"+socks.addr, envChild+"="+cfg)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "CODE 0\n") || !strings.Contains(string(out), "btc     OK     938266") {
		t.Fatalf("check: %v\n%s", err, out)
	}
	if re := `btc +bitcoind:` + onion + `:8332 +socks5://` + socks.addr + ` \(environment\)\n`; !regexp.MustCompile(re).Match(out) {
		t.Errorf("check output lacks %s:\n%s", re, out)
	}
	socks.mu.Lock()
	defer socks.mu.Unlock()
	if len(socks.dialed) != 1 || socks.dialed[0] != onion+":8332" {
		t.Fatalf("proxy dialed %q, want only the node", socks.dialed)
	}
}

func TestLockedStateExits3(t *testing.T) {
	w := newWorld(t)
	cfg, st := w.config(t)
	s, err := state.Open(st)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if code, _, _ := exec(t, "once", "-config", cfg, "-state", st); code != exitLocked {
		t.Fatalf("code = %d, want %d", code, exitLocked)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"once", "-config", "/nonexistent.yaml"},
		{"check", "extra-arg"},
		{"once", "-log-format", "xml"},
	} {
		if code, _, _ := exec(t, args...); code != exitUsage {
			t.Errorf("%v: code = %d, want %d", args, code, exitUsage)
		}
	}
	if code, out, _ := exec(t, "version"); code != exitOK || !strings.HasPrefix(out, "node-tracker ") {
		t.Errorf("version: %d %q", code, out)
	}
}

func TestOnceFailsWhileAlertsAreUndelivered(t *testing.T) {
	w := newWorld(t)
	cfg, st := w.config(t)
	w.node.Store(100)
	w.tip.Store(110)
	w.tgDown.Store(true)
	exec(t, "once", "-config", cfg, "-state", st) // debouncing
	if code, _, _ := exec(t, "once", "-config", cfg, "-state", st); code != exitFailure {
		t.Fatalf("code = %d, want %d while Telegram is down", code, exitFailure)
	}
	w.tgDown.Store(false)
	if code, _, _ := exec(t, "once", "-config", cfg, "-state", st); code != exitNotInSync || len(w.messages()) != 1 {
		t.Fatalf("code = %d, sent = %d after recovery", code, len(w.messages()))
	}
}

func TestInstallServiceDryRun(t *testing.T) {
	w := newWorld(t)
	cfg, _ := w.config(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	code, out, errs := exec(t, "install-service", "-user", "-dry-run", "-config", cfg)
	if code != exitOK {
		t.Fatalf("code = %d\n%s", code, errs)
	}
	for _, s := range []string{
		"would write " + filepath.Join(xdg, "systemd", "user", "node-tracker.service") + " (0644):\n",
		" run -config " + cfg + "\n",
		"password_env: the service does not get this shell's environment",
		"would run: systemctl --user enable --now node-tracker.service\n",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("no %q in\n%s", s, out)
		}
	}
	if _, err := os.Stat(filepath.Join(xdg, "systemd")); err == nil {
		t.Error("dry run wrote files")
	}
	for _, args := range [][]string{
		{"install-service"},
		{"install-service", "-user", "-system"},
		{"install-service", "-user", "extra"},
		{"install-service", "-user", "-dry-run", "-config", "/nonexistent.yaml"},
	} {
		if code, _, _ := exec(t, args...); code != exitUsage {
			t.Errorf("%v: code = %d, want %d", args, code, exitUsage)
		}
	}
}

// Without -config and XDG_CONFIG_HOME, the config is ~/.config/node-tracker/
// config.yaml on every OS (not ~/Library/Application Support on macOS), and
// the user unit goes to ~/.config/systemd/user.
func TestDefaultConfigPath(t *testing.T) {
	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	cfg, _ := w.config(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	dst := filepath.Join(home, ".config", "node-tracker", "config.yaml")
	os.MkdirAll(filepath.Dir(dst), 0o700)
	if err := os.Rename(cfg, dst); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := exec(t, "check"); code != exitOK || !strings.Contains(out, "btc     OK     938266") {
		t.Fatalf("check: code=%d out=%q\n%s", code, out, errs)
	}
	code, out, errs := exec(t, "install-service", "-user", "-dry-run")
	if want := "would write " + filepath.Join(home, ".config", "systemd", "user", "node-tracker.service") + " "; code != exitOK || !strings.Contains(out, want) {
		t.Fatalf("install-service: code=%d, no %q in\n%s%s", code, want, out, errs)
	}
}

// A missing config says where it was looked for and how to create one; a
// missing cookie is a warning, not an error.
func TestNoConfigAndMissingCookie(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	want := "node-tracker: no config at " + filepath.Join(home, ".config", "node-tracker", "config.yaml") + "; run 'node-tracker setup'"
	if code, _, errs := exec(t, "check"); code != exitUsage || !strings.HasPrefix(errs, want) {
		t.Fatalf("code=%d stderr=%q, want %q…", code, errs, want)
	}

	w := newWorld(t)
	w.node.Store(938266)
	w.tip.Store(938266)
	cfg, _ := w.config(t)
	b, _ := os.ReadFile(cfg)
	cookie := filepath.Join(home, ".bitcoin", ".cookie")
	os.WriteFile(cfg, []byte(strings.Replace(string(b), "username: u, password_env: NT_TEST_PW", "cookie_file: "+cookie, 1)), 0o600)
	code, out, errs := exec(t, "check", "-config", cfg)
	if code != exitNotInSync || !strings.Contains(out, "NODE_DOWN") || !strings.Contains(errs, "level=WARN msg=config warning=\"targets[0].node.cookie_file: "+cookie+" does not exist; bitcoind is not started yet") {
		t.Fatalf("check: code=%d out=%q\n%s", code, out, errs)
	}
}

// A target without node, alert and notify watches the detected local
// bitcoind with the balanced preset and every notifier; check logs what was
// detected and warns about references of another chain.
func TestDetectedNode(t *testing.T) {
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
	t.Setenv("NT_TEST_TOKEN", "TOKEN")
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(cfg, []byte(fmt.Sprintf(`
targets:
  - name: btc
    references: [{kind: esplora, url: %[1]s/signet/api}]
notifiers:
  - {name: tg, kind: telegram, api_url: %[1]s, bot_token_env: NT_TEST_TOKEN, chat_id: "42"}
`, w.srv.URL)), 0o600)

	detected := `level=INFO msg="detected local bitcoind" conf=` + conf + ` chain=main url=http://` + w.srv.Listener.Addr().String() + ` cookie_file=` + cookie + "\n"
	mismatch := `msg="reference is on another chain than the node, so it is ignored; set references for the node's chain (see docs/configuration.md)" target=btc node_chain=main reference=esplora:` + w.srv.Listener.Addr().String() + ` reference_chain=signet` + "\n"
	code, out, errs := exec(t, "check", "-config", cfg)
	if code != exitNotInSync || !strings.Contains(out, "btc     NO_REFERENCE  938266") || !strings.Contains(out, "is on chain signet, the node on main") ||
		!strings.Contains(out, "references 0/1") || !strings.Contains(errs, detected) || !strings.Contains(errs, mismatch) {
		t.Fatalf("check: code=%d out=%q\n%s", code, out, errs)
	}
	w.mu.Lock()
	w.chain = "signet"
	w.mu.Unlock()
	if code, _, errs := exec(t, "check", "-config", cfg); code != exitOK || strings.Contains(errs, "another chain") {
		t.Fatalf("check on signet: code=%d\n%s", code, errs)
	}
	// Debug, not info, for what runs from cron.
	if code, _, errs := exec(t, "test-notify", "-config", cfg); code != exitOK || len(w.messages()) != 1 || strings.Contains(errs, "detected local bitcoind") {
		t.Fatalf("test-notify: code=%d sent=%d\n%s", code, len(w.messages()), errs)
	}
	st := filepath.Join(t.TempDir(), "state.json")
	if code, _, errs := exec(t, "once", "-config", cfg, "-state", st); code != exitOK || strings.Contains(errs, "detected local bitcoind") {
		t.Fatalf("once: code=%d\n%s", code, errs)
	}
	if code, _, errs := exec(t, "once", "-config", cfg, "-state", st, "-log-level", "debug"); code != exitOK || !strings.Contains(errs, `level=DEBUG msg="detected local bitcoind"`) {
		t.Fatalf("once -log-level debug: code=%d\n%s", code, errs)
	} // Without references, the public ones are used, and said so.
	os.WriteFile(cfg, []byte(fmt.Sprintf(`
targets:
  - name: btc
notifiers:
  - {name: tg, kind: telegram, api_url: %s, bot_token_env: NT_TEST_TOKEN, chat_id: "42"}
`, w.srv.URL)), 0o600)
	public := `level=DEBUG msg="using public references; set references to choose others, or [] for none" target=btc references=https://mempool.space/api,https://blockstream.info/api` + "\n"
	if code, _, errs := exec(t, "test-notify", "-config", cfg, "-log-level", "debug"); code != exitOK || !strings.Contains(errs, public) {
		t.Fatalf("test-notify, default references: code=%d\n%s", code, errs)
	}
}
