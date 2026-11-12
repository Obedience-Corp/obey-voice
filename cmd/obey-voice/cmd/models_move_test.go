//go:build !windows

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// moveCommandFixture is an isolated install: HOME, the settings folder, the
// models folder and the serve probe all live in one temp root.
func moveCommandFixture(t *testing.T) (source, root string) {
	t.Helper()
	t.Setenv("MODELS_DIR", "")
	root = t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	config.SetConfigDirForTest(t, filepath.Join(root, "install"))
	source = filepath.Join(root, "models")
	for rel, body := range map[string]string{"vad.onnx": "vad", "whispercpp/ggml-base.en.bin": "whisper-weights"} {
		path := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := config.SetKeyFileValue("models_dir", source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { modelsMoveDryRun, modelsMoveJSON = false, false })
	return source, root
}

// crossDrive fails a rename into another folder the way a rename across
// drives does, which forces the copy.
func crossDrive(oldpath, newpath string) error {
	if filepath.Dir(oldpath) == filepath.Dir(newpath) {
		return os.Rename(oldpath, newpath)
	}
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
}

func savedModelsDir(t *testing.T) string {
	t.Helper()
	cfg, err := config.LoadRaw()
	if err != nil {
		t.Fatal(err)
	}
	return config.ModelsDirFrom(cfg)
}

// Ctrl-C and the desktop app's Cancel (SIGTERM to the process group) used to
// kill the process mid-copy with no cleanup.
func TestModelsMoveInterruptStopsTheCopyAndRemovesIt(t *testing.T) {
	source, root := moveCommandFixture(t)
	dest := filepath.Join(root, "fast", "models")
	// Keep the test binary alive even if the command failed to catch it.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, os.Interrupt)
	defer signal.Stop(guard)
	sent := false
	config.SetModelsMoveHooksForTest(t, config.ModelsMoveTestHooks{
		Rename:    crossDrive,
		ServeRoot: filepath.Join(root, "serve-control"),
		AfterCopy: func(ctx context.Context, _ string) {
			if sent {
				return
			}
			sent = true
			if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
				t.Errorf("signal: %v", err)
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Error("SIGINT did not cancel the move")
			}
		},
	})
	_, err := runRoot(t, "models", "move", dest)
	if err == nil || !strings.Contains(err.Error(), "cancelled") || !strings.Contains(err.Error(), source) {
		t.Fatalf("err = %v, want a cancellation naming %s", err, source)
	}
	if !sent {
		t.Fatal("the copy never ran")
	}
	if _, err := os.Lstat(filepath.Join(root, "fast")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the partial copy was left: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(source, "whispercpp", "ggml-base.en.bin")); err != nil || string(got) != "whisper-weights" {
		t.Fatalf("the old folder lost a file: %q, %v", got, err)
	}
	if got := savedModelsDir(t); got != source {
		t.Fatalf("models_dir = %q after a cancelled move", got)
	}
}

func TestModelsMoveDryRunPrintsThePlanAndChangesNothing(t *testing.T) {
	source, root := moveCommandFixture(t)
	config.SetModelsMoveHooksForTest(t, config.ModelsMoveTestHooks{ServeRoot: filepath.Join(root, "serve-control")})
	dest := filepath.Join(root, "fast", "models")
	before, _ := os.ReadFile(config.ConfigFile())

	out, err := runRoot(t, "models", "move", "--dry-run", dest)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"Dry run", "From: " + source, "To:   " + dest, "18 B in 2 files", "renamed in place", "vad.onnx", "whispercpp"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry run output is missing %q:\n%s", want, out)
		}
	}

	modelsMoveDryRun = false
	out, err = runRoot(t, "models", "move", "--dry-run", "--json", filepath.Join(source, "inside"))
	if err == nil || !strings.Contains(err.Error(), "inside the current models folder") {
		t.Fatalf("err = %v", err)
	}
	var plan config.MovePlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil || len(plan.Refusals) != 1 {
		t.Fatalf("json plan = %+v, %v\n%s", plan, err, out)
	}

	if _, err := os.Lstat(filepath.Join(root, "fast")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a dry run created the destination: %v", err)
	}
	if after, _ := os.ReadFile(config.ConfigFile()); string(after) != string(before) {
		t.Fatalf("a dry run changed config.yaml:\n%s", after)
	}
	if got := savedModelsDir(t); got != source {
		t.Fatalf("models_dir = %q", got)
	}
}
