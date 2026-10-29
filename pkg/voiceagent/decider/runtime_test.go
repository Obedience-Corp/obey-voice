//go:build darwin || linux

package decider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakePython(t *testing.T, root, script string) {
	t.Helper()
	python := PythonPath(root)
	if err := os.MkdirAll(filepath.Dir(python), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "decider", runtimeID(), "installed"), []byte(runtimeID()), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadyEnvironmentDoesNotRequireInstallerOrSource(t *testing.T) {
	root := t.TempDir()
	fakePython(t, root, "#!/bin/sh\nexit 0\n")
	if !PythonReady(root) {
		t.Fatal("installed environment not ready")
	}
	if err := EnsurePython(t.Context(), root, func(string) { t.Error("ready environment reran installer") }); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(PythonPath(root)); err != nil {
		t.Fatal(err)
	}
	if PythonReady(root) {
		t.Fatal("marker alone counts as usable Python")
	}
	if err := EnsurePython(t.Context(), root, nil); err == nil {
		t.Fatal("missing source silently repaired environment")
	}
	if PythonReady(root) {
		t.Fatal("failed repair marked ready")
	}
}

func TestInstallRejectsCorruptSourceBeforeLaunchingInstaller(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(SourcePath(root)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SourcePath(root), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	err := EnsurePython(t.Context(), root, func(string) { t.Error("corrupt source launched installer") })
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error=%v", err)
	}
	if PythonReady(root) {
		t.Fatal("failed install marked ready")
	}
}

func TestInstallLockCancellationAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.lock")
	release, err := installLock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	_, err = installLock(ctx, path)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("competing lock=%v", err)
	}
	release()
	release, err = installLock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestLocalProcessEnvironmentExcludesCredentialsAndOverrides(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "DECISION_API_KEY", "HF_TOKEN", "PYTHONPATH", "UV_INDEX_URL", "HTTPS_PROXY", "DECIDER_MODEL"} {
		t.Setenv(key, "must-not-inherit")
	}
	for _, value := range cleanEnvironment() {
		if strings.Contains(value, "must-not-inherit") {
			t.Fatalf("leaked process override: %s", strings.SplitN(value, "=", 2)[0])
		}
	}
}

func TestServerStartupFailureAndCancellationCleanUp(t *testing.T) {
	for _, script := range []string{"#!/bin/sh\nexit 7\n", "#!/bin/sh\nexec sleep 30\n"} {
		t.Run(strings.TrimSpace(script), func(t *testing.T) {
			root := t.TempDir()
			fakePython(t, root, script)
			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			start := time.Now()
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(cwd, root)
			if err != nil {
				t.Fatal(err)
			}
			server, err := Start(ctx, relative, "2b")
			if server != nil || err == nil {
				if server != nil {
					server.Close()
				}
				t.Fatalf("server=%v err=%v", server, err)
			}
			if strings.Contains(err.Error(), "no such file") {
				t.Fatalf("relative model directory failed: %v", err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("startup failed to join cancelled child")
			}
		})
	}
}
