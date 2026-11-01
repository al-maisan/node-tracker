package bitcoind

import (
	"regexp"
	"strings"
	"testing"
)

// The vector is from Bitcoin Core's test/functional/rpc_users.py, and was
// checked with Python's hmac module.
func TestRPCAuth(t *testing.T) {
	const want = "rt:93648e835a54c573682c2eb19f882535$7681e9c5b74bdd85e78166031d2058e1069b3ed7ed967c93fc63abba06f31144"
	if got := RPCAuth("rt", "cA773lm788buwYe4g4WT+05pKyNruVKjQ25x3n0DQcM=", "93648e835a54c573682c2eb19f882535"); got != want {
		t.Fatalf("RPCAuth = %s, want %s", got, want)
	}
	pw, line := NewRPCAuth("nodetracker")
	m := regexp.MustCompile(`^nodetracker:([0-9a-f]{32})\$[0-9a-f]{64}$`).FindStringSubmatch(line)
	if len(pw) != 43 || strings.ContainsAny(pw, "+/=") || m == nil || RPCAuth("nodetracker", pw, m[1]) != line {
		t.Fatalf("NewRPCAuth = %q, %q", pw, line)
	}
	if pw2, line2 := NewRPCAuth("nodetracker"); pw2 == pw || line2 == line {
		t.Fatal("NewRPCAuth is not random")
	}
}
