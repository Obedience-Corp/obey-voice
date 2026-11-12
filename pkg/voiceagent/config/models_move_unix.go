//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

func deviceOf(path string) (uint64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

// isMountPoint reports whether path is the top of a mounted volume.
func isMountPoint(path string) bool {
	parent := filepath.Dir(path)
	if parent == path {
		return true
	}
	dev, ok := deviceOf(path)
	parentDev, parentOK := deviceOf(parent)
	return ok && parentOK && dev != parentDev
}

func freeSpace(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// syncDir flushes a directory's entries. Some volumes (exFAT, network shares)
// cannot sync a directory at all; that is not a reason to fail the move.
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.EOPNOTSUPP) && !errors.Is(err, syscall.ENOTTY) {
		return err
	}
	return nil
}
