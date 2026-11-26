//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"golang.org/x/sys/unix"
)

var testHomeLockName = regexp.MustCompile(`^([1-9][0-9]*-[0-9a-f]{16})\.lock(\.new)?$`)

func ownedPrivateDir(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Getuid()) && info.Mode().Perm()&0o077 == 0
}

// lockTestHome marks the home called name as in use for as long as the
// returned file stays open. Without a lock that excludes, no lock file is
// left and the home is never pruned.
func lockTestHome(parent, name string) *os.File {
	lock := filepath.Join(parent, name+".lock")
	// A pruning binary can take a pending file in the instant before it is
	// locked, which costs that attempt.
	for range 3 {
		if f := claimLockFile(lock, flockExcludes); f != nil {
			return f
		}
	}
	return nil
}

// claimLockFile takes the lock before the file gets its final name, so a
// pruning binary never finds a lock file its owner does not hold yet.
func claimLockFile(lock string, excludes func(locked string) bool) *os.File {
	pending := lock + ".new"
	f, err := os.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil
	}
	if tryFlock(f) && excludes(pending) && os.Rename(pending, lock) == nil {
		return f
	}
	_ = f.Close()
	_ = os.Remove(pending)
	return nil
}

func tryFlock(f *os.File) bool {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil
}

// flockExcludes reports whether a second open of an already locked file is
// refused as locked. Some network filesystems grant it, and there a free lock
// would not mean the owner has exited.
func flockExcludes(locked string) bool {
	probe, err := os.OpenFile(locked, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer probe.Close()
	err = unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	return errors.Is(err, unix.EWOULDBLOCK)
}

// pruneTestHomes removes the homes whose binary has exited: those whose lock
// file nothing holds. A home with no lock file is left alone. A pending lock
// file nothing holds is from a binary that died while claiming its home.
func pruneTestHomes(parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, entry := range entries {
		m := testHomeLockName.FindStringSubmatch(entry.Name())
		if m == nil || !entry.Type().IsRegular() {
			continue
		}
		home := filepath.Join(parent, m[1])
		if m[2] != "" {
			home = ""
		}
		removeUnlocked(home, filepath.Join(parent, entry.Name()))
	}
}

// removeUnlocked removes home, when there is one, and then its lock file.
func removeUnlocked(home, lock string) {
	f, err := os.OpenFile(lock, os.O_RDWR|unix.O_NOFOLLOW, 0)
	if err != nil {
		return
	}
	defer f.Close()
	if !tryFlock(f) {
		return
	}
	if home != "" {
		info, err := os.Lstat(home)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil || !info.IsDir() || os.RemoveAll(home) != nil:
			return
		}
	}
	_ = os.Remove(lock)
}
