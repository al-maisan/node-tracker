package torctl

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTor is a control port. methods is what PROTOCOLINFO offers; it
// records the commands it gets and answers SAFECOOKIE like Tor does.
type fakeTor struct {
	methods  string
	cookie   []byte // of the cookie file; nil: no COOKIEFILE
	wrongKey bool   // answer AUTHCHALLENGE as if it had another cookie
	reply    map[string]string

	mu   sync.Mutex
	cmds []string
}

func (f *fakeTor) serve(t *testing.T, ln net.Listener) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "control.authcookie")
	if f.cookie != nil {
		if err := os.WriteFile(file, f.cookie, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.session(c, file)
		}
	}()
}

func (f *fakeTor) session(c net.Conn, file string) {
	defer c.Close()
	r := bufio.NewReader(c)
	var nonce []byte
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		f.mu.Lock()
		f.cmds = append(f.cmds, cmd)
		f.mu.Unlock()
		verb, arg, _ := strings.Cut(cmd, " ")
		if r, ok := f.reply[verb]; ok {
			fmt.Fprint(c, r)
			continue
		}
		switch verb {
		case "PROTOCOLINFO":
			cf := ""
			if f.cookie != nil {
				cf = " COOKIEFILE=" + strconv.Quote(file)
			}
			fmt.Fprintf(c, "250-PROTOCOLINFO 1\r\n250-AUTH METHODS=%s%s\r\n250-VERSION Tor=\"0.4.9.15\"\r\n250 OK\r\n", f.methods, cf)
		case "AUTHCHALLENGE":
			_, n, _ := strings.Cut(arg, " ")
			nonce, _ = hex.DecodeString(n)
			cookie := f.cookie
			if f.wrongKey {
				cookie = make([]byte, cookieLen)
			}
			sn := make([]byte, 32)
			sn[0] = 7
			msg := append(append(append([]byte{}, cookie...), nonce...), sn...)
			nonce = msg
			fmt.Fprintf(c, "250 AUTHCHALLENGE SERVERHASH=%X SERVERNONCE=%X\r\n", mac(serverKey, msg), sn)
		case "AUTHENTICATE":
			switch {
			case arg == "" && strings.Contains(f.methods, "NULL"):
			case arg != "" && strings.EqualFold(arg, hex.EncodeToString(mac(clientKey, nonce))):
			default:
				fmt.Fprint(c, "515 Authentication failed\r\n")
				return
			}
			fmt.Fprint(c, "250 OK\r\n")
		case "QUIT":
			fmt.Fprint(c, "250 closing connection\r\n")
			return
		default:
			fmt.Fprint(c, "250 OK\r\n")
		}
	}
}

func (f *fakeTor) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.cmds...)
}

