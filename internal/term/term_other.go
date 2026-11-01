//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

// Package term turns off the echo of a terminal, so a secret can be typed
// without showing it. This OS is not supported: setup needs
// -non-interactive there.
package term

import "errors"

// IsTerminal reports whether fd is a terminal; here: never.
func IsTerminal(fd uintptr) bool { return false }

// NoEcho is not supported on this OS.
func NoEcho(fd uintptr) (restore func(), err error) {
	return nil, errors.New("turning off the terminal echo is not supported on this OS")
}
