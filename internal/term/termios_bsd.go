//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package term

import "syscall"

const getTermios, setTermios = syscall.TIOCGETA, syscall.TIOCSETA
