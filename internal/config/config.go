// Package config loads and validates the YAML configuration and resolves
// secrets from files or environment variables.
package config

import (
	"bytes"
	"crypto/sha3"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Kinds known to the validator. Adding a kind means: list it here, validate
// its fields below and give it a constructor in package app.
const (
	KindBitcoind = "bitcoind"
	KindEsplora  = "esplora"
	KindTelegram = "telegram"

	DefaultTelegramAPI = "https://api.telegram.org"
	DefaultTorProxy    = "socks5://127.0.0.1:9050" // proxy of .onion endpoints without one
)

var (
	NodeKinds      = []string{KindBitcoind}
	ReferenceKinds = []string{KindBitcoind, KindEsplora}
	NotifierKinds  = []string{KindTelegram}
	proxySchemes   = []string{"socks5", "socks5h", "http", "https"}
)

const (
	defaultInterval  = 30 * time.Second
	defaultTimeout   = 10 * time.Second
	onionTimeout     = 30 * time.Second // minimum per-call timeout for .onion hosts
	defaultThreshold = 3
	defaultRecover   = 1
	defaultFor       = 2
	defaultOutlier   = 6
	maxFor           = 100
	maxConfigSize    = 1 << 20
	maxSecretSize    = 4 << 10
)

var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	chatIDRE = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)
	schemeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
)

// Secret is a resolved secret. It formats as "REDACTED" so it can't leak
// through logs or %v.
type Secret string

func (Secret) String() string   { return "REDACTED" }
func (Secret) GoString() string { return `"REDACTED"` }

// Config is the validated configuration.
type Config struct {
	Interval  time.Duration `yaml:"interval"`
	Timeout   time.Duration `yaml:"timeout"`
	StateFile string        `yaml:"state_file"`
	Targets   []Target      `yaml:"targets"`
	Notifiers []Notifier    `yaml:"notifiers"`

	Detected *Detected `yaml:"-"` // the local bitcoind, if a target's node was left out
}

// Target is one watched node.
type Target struct {
	Name             string     `yaml:"name"`
	Node             Endpoint   `yaml:"node"`
	References       []Endpoint `yaml:"references"`
	ReferenceOutlier *uint64    `yaml:"reference_outlier"` // default 6; 0 disables
	Alert            Alert      `yaml:"alert"`
	Notify           []string   `yaml:"notify"`

	DefaultRefs bool `yaml:"-"` // derived: references left out, so the public ones of the chain
}

// Endpoint is a node or a reference.
type Endpoint struct {
	Kind          string `yaml:"kind"`
	URL           string `yaml:"url"`
	CookieFile    string `yaml:"cookie_file"`
	Username      string `yaml:"username"`
	PasswordFile  string `yaml:"password_file"`
	PasswordEnv   string `yaml:"password_env"`
	AllowInsecure bool   `yaml:"allow_insecure"`
	Proxy         string `yaml:"proxy"` // empty: from the environment; for .onion, DefaultTorProxy if the environment has none

	Name         string        `yaml:"-"` // derived: kind:host
	Password     Secret        `yaml:"-"` // resolved from PasswordFile / PasswordEnv
	Timeout      time.Duration `yaml:"-"` // derived: per-call timeout of .onion hosts; 0 means Config.Timeout
	DefaultProxy bool          `yaml:"-"` // derived: Proxy was set to DefaultTorProxy
}

// Alert holds the thresholds. Pointers tell "unset" from 0.
type Alert struct {
	Threshold        *uint64 `yaml:"threshold"`
	RecoverThreshold *uint64 `yaml:"recover_threshold"`
	For              int     `yaml:"for"`
}

// Notifier is an alert channel.
type Notifier struct {
	Name         string `yaml:"name"`
	Kind         string `yaml:"kind"`
	BotTokenFile string `yaml:"bot_token_file"`
	BotTokenEnv  string `yaml:"bot_token_env"`
	ChatID       string `yaml:"chat_id"`
	APIURL       string `yaml:"api_url"` // optional, for a self-hosted Bot API server

	BotToken Secret `yaml:"-"`
}

// Load is LoadEnv on the real machine.
func Load(path string) (*Config, []string, error) { return LoadEnv(path, SystemEnv()) }

