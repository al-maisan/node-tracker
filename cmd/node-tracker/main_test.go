package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/al-maisan/node-tracker/internal/state"
)

// world fakes bitcoind, an Esplora API and the Telegram Bot API.
type world struct {
	node, tip atomic.Int64
	tgDown    atomic.Bool
	mu        sync.Mutex
	sent      []string // telegram message texts
	srv       *httptest.Server
}

func newWorld(t *testing.T) *world {
	w := &world{}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rpc":
			fmt.Fprintf(rw, `{"result":{"blocks":%d,"headers":%d},"error":null}`, w.node.Load(), w.node.Load())
		case r.URL.Path == "/api/blocks/tip/height":
			fmt.Fprint(rw, w.tip.Load())
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
