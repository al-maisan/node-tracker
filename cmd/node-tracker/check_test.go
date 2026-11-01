package main

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/source/bitcoind"
)

func TestHint(t *testing.T) {
	const onion = "http://2gzyxa5ihm7nsggfxnu52rck2vv4rvmdlkiu3zzui5du4xyclen53wid.onion:8332"
	refused := &url.Error{Op: "Post", URL: "http://127.0.0.1:8332", Err: &net.OpError{Op: "dial", Net: "tcp",
		Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}
	timeout := &url.Error{Op: "Post", URL: onion, Err: context.DeadlineExceeded}
	local := config.Endpoint{Kind: config.KindBitcoind, URL: "http://127.0.0.1:8332"}
	tor := config.Endpoint{Kind: config.KindBitcoind, URL: onion, Proxy: "socks5://127.0.0.1:9050"}
	for _, tc := range []struct {
		name string
		e    config.Endpoint
		err  error
		want string
	}{
		{"refused", local, refused, "server=1"},
		{"proxy refused", tor, refused, "is Tor running?"},
		{"401", local, &bitcoind.AuthError{Status: 401}, "stale cookie"},
		{"403", local, &bitcoind.AuthError{Status: 403}, "rpcwhitelist"},
		{"warmup", local, &bitcoind.RPCError{Code: bitcoind.ErrWarmup, Message: "Loading"}, "starting"},
		{"other rpc error", local, &bitcoind.RPCError{Code: -1, Message: "x"}, ""},
		{"onion timeout", tor, timeout, "is Tor running and bootstrapped?"},
		{"local timeout", local, timeout, ""},
		{"other", local, errors.New("boom"), ""},
	} {
		got := hint(tc.e, tc.err)
		if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("%s: hint = %q, want %q", tc.name, got, tc.want)
		}
	}
}