// LoadEnv reads, defaults and validates the file at path and resolves
// secrets. A target without node gets the local bitcoind, detected in env.
// Every problem is reported in one joined error. Warnings are non-fatal
// findings, such as a secret file readable by others.
func LoadEnv(path string, env Env) (*Config, []string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, NoConfigError{path, err}
	}
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > maxConfigSize {
		return nil, nil, fmt.Errorf("%s: larger than %d bytes", path, maxConfigSize)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, fmt.Errorf("%s: empty config", path)
		}
		return nil, nil, fmt.Errorf("%s: invalid config:\n%w", path, yamlError(raw, err))
	}
	v := &validator{}
	c.validate(v, env)
	if err := errors.Join(v.errs...); err != nil {
		return nil, v.warns, fmt.Errorf("%s: invalid config:\n%w", path, err)
	}
	return &c, v.warns, nil
}

// NoConfigHint tells how to create a config. It follows "no config at
// <path>; ".
const NoConfigHint = "copy examples/config.yaml there and edit it; see docs/configuration.md"

// NoConfigError is Load's error for a missing file. It unwraps to
// fs.ErrNotExist.
type NoConfigError struct {
	Path string
	Err  error
}

func (e NoConfigError) Error() string {
	return fmt.Sprintf("no config at %s; %s", e.Path, NoConfigHint)
}
func (e NoConfigError) Unwrap() error { return e.Err }

type validator struct {
	errs  []error
	warns []string
}

func (v *validator) errf(path, format string, a ...any) {
	v.errs = append(v.errs, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, a...)))
}

// bad is errf for a field whose value is shown, with a hint for common
// mistakes. value must not be a secret; URLs go through redactURL first.
func (v *validator) bad(path, value, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	switch {
	case strings.Contains(value, "$"):
		msg += "; YAML does not expand environment variables"
	case strings.Contains(value, "<") && strings.Contains(value, ">"):
		msg += "; this looks like a placeholder"
	}
	v.errf(fmt.Sprintf("%s %q", path, value), "%s", msg)
}

func (c *Config) validate(v *validator, env Env) {
	c.StateFile = expandHome(c.StateFile)
	if c.StateFile != "" && !filepath.IsAbs(c.StateFile) {
		v.bad("state_file", c.StateFile, "must be an absolute path or start with ~/")
	}
	if c.Interval == 0 {
		c.Interval = defaultInterval
	}
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.Interval < time.Second {
		v.errf("interval", "must be at least 1s")
	}
	if c.Timeout < 0 || c.Timeout > c.Interval {
		v.errf("timeout", "must be positive and at most interval (%s)", c.Interval)
	}

	notifiers := map[string]bool{}
	for i := range c.Notifiers {
		n := &c.Notifiers[i]
		p := fmt.Sprintf("notifiers[%d]", i)
		if !nameRE.MatchString(n.Name) {
			v.bad(p+".name", n.Name, "must match %s", nameRE)
		} else if notifiers[n.Name] {
			v.errf(p+".name", "duplicate %q", n.Name)
		}
		notifiers[n.Name] = true
		n.validate(v, p)
	}

	if len(c.Targets) == 0 {
		v.errf("targets", "at least one target is required")
	}
	targets := map[string]bool{}
	for i := range c.Targets {
		t := &c.Targets[i]
		p := fmt.Sprintf("targets[%d]", i)
		if !nameRE.MatchString(t.Name) {
			v.bad(p+".name", t.Name, "must match %s", nameRE)
		} else if targets[t.Name] {
			v.errf(p+".name", "duplicate %q", t.Name)
		}
		targets[t.Name] = true

		chain := ChainMain // of the default references
		if t.Node == (Endpoint{}) {
			if c.Detected == nil {
				d := env.Detect()
				c.Detected = &d
			}
			t.Node = Endpoint{Kind: KindBitcoind, URL: c.Detected.URL, CookieFile: c.Detected.CookieFile}
			chain = c.Detected.Chain
		} else if t.Node.Kind == KindBitcoind {
			chain = portChain(t.Node.URL)
		}
		if t.References == nil {
			t.References, t.DefaultRefs = DefaultReferences(chain), true
		}
		t.Node.validate(v, p+".node", NodeKinds, c)
		seen := map[string]bool{t.Node.URL: true}
		for j := range t.References {
			r := &t.References[j]
			rp := fmt.Sprintf("%s.references[%d]", p, j)
			r.validate(v, rp, ReferenceKinds, c)
			if seen[r.URL] {
				v.errf(rp+".url", "duplicate of the node or another reference")
			}
			seen[r.URL] = true
		}
		if t.ReferenceOutlier == nil {
			t.ReferenceOutlier = new(uint64)
			*t.ReferenceOutlier = defaultOutlier
		}
		if len(t.References) == 0 {
			v.warns = append(v.warns, p+": no references; lag is measured against the node's own headers only, which can't detect an isolated node")
		}
		t.Alert.validate(v, p+".alert")

		if t.Notify == nil {
			for _, n := range c.Notifiers {
				t.Notify = append(t.Notify, n.Name)
			}
		}
		if len(t.Notify) == 0 {
			v.errf(p+".notify", "at least one notifier is required")
		}
		for _, n := range t.Notify {
			if !notifiers[n] {
				v.errf(p+".notify", "unknown notifier %q", n)
			}
		}
	}
}

