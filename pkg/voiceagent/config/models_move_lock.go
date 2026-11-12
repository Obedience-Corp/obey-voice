package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// moveLocks takes the locks a move holds for its whole run: its own, so two
// moves cannot overlap, and the decision-model installer's, so an install
// cannot start writing into the folder mid-move. It also refuses while a serve
// process of this install is running, because serve reads models on demand.
func moveLocks(source string) (release func(), refusals []string) {
	var releases []func()
	release = func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return release, []string{fmt.Sprintf("cannot open the settings folder %s: %v", dir, err)}
	}
	own, err := acquireLockGuard(filepath.Join(dir, "models-move.lock"))
	switch {
	case errors.Is(err, errLockGuardBusy):
		return release, []string{"another models move is already running"}
	case err != nil:
		return release, []string{fmt.Sprintf("cannot take the models move lock: %v", err)}
	}
	releases = append(releases, own)

	decider := filepath.Join(source, "decider", "native-install.lock")
	if _, err := os.Lstat(decider); err == nil {
		held, err := acquireLockGuard(decider)
		switch {
		case errors.Is(err, errLockGuardBusy):
			refusals = append(refusals, "a decision model is being installed into this folder; wait for it to finish")
		case err != nil:
			refusals = append(refusals, fmt.Sprintf("cannot check the decision model install lock %s: %v", decider, err))
		default:
			releases = append(releases, held)
		}
	}
	for _, path := range runningServeLocks(dir) {
		name := strings.TrimSuffix(filepath.Base(path), ".sock.lock")
		port := name[strings.LastIndex(name, "-")+1:]
		refusals = append(refusals, fmt.Sprintf("`obey-voice serve` is running on port %s and reads these models; stop it first with `obey-voice serve stop --port %s`", port, port))
	}
	return release, refusals
}
