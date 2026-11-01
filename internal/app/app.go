// Package app wires a validated config into a monitor: it maps each kind to
// its implementation.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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
			CookieFile: e.CookieFile, HideCookie: e.HideCookie, Username: e.Username, Password: string(e.Password),
		}, hc)
	},
	config.KindEsplora: func(e config.Endpoint, hc *http.Client) source.Source {
		return esplora.New(e.Name, e.URL, config.ReferenceChain(e), hc)
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
		Tracker:     c.Name,
		Log:         log,
	}
	for _, n := range c.Notifiers {
		mk, ok := notifiers[n.Kind]
		if !ok {
			return nil, fmt.Errorf("notifier %s: kind %q not implemented", n.Name, n.Kind)
		}
		nc, err := Client(n.Proxy, hc)
		if err != nil {
			return nil, fmt.Errorf("notifier %s: %w", n.Name, err)
		}
		m.Notifiers[n.Name] = mk(n, nc)
	}
	for _, t := range c.Targets {
		node, err := NewSource(t.Node, hc)
		if err != nil {
			return nil, fmt.Errorf("target %s: %w", t.Name, err)
		}
		mt := monitor.Target{
			Name: t.Name,
			Node: node,
			Rule: health.Rule{Threshold: *t.Alert.Threshold, Recover: *t.Alert.RecoverThreshold,
				For: t.Alert.For, DownFor: t.Alert.DownFor, Outlier: *t.ReferenceOutlier},
			Notify: t.Notify,
		}
		for _, r := range t.References {
			s, err := NewSource(r, hc)
			if err != nil {
				return nil, fmt.Errorf("target %s: %w", t.Name, err)
			}
			mt.Refs, mt.Local = append(mt.Refs, s), append(mt.Local, r.NodeHost)
		}
		m.Targets = append(m.Targets, mt)
	}
	return m, nil
}

// NewSource is the source of e, which must come from config.Load or
// config.CheckEndpoint. It uses hc, or its own client if e has a proxy.
func NewSource(e config.Endpoint, hc *http.Client) (source.Source, error) {
	mk, ok := sources[e.Kind]
	if !ok {
		return nil, fmt.Errorf("source kind %q not implemented", e.Kind)
	}
	hc, err := Client(e.Proxy, hc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.Name, err)
	}
	s := mk(e, hc)
	if e.Timeout > 0 {
		s = source.WithTimeout(s, e.Timeout)
	}
	return s, nil
}

// Client is hc, or a client of its own for proxy, which must be validated
// (config.Load or config.CheckProxy).
func Client(proxy string, hc *http.Client) (*http.Client, error) {
	if proxy == "" {
		return hc, nil
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return nil, errors.New("invalid proxy")
	}
	return httpx.NewProxyClient(u), nil
}

// Proxy describes the proxy e's requests go through: e.Proxy (marked if it
// is the .onion default), the one from the environment (scheme and host
// only, it may hold credentials), or "none".
func Proxy(e config.Endpoint) string {
	switch {
	case e.DefaultProxy:
		return e.Proxy + " (default)"
	case e.Proxy != "":
		return e.Proxy
	}
	req, err := http.NewRequest(http.MethodGet, e.URL, nil)
	if err != nil {
		return "none"
	}
	u, err := http.ProxyFromEnvironment(req)
	switch {
	case err != nil:
		return "invalid (environment)"
	case u == nil:
		return "none"
	}
	return httpx.Origin(u.String()) + " (environment)"
}