// validate checks e and sets its defaults. A .onion host without proxy uses
// the environment's proxy for its URL, else DefaultTorProxy, and gets at
// least 30s per call, capped at c.Interval.
func (e *Endpoint) validate(v *validator, p string, kinds []string, c *Config) {
	if !slices.Contains(kinds, e.Kind) {
		v.bad(p+".kind", e.Kind, "must be one of %v", kinds)
		return
	}
	var proxy *url.URL
	if e.Proxy != "" {
		proxy = checkProxy(v, p+".proxy", e.Proxy)
	}
	u := checkURL(v, p+".url", e.URL, e.AllowInsecure, true)
	if u == nil {
		return
	}
	e.Name = e.Kind + ":" + u.Host
	if isOnion(u.Hostname()) {
		e.Timeout = min(max(c.Timeout, onionTimeout), c.Interval)
		from := ""
		if e.Proxy == "" {
			from = " (environment)"
			var err error
			if proxy, err = http.ProxyFromEnvironment(&http.Request{URL: u}); err != nil {
				v.errf(p+".url", "invalid proxy in the environment (HTTP_PROXY / HTTPS_PROXY)")
				return
			}
		}
		if proxy == nil && e.Proxy == "" {
			e.Proxy, e.DefaultProxy, from = DefaultTorProxy, true, ""
			proxy, _ = url.Parse(DefaultTorProxy)
		}
		// Plain http is fine only up to a local proxy: from there, Tor
		// encrypts. A remote proxy would see the RPC credentials.
		if proxy != nil && u.Scheme == "http" && !e.AllowInsecure && !isLoopback(proxy.Hostname()) {
			v.bad(p+".url", e.URL, "plain http to .onion needs a loopback proxy; %q%s would see the RPC credentials. Use a local Tor, or set allow_insecure: true if that network is trusted",
				redactURL(proxy.String()), from)
		}
	}

	switch e.Kind {
	case KindBitcoind:
		e.CookieFile = expandHome(e.CookieFile)
		if e.CookieFile != "" && !filepath.IsAbs(e.CookieFile) {
			v.bad(p+".cookie_file", e.CookieFile, "must be an absolute path or start with ~/")
		} else if _, err := os.Stat(e.CookieFile); e.CookieFile != "" && errors.Is(err, fs.ErrNotExist) {
			// bitcoind writes it on start, so this may fix itself.
			v.warns = append(v.warns, fmt.Sprintf("%s.cookie_file: %s does not exist; bitcoind is not started yet or its datadir is elsewhere. For a remote node, use username and password_file", p, e.CookieFile))
		}
		hasUser := e.Username != ""
		switch {
		case e.CookieFile != "" && hasUser:
			v.errf(p, "set either cookie_file or username, not both")
		case e.CookieFile == "" && !hasUser:
			v.errf(p, "auth required: cookie_file, or username with password_file/password_env")
		case hasUser:
			e.Password = resolveSecret(v, p+".password", e.PasswordFile, e.PasswordEnv)
		}
	case KindEsplora:
		if e.CookieFile != "" || e.Username != "" || e.PasswordFile != "" || e.PasswordEnv != "" {
			v.errf(p, "esplora takes no auth fields")
		}
	}
}

func (a *Alert) validate(v *validator, p string) {
	if a.Threshold == nil { // the balanced preset
		a.Threshold = new(uint64)
		*a.Threshold = defaultThreshold
		if a.RecoverThreshold == nil {
			a.RecoverThreshold = new(uint64)
			*a.RecoverThreshold = defaultRecover
		}
	}
	if a.RecoverThreshold == nil {
		a.RecoverThreshold = a.Threshold
	} else if *a.RecoverThreshold > *a.Threshold {
		v.errf(p+".recover_threshold", "must not exceed threshold (%d)", *a.Threshold)
	}
	if a.For == 0 {
		a.For = defaultFor
	}
	if a.For < 1 || a.For > maxFor {
		v.errf(p+".for", "must be between 1 and %d", maxFor)
	}
}

