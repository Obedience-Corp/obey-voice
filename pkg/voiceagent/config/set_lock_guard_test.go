package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigLockGuardRejectsConcurrentAttemptAndReleaseAllowsOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	release, err := acquireConfigLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := acquireConfigLock(path); err == nil {
		t.Fatal("concurrent lock acquisition succeeded")
	} else {
		var setErr *SetError
		if !errors.As(err, &setErr) || setErr.Code != CodeLocked {
			t.Fatalf("concurrent acquisition error = %v, want locked", err)
		}
	}
	release()
	release, err = acquireConfigLock(path)
	if err != nil {
		t.Fatal("lock was not released:", err)
	}
	release()
}

func TestConfigLockGuardCannotBeStolenByAgedChangedMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	release, err := acquireConfigLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	marker := path + ".lock"
	if err := os.WriteFile(marker, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireConfigLock(path); err == nil {
		t.Fatal("aged marker replacement succeeded while guard was held")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("guarded marker was removed: %v", err)
	}
}

func TestConfigLockGuardFailureDoesNotLeakGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	guardRelease, err := acquireLockGuard(path + ".guard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireConfigLock(path); err == nil {
		t.Fatal("acquisition succeeded while guard was held")
	}
	guardRelease()
	release, err := acquireConfigLock(path)
	if err != nil {
		t.Fatal("guard remained held after failed acquisition:", err)
	}
	release()
}
