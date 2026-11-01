package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secret = "123456:TOP-SECRET"

// proxyEnv are the variables http.ProxyFromEnvironment reads.
var proxyEnv = []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"}

// TestMain clears the proxy environment, so the developer's one can't change
// results. A child of TestOnionEnvProxy keeps the one it was given.
func TestMain(m *testing.M) {
	if os.Getenv(envChild) == "" {
		for _, k := range proxyEnv {
			os.Unsetenv(k)
		}
	}
	os.Exit(m.Run())
}

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
		{"plain http api_url", "chat_id: \"-1001234567890\"", "chat_id: \"-1001234567890\"\n    api_url: http://example.com", `api_url "http://example.com": plain http`},
		{"relative cookie path", "cookie_file: /var/lib/bitcoind/.cookie", "cookie_file: bitcoind/.cookie", `cookie_file "bitcoind/.cookie": must be an absolute path`},
		{"relative state file", "targets:", "state_file: state.json\ntargets:", `state_file "state.json": must be an absolute path`},
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

// Errors show the rejected value and a hint for common mistakes.
func TestErrorShowsValue(t *testing.T) {
	tests := []struct{ name, from, to, want string }{
		{"env var in url", "http://127.0.0.1:8332", "http://$ONION:8332",
			`targets[0].node.url "http://$ONION:8332": invalid character "$" in host name; YAML does not expand environment variables`},
		{"braced env var in url", "http://127.0.0.1:8332", "http://${ONION}:8332",
			`targets[0].node.url "http://${ONION}:8332": invalid character "{" in host name; YAML does not expand environment variables`},
		{"placeholder in url", "http://127.0.0.1:8332", "http://<your-node>:8332",
			`targets[0].node.url "http://<your-node>:8332": invalid character "<" in host name; this looks like a placeholder`},
		{"missing scheme", "http://127.0.0.1:8332", "127.0.0.1:8332",
			`targets[0].node.url "127.0.0.1:8332": missing scheme; add http:// or https://`},
		{"missing scheme with host", "https://mempool.space/api", "mempool.space/api",
			`targets[0].references[0].url "mempool.space/api": missing scheme; add http:// or https://`},
		{"empty url", "url: http://127.0.0.1:8332", "url: ''", `targets[0].node.url: required`},
		{"other scheme", "https://mempool.space/api", "ftp://mempool.space/api",
			`targets[0].references[0].url "ftp://mempool.space/api": must be an absolute http(s) URL`},
		{"bad port", "http://127.0.0.1:8332", "http://127.0.0.1:rpc",
			`targets[0].node.url "http://127.0.0.1:rpc": invalid port ":rpc" after host`},
		{"plain http", "http://127.0.0.1:8332", "http://10.0.0.5:8332",
			`targets[0].node.url "http://10.0.0.5:8332": plain http is only allowed for loopback and .onion hosts`},
		{"kind", "kind: bitcoind", "kind: geth", `targets[0].node.kind "geth": must be one of [bitcoind]`},
		{"chat id placeholder", `"-1001234567890"`, `"<chat id>"`,
			`notifiers[0].chat_id "<chat id>": must be a numeric chat id or @channelname; this looks like a placeholder`},
		{"env var in path", "cookie_file: /var/lib/bitcoind/.cookie", "cookie_file: $HOME/.bitcoin/.cookie",
			`targets[0].node.cookie_file "$HOME/.bitcoin/.cookie": must be an absolute path or start with ~/; YAML does not expand environment variables`},
		{"target name", "name: orendil", "name: 'a b'", `targets[0].name "a b": must match`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := strings.Replace(valid, tt.from, tt.to, 1)
			if cfg == valid {
				t.Fatalf("replacement %q did not apply", tt.from)
			}
			_, _, err := Load(write(t, cfg))
			if err == nil || !strings.Contains(err.Error(), "\n"+tt.want) {
				t.Fatalf("err = %v\nwant line %s", err, tt.want)
			}
		})
	}
}