func (n *Notifier) validate(v *validator, p string) {
	if !slices.Contains(NotifierKinds, n.Kind) {
		v.bad(p+".kind", n.Kind, "must be one of %v", NotifierKinds)
		return
	}
	switch n.Kind {
	case KindTelegram:
		if !chatIDRE.MatchString(n.ChatID) {
			v.bad(p+".chat_id", n.ChatID, "must be a numeric chat id or @channelname")
		}
		if n.APIURL == "" {
			n.APIURL = DefaultTelegramAPI
		} else {
			checkURL(v, p+".api_url", n.APIURL, false, false)
		}
		n.BotToken = resolveSecret(v, p+".bot_token", n.BotTokenFile, n.BotTokenEnv)
		if n.BotToken != "" && strings.ContainsAny(string(n.BotToken), " \t\r\n/?#") {
			v.errf(p+".bot_token", "contains invalid characters")
		}
	}
}

// resolveSecret reads a secret from exactly one of file or env. Secret
// values never appear in errors or warnings.
func resolveSecret(v *validator, p, file, env string) Secret {
	switch {
	case file != "" && env != "":
		v.errf(p, "set either %s_file or %s_env, not both", base(p), base(p))
		return ""
	case file != "":
		if file = expandHome(file); !filepath.IsAbs(file) {
			v.bad(p+"_file", file, "must be an absolute path or start with ~/")
			return ""
		}
		fi, err := os.Stat(file)
		if err != nil {
			v.errf(p+"_file", "%v", err)
			return ""
		}
		if fi.Size() > maxSecretSize {
			v.errf(p+"_file", "larger than %d bytes", maxSecretSize)
			return ""
		}
		if fi.Mode().Perm()&0o007 != 0 { // group access is fine: a dedicated service group
			v.warns = append(v.warns, fmt.Sprintf("%s_file: %s is accessible by others (mode %v); chmod o-rwx it", p, file, fi.Mode().Perm()))
		}
		b, err := os.ReadFile(file)
		if err != nil {
			v.errf(p+"_file", "%v", err)
			return ""
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			v.errf(p+"_file", "%s is empty", file)
		}
		return Secret(s)
	case env != "":
		s, ok := os.LookupEnv(env)
		if s = strings.TrimSpace(s); !ok || s == "" {
			v.errf(p+"_env", "environment variable %s is not set or empty", env)
		}
		return Secret(s)
	default:
		v.errf(p, "set %s_file or %s_env", base(p), base(p))
		return ""
	}
}

// checkURL validates a URL: absolute http(s), a valid host, no credentials,
// and plain http only for loopback hosts unless allowInsecure. endpoint is
// set for nodes and references: they have allow_insecure, and plain http to
// .onion hosts is fine for them through a local proxy (Tor encrypts; the
// caller checks the proxy). Errors show the URL with userinfo redacted, and
// never url.Parse's error for raw, which quotes the input.
func checkURL(v *validator, p, raw string, allowInsecure, endpoint bool) *url.URL {
	u, shown := parseURL(v, p, raw, "add http:// or https://")
	if u == nil {
		return nil
	}
	host := u.Hostname()
	switch {
	case u.Scheme != "http" && u.Scheme != "https" || u.Host == "":
		v.bad(p, shown, "must be an absolute http(s) URL")
	case u.User != nil:
		v.bad(p, shown, "must not contain credentials; use the auth fields")
	case badHost(host) != "":
		v.bad(p, shown, "%s", badHost(host))
	case isOnion(host) && badOnion(host) != "":
		v.bad(p, shown, "%s", badOnion(host))
	case u.Scheme == "http" && !allowInsecure && !isLoopback(host) && endpoint && !isOnion(host):
		v.bad(p, shown, "plain http is only allowed for loopback and .onion hosts; use https or set allow_insecure: true (e.g. LAN)")
	case u.Scheme == "http" && !isLoopback(host) && !endpoint:
		v.bad(p, shown, "plain http is only allowed for loopback hosts; use https")
	default:
		return u
	}
	return nil
}

