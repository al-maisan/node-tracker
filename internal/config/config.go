// Package config loads and validates the YAML configuration and resolves
// secrets from files or environment variables.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
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
)

var (
	NodeKinds      = []string{KindBitcoind}
	ReferenceKinds = []string{KindBitcoind, KindEsplora}
	NotifierKinds  = []string{KindTelegram}
)

const (
	defaultInterval = 30 * time.Second
	defaultTimeout  = 10 * time.Second
	defaultFor      = 2
	defaultOutlier  = 6
	maxFor          = 100
	maxConfigSize   = 1 << 20
	maxSecretSize   = 4 << 10
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
}

// Target is one watched node.
type Target struct {
	Name             string     `yaml:"name"`
	Node             Endpoint   `yaml:"node"`
	References       []Endpoint `yaml:"references"`
	ReferenceOutlier *uint64    `yaml:"reference_outlier"` // default 6; 0 disables
	Alert            Alert      `yaml:"alert"`
	Notify           []string   `yaml:"notify"`
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

	Name     string `yaml:"-"` // derived: kind:host
	Password Secret `yaml:"-"` // resolved from PasswordFile / PasswordEnv
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

// Load reads, defaults and validates the file at path and resolves secrets.
// Every problem is reported in one joined error. Warnings are non-fatal
// findings, such as a secret file readable by others.
func Load(path string) (*Config, []string, error) {
	f, err := os.Open(path)
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
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	v := &validator{}
	c.validate(v)
	if err := errors.Join(v.errs...); err != nil {
		return nil, v.warns, fmt.Errorf("%s: invalid config:\n%w", path, err)
	}
	return &c, v.warns, nil
}

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

func (c *Config) validate(v *validator) {
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

		t.Node.validate(v, p+".node", NodeKinds)
		seen := map[string]bool{t.Node.URL: true}
		for j := range t.References {
			r := &t.References[j]
			rp := fmt.Sprintf("%s.references[%d]", p, j)
			r.validate(v, rp, ReferenceKinds)
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

func (e *Endpoint) validate(v *validator, p string, kinds []string) {
	if !slices.Contains(kinds, e.Kind) {
		v.bad(p+".kind", e.Kind, "must be one of %v", kinds)
		return
	}
	u := checkURL(v, p+".url", e.URL, e.AllowInsecure, true)
	if u == nil {
		return
	}
	e.Name = e.Kind + ":" + u.Host

	switch e.Kind {
	case KindBitcoind:
		e.CookieFile = expandHome(e.CookieFile)
		if e.CookieFile != "" && !filepath.IsAbs(e.CookieFile) {
			v.bad(p+".cookie_file", e.CookieFile, "must be an absolute path or start with ~/")
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
	if a.Threshold == nil {
		v.errf(p+".threshold", "required (lag in blocks)")
		return
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

// checkURL validates an endpoint URL: absolute http(s), a valid host, no
// credentials, and plain http only for loopback hosts unless allowInsecure.
// endpoint is set for nodes and references, which have allow_insecure.
// Errors show the URL with userinfo redacted, and never url.Parse's error
// for raw, which quotes the input.
func checkURL(v *validator, p, raw string, allowInsecure, endpoint bool) *url.URL {
	if raw == "" {
		v.errf(p, "required")
		return nil
	}
	shown := redactURL(raw)
	if !schemeRE.MatchString(raw) {
		v.bad(p, shown, "missing scheme; add http:// or https://")
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		v.bad(p, shown, "%s", parseError(shown))
		return nil
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https" || u.Host == "":
		v.bad(p, shown, "must be an absolute http(s) URL")
	case u.User != nil:
		v.bad(p, shown, "must not contain credentials; use the auth fields")
	case badHost(u.Hostname()) != "":
		v.bad(p, shown, "%s", badHost(u.Hostname()))
	case u.Scheme == "http" && !allowInsecure && !isLoopback(u.Hostname()) && endpoint:
		v.bad(p, shown, "plain http is only allowed for loopback hosts; use https or set allow_insecure: true (e.g. LAN or Tor)")
	case u.Scheme == "http" && !isLoopback(u.Hostname()):
		v.bad(p, shown, "plain http is only allowed for loopback hosts; use https")
	default:
		return u
	}
	return nil
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
	return "invalid URL; credentials (user:pass@) are not allowed, use the auth fields"
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

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func base(p string) string { return p[strings.LastIndex(p, ".")+1:] }

// DefaultConfigPath is $XDG_CONFIG_HOME/node-tracker/config.yaml.
func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "node-tracker", "config.yaml"), nil
}

// DefaultStatePath is $XDG_STATE_HOME/node-tracker/state.json, falling back
// to ~/.local/state/node-tracker/state.json.
func DefaultStatePath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "node-tracker", "state.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "node-tracker", "state.json"), nil
}
