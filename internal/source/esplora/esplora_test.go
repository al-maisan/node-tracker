package esplora

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/al-maisan/node-tracker/internal/httpx"
)

func TestHeight(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    uint64
		wantErr string
	}{
		{"ok", 200, "945013", 945013, ""},
		{"ok with newline", 200, "945013\n", 945013, ""},
		{"server error", 502, "bad gateway", 0, "HTTP 502"},
		{"not a number", 200, "abc", 0, "parse"},
		{"negative", 200, "-5", 0, "implausible"},
		{"absurd", 200, "123456789012", 0, "implausible"},
		{"oversized", 200, strings.Repeat("1", 100), 0, "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/blocks/tip/height" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			h, err := New("e", srv.URL+"/api/", "signet", httpx.NewClient()).Height(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || h.Blocks != tt.want || h.Headers != tt.want || h.Chain != "signet" {
				t.Fatalf("got %+v, %v", h, err)
			}
		})
	}
}
