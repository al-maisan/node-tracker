//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

// Package term turns off the echo of a terminal, so a secret can be typed
// without showing it. It uses the termios ioctls of package syscall, not
// golang.org/x/term, to keep the dependencies at the standard library.
package term

import (
	"syscall"
	"unsafe"
)

func ioctl(fd, req uintptr, t *syscall.Termios) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t))); errno != 0 {
		return errno
	}
	return nil
}

// IsTerminal reports whether fd is a terminal.
func IsTerminal(fd uintptr) bool {
	var t syscall.Termios
	return ioctl(fd, getTermios, &t) == nil
}

// NoEcho turns off the echo of the terminal fd until restore is called.
func NoEcho(fd uintptr) (restore func(), err error) {
	var old syscall.Termios
	if err := ioctl(fd, getTermios, &old); err != nil {
		return nil, err
	}
	t := old
	t.Lflag &^= syscall.ECHO
	if err := ioctl(fd, setTermios, &t); err != nil {
		return nil, err
	}
	return func() { ioctl(fd, setTermios, &old) }, nil
}
