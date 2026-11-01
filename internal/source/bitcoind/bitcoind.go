// Package bitcoind reads the chain height from a Bitcoin Core node over
// JSON-RPC. Only the read-only getblockchaininfo call is used.
package bitcoind

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/al-maisan/node-tracker/internal/httpx"
	"github.com/al-maisan/node-tracker/internal/source"
)

const maxBody = 64 << 10 // getblockchaininfo is ~1-2 KiB

// Auth supplies RPC credentials. Exactly one way must be set.
type Auth struct {
	CookieFile string // re-read on every call: bitcoind rewrites it on restart
	Username   string
	Password   string
}

// Client is a bitcoind JSON-RPC client.
type Client struct {
	name string
	url  string
	auth Auth
	http *http.Client
}

// New returns a client for the node at url (e.g. http://127.0.0.1:8332).
func New(name, url string, auth Auth, hc *http.Client) *Client {
	return &Client{name: name, url: url, auth: auth, http: hc}
}

func (c *Client) Name() string { return c.name }

type rpcResponse struct {
	Result *struct {
		Blocks  *int64 `json:"blocks"`
		Headers *int64 `json:"headers"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var reqBody = []byte(`{"jsonrpc":"1.0","id":"node-tracker","method":"getblockchaininfo","params":[]}`)

// Height implements source.Source.
func (c *Client) Height(ctx context.Context) (source.Height, error) {
	user, pass, err := c.credentials()
	if err != nil {
		return source.Height{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(reqBody))
	if err != nil {
		return source.Height{}, fmt.Errorf("build request: %w", httpx.Redact(err))
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/json")

	status, body, err := httpx.Do(c.http, req, maxBody)
	if err != nil {
		return source.Height{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return source.Height{}, fmt.Errorf("rpc auth failed (HTTP %d)", status)
	}
	var r rpcResponse
	if err := json.Unmarshal(body, &r); err != nil {
		// Older bitcoind returns HTTP 500 with a JSON error body, so the
		// status alone is not conclusive: only report it if the body is junk.
		if status != http.StatusOK {
			return source.Height{}, fmt.Errorf("rpc: HTTP %d", status)
		}
		return source.Height{}, fmt.Errorf("rpc: decode response: %w", err)
	}
	if r.Error != nil {
		return source.Height{}, fmt.Errorf("rpc error %d: %s", r.Error.Code, r.Error.Message)
	}
	if r.Result == nil || r.Result.Blocks == nil || r.Result.Headers == nil {
		return source.Height{}, errors.New("rpc: response lacks blocks/headers")
	}
	blocks, err := source.Check(*r.Result.Blocks)
	if err != nil {
		return source.Height{}, err
	}
	headers, err := source.Check(*r.Result.Headers)
	if err != nil {
		return source.Height{}, err
	}
	return source.Height{Blocks: blocks, Headers: headers}, nil
}

func (c *Client) credentials() (user, pass string, err error) {
	if c.auth.CookieFile == "" {
		return c.auth.Username, c.auth.Password, nil
	}
	b, err := os.ReadFile(c.auth.CookieFile)
	if err != nil {
		// The path is not secret; the content is never echoed.
		return "", "", fmt.Errorf("read cookie file: %w", err)
	}
	user, pass, ok := strings.Cut(strings.TrimSpace(string(b)), ":")
	if !ok || user == "" || pass == "" {
		return "", "", fmt.Errorf("cookie file %s: malformed", c.auth.CookieFile)
	}
	return user, pass, nil
}
