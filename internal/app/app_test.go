package app

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/source"
)

// Every kind the config accepts must have an implementation.
func TestEveryKindIsImplemented(t *testing.T) {
	for _, k := range append(config.NodeKinds, config.ReferenceKinds...) {
		if _, ok := sources[k]; !ok {
			t.Errorf("source kind %q has no constructor", k)
		}
	}
	for _, k := range config.NotifierKinds {
		if _, ok := notifiers[k]; !ok {
			t.Errorf("notifier kind %q has no constructor", k)
		}
	}
}

func TestBuild(t *testing.T) {
	th, rec, out := uint64(3), uint64(1), uint64(6)
	c := &config.Config{
		Timeout: 1,
		Targets: []config.Target{{
			Name: "btc",
			Node: config.Endpoint{Kind: config.KindBitcoind, Name: "bitcoind:x", URL: "http://127.0.0.1:8332", CookieFile: "/c",
				Proxy: config.DefaultTorProxy, Timeout: 30 * time.Second},
			References:       []config.Endpoint{{Kind: config.KindEsplora, Name: "esplora:y", URL: "https://y/api"}},
			Alert:            config.Alert{Threshold: &th, RecoverThreshold: &rec, For: 2},
			ReferenceOutlier: &out,
			Notify:           []string{"tg"},
		}},
		Notifiers: []config.Notifier{{Name: "tg", Kind: config.KindTelegram, ChatID: "1", BotToken: "t"}},
	}
	m, err := Build(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	tg := m.Targets[0]
	if tg.Node.Name() != "bitcoind:x" || tg.Refs[0].Name() != "esplora:y" || tg.Rule.Recover != 1 || tg.Rule.Outlier != 6 || m.Notifiers["tg"] == nil {
		t.Fatalf("bad wiring: %+v", m)
	}
	if source.Timeout(tg.Node, 0) != 30*time.Second || source.Timeout(tg.Refs[0], 0) != 0 {
		t.Fatal("per-endpoint timeout not wired")
	}
}

func TestProxy(t *testing.T) {
	// http.ProxyFromEnvironment reads the environment once per process.
	t.Setenv("HTTPS_PROXY", "http://user:s3cr3tPW@10.9.9.9:3128")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	for _, tt := range []struct {
		e    config.Endpoint
		want string
	}{
		{config.Endpoint{URL: "http://x.onion", Proxy: "socks5://127.0.0.1:9150"}, "socks5://127.0.0.1:9150"},
		{config.Endpoint{URL: "http://x.onion", Proxy: config.DefaultTorProxy, DefaultProxy: true}, config.DefaultTorProxy + " (default)"},
		{config.Endpoint{URL: "https://mempool.space/api"}, "http://10.9.9.9:3128 (environment)"},
		{config.Endpoint{URL: "http://127.0.0.1:8332"}, "none"}, // loopback never uses the environment's proxy
	} {
		if got := Proxy(tt.e); got != tt.want || strings.Contains(got, "s3cr3tPW") {
			t.Errorf("Proxy(%s) = %q, want %q", tt.e.URL, got, tt.want)
		}
	}
}
