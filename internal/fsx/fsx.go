// Package fsx writes files atomically, for the configs, secrets and units
// that install-service and setup write, and reads secrets and cookies safely.
package fsx

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// WriteFile replaces path atomically with data in mode: the data goes to a
// new temporary file (O_EXCL) in the same directory, which gets mode, uid
// and gid (-1 keeps one; both -1: no chown) before it is renamed to path.
// With bak, the old file is kept as path.bak.
func WriteFile(path string, data []byte, mode fs.FileMode, uid, gid int, bak bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly after the rename
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode.Perm())
	}
	if err == nil && (uid != -1 || gid != -1) {
		err = tmp.Chown(uid, gid)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if bak { // a link, not a rename: path exists at every point
		if err := os.Remove(path + ".bak"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Link(path, path+".bak"); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// The rename is durable only once the directory is synced.
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

var (
	// ErrNotRegular: the path is not (or does not link to) a regular file.
	ErrNotRegular = errors.New("not a regular file")
	// ErrTooLarge: ReadSmall's file is larger than its limit.
	ErrTooLarge = errors.New("file too large")
)

// OpenRegular opens path for reading, which must be (or link to) a regular
// file; it is for files in directories another user controls, such as a
// datadir. The type is checked before the open, since opening some devices
// has effects (/dev/watchdog), and again on the open file, which catches a
// swap in between; O_NONBLOCK keeps a FIFO swapped in from blocking the
// open. ErrNotRegular doesn't name the path: the caller decides whether it
// may be shown.
func OpenRegular(path string) (*os.File, error) {
	if fi, err := os.Stat(path); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, cmp.Or(err, ErrNotRegular)
	}
	return f, nil
}

// ReadSmall reads path, which must be (or link to) a regular file of at
// most max bytes; it is for secrets and cookies. It opens with OpenRegular
// and reads at most max+1 bytes, so /dev/zero or a huge file can't exhaust
// memory. ErrNotRegular and ErrTooLarge don't name the path.
func ReadSmall(path string, max int64) ([]byte, error) {
	f, err := OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrTooLarge, max)
	}
	return b, nil
}

// ReadRegular reads path, which must be a regular file, not a symlink.
func ReadRegular(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (a masked unit is a symlink to /dev/null)", path)
	}
	return os.ReadFile(path)
}
