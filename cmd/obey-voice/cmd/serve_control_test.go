//go:build !integration && !windows

package cmd

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeControlRestartAndStop(t *testing.T) {
	dir := t.TempDir()
	port := 21762
	credential := filepath.Join(dir, "token")
	if err := os.WriteFile(credential, []byte("preserve pairing"), 0o600); err != nil {
		t.Fatal(err)
	}
	control, err := listenServeControl(dir, port)
	if err != nil {
		t.Fatal(err)
	}
	defer control.listener.Close()
	if second, err := listenServeControl(dir, port); err == nil {
		second.listener.Close()
		t.Fatal("second server acquired a live control endpoint")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	done := make(chan error, 1)
	go func() {
		done <- serveControlLoop(context.Background(), cwd, control, func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for server")
		}
	}
	wait(started)
	if err := requestServeControl(dir, port, "restart"); err != nil {
		t.Fatal(err)
	}
	wait(started)
	if err := requestServeControl(dir, port, "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
	if got, err := os.ReadFile(credential); err != nil || string(got) != "preserve pairing" {
		t.Fatalf("credentials changed: %q, %v", got, err)
	}
}

func TestServeControlRecoversStaleSocket(t *testing.T) {
	dir := t.TempDir()
	path := serveControlPath(dir, 21764)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	control, err := listenServeControl(dir, 21764)
	if err != nil {
		t.Fatal(err)
	}
	defer control.listener.Close()
	if err := requestServeControl(dir, 21764, "stop"); err != nil {
		t.Fatal(err)
	}
}

func TestServeRestartDoesNotHideShutdownFailure(t *testing.T) {
	dir := t.TempDir()
	control, err := listenServeControl(dir, 21765)
	if err != nil {
		t.Fatal(err)
	}
	defer control.listener.Close()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	failure := errors.New("could not save conversation")
	go func() {
		done <- serveControlLoop(context.Background(), cwd, control, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return failure
		})
	}()
	<-started
	if err := requestServeControl(dir, 21765, "restart"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, failure) {
			t.Fatalf("restart hid save failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restart did not stop after shutdown failure")
	}
}

func TestServeControlMissingAndInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := requestServeControl(dir, 21763, "stop"); err == nil || !strings.Contains(err.Error(), "no controllable serve instance") {
		t.Fatalf("missing server: %v", err)
	}
	control, err := listenServeControl(dir, 21763)
	if err != nil {
		t.Fatal(err)
	}
	defer control.listener.Close()
	if err := requestServeControl(dir, 21763, "unknown"); err == nil || !strings.Contains(err.Error(), "invalid request") {
		t.Fatalf("invalid request: %v", err)
	}
	if action := control.takeAction(); action != "" {
		t.Fatalf("invalid request scheduled %q", action)
	}
	if err := control.listener.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}
