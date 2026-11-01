package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

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
