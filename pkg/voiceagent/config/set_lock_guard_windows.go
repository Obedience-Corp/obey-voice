//go:build windows

package config

import (
	"os"

	"golang.org/x/sys/windows"
)

func acquireLockGuard(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	o := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, o)
	if err != nil {
		_ = f.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
			return nil, errLockGuardBusy
		}
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, o)
		_ = f.Close()
	}, nil
}
