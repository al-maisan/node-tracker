// Package fsx writes files atomically, for the configs, secrets and units
// that install-service and setup write.
package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	return os.Rename(tmp.Name(), path)
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