// Notifiers have no allow_insecure, so the error must not suggest it.
func TestNotifierPlainHTTP(t *testing.T) {
	cfg := strings.Replace(valid, `chat_id: "-1001234567890"`, `chat_id: "-1001234567890"`+"\n    api_url: http://example.com", 1)
	_, _, err := Load(write(t, cfg))
	if want := "\n" + `notifiers[0].api_url "http://example.com": plain http is only allowed for loopback hosts; use https`; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("err = %v, want it to end with %s", err, want)
	}
}

// A URL password never shows up in an error, whether or not the URL parses.
func TestURLPasswordNeverShown(t *testing.T) {
	const pw = "s3cr3tPW"
	for _, tt := range []struct{ url, want string }{
		{"http://u:" + pw + "@127.0.0.1:8332", `"http://REDACTED@127.0.0.1:8332": must not contain credentials`},
		{"https://u:" + pw + "@mempool.space/api", `"https://REDACTED@mempool.space/api": must not contain credentials`},
		{"http://u:" + pw + "%zz@127.0.0.1:8332", `"http://REDACTED@127.0.0.1:8332": invalid URL; credentials`},
		{"http://u:" + pw + "/x@127.0.0.1:8332", `"http://REDACTED@127.0.0.1:8332": invalid URL; credentials`},
		{"http://u:p@" + pw + "@127.0.0.1:8332", `"http://REDACTED@127.0.0.1:8332": must not contain credentials`},
		{"http://u:" + pw + "@$HOST:8332", `"http://REDACTED@$HOST:8332": must not contain credentials`},
		{"u:" + pw + "@127.0.0.1:8332", `"REDACTED@127.0.0.1:8332": missing scheme`},
		{"http://u:" + pw + "@ftp://x@h", `"http://REDACTED@h": must not contain credentials`},
	} {
		t.Run(tt.want, func(t *testing.T) { // not tt.url: the name is part of the temp dir in the error
			_, _, err := Load(write(t, strings.Replace(valid, "http://127.0.0.1:8332", tt.url, 1)))
			if err == nil || !strings.Contains(err.Error(), "targets[0].node.url "+tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
			if strings.Contains(err.Error(), pw) {
				t.Fatalf("error leaks the password: %v", err)
			}
		})
	}
}

// Tor Project's v3 onion address; its checksum is valid.
const onion = "2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion"

func TestProxy(t *testing.T) {
	const node = "      url: http://127.0.0.1:8332\n"
	tests := []struct{ name, proxy, want string }{
		{"socks5", "socks5://127.0.0.1:9050", ""},
		{"socks5h", "socks5h://localhost:9050/", ""},
		{"http", "http://10.0.0.1:3128", ""},
		{"https", "https://proxy.example.com", ""},
		{"bad scheme", "ftp://127.0.0.1:21", `proxy "ftp://127.0.0.1:21": scheme must be one of [socks5 socks5h http https]`},
		{"no scheme", "127.0.0.1:9050", `proxy "127.0.0.1:9050": missing scheme; e.g. socks5://127.0.0.1:9050`},
		{"credentials", "socks5://u:s3cr3tPW@127.0.0.1:9050", `proxy "socks5://REDACTED@127.0.0.1:9050": must not contain credentials`},
		{"path", "http://10.0.0.1:3128/x", `proxy "http://10.0.0.1:3128/x": must be scheme://host:port`},
		{"no host", "socks5://", `proxy "socks5://": must be scheme://host:port`},
		{"env var", "socks5://$TOR:9050", `proxy "socks5://$TOR:9050": invalid character "$" in host name; YAML`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, err := Load(write(t, strings.Replace(valid, node, node+"      proxy: "+tt.proxy+"\n", 1)))
			switch {
			case tt.want == "" && err != nil:
				t.Fatal(err)
			case tt.want == "" && (c.Targets[0].Node.Proxy != tt.proxy || c.Targets[0].Node.Timeout != 0):
				t.Fatalf("node = %+v", c.Targets[0].Node)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), "targets[0].node."+tt.want)):
				t.Fatalf("err = %v, want %s", err, tt.want)
			case err != nil && strings.Contains(err.Error(), "s3cr3tPW"):
				t.Fatalf("error leaks the password: %v", err)
			}
		})
	}
}

