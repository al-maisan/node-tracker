//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package term

import (
	"os"
	"testing"
)

// Tests run without a terminal; a pipe is none.
func TestNotATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if IsTerminal(r.Fd()) {
		t.Error("a pipe is a terminal")
	}
	if _, err := NoEcho(r.Fd()); err == nil {
		t.Error("NoEcho on a pipe succeeded")
	}
}
