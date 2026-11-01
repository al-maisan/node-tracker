package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secret = "123456:TOP-SECRET"

// write puts the config and a token file in a temp dir.
func write(t *testing.T, cfg string) string {
	t.Helper()
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(cfg, "TOKENFILE", tok)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const valid = `
targets:
  - name: orendil
    node:
      kind: bitcoind
      url: http://127.0.0.1:8332
      cookie_file: /var/lib/bitcoind/.cookie
    references:
      - kind: esplora
        url: https://mempool.space/api
    alert:
      threshold: 3
    notify: [ops]
notifiers:
  - name: ops
    kind: telegram
    bot_token_file: TOKENFILE
    chat_id: "-1001234567890"
`

func TestValidDefaults(t *testing.T) {
	c, warns, err := Load(write(t, valid))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	if c.Interval != 30*time.Second || c.Timeout != 10*time.Second {
		t.Errorf("durations: %s %s", c.Interval, c.Timeout)
	}
	a := c.Targets[0].Alert
	if *a.RecoverThreshold != 3 || a.For != 2 || *c.Targets[0].ReferenceOutlier != 6 {
		t.Errorf("alert defaults: %+v", a)
	}
	if c.Notifiers[0].APIURL != DefaultTelegramAPI {
		t.Errorf("api_url default = %q", c.Notifiers[0].APIURL)
	}
	if c.Notifiers[0].BotToken != secret {
		t.Error("token not resolved")
	}
	if n := c.Targets[0].References[0].Name; n != "esplora:mempool.space" {
		t.Errorf("name = %q", n)
	}
	if s := fmt.Sprintf("%v %+v %#v", c.Notifiers[0].BotToken, c.Notifiers[0], c); strings.Contains(s, "SECRET") {
		t.Errorf("secret leaks through formatting: %s", s)
	}
}

func TestInvalid(t *testing.T) {
	tests := []struct {
		name, from, to, wantErr string
	}{
		{"unknown key", "    notify: [ops]", "    notify: [ops]\n    notfy: [x]", "field notfy not found"},
		{"bad interval", "targets:", "interval: 10ms\ntargets:", "interval: must be at least 1s"},
		{"timeout > interval", "targets:", "interval: 5s\ntimeout: 6s\ntargets:", "timeout:"},
		{"bad target name", "name: orendil", "name: 'a b'", "targets[0].name"},
		{"bad node kind", "kind: bitcoind", "kind: geth", "targets[0].node.kind"},
		{"credentials in url", "http://127.0.0.1:8332", "http://u:p@127.0.0.1:8332", "must not contain credentials"},
		{"plain http to remote node", "http://127.0.0.1:8332", "http://10.0.0.5:8332", "only allowed for loopback"},
		{"plain http reference", "https://mempool.space/api", "http://mempool.space/api", "only allowed for loopback"},
		{"no auth", "      cookie_file: /var/lib/bitcoind/.cookie\n", "", "auth required"},
		{"user without password", "cookie_file: /var/lib/bitcoind/.cookie", "username: u", "password: set password_file or password_env"},
		{"missing threshold", "      threshold: 3", "      for: 2", "threshold: required"},
		{"recover above threshold", "threshold: 3", "threshold: 3\n      recover_threshold: 4", "must not exceed"},
		{"unknown notifier", "notify: [ops]", "notify: [nope]", `unknown notifier "nope"`},
		{"bad chat id", `"-1001234567890"`, `"x y"`, "chat_id"},
		{"token file and env", "bot_token_file: TOKENFILE", "bot_token_file: TOKENFILE\n    bot_token_env: X", "not both"},
		{"missing token env", "bot_token_file: TOKENFILE", "bot_token_env: NODE_TRACKER_TEST_UNSET", "not set"},
		{"plain http api_url", "chat_id: \"-1001234567890\"", "chat_id: \"-1001234567890\"\n    api_url: http://example.com", "api_url: plain http"},
		{"relative cookie path", "cookie_file: /var/lib/bitcoind/.cookie", "cookie_file: bitcoind/.cookie", "cookie_file: must be an absolute path"},
		{"relative state file", "targets:", "state_file: state.json\ntargets:", "state_file: must be an absolute path"},
		{"duplicate reference", "https://mempool.space/api\n", "https://mempool.space/api\n      - kind: esplora\n        url: https://mempool.space/api\n", "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := strings.Replace(valid, tt.from, tt.to, 1)
			if cfg == valid {
				t.Fatalf("replacement %q did not apply", tt.from)
			}
			_, _, err := Load(write(t, cfg))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error leaks secret: %v", err)
			}
		})
	}
}

func TestAllErrorsReported(t *testing.T) {
	cfg := strings.NewReplacer("threshold: 3", "for: 2", `"-1001234567890"`, `"x y"`).Replace(valid)
	_, _, err := Load(write(t, cfg))
	if err == nil || !strings.Contains(err.Error(), "threshold") || !strings.Contains(err.Error(), "chat_id") {
		t.Fatalf("err = %v", err)
	}
}

func TestWarnings(t *testing.T) {
	path := write(t, strings.Replace(valid, "    references:\n      - kind: esplora\n        url: https://mempool.space/api\n", "", 1))
	os.Chmod(filepath.Join(filepath.Dir(path), "token"), 0o644)
	_, warns, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 2 || !strings.Contains(warns[0]+warns[1], "no references") || !strings.Contains(warns[0]+warns[1], "chmod o-rwx") {
		t.Fatalf("warns = %q", warns)
	}
}

func TestEnvSecretAndPassword(t *testing.T) {
	t.Setenv("NT_TOKEN", secret)
	t.Setenv("NT_PW", "pw")
	cfg := strings.NewReplacer(
		"bot_token_file: TOKENFILE", "bot_token_env: NT_TOKEN",
		"cookie_file: /var/lib/bitcoind/.cookie", "username: u\n      password_env: NT_PW",
	).Replace(valid)
	c, _, err := Load(write(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if c.Notifiers[0].BotToken != secret || c.Targets[0].Node.Password != "pw" {
		t.Fatal("secrets not resolved from env")
	}
}

func TestEmptyAndMissing(t *testing.T) {
	if _, _, err := Load(write(t, "")); err == nil {
		t.Error("empty: want error")
	}
	if _, _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("missing: want error")
	}
}

func TestDefaultStatePath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if p, _ := DefaultStatePath(); p != "/xdg/state/node-tracker/state.json" {
		t.Errorf("p = %s", p)
	}
	t.Setenv("XDG_STATE_HOME", "relative") // spec: ignore non-absolute
	t.Setenv("HOME", "/home/u")
	if p, _ := DefaultStatePath(); p != "/home/u/.local/state/node-tracker/state.json" {
		t.Errorf("p = %s", p)
	}
}

// The shipped example must stay valid.
func TestExampleConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tok := filepath.Join(home, ".config", "node-tracker", "telegram-token")
	os.MkdirAll(filepath.Dir(tok), 0o700)
	os.WriteFile(tok, []byte(secret), 0o600)
	c, warns, err := Load("../../examples/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 || c.Notifiers[0].BotToken != secret {
		t.Fatalf("warns=%v token resolved=%v", warns, c.Notifiers[0].BotToken == secret)
	}
}
