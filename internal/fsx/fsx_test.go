package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadSmall(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cookie")
	if err := os.WriteFile(file, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "zero")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		max  int64
		want error // nil: reads "1234"
	}{
		{"cookie", 4, nil},
		{"link", 4, nil}, // a symlink to a regular file is fine
		{"cookie", 3, ErrTooLarge},
		{"zero", 4 << 10, ErrNotRegular}, // reports size 0, never ends
		{"fifo", 4 << 10, ErrNotRegular}, // must not block in open
		{".", 4 << 10, ErrNotRegular},
		{"missing", 4 << 10, fs.ErrNotExist},
	} {
		b, err := ReadSmall(filepath.Join(dir, c.name), c.max)
		switch {
		case c.want == nil && (err != nil || string(b) != "1234"):
			t.Errorf("%s: %q, %v; want \"1234\"", c.name, b, err)
		case c.want != nil && !errors.Is(err, c.want):
			t.Errorf("%s (max %d): %v; want %v", c.name, c.max, err, c.want)
		}
	}
}

func TestWriteFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	if err := WriteFile(p, []byte("one"), 0o600, -1, -1, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("two"), 0o640, -1, os.Getgid(), true); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{p: "two", p + ".bak": "one"} {
		if b, err := os.ReadFile(path); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", path, b, err, want)
		}
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", fi.Mode())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".secret.*")); len(m) != 0 {
		t.Errorf("temporary files left: %v", m)
	}
	os.Symlink(p, p+".link")
	if _, err := ReadRegular(p + ".link"); err == nil {
		t.Error("ReadRegular followed a symlink")
	}
}

// OpenRegular refuses non-regular files before opening them: opening some
// devices has effects, and a FIFO would block.
func TestOpenRegular(t *testing.T) {
	dir := t.TempDir()
	file, fifo := filepath.Join(dir, "f"), filepath.Join(dir, "fifo")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRegular(file)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, p := range []string{"/dev/null", fifo, dir} {
		if f, err := OpenRegular(p); !errors.Is(err, ErrNotRegular) {
			if f != nil {
				f.Close()
			}
			t.Errorf("%s: err = %v; want ErrNotRegular", p, err)
		}
	}
}

// anotherUsersFile is a readable regular file the test user doesn't own:
// root's /etc/passwd, or as root a file of another user.
func anotherUsersFile(t *testing.T) string {
	if os.Geteuid() != 0 {
		return "/etc/passwd"
	}
	p := filepath.Join(t.TempDir(), "not-the-test-users")
	if err := os.WriteFile(p, []byte("other:user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(p, 1, -1); err != nil {
		t.Skipf("chown: %v", err)
	}
	return p
}

// ReadSmallOwner reads the file it opened only while it keeps the device
// and owner it had when its path was listed: a cookie swapped for a
// symlink to someone else's file is refused, a rewrite by the same owner
// (bitcoind rotates its cookie atomically, under a new inode) is not.
func TestReadSmallOwner(t *testing.T) {
	dir := t.TempDir()
	cookie := filepath.Join(dir, ".cookie")
	if err := os.WriteFile(cookie, []byte("__cookie__:one"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(cookie)
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := Owner(fi)
	if !ok {
		t.Fatal("no owner information")
	}
	// The listed file.
	if b, err := ReadSmallOwner(cookie, 4<<10, &owner); err != nil || string(b) != "__cookie__:one" {
		t.Errorf("the listed cookie: %q, %v", b, err)
	}
	// No owner expected: the check is off, as for the service's reads.
	if b, err := ReadSmallOwner(cookie, 4<<10, nil); err != nil || string(b) != "__cookie__:one" {
		t.Errorf("no owner expected: %q, %v", b, err)
	}
	// Rewritten under the same path: a new inode of the same owner, as
	// bitcoind's rotation does.
	os.Remove(cookie)
	if err := os.WriteFile(cookie, []byte("__cookie__:two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadSmallOwner(cookie, 4<<10, &owner); err != nil || string(b) != "__cookie__:two" {
		t.Errorf("the rotated cookie: %q, %v", b, err)
	}
	// Swapped for a symlink to another user's file: refused, whatever
	// it leads to is not read.
	os.Remove(cookie)
	if err := os.Symlink(anotherUsersFile(t), cookie); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadSmallOwner(cookie, 4<<10, &owner); !errors.Is(err, ErrNotListed) || b != nil {
		t.Errorf("the swapped cookie: %q, %v; want ErrNotListed", b, err)
	}
	// A symlink the listing already saw is fine: it leads to the same
	// owner's file.
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("__cookie__:x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	gfi, err := os.Stat(link) // follows the symlink, as the listing does
	if err != nil {
		t.Fatal(err)
	}
	gowner, _ := Owner(gfi)
	if b, err := ReadSmallOwner(link, 4<<10, &gowner); err != nil || string(b) != "__cookie__:x" {
		t.Errorf("a listed symlink: %q, %v", b, err)
	}
	// Another device is a refusal too, even with the owner unchanged.
	dev := gowner
	dev.Dev++
	if _, err := ReadSmallOwner(good, 4<<10, &dev); !errors.Is(err, ErrNotListed) {
		t.Errorf("another device: %v; want ErrNotListed", err)
	}
}
