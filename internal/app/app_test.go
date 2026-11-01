package app

import (
	"io"
	"log/slog"
	"testing"

	"github.com/al-maisan/node-tracker/internal/config"
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
	th, rec := uint64(3), uint64(1)
	c := &config.Config{
		Timeout: 1,
		Targets: []config.Target{{
			Name:       "btc",
			Node:       config.Endpoint{Kind: config.KindBitcoind, Name: "bitcoind:x", URL: "http://127.0.0.1:8332", CookieFile: "/c"},
			References: []config.Endpoint{{Kind: config.KindEsplora, Name: "esplora:y", URL: "https://y/api"}},
			Alert:      config.Alert{Threshold: &th, RecoverThreshold: &rec, For: 2},
			Notify:     []string{"tg"},
		}},
		Notifiers: []config.Notifier{{Name: "tg", Kind: config.KindTelegram, ChatID: "1", BotToken: "t"}},
	}
	m, err := Build(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	tg := m.Targets[0]
	if tg.Node.Name() != "bitcoind:x" || tg.Refs[0].Name() != "esplora:y" || tg.Rule.Recover != 1 || m.Notifiers["tg"] == nil {
		t.Fatalf("bad wiring: %+v", m)
	}
}
