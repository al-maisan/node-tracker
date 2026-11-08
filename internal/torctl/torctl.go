// Package torctl speaks just enough of Tor's control protocol to tell the
// local Tor client to drop its circuits and its cached onion service
// descriptors (SIGNAL NEWNYM). After the node's Tor restarts, its onion
// service has new introduction points; a client that keeps the old
// descriptor can fail to reach it for hours.
package torctl

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/al-maisan/node-tracker/internal/fsx"
)

const (
	cookieLen  = 32
	maxLine    = 4096 // longest reply line accepted
	maxLines   = 64   // most lines in one reply
	defTimeout = 10 * time.Second

	serverKey = "Tor safe cookie authentication server-to-controller hash"
	clientKey = "Tor safe cookie authentication controller-to-server hash"
)

// ErrAuth means Tor offers no authentication method this package supports.
var ErrAuth = errors.New("no supported authentication method")

// Controller is a Tor control port: "unix:/run/tor/control" or a loopback
// "host:port".
type Controller struct {
	network, addr string
}

// New parses addr: "unix:" and an absolute path, or a loopback IP or
// localhost with a port. The control port gives full control over Tor, so
// it must not cross a network.
func New(addr string) (Controller, error) {
	if p, ok := strings.CutPrefix(addr, "unix:"); ok {
		if !filepath.IsAbs(p) {
			return Controller{}, errors.New("unix: needs an absolute path, e.g. unix:/run/tor/control")
		}
		return Controller{"unix", filepath.Clean(p)}, nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return Controller{}, errors.New("must be unix:/path or host:port, e.g. 127.0.0.1:9051")
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return Controller{}, errors.New("invalid port")
	}
	if ip, err := netip.ParseAddr(host); !strings.EqualFold(host, "localhost") && (err != nil || !ip.IsLoopback()) {
		return Controller{}, errors.New("host must be loopback (127.0.0.1, ::1 or localhost): the control port controls Tor")
	}
	return Controller{"tcp", addr}, nil
}

// Socket is the path of a unix control socket.
func (c Controller) Socket() (string, bool) { return c.addr, c.network == "unix" }

// String is the address as configured.
func (c Controller) String() string {
	if c.network == "unix" {
		return "unix:" + c.addr
	}
	return c.addr
}

// CookieFile asks Tor which cookie file it authenticates with, without
// authenticating; "" if Tor allows NULL authentication.
func (c Controller) CookieFile(ctx context.Context) (string, error) {
	var file string
	err := c.session(ctx, func(s *session) error {
		info, err := s.protocolInfo()
		if err == nil && !slices.Contains(info.methods, "NULL") {
			file = info.cookieFile
		}
		return err
	})
	return file, err
}

// Check connects and authenticates, and changes nothing.
func (c Controller) Check(ctx context.Context) error { return c.do(ctx, "") }

// Newnym sends SIGNAL NEWNYM: new circuits for new streams, and the cached
// onion service descriptors are dropped, so the next connection fetches
// fresh ones. Tor may delay it by up to 10 seconds (rate limit).
func (c Controller) Newnym(ctx context.Context) error { return c.do(ctx, "SIGNAL NEWNYM") }

func (c Controller) do(ctx context.Context, cmd string) error {
	return c.session(ctx, func(s *session) error {
		if err := s.auth(); err != nil {
			return err
		}
		if cmd != "" {
			_, err := s.call(cmd)
			return err
		}
		return nil
	})
}

