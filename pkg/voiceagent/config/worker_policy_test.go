package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestWithSavedWorkerPolicyReadsDiskAndRejectsMissingMalformed(t *testing.T) {
	SetConfigDirForTest(t, t.TempDir())
	if err := os.WriteFile(ConfigFile(), []byte("voice_workers: [broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WithSavedWorkerPolicy(context.Background(), func(*VoiceWorkers) error { t.Fatal("callback on malformed policy"); return nil }); err == nil {
		t.Fatal("malformed policy accepted")
	}
	if err := os.Remove(ConfigFile()); err != nil {
		t.Fatal(err)
	}
	if err := WithSavedWorkerPolicy(context.Background(), func(*VoiceWorkers) error { t.Fatal("callback on missing policy"); return nil }); err == nil {
		t.Fatal("missing policy accepted")
	}
	w := validTierWorkers()
	if err := SaveVoiceWorkersCAS(w, 0); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := WithSavedWorkerPolicy(context.Background(), func(saved *VoiceWorkers) error { called = saved.Revision == 1; return nil }); err != nil || !called {
		t.Fatalf("saved policy callback: %v %v", err, called)
	}
}

func TestWithSavedWorkerPolicyCanceledDoesNotCallback(t *testing.T) {
	SetConfigDirForTest(t, t.TempDir())
	if err := SaveVoiceWorkersCAS(validTierWorkers(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if err := WithSavedWorkerPolicy(ctx, func(*VoiceWorkers) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestSavedPolicyLockExcludesConcurrentCAS(t *testing.T) {
	SetConfigDirForTest(t, t.TempDir())
	if err := SaveVoiceWorkersCAS(validTierWorkers(), 0); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		done <- WithSavedWorkerPolicy(context.Background(), func(*VoiceWorkers) error { once.Do(func() { close(entered) }); <-release; return nil })
	}()
	<-entered
	_, err := CommitVoiceWorkersCAS(validTierWorkers(), 1)
	if err == nil {
		t.Fatal("CAS acquired policy lock concurrently")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAgedLivePIDLockCannotBeReclaimed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	lock := path + ".lock"
	if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	_, err := acquireConfigLock(path)
	if err == nil {
		t.Fatal("live aged lock reclaimed")
	}
	var se *SetError
	if !errors.As(err, &se) || se.Code != CodeLocked {
		t.Fatalf("err=%v", err)
	}
}