func TestOnion(t *testing.T) {
	const lan = `plain http to .onion needs a loopback proxy; `
	tests := []struct {
		name, url, proxy string
		insecure         bool
		wantProxy        string
		wantErr          string
	}{
		{"defaults", "http://" + onion + ":8332", "", false, DefaultTorProxy, ""},
		{"upper case", "http://" + strings.ToUpper(onion) + ":8332", "", false, DefaultTorProxy, ""},
		{"subdomain", "https://www." + onion, "", false, DefaultTorProxy, ""},
		{"own local proxy", "http://" + onion + ":8332", "socks5://127.0.0.1:9150", false, "socks5://127.0.0.1:9150", ""},
		{"localhost proxy", "http://" + onion + ":8332", "socks5h://localhost:9050", false, "socks5h://localhost:9050", ""},
		{"ipv6 loopback proxy", "http://" + onion + ":8332", "socks5://[::1]:9050", false, "socks5://[::1]:9050", ""},
		{"LAN proxy", "http://" + onion + ":8332", "socks5h://192.168.1.10:9050", false, "", lan + `"socks5h://192.168.1.10:9050" would see the RPC credentials`},
		{"http proxy", "http://" + onion + ":8332", "http://proxy.corp:3128", false, "", lan + `"http://proxy.corp:3128" would see`},
		{"LAN proxy, allow_insecure", "http://" + onion + ":8332", "socks5h://192.168.1.10:9050", true, "socks5h://192.168.1.10:9050", ""},
		{"LAN proxy, https", "https://" + onion, "socks5h://192.168.1.10:9050", false, "socks5h://192.168.1.10:9050", ""},
		{"v2", "http://expyuzz4wqqyqhjn.onion", "", false, "", "not a v3 onion address"},
		{"typo", "http://3gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion", "", false, "", "checksum mismatch"},
		{"not base32", "http://1gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion", "", false, "", "not a v3 onion address"},
		{"bad proxy", "http://" + onion, "tor://x", false, "", "scheme must be one of"},
	}
	t.Run("notifier api_url", func(t *testing.T) { // the .onion exemption is for endpoints only
		cfg := strings.Replace(valid, `chat_id: "-1001234567890"`, `chat_id: "-1001234567890"`+"\n    api_url: http://"+onion, 1)
		want := "\n" + `notifiers[0].api_url "http://` + onion + `": plain http is only allowed for loopback hosts; use https`
		if _, _, err := Load(write(t, cfg)); err == nil || !strings.HasSuffix(err.Error(), want) {
			t.Fatalf("err = %v, want %s", err, want)
		}
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := "url: " + tt.url
			if tt.proxy != "" {
				e += "\n      proxy: " + tt.proxy
			}
			if tt.insecure {
				e += "\n      allow_insecure: true"
			}
			c, _, err := Load(write(t, strings.Replace(valid, "url: http://127.0.0.1:8332", e, 1)))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if n := c.Targets[0].Node; n.Proxy != tt.wantProxy || n.DefaultProxy != (tt.proxy == "") || n.Timeout != 30*time.Second {
				t.Fatalf("proxy = %q default = %v timeout = %s, want %q 30s", n.Proxy, n.DefaultProxy, n.Timeout, tt.wantProxy)
			}
			if r := c.Targets[0].References[0]; r.Proxy != "" || r.Timeout != 0 {
				t.Fatalf("reference got onion defaults: %+v", r)
			}
		})
	}
}

const envChild = "NT_TEST_ENV_PROXY_CONFIG"

