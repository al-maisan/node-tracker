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
