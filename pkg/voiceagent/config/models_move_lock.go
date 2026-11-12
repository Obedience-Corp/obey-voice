package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// serveControlRoot is where `serve` keeps its control socket and lock. A fixed
// location lets a desktop-launched server and an interactive shell find each
// other even when their TMPDIR values differ.
var serveControlRoot = func() string {
	return filepath.Join("/tmp", fmt.Sprintf("obey-voice-%d", os.Getuid()))
}

// ServeControlPath is the control socket of the serve instance for the install
// at dir listening on port. Its lock file is the same path plus ".lock".
func ServeControlPath(dir string, port int) string {
	return filepath.Join(serveControlRoot(), fmt.Sprintf("%s-%d.sock", serveControlPrefix(dir), port))
}

func serveControlPrefix(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return fmt.Sprintf("serve-%x", sum[:8])
}

// runningServeLocks lists the serve control locks of this install that a live
// serve process holds.
func runningServeLocks(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(serveControlRoot(), serveControlPrefix(dir)+"-*.sock.lock"))
	sort.Strings(matches)
	var held []string
	for _, path := range matches {
		if lockHeld(path) {
			held = append(held, path)
		}
	}
	return held
}

// lockHeld probes an existing lock file without creating one.
func lockHeld(path string) bool {
	if _, err := os.Lstat(path); err != nil {
		return false
	}
	release, err := acquireLockGuard(path)
	if err != nil {
		return errors.Is(err, errLockGuardBusy)
	}
	release()
	return false
}

const (
	moveLockName      = "models-move.lock"
	deciderLockSubdir = "decider"
	deciderLockName   = "native-install.lock"
	moveBusy          = "another models move is already running"
	deciderBusy       = "a decision model is being installed into this folder; wait for it to finish"
)

// probeMoveLocks reports what would stop a move right now without creating
// or holding anything, for a dry run or the confirm step.
func probeMoveLocks(source string) []string {
	var refusals []string
	if lockHeld(filepath.Join(ConfigDir(), moveLockName)) {
		refusals = append(refusals, moveBusy)
	}
	if lockHeld(filepath.Join(source, deciderLockSubdir, deciderLockName)) {
		refusals = append(refusals, deciderBusy)
	}
	return append(refusals, serveRefusals()...)
}

// moveLocks are the locks a move holds for its whole run.
type moveLocks struct {
	releases []func()
	once     sync.Once
	// madeDir and madeFile record that the move itself created the
	// installer's lock, so it can take it away again.
	madeDir, madeFile bool
}

func (l *moveLocks) release() {
	l.once.Do(func() {
		for i := len(l.releases) - 1; i >= 0; i-- {
			l.releases[i]()
		}
	})
}

// removeMade takes away the installer lock this move created, from the
// folder the models ended up in, so nothing is left that the user never had.
func (l *moveLocks) removeMade(modelsDir string) {
	dir := filepath.Join(modelsDir, deciderLockSubdir)
	if l.madeFile {
		if info, err := os.Lstat(filepath.Join(dir, deciderLockName)); err == nil && info.Mode().IsRegular() && info.Size() == 0 {
			_ = os.Remove(filepath.Join(dir, deciderLockName))
		}
	}
	if l.madeDir {
		_ = os.Remove(dir)
	}
}

// takeMoveLocks takes the locks a move holds for its whole run: its own, so
// two moves cannot overlap, and the decision-model installer's, created the
// way the installer creates it, so an install cannot start writing into the
// folder mid-move.
func takeMoveLocks(source string) (*moveLocks, []string) {
	locks := &moveLocks{}
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return locks, []string{fmt.Sprintf("cannot open the settings folder %s: %v", dir, err)}
	}
	own, err := acquireLockGuard(filepath.Join(dir, moveLockName))
	switch {
	case errors.Is(err, errLockGuardBusy):
		return locks, []string{moveBusy}
	case err != nil:
		return locks, []string{fmt.Sprintf("cannot take the models move lock: %v", err)}
	}
	locks.releases = append(locks.releases, own)

	var refusals []string
	deciderDir := filepath.Join(source, deciderLockSubdir)
	lockPath := filepath.Join(deciderDir, deciderLockName)
	_, dirErr := os.Lstat(deciderDir)
	_, fileErr := os.Lstat(lockPath)
	// A folder nothing can write into has no installer to fear.
	if err := os.MkdirAll(deciderDir, 0o700); err == nil {
		locks.madeDir = errors.Is(dirErr, fs.ErrNotExist)
		held, err := acquireLockGuard(lockPath)
		locks.madeFile = errors.Is(fileErr, fs.ErrNotExist)
		switch {
		case errors.Is(err, errLockGuardBusy):
			refusals = append(refusals, deciderBusy)
		case err != nil:
			refusals = append(refusals, fmt.Sprintf("cannot take the decision model install lock in %s: %v", deciderDir, err))
		default:
			locks.releases = append(locks.releases, held)
		}
	}
	return locks, append(refusals, serveRefusals()...)
}

func serveRefusals() []string {
	var refusals []string
	for _, path := range runningServeLocks(ConfigDir()) {
		name := strings.TrimSuffix(filepath.Base(path), ".sock.lock")
		port := name[strings.LastIndex(name, "-")+1:]
		refusals = append(refusals, fmt.Sprintf("`obey-voice serve` is running on port %s and reads these models; stop it first with `obey-voice serve stop --port %s`", port, port))
	}
	return refusals
}
