package term

import (
	"os"
	"strconv"
	"syscall"
	"testing"
	"unsafe"
)

// NoEcho clears ECHO on a real pseudo-terminal, and restore sets it again.
func TestNoEchoOnPTY(t *testing.T) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no /dev/ptmx:", err)
	}
	defer ptmx.Close()
	var n, unlock uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Skip("unlockpt:", e)
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, ptmx.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Skip("ptsname:", e)
	}
	pts, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no pts:", err)
	}
	defer pts.Close()
	echo := func() bool {
		var tio syscall.Termios
		if err := ioctl(pts.Fd(), getTermios, &tio); err != nil {
			t.Fatal(err)
		}
		return tio.Lflag&syscall.ECHO != 0
	}
	if !IsTerminal(pts.Fd()) || !echo() {
		t.Fatal("pts is not a terminal with echo")
	}
	restore, err := NoEcho(pts.Fd())
	if err != nil || echo() {
		t.Fatalf("NoEcho: %v, echo=%v", err, echo())
	}
	restore()
	if !echo() {
		t.Fatal("echo not restored")
	}
}
