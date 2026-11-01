// Package httpx holds the small HTTP helpers shared by sources and notifiers:
// a hardened client, size-limited bodies and URL redaction for errors.
package httpx

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// NewClient returns a client that honours HTTP(S)_PROXY / NO_PROXY
// (including socks5:// for Tor) and never follows redirects, so a reference
// can't bounce requests elsewhere. Timeouts come from the request context.
func NewClient() *http.Client { return newClient(http.ProxyFromEnvironment) }

// NewProxyClient is NewClient, but sends every request through proxy
// (socks5, socks5h, http or https) and ignores the environment.
func NewProxyClient(proxy *url.URL) *http.Client { return newClient(http.ProxyURL(proxy)) }

func newClient(proxy func(*http.Request) (*url.URL, error)) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = proxy
	return &http.Client{
		Transport: t,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Do sends req and returns the status and at most limit bytes of body.
// A larger body is an error. Errors never contain the request URL's path,
// query or credentials (a Telegram bot token lives in the path).
func Do(c *http.Client, req *http.Request, limit int64) (status int, body []byte, err error) {
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, Redact(err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read body: %w", Redact(err))
	}
	if int64(len(body)) > limit {
		return resp.StatusCode, nil, fmt.Errorf("response body larger than %d bytes", limit)
	}
	return resp.StatusCode, body, nil
}

// Redact strips everything but scheme and host from a *url.Error's URL.
func Redact(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	return &url.Error{Op: ue.Op, URL: Origin(ue.URL), Err: ue.Err}
}

// Origin returns scheme://host of raw, or "<invalid url>".
func Origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid url>"
	}
	return u.Scheme + "://" + u.Host
}
