// Package esplora reads the chain tip from an Esplora REST API, as served by
// mempool.space, blockstream.info or a self-hosted electrs/esplora.
package esplora

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/al-maisan/node-tracker/internal/httpx"
	"github.com/al-maisan/node-tracker/internal/source"
)

const maxBody = 32 // a decimal height

// Client queries GET {base}/blocks/tip/height.
type Client struct {
	name string
	url  string
	http *http.Client
}

// New returns a client for the API rooted at base (e.g. https://mempool.space/api).
func New(name, base string, hc *http.Client) *Client {
	return &Client{name: name, url: strings.TrimRight(base, "/") + "/blocks/tip/height", http: hc}
}

func (c *Client) Name() string { return c.name }

// Height implements source.Source.
func (c *Client) Height(ctx context.Context) (source.Height, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return source.Height{}, fmt.Errorf("build request: %w", httpx.Redact(err))
	}
	status, body, err := httpx.Do(c.http, req, maxBody)
	if err != nil {
		return source.Height{}, err
	}
	if status != http.StatusOK {
		return source.Height{}, fmt.Errorf("HTTP %d", status)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		return source.Height{}, fmt.Errorf("parse height: %w", err)
	}
	h, err := source.Check(n)
	if err != nil {
		return source.Height{}, err
	}
	return source.Height{Blocks: h, Headers: h}, nil
}
