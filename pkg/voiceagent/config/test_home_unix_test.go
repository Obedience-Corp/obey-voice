//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestHomeIsLockedInAFolderOnlyThisUserCanEnter(t *testing.T) {
	home := testHomeDir()
	info, err := os.Lstat(filepath.Dir(home))
	if err != nil || !info.IsDir() || !ownedPrivateDir(info) {
		t.Fatalf("parent of %q is not a folder private to this user: %v, %v", home, info, err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %v, want 0700", info.Mode().Perm())
	}
	if testHomeLock == nil {
		t.Fatal("the test binary holds no lock on its home")
	}
	lock := home + ".lock"
	if m := testHomeLockName.FindStringSubmatch(filepath.Base(lock)); m == nil || m[1] != filepath.Base(home) || m[2] != "" {
		t.Fatalf("lock name %q is not one pruning looks at", filepath.Base(lock))
	}
	held, err := testHomeLock.Stat()
	onDisk, derr := os.Lstat(lock)
	if err != nil || derr != nil || !os.SameFile(held, onDisk) {
		t.Fatalf("the held lock is not the file at %q: %v, %v", lock, err, derr)
	}
}

func TestOwnedPrivateDirRefusesAFolderOthersCanEnter(t *testing.T) {
	dir := t.TempDir()
	for mode, want := range map[os.FileMode]bool{0o700: true, 0o750: false, 0o705: false, 0o777: false} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := ownedPrivateDir(info); got != want {
			t.Errorf("ownedPrivateDir(mode %v) = %v, want %v", mode, got, want)
		}
	}
}

func TestClaimTestHomesParentRefusesAFolderOthersCanEnter(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "homes")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if claimTestHomesParent(parent, ownedPrivateDir) {
		t.Fatal("claimed a folder with mode 0755")
	}
	created := filepath.Join(t.TempDir(), "homes")
	if !claimTestHomesParent(created, ownedPrivateDir) {
		t.Fatal("refused the folder it created")
	}
	if info, err := os.Lstat(created); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("created parent mode = %v, %v; want 0700", info, err)
	}
}

func TestLockTestHomeLeavesOnlyAHeldLockFile(t *testing.T) {
	parent := t.TempDir()
	name := "101-0123456789abcdef"
	held := lockTestHome(parent, name)
	if held == nil {
		t.Skip("this filesystem gives no exclusive flock")
	}
	defer held.Close()
	lock := filepath.Join(parent, name+".lock")
	if _, err := os.Lstat(lock + ".new"); !os.IsNotExist(err) {
		t.Errorf("the pending lock file was left behind: %v", err)
	}
	second, err := os.OpenFile(lock, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if tryFlock(second) {
		t.Fatal("a second open took the lock a running binary holds")
	}
}

func TestPruneTestHomesRemovesOnlyHomesWhoseLockIsFree(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "keep")
	if err := os.WriteFile(outsideFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mkdir := func(name string) string {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Join(dir, ".obey"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	lockOf := func(home string) string { return home + ".lock" }
	freeLock := func(home string) {
		if err := os.WriteFile(lockOf(home), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	exited := mkdir("101-0123456789abcdef")
	if f := lockTestHome(parent, filepath.Base(exited)); f == nil {
		t.Skip("this filesystem gives no exclusive flock")
	} else {
		f.Close()
	}
	running := mkdir("202-0123456789abcdef")
	held := lockTestHome(parent, filepath.Base(running))
	if held == nil {
		t.Fatal("no lock for the running home")
	}
	defer held.Close()
	unlocked := mkdir("303-0123456789abcdef")
	other := mkdir("notes")
	freeLock(other)
	linkedLock := mkdir("404-0123456789abcdef")
	if err := os.Symlink(outsideFile, lockOf(linkedLock)); err != nil {
		t.Fatal(err)
	}
	linkedHome := filepath.Join(parent, "505-0123456789abcdef")
	if err := os.Symlink(outside, linkedHome); err != nil {
		t.Fatal(err)
	}
	freeLock(linkedHome)
	fileHome := filepath.Join(parent, "606-0123456789abcdef")
	if err := os.WriteFile(fileHome, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	freeLock(fileHome)
	diedClaiming := mkdir("707-0123456789abcdef")
	if err := os.WriteFile(lockOf(diedClaiming)+".new", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	claiming := filepath.Join(parent, "909-0123456789abcdef.lock.new")
	beingClaimed, err := os.OpenFile(claiming, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer beingClaimed.Close()
	if !tryFlock(beingClaimed) {
		t.Fatal("no lock on the pending file")
	}
	goneHome := filepath.Join(parent, "808-0123456789abcdef")
	freeLock(goneHome)

	pruneTestHomes(parent)

	for _, removed := range []string{exited, lockOf(exited), lockOf(goneHome), lockOf(diedClaiming) + ".new"} {
		if _, err := os.Lstat(removed); !os.IsNotExist(err) {
			t.Errorf("%s kept after its binary exited: %v", filepath.Base(removed), err)
		}
	}
	for _, kept := range []string{
		running, lockOf(running), unlocked, other, lockOf(other),
		linkedLock, lockOf(linkedLock), linkedHome, lockOf(linkedHome),
		fileHome, lockOf(fileHome), diedClaiming, claiming,
		outside, outsideFile,
	} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s removed: %v", filepath.Base(kept), err)
		}
	}
}

func TestPruneTestHomesAcrossProcesses(t *testing.T) {
	if os.Getenv("OBEY_VOICE_TEST_HOME_HELPER") == "1" {
		home := homeDir()
		if err := os.MkdirAll(filepath.Join(home, ".obey"), 0o755); err != nil {
			fmt.Println("error:", err)
			return
		}
		fmt.Println("home:" + home)
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return
	}
	tmp := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPruneTestHomesAcrossProcesses$", "-test.v")
	cmd.Env = append(os.Environ(), "OBEY_VOICE_TEST_HOME_HELPER=1", "TMPDIR="+tmp)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	home := ""
	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		if rest, ok := strings.CutPrefix(lines.Text(), "home:"); ok {
			home = rest
			break
		}
		if strings.HasPrefix(lines.Text(), "error:") {
			t.Fatal(lines.Text())
		}
	}
	parent := filepath.Join(tmp, testHomesParentName())
	if home == "" || filepath.Dir(home) != parent {
		t.Fatalf("the other binary's home = %q, want one under %q", home, parent)
	}

	pruneTestHomes(parent)
	if _, err := os.Stat(filepath.Join(home, ".obey")); err != nil {
		t.Fatalf("the home of a running binary was pruned: %v", err)
	}

	stdin.Close()
	for lines.Scan() {
	}
	err = cmd.Wait()
	exited = true
	if err != nil {
		t.Fatalf("the other binary failed: %v", err)
	}
	pruneTestHomes(parent)
	for _, removed := range []string{home, home + ".lock"} {
		if _, err := os.Lstat(removed); !os.IsNotExist(err) {
			t.Errorf("%s kept after its binary exited: %v", filepath.Base(removed), err)
		}
	}
}
