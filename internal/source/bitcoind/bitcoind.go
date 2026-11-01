// Package bitcoind reads the chain height from a Bitcoin Core node over
// JSON-RPC. Only the read-only getblockchaininfo call is used.
package bitcoind

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/al-maisan/node-tracker/internal/httpx"
	"github.com/al-maisan/node-tracker/internal/source"
)

const maxBody = 64 << 10 // getblockchaininfo is ~1-2 KiB

var chainRE = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// Auth supplies RPC credentials. Exactly one way must be set.
type Auth struct {
	CookieFile string // re-read on every call: bitcoind rewrites it on restart
	HideCookie bool   // CookieFile may be a pasted secret: keep it out of errors
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
		Chain   string `json:"chain"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// AuthError is an HTTP 401 or 403 from the node.
type AuthError struct{ Status int }

func (e *AuthError) Error() string { return fmt.Sprintf("rpc auth failed (HTTP %d)", e.Status) }

// RPCError is an error the node returned in the JSON-RPC response.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// ErrWarmup is RPCError.Code while the node is starting (RPC_IN_WARMUP).
const ErrWarmup = -28

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
	// getblockchaininfo is read-only, so the transport may resend it when a
	// kept-alive connection turns out closed: bitcoind drops idle ones after
	// rpcservertimeout (30 s), about the polling interval. The empty key is
	// not sent.
	req.Header["Idempotency-Key"] = nil

	status, body, err := httpx.Do(c.http, req, maxBody)
	if err != nil {
		return source.Height{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return source.Height{}, &AuthError{status}
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
		return source.Height{}, &RPCError{r.Error.Code, r.Error.Message}
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
	chain := r.Result.Chain
	if !chainRE.MatchString(chain) { // from the node, so possibly junk
		chain = ""
	}
	return source.Height{Blocks: blocks, Headers: headers, Chain: chain}, nil
}

func (c *Client) credentials() (user, pass string, err error) {
	if c.auth.CookieFile == "" {
		return c.auth.Username, c.auth.Password, nil
	}
	// The content is never echoed, nor the path if it may be a secret.
	shown := c.auth.CookieFile
	if c.auth.HideCookie {
		shown = "(path not shown: it may be the secret itself)"
	}
	b, err := os.ReadFile(c.auth.CookieFile)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return "", "", fmt.Errorf("read cookie file %s: %w", shown, pe.Err)
		}
		return "", "", fmt.Errorf("read cookie file: %w", err)
	}
	user, pass, ok := strings.Cut(strings.TrimSpace(string(b)), ":")
	if !ok || user == "" || pass == "" {
		return "", "", fmt.Errorf("cookie file %s: malformed", shown)
	}
	return user, pass, nil
}
