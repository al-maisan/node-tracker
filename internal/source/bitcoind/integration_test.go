//go:build integration

package bitcoind

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/al-maisan/node-tracker/internal/httpx"
)

// TestIntegrationRegtest runs against a throwaway bitcoind -regtest.
func TestIntegrationRegtest(t *testing.T) {
	bin, err := exec.LookPath("bitcoind")
	if err != nil {
		t.Skip("bitcoind not in PATH")
	}
	dir := t.TempDir()
	port := freePort(t)
	cmd := exec.Command(bin, "-regtest", "-datadir="+dir, "-server", "-listen=0", "-dnsseed=0",
		"-disablewallet", "-printtoconsole=0", fmt.Sprintf("-rpcport=%d", port), "-rpcbind=127.0.0.1", "-rpcallowip=127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Signal(os.Interrupt); cmd.Wait() })

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	c := New("bitcoind:regtest", url, Auth{CookieFile: filepath.Join(dir, "regtest", ".cookie")}, httpx.NewClient())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var h0 uint64
	for {
		h, err := c.Height(ctx)
		if err == nil {
			h0 = h.Blocks
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("bitcoind never became ready: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	user, pass, err := c.credentials()
	if err != nil {
		t.Fatal(err)
	}
	var info struct{ Descriptor string }
	call(t, url, user, pass, "getdescriptorinfo", []any{"raw(51)"}, &info)
	call(t, url, user, pass, "generatetodescriptor", []any{5, info.Descriptor}, nil)

	h, err := c.Height(ctx)
	if err != nil || h.Blocks != h0+5 || h.Headers != h0+5 {
		t.Fatalf("after mining 5: %+v %v (start %d)", h, err, h0)
	}
}

func call(t *testing.T, url, user, pass, method string, params []any, out any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "1.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.SetBasicAuth(user, pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage
		Error  *struct{ Message string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.Error != nil {
		t.Fatalf("%s: %v %+v", method, err, r.Error)
	}
	if out != nil {
		json.Unmarshal(r.Result, out)
	}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
