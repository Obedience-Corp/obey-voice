//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import (
	"errors"
	"path/filepath"
)

func isCrossDevice(error) bool { return false }

func deviceOf(string) (uint64, bool) { return 0, false }

func isMountPoint(path string) bool { return filepath.Dir(path) == path }

func freeSpace(string) (int64, error) { return 0, errors.New("free space is unknown on this platform") }

func syncDir(string) error { return nil }
