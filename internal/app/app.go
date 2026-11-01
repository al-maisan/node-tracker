// Package app wires a validated config into a monitor: it maps each kind to
// its implementation.
package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/al-maisan/node-tracker/internal/config"
	"github.com/al-maisan/node-tracker/internal/health"
	"github.com/al-maisan/node-tracker/internal/httpx"
	"github.com/al-maisan/node-tracker/internal/monitor"
	"github.com/al-maisan/node-tracker/internal/notify"
	"github.com/al-maisan/node-tracker/internal/notify/telegram"
	"github.com/al-maisan/node-tracker/internal/source"
	"github.com/al-maisan/node-tracker/internal/source/bitcoind"
	"github.com/al-maisan/node-tracker/internal/source/esplora"
)

// OutboxLimit caps undelivered alerts per notifier.
const OutboxLimit = 100

var sources = map[string]func(config.Endpoint, *http.Client) source.Source{
	config.KindBitcoind: func(e config.Endpoint, hc *http.Client) source.Source {
		return bitcoind.New(e.Name, e.URL, bitcoind.Auth{
			CookieFile: e.CookieFile, Username: e.Username, Password: string(e.Password),
		}, hc)
	},
	config.KindEsplora: func(e config.Endpoint, hc *http.Client) source.Source {
		return esplora.New(e.Name, e.URL, hc)
	},
}

var notifiers = map[string]func(config.Notifier, *http.Client) notify.Notifier{
	config.KindTelegram: func(n config.Notifier, hc *http.Client) notify.Notifier {
		return telegram.New(n.Name, strings.TrimRight(n.APIURL, "/"), string(n.BotToken), n.ChatID, hc)
	},
}

// Build returns a monitor for c, which must come from config.Load.
func Build(c *config.Config, log *slog.Logger) (*monitor.Monitor, error) {
	hc := httpx.NewClient()
	m := &monitor.Monitor{
		Notifiers:   map[string]notify.Notifier{},
		Timeout:     c.Timeout,
		Retry:       notify.DefaultRetry,
		OutboxLimit: OutboxLimit,
		Log:         log,
	}
	for _, n := range c.Notifiers {
		mk, ok := notifiers[n.Kind]
		if !ok {
			return nil, fmt.Errorf("notifier %s: kind %q not implemented", n.Name, n.Kind)
		}
		m.Notifiers[n.Name] = mk(n, hc)
	}
	for _, t := range c.Targets {
		node, err := newSource(t.Node, hc)
		if err != nil {
			return nil, fmt.Errorf("target %s: %w", t.Name, err)
		}
		mt := monitor.Target{
			Name:   t.Name,
			Node:   node,
			Rule:   health.Rule{Threshold: *t.Alert.Threshold, Recover: *t.Alert.RecoverThreshold, For: t.Alert.For},
			Notify: t.Notify,
		}
		for _, r := range t.References {
			s, err := newSource(r, hc)
			if err != nil {
				return nil, fmt.Errorf("target %s: %w", t.Name, err)
			}
			mt.Refs = append(mt.Refs, s)
		}
		m.Targets = append(m.Targets, mt)
	}
	return m, nil
}

func newSource(e config.Endpoint, hc *http.Client) (source.Source, error) {
	mk, ok := sources[e.Kind]
	if !ok {
		return nil, fmt.Errorf("source kind %q not implemented", e.Kind)
	}
	return mk(e, hc), nil
}
