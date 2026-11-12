//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import (
	"errors"
	"os"
	"path/filepath"
)

func isCrossDevice(error) bool { return false }

func deviceOf(string) (uint64, bool) { return 0, false }

func isMountPoint(path string) bool { return filepath.Dir(path) == path }

func syncDir(string) error { return nil }

func stampOf(f *os.File) (fileStamp, error) {
	info, err := f.Stat()
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{size: info.Size(), mtime: info.ModTime().UnixNano()}, nil
}

func stampAt(path string) (fileStamp, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{size: info.Size(), mtime: info.ModTime().UnixNano()}, nil
}

func freeSpace(string) (int64, error) { return 0, errors.New("free space is unknown on this platform") }
