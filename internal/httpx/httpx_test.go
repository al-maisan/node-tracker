package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/small":
			w.Write([]byte("hello"))
		case "/big":
			w.Write([]byte(strings.Repeat("x", 1000)))
		case "/redirect":
			http.Redirect(w, r, "/small", http.StatusFound)
		case "/slow":
			time.Sleep(200 * time.Millisecond)
		}
	}))
	defer srv.Close()
	c := NewClient()
	get := func(ctx context.Context, path string) (int, []byte, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
		return Do(c, req, 100)
	}

	if st, b, err := get(context.Background(), "/small"); err != nil || st != 200 || string(b) != "hello" {
		t.Errorf("small: %d %q %v", st, b, err)
	}
	if _, _, err := get(context.Background(), "/big"); err == nil {
		t.Error("big: want size error")
	}
	if st, _, err := get(context.Background(), "/redirect"); err != nil || st != http.StatusFound {
		t.Errorf("redirect must not be followed: %d %v", st, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := get(ctx, "/slow"); err == nil {
		t.Error("slow: want timeout")
	}
}

func TestRedactHidesPathAndCredentials(t *testing.T) {
	c := NewClient()
	req, _ := http.NewRequest(http.MethodGet, "http://user:pw@127.0.0.1:1/botSECRET/sendMessage?x=SECRET", nil)
	_, _, err := Do(c, req, 10)
	if err == nil {
		t.Fatal("want connection error")
	}
	for _, s := range []string{"SECRET", "pw", "user"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error leaks %q: %v", s, err)
		}
	}
	if !strings.Contains(err.Error(), "http://127.0.0.1:1") {
		t.Errorf("error should keep the origin: %v", err)
	}
}

func TestOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://mempool.space/api":    "https://mempool.space",
		"http://u:p@10.0.0.1:8332/x?y": "http://10.0.0.1:8332",
		"::bad":                        "<invalid url>",
		"relative/path":                "<invalid url>",
	} {
		if got := Origin(in); got != want {
			t.Errorf("Origin(%q) = %q, want %q", in, got, want)
		}
	}
}
