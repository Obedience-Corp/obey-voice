package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMoveModelsFlushesTheCopyBeforeRemovingTheOriginal(t *testing.T) {
	cfg, _, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	var events []string
	opts := copyOpts(t, nil)
	moveHooks.trace = func(e string) { events = append(events, e) }
	if _, err := MoveModels(context.Background(), cfg, dest, opts); err != nil {
		t.Fatal(err)
	}
	save := slices.Index(events, "save")
	remove := slices.IndexFunc(events, func(e string) bool { return strings.HasPrefix(e, "remove ") })
	if save < 0 || remove < save {
		t.Fatalf("save at %d, removal at %d: %v", save, remove, events)
	}
	for _, want := range []string{
		"sync file " + filepath.Join(dest, "vad.onnx"),
		"sync file " + filepath.Join(dest, "whispercpp", "ggml-base.en.bin"),
		"sync dir " + dest,
		"sync dir " + filepath.Join(dest, "whispercpp"),
	} {
		if i := slices.Index(events, want); i < 0 || i > save {
			t.Fatalf("%q at %d, save at %d: %v", want, i, save, events)
		}
	}
}

// A writer that keeps an open file in the old folder changes it after it was
// set aside. The move must not delete what it cannot match.
func TestMoveModelsKeepsTheOldFolderWhileAWriterKeepsChangingIt(t *testing.T) {
	cfg, _, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	opts := copyOpts(t, nil)
	var quarantine string
	moveHooks.beforeVerify = func(old string, pass int) {
		quarantine = old
		f, err := os.OpenFile(filepath.Join(old, "vad.onnx"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(f, "+%d", pass)
		_ = f.Close()
	}
	result, err := MoveModels(context.Background(), cfg, dest, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], quarantine) || !strings.Contains(result.Warnings[0], "vad.onnx") {
		t.Fatalf("warnings = %v, want the kept folder %s and the changing file named", result.Warnings, quarantine)
	}
	if got, _ := os.ReadFile(filepath.Join(quarantine, "vad.onnx")); string(got) != "vad+1+2" {
		t.Fatalf("kept old vad.onnx = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "vad.onnx")); string(got) != "vad+1" {
		t.Fatalf("new vad.onnx = %q, want the first change brought over", got)
	}
	if ModelsDir() != dest {
		t.Fatalf("models_dir = %q", ModelsDir())
	}
}

// Something that recreates the old path after it was set aside gets its own
// folder back; the move never deletes at that path.
func TestMoveModelsLeavesAnOldFolderSomethingRecreated(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	opts := copyOpts(t, nil)
	moveHooks.beforeVerify = func(_ string, pass int) {
		if pass == 1 {
			writeMoveFile(t, filepath.Join(source, "decider", "late.bin"), "late")
		}
	}
	result, err := MoveModels(context.Background(), cfg, dest, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(source, "decider", "late.bin")); err != nil || string(got) != "late" {
		t.Fatalf("the recreated folder was touched: %q, %v", got, err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "created again") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	if siblings, _ := filepath.Glob(source + ".obey-voice-old-*"); len(siblings) != 0 {
		t.Fatalf("the set-aside folder was not removed: %v", siblings)
	}
}

func TestMoveModelsRefusesACopyThatDoesNotFit(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	opts := copyOpts(t, nil)
	moveHooks.freeSpace = func(string) (int64, error) { return 1 << 20, nil }
	_, err := MoveModels(context.Background(), cfg, dest, opts)
	var refused *MoveRefusedError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "not enough space") || !strings.Contains(err.Error(), "1.0 MiB free") {
		t.Fatalf("err = %v", err)
	}
	assertNothingMoved(t, cfg, source, dest)
}

func TestMoveModelsRefusesTheTopOfADrive(t *testing.T) {
	cfg, source, root := moveFixture(t)
	moveHooks.isMountPoint = func(p string) bool { return p == source }
	dest := filepath.Join(root, "fast", "models")
	_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err == nil || !strings.Contains(err.Error(), "top of a drive") {
		t.Fatalf("err = %v", err)
	}
	assertNothingMoved(t, cfg, source, dest)
}

// exFAT and FAT32 drives cannot hold links. That has to fail the move with a
// clear message and leave the old folder whole.
func TestMoveModelsLinkFailureOnTheDestinationLeavesTheSourceIntact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	opts := copyOpts(t, nil)
	moveHooks.symlink = func(string, string) error { return errors.New("operation not supported") }
	_, err := MoveModels(context.Background(), cfg, dest, opts)
	if err == nil || !strings.Contains(err.Error(), "audio8-tts") || !strings.Contains(err.Error(), "exFAT") {
		t.Fatalf("err = %v", err)
	}
	assertNothingMoved(t, cfg, source, dest)
	if info, err := os.Lstat(filepath.Join(source, "audio8-tts")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the source link is gone: %v", err)
	}
}

func TestMoveModelsRefusesWhileServeIsRunning(t *testing.T) {
	cfg, source, root := moveFixture(t)
	lock := ServeControlPath(ConfigDir(), 8765) + ".lock"
	stale := ServeControlPath(ConfigDir(), 9000) + ".lock"
	other := ServeControlPath(filepath.Join(root, "another-install"), 8766) + ".lock"
	for _, path := range []string{lock, stale, other} {
		writeMoveFile(t, path, "")
	}
	for _, path := range []string{lock, other} {
		release, err := acquireLockGuard(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	dest := filepath.Join(root, "fast", "models")
	_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	var refused *MoveRefusedError
	if !errors.As(err, &refused) || len(refused.Reasons) != 1 || !strings.Contains(err.Error(), "serve stop --port 8765") {
		t.Fatalf("err = %v, want one refusal naming port 8765 only", err)
	}
	assertNothingMoved(t, cfg, source, dest)
}

func TestMoveModelsHoldsTheInstallLockWhileCopying(t *testing.T) {
	cfg, source, root := moveFixture(t)
	installLock := filepath.Join(source, "decider", "native-install.lock")
	writeMoveFile(t, installLock, "")
	opts := copyOpts(t, nil)
	checked := false
	moveHooks.afterCopy = func(context.Context, string) {
		if checked {
			return
		}
		checked = true
		if release, err := acquireLockGuard(installLock); !errors.Is(err, errLockGuardBusy) {
			if err == nil {
				release()
			}
			t.Errorf("an installer could take the lock mid-move: %v", err)
		}
	}
	if _, err := MoveModels(context.Background(), cfg, filepath.Join(root, "fast", "models"), opts); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("no file was copied")
	}
	release, err := acquireLockGuard(filepath.Join(ConfigDir(), "models-move.lock"))
	if err != nil {
		t.Fatalf("the move lock was not released: %v", err)
	}
	release()
}

func TestPlanModelsMoveReportsWithoutChangingAnything(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	before, _ := os.ReadFile(ConfigFile())
	plan, err := PlanModelsMove(context.Background(), cfg, dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Refusals) != 0 || plan.From != source || plan.To != dest || plan.Files != 2 || !plan.SameVolume {
		t.Fatalf("plan = %+v", plan)
	}
	var names []string
	for _, e := range plan.Entries {
		names = append(names, e.Name)
	}
	want := []string{"vad.onnx", "whispercpp"}
	if runtime.GOOS != "windows" {
		want = []string{"audio8-tts", "vad.onnx", "whispercpp"}
	}
	if !slices.Equal(names, want) || !slices.Equal(plan.RewriteKeys, []string{"whispercpp_model_path"}) {
		t.Fatalf("entries = %v, rewrites = %v", names, plan.RewriteKeys)
	}
	if after, _ := os.ReadFile(ConfigFile()); string(after) != string(before) {
		t.Fatal("planning changed config.yaml")
	}
	assertNothingMoved(t, cfg, source, dest)

	t.Setenv("WHISPERCPP_MODEL_PATH", filepath.Join(source, "whispercpp", "ggml-base.en.bin"))
	plan, err = PlanModelsMove(context.Background(), cfg, dest)
	if err != nil || len(plan.Refusals) != 1 || !strings.Contains(plan.Refusals[0], "WHISPERCPP_MODEL_PATH") {
		t.Fatalf("refusals = %v, %v", plan.Refusals, err)
	}
}
