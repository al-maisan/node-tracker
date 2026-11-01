// Package explain turns node, reference and Telegram errors into plain
// words with a likely cause, for check, test-notify, setup and alerts.
package explain

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/source/bitcoind"
)

// BotUser matches a valid Telegram bot username.
var BotUser = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// Printable replaces what a terminal could interpret, such as escape
// sequences, in text from the network.
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, s)
}

// Node is a node error, printable, with a hint at its likely cause. e is
// the validated endpoint; proxied tells whether its requests go through a
// proxy.
func Node(e config.Endpoint, proxied bool, err error) string {
	msg := Printable(err.Error())
	if h := Hint(e, proxied, err); h != "" {
		msg += " (" + h + ")"
	}
	return msg
}

// Hint is the likely cause of a node error, as in Node, or "".
func Hint(e config.Endpoint, proxied bool, err error) string {
	return cmp.Or(hint(e, proxied, err), Text(Printable(err.Error())))
}

// hint suggests a likely cause of a node error that needs e to tell, or
// returns "".
func hint(e config.Endpoint, proxied bool, err error) string {
	var auth *bitcoind.AuthError
	var rpc *bitcoind.RPCError
	var ne net.Error
	switch {
	case e.CookieFile != "" && errors.Is(err, fs.ErrNotExist):
		return "is bitcoind running? It writes the cookie on start. If its datadir is elsewhere, give the cookie's path (cookie_file)"
	case e.CookieFile != "" && errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("to read it, join its group: sudo usermod -aG %s \"$USER\", then log in again; on Bitcoin Core 28+ also set rpccookieperms=group in bitcoin.conf", group(e.CookieFile))
	case errors.Is(err, syscall.ECONNREFUSED) && proxied:
		return "is the proxy running? for .onion: is Tor running?"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "is bitcoind running with server=1? RPC ports: main 8332, testnet3 18332, testnet4 48332, signet 38332, regtest 18443"
	case errors.As(err, &auth) && auth.Status == 401:
		return "wrong username/password, or a stale cookie"
	case errors.As(err, &auth):
		return "not allowed: check rpcallowip, and rpcwhitelist for getblockchaininfo"
	case errors.As(err, &rpc) && rpc.Code == bitcoind.ErrWarmup:
		return "bitcoind is starting; try again when it has loaded"
	case (errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()) && IsOnion(e.URL):
		return "is Tor running and bootstrapped? .onion calls can be slow"
	}
	return ""
}

// group is the name of the group of file, else "bitcoind's group".
func group(file string) string {
	if fi, err := os.Stat(file); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			if g, err := user.LookupGroupId(strconv.Itoa(int(st.Gid))); err == nil {
				return g.Name
			}
		}
	}
	return "bitcoind's group"
}

// IsOnion tells whether raw is a URL of a .onion host.
func IsOnion(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Hostname()), ".onion")
}

// References is a hint for NO_REFERENCE, from the references' errors.
func References(msg string) string {
	if strings.Contains(msg, " is on chain ") {
		return "the references are on another chain than the node: set references for the node's chain"
	}
	return "no independent reference answered: is this machine online, does it need a proxy, are the URLs right?"
}

// Telegram is a notifier error, printable on one line, with a hint at its
// likely cause.
func Telegram(err error) string {
	msg := Printable(strings.ReplaceAll(err.Error(), "\n", "; "))
	h := ""
	switch {
	case strings.Contains(msg, "chat not found"):
		h = "wrong chat id, or the bot was never started in that chat"
	case strings.Contains(msg, "HTTP 403"):
		h = "the bot is blocked, or not a member of that chat; a channel needs it as an administrator"
	case strings.Contains(msg, "bot token rejected") || strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 404"):
		h = "wrong bot token: check bot_token_file / bot_token_env"
	case strings.Contains(msg, "HTTP 429"):
		h = "Telegram asks to slow down; alerts are retried"
	default:
		h = Text(msg)
	}
	if h != "" {
		msg += " (" + h + ")"
	}
	return msg
}

var (
	refusedRE = regexp.MustCompile(`dial tcp (\S+): connect: connection refused`)
	timeoutRE = regexp.MustCompile(`deadline exceeded|Timeout exceeded|i/o timeout`)
)

// Text is a plain-words cause of a common error, from its text only (as
// stored in alerts), or "" if it is not a common one. Where the error value
// is at hand, Hint tells by its type first.
func Text(msg string) string {
	addr := "that address"
	if m := refusedRE.FindAllStringSubmatch(msg, -1); m != nil {
		addr = m[len(m)-1][1]
	}
	proxy := strings.Contains(msg, "socks connect") || strings.Contains(msg, "proxyconnect")
	switch {
	case strings.Contains(msg, "general SOCKS server failure") || strings.Contains(msg, "host unreachable") && proxy:
		return "the proxy could not reach the host; for .onion: is that Tor service up, and the address right?"
	case strings.Contains(msg, "connection refused") && proxy:
		return "the proxy at " + addr + " does not answer; for .onion: is Tor running?"
	case strings.Contains(msg, "connection refused"):
		return "nothing answers at " + addr
	case strings.Contains(msg, "read cookie file") && strings.Contains(msg, "no such file"):
		return "the cookie file is missing: is bitcoind running?"
	case strings.Contains(msg, "read cookie file") && strings.Contains(msg, "permission denied"):
		return "node-tracker may not read the cookie file"
	case strings.Contains(msg, "rpc auth failed (HTTP 401)"):
		return "wrong RPC username/password, or a stale cookie"
	case strings.Contains(msg, "rpc auth failed (HTTP 403)"):
		return "the node refuses the call: check rpcallowip and rpcwhitelist"
	case strings.Contains(msg, "rpc error -28"):
		return "bitcoind is starting"
	case strings.Contains(msg, "no such host"):
		return "unknown host name: a typo, or no DNS"
	case strings.Contains(msg, "network is unreachable"):
		return "no network: is this machine online?"
	case timeoutRE.MatchString(msg):
		return "no answer in time"
	case strings.Contains(msg, "x509: "):
		return "the TLS certificate is not trusted"
	case strings.Contains(msg, "HTTP response to HTTPS client"):
		return "the server speaks http, not https"
	case strings.Contains(msg, "HTTP 429"):
		return "rate limited"
	}
	return ""
}
