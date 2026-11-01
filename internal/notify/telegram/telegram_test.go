package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/al-maisan/node-tracker/internal/httpx"
	"github.com/al-maisan/node-tracker/internal/notify"
)

const token = "123456:SECRET-token"

func TestNotify(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantErr       bool
		wantPermanent bool
		wantAfter     time.Duration
	}{
		{"ok", 200, `{"ok":true}`, false, false, 0},
		{"rate limited", 429, `{"ok":false,"description":"Too Many Requests","parameters":{"retry_after":7}}`, true, false, 7 * time.Second},
		{"server error", 502, `bad gateway`, true, false, 0},
		{"bad token", 401, `{"ok":false,"description":"Unauthorized"}`, true, true, 0},
		{"ok false on 200", 200, `{"ok":false}`, true, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bot"+token+"/sendMessage" {
					t.Errorf("path = %s", r.URL.Path)
				}
				json.NewDecoder(r.Body).Decode(&got)
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			m := notify.Message{Subject: "orendil <lagging>", Body: "a & b"}
			err := New("tg", srv.URL, token, "-100", httpx.NewClient()).Send(context.Background(), m)
			if (err != nil) != tt.wantErr || notify.IsPermanent(err) != tt.wantPermanent {
				t.Fatalf("err = %v (permanent=%v)", err, notify.IsPermanent(err))
			}
			if ra := (*notify.RetryAfter)(nil); errors.As(err, &ra) != (tt.wantAfter != 0) || (ra != nil && ra.After != tt.wantAfter) {
				t.Fatalf("retry-after: %v", err)
			}
			if got["chat_id"] != "-100" || got["parse_mode"] != "HTML" || got["text"] != "<b>orendil &lt;lagging&gt;</b>\n\na &amp; b" {
				t.Fatalf("payload = %v", got)
			}
		})
	}
}

func TestErrorsNeverLeakToken(t *testing.T) {
	bot := New("tg", "http://127.0.0.1:1", token, "-100", httpx.NewClient())
	err := bot.Send(context.Background(), notify.Message{})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestLongMessageIsTruncated(t *testing.T) {
	var text string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		text = p["text"].(string)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	// Emoji take 2 UTF-16 units each, which is how Telegram counts.
	m := notify.Message{Subject: "🔴 s", Body: strings.Repeat("🟢x", 3000)}
	if err := New("tg", srv.URL, token, "1", httpx.NewClient()).Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	plain := strings.NewReplacer("<b>", "", "</b>", "").Replace(text)
	if n := utf16Len(plain); n > maxText || n < maxText-1 || !strings.HasSuffix(text, "…") {
		t.Fatalf("utf16 len = %d", n)
	}
}