// session connects, runs f and says QUIT if f succeeded.
func (c Controller) session(ctx context.Context, f func(*session) error) error {
	if c.network == "" {
		return errors.New("no control address")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, c.network, c.addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	dl, ok := ctx.Deadline()
	if !ok {
		dl = time.Now().Add(defTimeout)
	}
	if err := conn.SetDeadline(dl); err != nil {
		return err
	}
	s := &session{w: conn, r: bufio.NewReaderSize(conn, maxLine)}
	if err := f(s); err != nil {
		return err
	}
	s.call("QUIT") // best effort; the close ends the session anyway
	return nil
}

type session struct {
	w net.Conn
	r *bufio.Reader
}

// call sends one command and returns the text of its reply lines. Any
// status but 250 is an error.
func (s *session) call(cmd string) ([]string, error) {
	if _, err := s.w.Write([]byte(cmd + "\r\n")); err != nil {
		return nil, err
	}
	verb, _, _ := strings.Cut(cmd, " ")
	var lines []string
	for range maxLines {
		line, more, err := s.r.ReadLine()
		if err != nil {
			return nil, err
		}
		if more {
			return nil, fmt.Errorf("%s: reply line longer than %d bytes", verb, maxLine)
		}
		l := string(line)
		if len(l) < 4 {
			return nil, fmt.Errorf("%s: malformed reply", verb)
		}
		code, sep, text := l[:3], l[3], l[4:]
		if code != "250" {
			return nil, fmt.Errorf("%s: Tor says %s %s", verb, code, printable(text))
		}
		switch sep {
		case ' ':
			return append(lines, text), nil
		case '-':
			lines = append(lines, text)
		default: // '+' data replies are not used by the commands sent here
			return nil, fmt.Errorf("%s: unexpected reply", verb)
		}
	}
	return nil, fmt.Errorf("%s: reply longer than %d lines", verb, maxLines)
}

// auth authenticates with NULL if Tor allows it, else SAFECOOKIE, which
// never sends the cookie and checks that the other end knows it too.
// COOKIE and HASHEDPASSWORD alone are not supported.
func (s *session) auth() error {
	info, err := s.protocolInfo()
	if err != nil {
		return err
	}
	switch {
	case slices.Contains(info.methods, "NULL"):
		_, err := s.call("AUTHENTICATE")
		return err
	case slices.Contains(info.methods, "SAFECOOKIE") && info.cookieFile != "":
		return s.safeCookie(info.cookieFile)
	}
	return fmt.Errorf("%w: Tor offers %q; enable CookieAuthentication 1 in torrc", ErrAuth, strings.Join(info.methods, ","))
}

type protocolInfo struct {
	methods    []string
	cookieFile string
}

func (s *session) protocolInfo() (protocolInfo, error) {
	var info protocolInfo
	lines, err := s.call("PROTOCOLINFO 1")
	if err != nil {
		return info, err
	}
	for _, l := range lines {
		rest, ok := strings.CutPrefix(l, "AUTH METHODS=")
		if !ok {
			continue
		}
		m, file, _ := strings.Cut(rest, " ")
		info.methods = strings.Split(m, ",")
		if q, ok := strings.CutPrefix(file, "COOKIEFILE="); ok {
			if info.cookieFile, err = strconv.Unquote(q); err != nil {
				return info, errors.New("PROTOCOLINFO: malformed COOKIEFILE")
			}
		}
	}
	return info, nil
}

func (s *session) safeCookie(file string) error {
	cookie, err := fsx.ReadSmall(file, cookieLen)
	if err != nil {
		return fmt.Errorf("read cookie: %w", err)
	}
	if len(cookie) != cookieLen {
		return fmt.Errorf("read cookie: %d bytes, want %d", len(cookie), cookieLen)
	}
	nonce := make([]byte, 32)
	rand.Read(nonce)
	lines, err := s.call("AUTHCHALLENGE SAFECOOKIE " + hex.EncodeToString(nonce))
	if err != nil {
		return err
	}
	var serverHash, serverNonce []byte
	for _, f := range strings.Fields(lines[len(lines)-1]) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "SERVERHASH":
			serverHash, _ = hex.DecodeString(v)
		case "SERVERNONCE":
			serverNonce, _ = hex.DecodeString(v)
		}
	}
	if len(serverHash) != sha256.Size || len(serverNonce) != 32 {
		return errors.New("AUTHCHALLENGE: malformed reply")
	}
	msg := append(append(append([]byte{}, cookie...), nonce...), serverNonce...)
	if !hmac.Equal(serverHash, mac(serverKey, msg)) {
		return errors.New("AUTHCHALLENGE: Tor does not know the cookie; is this the right control port?")
	}
	_, err = s.call("AUTHENTICATE " + hex.EncodeToString(mac(clientKey, msg)))
	return err
}

func mac(key string, msg []byte) []byte {
	h := hmac.New(sha256.New, []byte(key))
	h.Write(msg)
	return h.Sum(nil)
}

// printable clips text from the control port and drops control characters.
func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