// checkProxy validates a proxy URL: one of proxySchemes, a host and an
// optional port, nothing else. Proxy credentials are not supported. It
// returns nil if raw is invalid.
func checkProxy(v *validator, p, raw string) *url.URL {
	u, shown := parseURL(v, p, raw, "e.g. "+DefaultTorProxy)
	switch {
	case u == nil:
	case !slices.Contains(proxySchemes, u.Scheme):
		v.bad(p, shown, "scheme must be one of %v", proxySchemes)
	case u.User != nil:
		v.bad(p, shown, "must not contain credentials; proxy authentication is not supported")
	case u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "":
		v.bad(p, shown, "must be scheme://host:port, e.g. %s", DefaultTorProxy)
	case badHost(u.Hostname()) != "":
		v.bad(p, shown, "%s", badHost(u.Hostname()))
	default:
		return u
	}
	return nil
}

// parseURL parses raw, or reports why it can't and returns nil. shown is raw
// with userinfo redacted; example tells how to fix a missing scheme.
func parseURL(v *validator, p, raw, example string) (u *url.URL, shown string) {
	if raw == "" {
		v.errf(p, "required")
		return nil, ""
	}
	shown = redactURL(raw)
	if !schemeRE.MatchString(raw) {
		v.bad(p, shown, "missing scheme; %s", example)
		return nil, shown
	}
	u, err := url.Parse(raw)
	if err != nil {
		v.bad(p, shown, "%s", parseError(shown))
		return nil, shown
	}
	return u, shown
}

// redactURL replaces userinfo with REDACTED. It works on unparsable input
// too: everything after "scheme://" (or from the start) up to the last "@"
// goes, as a password may contain any character, including "/" and "@".
func redactURL(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	start := len(schemeRE.FindString(raw[:at]))
	return raw[:start] + "REDACTED" + raw[at:]
}

// parseError says why the redacted URL shown does not parse. If it parses,
// the problem is in the redacted part.
func parseError(shown string) string {
	var ue *url.Error
	if _, err := url.Parse(shown); errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return "invalid URL; credentials (user:pass@) are not allowed"
}

// badHost says why host is neither an IP address nor a DNS name, or "".
func badHost(host string) string {
	if net.ParseIP(host) != nil {
		return ""
	}
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return fmt.Sprintf("invalid character %q in host name", string(c))
		}
	}
	return ""
}

// expandHome replaces a leading "~/" with the user's home directory.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p // the later stat/read reports the unusable path
	}
	return filepath.Join(home, p[2:])
}

func isOnion(host string) bool { return strings.HasSuffix(strings.ToLower(host), ".onion") }

// badOnion says why the .onion host is not a valid v3 onion address, or "".
// A v3 address is base32(pubkey[32] | checksum[2] | version 3), with
// checksum = SHA3-256(".onion checksum" | pubkey | version)[:2], so typos
// are caught here instead of as a Tor timeout.
func badOnion(host string) string {
	labels := strings.Split(strings.ToUpper(host), ".")
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(labels[len(labels)-2])
	if err != nil || len(b) != 35 || b[34] != 3 {
		return "not a v3 onion address (56 base32 characters before .onion)"
	}
	sum := sha3.Sum256(slices.Concat([]byte(".onion checksum"), b[:32], b[34:]))
	if !bytes.Equal(sum[:2], b[32:34]) {
		return "onion address checksum mismatch; check it for typos"
	}
	return ""
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func base(p string) string { return p[strings.LastIndex(p, ".")+1:] }

// DefaultConfigDir is $XDG_CONFIG_HOME, falling back to ~/.config, on every
// OS. os.UserConfigDir is not used: on macOS it is ~/Library/Application
// Support, which the docs don't mention.
func DefaultConfigDir() (string, error) { return xdgDir("XDG_CONFIG_HOME", ".config") }

// DefaultConfigPath is node-tracker/config.yaml in DefaultConfigDir.
func DefaultConfigPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "node-tracker", "config.yaml"), nil
}

// DefaultStatePath is $XDG_STATE_HOME/node-tracker/state.json, falling back
// to ~/.local/state/node-tracker/state.json.
func DefaultStatePath() (string, error) {
	dir, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "node-tracker", "state.json"), nil
}

// xdgDir is the XDG base directory in env, or ~/fallback if env is unset or
// not absolute (the spec says to ignore relative paths).
func xdgDir(env, fallback string) (string, error) {
	if dir := os.Getenv(env); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, fallback), nil
}
