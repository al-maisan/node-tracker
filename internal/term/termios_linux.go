package term

import "syscall"

const getTermios, setTermios = syscall.TCGETS, syscall.TCSETS