// A .onion endpoint without proxy uses the environment's proxy, as net/http
// would, else DefaultTorProxy. net/http reads the environment once per
// process, so each case runs Load in a child process.
func TestOnionEnvProxy(t *testing.T) {
	if path := os.Getenv(envChild); path != "" {
		c, _, err := Load(path)
		if err != nil {
			fmt.Printf("RESULT error: %v\n", err)
		} else {
			fmt.Printf("RESULT proxy=%q default=%v\n", c.Targets[0].Node.Proxy, c.Targets[0].Node.DefaultProxy)
		}
		os.Exit(0)
	}
	url := "url: http://" + onion + ":8332"
	tests := []struct {
		name, env, extra, want string
	}{
		{"no env", "", "", `proxy="socks5://127.0.0.1:9050" default=true`},
		{"local Tor Browser", "HTTP_PROXY=socks5://127.0.0.1:9150", "", `proxy="" default=false`},
		{"lower case", "http_proxy=socks5://localhost:9050", "", `proxy="" default=false`},
		{"LAN", "HTTP_PROXY=socks5://192.168.1.10:9050", "",
			`targets[0].node.url "http://` + onion + `:8332": plain http to .onion needs a loopback proxy; "socks5://192.168.1.10:9050" (environment) would see`},
		{"LAN with credentials", "HTTP_PROXY=http://u:s3cr3tPW@10.0.0.1:3128", "", `"http://REDACTED@10.0.0.1:3128" (environment) would see`},
		{"LAN, allow_insecure", "HTTP_PROXY=socks5://192.168.1.10:9050", "\n      allow_insecure: true", `proxy="" default=false`},
		{"NO_PROXY", "HTTP_PROXY=socks5://192.168.1.10:9050 NO_PROXY=.onion", "", `proxy="socks5://127.0.0.1:9050" default=true`},
		{"HTTPS_PROXY ignored for http", "HTTPS_PROXY=socks5://192.168.1.10:9050", "", `proxy="socks5://127.0.0.1:9050" default=true`},
		{"explicit proxy wins", "HTTP_PROXY=socks5://192.168.1.10:9050", "\n      proxy: socks5://127.0.0.1:9150", `proxy="socks5://127.0.0.1:9150" default=false`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := write(t, strings.Replace(valid, "url: http://127.0.0.1:8332", url+tt.extra, 1))
			cmd := exec.Command(os.Args[0], "-test.run=^TestOnionEnvProxy$")
			cmd.Env = os.Environ()
			for _, k := range proxyEnv {
				cmd.Env = append(cmd.Env, k+"=")
			}
			cmd.Env = append(append(cmd.Env, envChild+"="+path), strings.Fields(tt.env)...)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "RESULT ") || !strings.Contains(string(out), tt.want) {
				t.Fatalf("got %s(%v), want %s", out, err, tt.want)
			}
			if strings.Contains(string(out), "s3cr3tPW") {
				t.Fatalf("leaks the proxy password: %s", out)
			}
		})
	}
}

// .onion: per-call timeout is max(timeout, 30s), at most interval.
func TestOnionTimeout(t *testing.T) {
	for _, tt := range []struct {
		top  string
		want time.Duration
	}{
		{"", 30 * time.Second},
		{"timeout: 45s\ninterval: 1m\n", 45 * time.Second},
		{"interval: 20s\n", 20 * time.Second},
		{"interval: 2m\n", 30 * time.Second},
	} {
		cfg := tt.top + strings.Replace(valid, "url: http://127.0.0.1:8332", "url: http://"+onion+":8332", 1)
		c, _, err := Load(write(t, cfg))
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Targets[0].Node.Timeout; got != tt.want {
			t.Errorf("%q: timeout = %s, want %s", tt.top, got, tt.want)
		}
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

// The config path is the same on every OS, macOS included.
func TestDefaultConfigPath(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	for _, tt := range []struct{ xdg, want string }{
		{"/xdg/config", "/xdg/config/node-tracker/config.yaml"},
		{"relative", "/home/u/.config/node-tracker/config.yaml"}, // spec: ignore non-absolute
		{"", "/home/u/.config/node-tracker/config.yaml"},
	} {
		t.Setenv("XDG_CONFIG_HOME", tt.xdg)
		if p, err := DefaultConfigPath(); err != nil || p != tt.want {
			t.Errorf("XDG_CONFIG_HOME=%q: %s %v, want %s", tt.xdg, p, err, tt.want)
		}
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