// listen serves f on a unix socket (short path: sun_path is ~108 bytes).
func listen(t *testing.T, f *fakeTor) Controller {
	t.Helper()
	dir, err := os.MkdirTemp("", "torctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "c"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f.serve(t, ln)
	c, err := New("unix:" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func cookie() []byte {
	c := make([]byte, cookieLen)
	for i := range c {
		c[i] = byte(i + 1)
	}
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestNewnymSafeCookie(t *testing.T) {
	f := &fakeTor{methods: "COOKIE,SAFECOOKIE", cookie: cookie()}
	c := listen(t, f)
	if err := c.Newnym(ctx(t)); err != nil {
		t.Fatal(err)
	}
	got := f.got()
	if len(got) != 5 || got[0] != "PROTOCOLINFO 1" || !strings.HasPrefix(got[1], "AUTHCHALLENGE SAFECOOKIE ") ||
		!strings.HasPrefix(got[2], "AUTHENTICATE ") || got[3] != "SIGNAL NEWNYM" || got[4] != "QUIT" {
		t.Fatalf("commands = %q", got)
	}
	for _, cmd := range got {
		if strings.Contains(strings.ToLower(cmd), hex.EncodeToString(cookie())) {
			t.Fatalf("cookie sent in clear: %q", cmd)
		}
	}
}

func TestCheckSendsNoSignal(t *testing.T) {
	f := &fakeTor{methods: "NULL"}
	c := listen(t, f)
	if err := c.Check(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if got := f.got(); strings.Join(got, "|") != "PROTOCOLINFO 1|AUTHENTICATE|QUIT" {
		t.Fatalf("commands = %q", got)
	}
}

func TestNewnymOverTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	f := &fakeTor{methods: "NULL"}
	f.serve(t, ln)
	c, err := New(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Newnym(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name   string
		f      *fakeTor
		want   string
		noAuth bool // authentication fails, so no signal may be sent
	}{
		{"only password", &fakeTor{methods: "HASHEDPASSWORD"}, "no supported authentication method", true},
		{"cookie without file", &fakeTor{methods: "SAFECOOKIE"}, "no supported authentication method", true},
		{"plain COOKIE only", &fakeTor{methods: "COOKIE", cookie: cookie()}, "no supported authentication method", true},
		{"server without the cookie", &fakeTor{methods: "SAFECOOKIE", cookie: cookie(), wrongKey: true}, "does not know the cookie", true},
		{"short cookie", &fakeTor{methods: "SAFECOOKIE", cookie: []byte("short")}, "5 bytes, want 32", true},
		{"long cookie", &fakeTor{methods: "SAFECOOKIE", cookie: make([]byte, 33)}, "larger than 32 bytes", true},
		{"signal refused", &fakeTor{methods: "NULL", reply: map[string]string{"SIGNAL": "552 Unrecognized signal\x07\r\n"}}, "SIGNAL: Tor says 552 Unrecognized signal", false},
		{"malformed", &fakeTor{methods: "NULL", reply: map[string]string{"SIGNAL": "25\r\n"}}, "malformed reply", false},
		{"data reply", &fakeTor{methods: "NULL", reply: map[string]string{"SIGNAL": "250+x\r\n"}}, "unexpected reply", false},
		{"long line", &fakeTor{methods: "NULL", reply: map[string]string{"SIGNAL": "250 " + strings.Repeat("x", maxLine) + "\r\n"}}, "longer than", false},
		{"endless reply", &fakeTor{methods: "NULL", reply: map[string]string{"SIGNAL": strings.Repeat("250-x\r\n", maxLines+1)}}, "longer than 64 lines", false},
		{"bad challenge", &fakeTor{methods: "SAFECOOKIE", cookie: cookie(), reply: map[string]string{"AUTHCHALLENGE": "250 AUTHCHALLENGE SERVERHASH=zz\r\n"}}, "malformed reply", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := listen(t, tt.f).Newnym(ctx(t))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if strings.ContainsRune(err.Error(), '\x07') {
				t.Fatalf("control character in %q", err)
			}
			if tt.noAuth && slices.Contains(tt.f.got(), "SIGNAL NEWNYM") {
				t.Fatalf("signal sent without authentication: %q", tt.f.got())
			}
		})
	}
}

func TestHangingTorTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(2 * time.Second) // never answers
		}
	}()
	c, _ := New(ln.Addr().String())
	cctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = c.Newnym(cctx)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("deadline not applied")
	}
}

func TestNew(t *testing.T) {
	ok := map[string]string{
		"unix:/run/tor/control":  "unix:/run/tor/control",
		"unix:/run/tor//control": "unix:/run/tor/control",
		"127.0.0.1:9051":         "127.0.0.1:9051",
		"[::1]:9051":             "[::1]:9051",
		"localhost:9051":         "localhost:9051",
	}
	for in, want := range ok {
		c, err := New(in)
		if err != nil || c.String() != want {
			t.Errorf("New(%q) = %v, %v; want %s", in, c, err, want)
		}
	}
	for _, in := range []string{"", "unix:", "unix:run/tor/control", "9051", "127.0.0.1", "127.0.0.1:0",
		"127.0.0.1:99999", "10.0.0.5:9051", "tor.example:9051", "0.0.0.0:9051", "/run/tor/control"} {
		if _, err := New(in); err == nil {
			t.Errorf("New(%q) accepted", in)
		}
	}
	if err := (Controller{}).Newnym(context.Background()); err == nil {
		t.Error("zero Controller accepted")
	}
}
