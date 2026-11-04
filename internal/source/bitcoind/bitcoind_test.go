package bitcoind

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/al-maisan/node-tracker/internal/fsx"
	"github.com/al-maisan/node-tracker/internal/httpx"
	"github.com/al-maisan/node-tracker/internal/source"
)

func fakeNode(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "u" || pass != "p" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct{ Method string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Method != "getblockchaininfo" {
			t.Errorf("unexpected request: %v %q", err, req.Method)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHeight(t *testing.T) {
	auth := Auth{Username: "u", Password: "p"}
	tests := []struct {
		name    string
		status  int
		body    string
		auth    Auth
		want    source.Height
		wantErr string
	}{
		{"ok", 200, `{"result":{"blocks":945000,"headers":945002},"error":null,"id":"x"}`, auth, source.Height{Blocks: 945000, Headers: 945002}, ""},
		{"chain", 200, `{"result":{"chain":"signet","blocks":1,"headers":2}}`, auth, source.Height{Blocks: 1, Headers: 2, Chain: "signet"}, ""},
		{"junk chain", 200, `{"result":{"chain":"<b>x</b>","blocks":1,"headers":2}}`, auth, source.Height{Blocks: 1, Headers: 2}, ""},
		{"bad auth", 200, ``, Auth{Username: "u", Password: "wrong"}, source.Height{}, "auth failed"},
		{"rpc error on HTTP 500", 500, `{"result":null,"error":{"code":-28,"message":"Loading block index"}}`, auth, source.Height{}, "Loading block index"},
		{"junk on HTTP 503", 503, `<html>`, auth, source.Height{}, "HTTP 503"},
		{"junk on HTTP 200", 200, `nope`, auth, source.Height{}, "decode"},
		{"missing fields", 200, `{"result":{"blocks":1}}`, auth, source.Height{}, "lacks"},
		{"negative height", 200, `{"result":{"blocks":-1,"headers":1}}`, auth, source.Height{}, "implausible"},
		{"absurd height", 200, `{"result":{"blocks":1,"headers":99999999999}}`, auth, source.Height{}, "implausible"},
		{"oversized", 200, strings.Repeat(" ", maxBody+1), auth, source.Height{}, "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeNode(t, tt.status, tt.body)
			got, err := New("n", srv.URL, tt.auth, httpx.NewClient()).Height(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

// Callers tell auth failures and RPC errors apart by type, not by text.
func TestErrorTypes(t *testing.T) {
	var ae *AuthError
	_, err := New("n", fakeNode(t, 200, ``).URL, Auth{Username: "u", Password: "wrong"}, httpx.NewClient()).Height(context.Background())
	if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized || err.Error() != "rpc auth failed (HTTP 401)" {
		t.Errorf("bad auth: %#v", err)
	}
	var re *RPCError
	srv := fakeNode(t, 500, `{"result":null,"error":{"code":-28,"message":"Loading block index"}}`)
	_, err = New("n", srv.URL, Auth{Username: "u", Password: "p"}, httpx.NewClient()).Height(context.Background())
	if !errors.As(err, &re) || re.Code != ErrWarmup || err.Error() != "rpc error -28: Loading block index" {
		t.Errorf("warmup: %#v", err)
	}
}

func TestCookieAuth(t *testing.T) {
	srv := fakeNode(t, 200, `{"result":{"blocks":7,"headers":7}}`)
	dir := t.TempDir()
	cookie := filepath.Join(dir, ".cookie")
	c := New("n", srv.URL, Auth{CookieFile: cookie}, httpx.NewClient())

	if _, err := c.Height(context.Background()); err == nil {
		t.Fatal("missing cookie: want error")
	}
	if err := os.WriteFile(cookie, []byte("no-colon"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Height(context.Background()); err == nil || strings.Contains(err.Error(), "no-colon") {
		t.Fatalf("malformed cookie: want error without content, got %v", err)
	}
	// The cookie is re-read on every call, so a rotated cookie just works.
	if err := os.WriteFile(cookie, []byte("u:p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h, err := c.Height(context.Background()); err != nil || h.Blocks != 7 {
		t.Fatalf("got %+v, %v", h, err)
	}
}

// The cookie's directory belongs to bitcoind's user: a cookie swapped for a
// device or a huge file is an error, not a hang or an unbounded read.
func TestCookieBounded(t *testing.T) {
	srv := fakeNode(t, 200, `{"result":{"blocks":7,"headers":7}}`)
	dir := t.TempDir()
	zero, big := filepath.Join(dir, "zero"), filepath.Join(dir, "big")
	if err := os.Symlink("/dev/zero", zero); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, []byte("u:"+strings.Repeat("p", maxCookie)), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]error{zero: fsx.ErrNotRegular, big: fsx.ErrTooLarge} {
		_, err := New("n", srv.URL, Auth{CookieFile: path}, httpx.NewClient()).Height(context.Background())
		if !errors.Is(err, want) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: err = %v; want %v naming the path", path, err, want)
		}
	}
}

// A cookie_file the config hides (it may be a pasted secret) stays out of
// errors; the error still tells what went wrong.
func TestHiddenCookiePath(t *testing.T) {
	srv := fakeNode(t, 200, `{"result":{"blocks":7,"headers":7}}`)
	const secret = "/Zm9vYmFyYmF6cXV4+abcdefghijklmnop"
	_, err := New("n", srv.URL, Auth{CookieFile: secret, HideCookie: true}, httpx.NewClient()).Height(context.Background())
	if err == nil || strings.Contains(err.Error(), "Zm9v") || !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "path not shown") {
		t.Fatalf("err = %v", err)
	}
	cookie := filepath.Join(t.TempDir(), "c")
	os.WriteFile(cookie, []byte("no-colon"), 0o600)
	_, err = New("n", srv.URL, Auth{CookieFile: cookie, HideCookie: true}, httpx.NewClient()).Height(context.Background())
	if err == nil || strings.Contains(err.Error(), cookie) || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed: err = %v", err)
	}
	// Shown paths keep the path, which helps to fix it.
	_, err = New("n", srv.URL, Auth{CookieFile: cookie + ".x"}, httpx.NewClient()).Height(context.Background())
	if err == nil || !strings.Contains(err.Error(), cookie+".x") || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("shown: err = %v", err)
	}
}

// A kept-alive connection that bitcoind closed while idle (its
// rpcservertimeout) is retried on a new one, not reported as EOF.
func TestStaleConnectionRetried(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	const resp = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s"
	body := `{"result":{"blocks":100,"headers":100,"chain":"main"},"error":null}`
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for i := 0; ; i++ {
					req, err := http.ReadRequest(r)
					if err != nil {
						return
					}
					io.Copy(io.Discard, req.Body)
					if i == 1 { // the second request on a connection: closed unanswered
						return
					}
					fmt.Fprintf(c, resp, len(body), body)
				}
			}()
		}
	}()
	c := New("n", "http://"+ln.Addr().String(), Auth{Username: "u", Password: "p"}, httpx.NewClient())
	for i := range 3 {
		if h, err := c.Height(context.Background()); err != nil || h.Blocks != 100 {
			t.Fatalf("call %d: %+v, %v", i, h, err)
		}
	}
}
