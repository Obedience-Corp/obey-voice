//go:build !integration && !windows

package cmd

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// The lock survives for the lifetime of the listener and is released by the
// OS on a crash. The lock file stays in place so concurrent starters cannot
// lock different inodes while recovering a stale socket.
func lockServeControl(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock serve control: %w", err)
	}
	return f, nil
}
