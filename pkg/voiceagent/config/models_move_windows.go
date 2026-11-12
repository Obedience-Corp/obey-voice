//go:build windows

package config

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func isCrossDevice(err error) bool { return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) }

func deviceOf(path string) (uint64, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, false
	}
	volume := strings.ToLower(filepath.VolumeName(abs))
	var id uint64
	for _, r := range volume {
		id = id*131 + uint64(r)
	}
	return id, volume != ""
}

func isMountPoint(path string) bool {
	clean := filepath.Clean(path)
	return clean == filepath.VolumeName(clean)+`\` || filepath.Dir(clean) == clean
}

func freeSpace(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return int64(free), nil
}

// syncDir is a no-op: Windows cannot flush a directory handle.
func syncDir(string) error { return nil }
