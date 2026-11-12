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
	for rel, body := range map[string]string{"silero_vad.onnx": "vad", "whispercpp/ggml-base.en.bin": "whisper-weights"} {
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
	if err == nil || !strings.Contains(err.Error(), "cancelled") || !strings.Contains(err.Error(), source) || ExitCode(err) != 130 {
		t.Fatalf("err = %v (exit %d), want a cancellation naming %s with status 130", err, ExitCode(err), source)
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
	for _, want := range []string{"Dry run", "From: " + source, "To:   " + dest, "18 B in 2 files", "renamed in place", "silero_vad.onnx", "whispercpp"} {
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

// The desktop app's Cancel sends SIGTERM, and quitting the app after a
// Cancel sends another. Only a second Ctrl-C may cut a move's cleanup short.
func TestModelsMoveOnlyASecondCtrlCQuitsAtOnce(t *testing.T) {
	exited := make(chan int, 1)
	orig := interruptExit
	interruptExit = func(code int) { exited <- code }
	t.Cleanup(func() { interruptExit = orig })
	guard := make(chan os.Signal, 4)
	signal.Notify(guard, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(guard)

	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		seen := make(chan bool, 4)
		ctx, stop := interruptContext(context.Background(), func(_ os.Signal, first bool) { seen <- first })
		for range 3 {
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-seen:
			case <-time.After(5 * time.Second):
				t.Fatalf("%v was not seen", sig)
			}
		}
		if ctx.Err() == nil {
			t.Fatalf("%v did not cancel", sig)
		}
		select {
		case code := <-exited:
			t.Fatalf("%v exited with %d", sig, code)
		default:
		}
		stop()
	}

	seen := make(chan bool, 2)
	ctx, stop := interruptContext(context.Background(), func(_ os.Signal, first bool) { seen <- first })
	defer stop()
	for range 2 {
		// One at a time: signals sent together can arrive as one.
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		select {
		case <-seen:
		case <-time.After(5 * time.Second):
			t.Fatal("SIGINT was not seen")
		}
	}
	select {
	case code := <-exited:
		if code != 130 || ctx.Err() == nil {
			t.Fatalf("exit %d, ctx %v", code, ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second Ctrl-C did not quit")
	}
}

func TestModelsMoveJSONLinesCarryATypeAndAFailureKind(t *testing.T) {
	source, root := moveCommandFixture(t)
	config.SetModelsMoveHooksForTest(t, config.ModelsMoveTestHooks{ServeRoot: filepath.Join(root, "serve-control")})
	out, err := runRoot(t, "models", "move", "--json", filepath.Join(source, "inside"))
	if err == nil {
		t.Fatal("a move into the source was not refused")
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var line struct {
		Type, Kind string
		Refusals   []string
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &line); err != nil || line.Type != "error" || line.Kind != "refused" || len(line.Refusals) != 1 {
		t.Fatalf("json = %q (%v)", out, err)
	}

	modelsMoveJSON = false
	dest := filepath.Join(root, "fast", "models")
	out, err = runRoot(t, "models", "move", "--json", dest)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	types := map[string]int{}
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		var l struct{ Type string }
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("not JSON: %q", raw)
		}
		types[l.Type]++
	}
	if types["result"] != 1 || types["progress"] == 0 || len(types) != 2 {
		t.Fatalf("line types = %v\n%s", types, out)
	}
}

func TestModelsMoveDryRunListsWhatTheAppDoesNotUse(t *testing.T) {
	source, root := moveCommandFixture(t)
	config.SetModelsMoveHooksForTest(t, config.ModelsMoveTestHooks{ServeRoot: filepath.Join(root, "serve-control")})
	if err := os.MkdirAll(filepath.Join(source, "lmstudio"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "lmstudio", "weights.gguf"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runRoot(t, "models", "move", "--dry-run", filepath.Join(root, "fast", "models"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, section, found := strings.Cut(out, "Not used by your settings or personas")
	var listed []string
	for _, line := range strings.Split(section, "\n")[1:] {
		if !strings.HasPrefix(line, "    ") {
			break
		}
		listed = append(listed, strings.Fields(line)[0])
	}
	if !found || strings.Join(listed, ",") != "lmstudio" {
		t.Fatalf("unused entries listed = %v, want only lmstudio:\n%s", listed, out)
	}
}
