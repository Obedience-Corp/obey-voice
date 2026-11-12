package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// configLockStale is how long a lock file may sit before a later writer
	// treats it as abandoned. A write is a parse + patch + rename measured in
	// milliseconds, so five seconds only ever breaks a crashed writer's lock.
	configLockStale = 5 * time.Second
	// keptBackups bounds the timestamped .bak files, which otherwise grow
	// forever — one per write, for the life of the install.
	keptBackups = 5
)

// backupCoalesce is the window in which a later save adds no backup of its
// own. The backup taken at the start of a burst holds the state from before
// it, and a backup per save would let one burst rotate every older backup away.
var backupCoalesce = 10 * time.Second

var errLockGuardBusy = errors.New("config lock guard is held")

// acquireConfigLock takes an advisory lock on the config file and returns the
// release function. The TUI, the CLI and the Mac app are three writers of one
// file; without this, two read-modify-writes interleave and one set silently
// disappears.
func acquireConfigLock(path string) (func(), error) {
	guardRelease, err := acquireLockGuard(path + ".guard")
	if err != nil {
		if errors.Is(err, errLockGuardBusy) {
			return nil, &SetError{Code: CodeLocked, Message: "another writer is saving settings right now; try again", cause: err}
		}
		return nil, writeFailed("", "acquiring config lock guard", err)
	}
	lockPath := path + ".lock"
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Close()
			return func() {
				_ = os.Remove(lockPath)
				guardRelease()
			}, nil
		}
		if !os.IsExist(err) {
			guardRelease()
			return nil, writeFailed("", "creating config lock", err)
		}
		info, statErr := os.Stat(lockPath)
		if statErr != nil || time.Since(info.ModTime()) <= configLockStale {
			break
		}
		// A ledger transaction or suspended process can outlive the normal
		// write duration. Age alone is never permission to steal its lock.
		if data, err := os.ReadFile(lockPath); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					if err := proc.Signal(syscall.Signal(0)); err == nil || errors.Is(err, syscall.EPERM) {
						break
					}
				}
			}
		}
		if removeErr := os.Remove(lockPath); removeErr != nil {
			break
		}
	}
	guardRelease()
	return nil, &SetError{
		Code:    CodeLocked,
		Message: "another writer is saving settings right now; try again",
	}
}

// pruneBackups keeps the newest keep backups of path and removes the rest. The
// backup name embeds a zero-padded UTC timestamp, so lexical order is
// chronological order.
func pruneBackups(path string, keep int) {
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil || len(matches) <= keep {
		return
	}
	sort.Strings(matches)
	for _, stale := range matches[:len(matches)-keep] {
		_ = os.Remove(stale)
	}
}

// backupWanted reports whether replacing current should leave a new backup:
// not when the newest backup already holds the same bytes, and not when it was
// taken inside the coalescing window.
func backupWanted(path string, current []byte) bool {
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil || len(matches) == 0 {
		return true
	}
	sort.Strings(matches)
	newest := matches[len(matches)-1]
	if data, err := os.ReadFile(newest); err == nil && bytes.Equal(data, current) {
		return false
	}
	stamp := strings.TrimPrefix(newest, path+".bak.")
	if taken, err := time.Parse("20060102T150405.000000000Z", stamp); err == nil && time.Since(taken) < backupCoalesce {
		return false
	}
	return true
}
