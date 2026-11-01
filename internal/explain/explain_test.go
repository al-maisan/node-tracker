package explain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/source/bitcoind"
)

const onion = "http://2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion:8332"

func refused(addr string) error {
	return &url.Error{Op: "Post", URL: "http://" + addr, Err: &net.OpError{Op: "dial", Net: "tcp",
		Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8332}, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}
}

func TestNode(t *testing.T) {
	timeout := &url.Error{Op: "Post", URL: onion, Err: context.DeadlineExceeded}
	local := config.Endpoint{Kind: config.KindBitcoind, URL: "http://127.0.0.1:8332"}
	tor := config.Endpoint{Kind: config.KindBitcoind, URL: onion, Proxy: "socks5://127.0.0.1:9050"}
	cookie := filepath.Join(t.TempDir(), ".cookie")
	os.WriteFile(cookie, nil, 0o600)
	withCookie := config.Endpoint{Kind: config.KindBitcoind, URL: "http://127.0.0.1:8332", CookieFile: cookie}
	for _, tc := range []struct {
		name    string
		e       config.Endpoint
		proxied bool
		err     error
		want    string // "": no hint
	}{
		{"refused", local, false, refused("127.0.0.1:8332"), "connection refused (is bitcoind running with server=1?"},
		{"proxy refused", tor, true, refused("127.0.0.1:9050"), "(is the proxy running? for .onion: is Tor running?)"},
		{"401", local, false, &bitcoind.AuthError{Status: 401}, "rpc auth failed (HTTP 401) (wrong username/password, or a stale cookie)"},
		{"403", local, false, &bitcoind.AuthError{Status: 403}, "rpcwhitelist"},
		{"warmup", local, false, &bitcoind.RPCError{Code: bitcoind.ErrWarmup, Message: "Loading"}, "starting"},
		{"other rpc error", local, false, &bitcoind.RPCError{Code: -1, Message: "x"}, ""},
		{"onion timeout", tor, true, timeout, "is Tor running and bootstrapped?"},
		{"local timeout", local, false, timeout, "(no answer in time)"},
		{"cookie missing", withCookie, false, fmt.Errorf("read cookie file: %w", fs.ErrNotExist), "(is bitcoind running? It writes the cookie on start."},
		{"cookie unreadable", withCookie, false, fmt.Errorf("read cookie file: %w", fs.ErrPermission), "join its group: sudo usermod -aG "},
		{"no cookie configured", local, false, fmt.Errorf("x: %w", fs.ErrPermission), ""},
		{"socks failure", tor, true, errors.New("socks connect tcp 127.0.0.1:9050->x.onion:8332: unknown error general SOCKS server failure"), "the proxy could not reach the host"},
		{"escape codes", local, false, errors.New("boom\x1b[2J"), ""},
	} {
		got := Node(tc.e, tc.proxied, tc.err)
		if !strings.HasPrefix(got, Printable(tc.err.Error())) || !strings.Contains(got, tc.want) || tc.want == "" && got != Printable(tc.err.Error()) {
			t.Errorf("%s: Node = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := Node(local, false, errors.New("a\x1b[2Jb")); got != "a?[2Jb" {
		t.Errorf("Node = %q, want the escape code replaced", got)
	}
}

func TestText(t *testing.T) {
	for msg, want := range map[string]string{
		`Post "http://127.0.0.1:1": dial tcp 127.0.0.1:1: connect: connection refused`:                                                     "nothing answers at 127.0.0.1:1",
		`Post "http://x": proxyconnect tcp: dial tcp 10.0.0.1:3128: connect: connection refused`:                                           "the proxy at 10.0.0.1:3128 does not answer",
		`Post "http://x.onion:8332": socks connect tcp 127.0.0.1:9050->x.onion:8332: dial tcp 127.0.0.1:9050: connect: connection refused`: "the proxy at 127.0.0.1:9050 does not answer; for .onion: is Tor running?",
		`socks connect tcp 127.0.0.1:9050->x.onion:8332: unknown error general SOCKS server failure`:                                       "is that Tor service up",
		`Post "http://127.0.0.1:8332": context deadline exceeded`:                                                                          "no answer in time",
		`Get "https://nope.example": dial tcp: lookup nope.example: no such host`:                                                          "unknown host name",
		`read cookie file: open /c: no such file or directory`:                                                                             "the cookie file is missing",
		`read cookie file: open /c: permission denied`:                                                                                     "may not read the cookie file",
		"read cookie file (path not shown: it may be the secret itself): permission denied":                                                "may not read the cookie file",
		"read cookie file /c: no such file or directory":                                                                                   "the cookie file is missing",
		`rpc auth failed (HTTP 401)`:          "wrong RPC username/password",
		`rpc auth failed (HTTP 403)`:          "rpcallowip",
		`rpc error -28: Loading block index…`: "bitcoind is starting",
		`Get "https://x": tls: failed to verify certificate: x509: certificate signed by unknown authority`: "not trusted",
		`HTTP 429`:        "rate limited",
		`rpc error -1: x`: "",
	} {
		if got := Text(msg); want == "" && got != "" || !strings.Contains(got, want) {
			t.Errorf("Text(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestTelegram(t *testing.T) {
	for msg, want := range map[string]string{
		"tg: HTTP 400: Bad Request: chat not found":                                        "tg: HTTP 400: Bad Request: chat not found (wrong chat id, or the bot was never started in that chat)",
		"HTTP 403: Forbidden: bot was blocked by the user":                                 "HTTP 403: Forbidden: bot was blocked by the user (the bot is blocked, or not a member of that chat; a channel needs it as an administrator)",
		"bot token rejected (HTTP 401)":                                                    "bot token rejected (HTTP 401) (wrong bot token: check bot_token_file / bot_token_env)",
		`Post "https://api.telegram.org": dial tcp: lookup api.telegram.org: no such host`: `Post "https://api.telegram.org": dial tcp: lookup api.telegram.org: no such host (unknown host name: a typo, or no DNS)`,
		"tg: HTTP 401: Unauthorized":                                                       "tg: HTTP 401: Unauthorized (wrong bot token: check bot_token_file / bot_token_env)",
		"HTTP 502: \x1b[2Jdown\nagain":                                                     "HTTP 502: ?[2Jdown; again",
	} {
		if got := Telegram(errors.New(msg)); got != want {
			t.Errorf("Telegram(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestReferences(t *testing.T) {
	if got := References("esplora:x: HTTP 503"); !strings.Contains(got, "no independent reference answered") || !strings.Contains(got, "proxy") {
		t.Errorf("References = %q", got)
	}
	if got := References("esplora:x is on chain signet, the node on main"); !strings.Contains(got, "set references for the node's chain") {
		t.Errorf("References, other chain = %q", got)
	}
}

func TestPrintable(t *testing.T) {
	if got := Printable("a\x1b[2Jb\nc\u202ed é"); got != "a?[2Jb?c?d é" {
		t.Errorf("Printable = %q", got)
	}
}

func TestBotUser(t *testing.T) {
	for u, ok := range map[string]bool{"nt_test_bot": true, "": false, "a\x1b[2J": false, "a-b": false, strings.Repeat("a", 65): false} {
		if BotUser.MatchString(u) != ok {
			t.Errorf("BotUser(%q) = %v", u, !ok)
		}
	}
}
