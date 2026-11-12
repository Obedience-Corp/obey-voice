package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMoveModelsFlushesTheCopyBeforeRemovingTheOriginal(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	var events []string
	opts := copyOpts(t, nil)
	moveHooks.syncFile = func(f *os.File) error {
		events = append(events, "sync file "+f.Name())
		return f.Sync()
	}
	moveHooks.syncDir = func(path string) error {
		events = append(events, "sync dir "+path)
		return syncDir(path)
	}
	write := moveHooks.writeConfig
	moveHooks.writeConfig = func(build func(func(string) (string, bool)) []KeyWrite) ([]KeyWrite, error) {
		events = append(events, "save")
		return write(build)
	}
	moveHooks.beforeVerify = func(string, int) {
		if _, err := os.Lstat(source); err == nil {
			t.Error("the old folder was still in place when it was checked for removal")
		}
		events = append(events, "verify")
	}
	if _, err := MoveModels(context.Background(), cfg, dest, opts); err != nil {
		t.Fatal(err)
	}
	// The move writes to the destination with links resolved (macOS's /var
	// is a link to /private/var).
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	dest = filepath.Join(realRoot, "fast", "models")
	save := slices.Index(events, "save")
	verify := slices.Index(events, "verify")
	if save < 0 || verify < save {
		t.Fatalf("save at %d, old folder checked at %d: %v", save, verify, events)
	}
	for _, want := range []string{
		"sync file " + filepath.Join(dest, "silero_vad.onnx"),
		"sync file " + filepath.Join(dest, "whispercpp", "ggml-base.en.bin"),
		"sync dir " + dest,
		"sync dir " + filepath.Join(dest, "whispercpp"),
		"sync dir " + filepath.Dir(dest),
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
		f, err := os.OpenFile(filepath.Join(old, "silero_vad.onnx"), os.O_APPEND|os.O_WRONLY, 0)
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
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], quarantine) || !strings.Contains(result.Warnings[0], "silero_vad.onnx") {
		t.Fatalf("warnings = %v, want the kept folder %s and the changing file named", result.Warnings, quarantine)
	}
	if got, _ := os.ReadFile(filepath.Join(quarantine, "silero_vad.onnx")); string(got) != "vad+1+2" {
		t.Fatalf("kept old silero_vad.onnx = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "silero_vad.onnx")); string(got) != "vad+1" {
		t.Fatalf("new silero_vad.onnx = %q, want the first change brought over", got)
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
	moveHooks.isMountPoint = func(p string) bool { return samePath(p, source) }
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
	if err == nil || !strings.Contains(err.Error(), "audio8-voices") || !strings.Contains(err.Error(), "exFAT") {
		t.Fatalf("err = %v", err)
	}
	assertNothingMoved(t, cfg, source, dest)
	if info, err := os.Lstat(filepath.Join(source, "audio8-voices")); err != nil || info.Mode()&os.ModeSymlink == 0 {
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

// The installer's lock used to be taken only if an install had already
// created its file, so an install could start mid-move on a fresh folder.
func TestMoveModelsHoldsTheInstallLockWhileCopying(t *testing.T) {
	cfg, source, root := moveFixture(t)
	installLock := filepath.Join(source, "decider", "native-install.lock")
	if _, err := os.Lstat(installLock); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the fixture should start with no install lock")
	}
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

// snapshotTree records every path under root with its size and content, so a
// test can show a call wrote nothing at all.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += " " + string(data)
		}
		out[path] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A dry run used to create the settings folder and a lock file.
func TestPlanModelsMoveReportsWithoutChangingAnything(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	before := snapshotTree(t, root)
	plan, err := PlanModelsMove(context.Background(), cfg, dest, MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Refusals) != 0 || plan.From != source || plan.To != dest || plan.Files != 2 || !plan.SameVolume || !plan.Measured {
		t.Fatalf("plan = %+v", plan)
	}
	var names []string
	for _, e := range plan.Entries {
		names = append(names, e.Name)
	}
	want := []string{"silero_vad.onnx", "whispercpp"}
	if runtime.GOOS != "windows" {
		want = []string{"audio8-voices", "silero_vad.onnx", "whispercpp"}
	}
	if !slices.Equal(names, want) || !slices.Equal(plan.RewriteKeys, []string{"whispercpp_model_path"}) {
		t.Fatalf("entries = %v, rewrites = %v", names, plan.RewriteKeys)
	}
	if after := snapshotTree(t, root); !maps.Equal(before, after) {
		for path, entry := range after {
			if before[path] != entry {
				t.Errorf("planning wrote %s", path)
			}
		}
		t.FailNow()
	}

	t.Setenv("WHISPERCPP_MODEL_PATH", filepath.Join(source, "whispercpp", "ggml-base.en.bin"))
	plan, err = PlanModelsMove(context.Background(), cfg, dest, MoveOptions{})
	if err != nil || len(plan.Refusals) != 1 || !strings.Contains(plan.Refusals[0], "WHISPERCPP_MODEL_PATH") {
		t.Fatalf("refusals = %v, %v", plan.Refusals, err)
	}
	plan, _ = PlanModelsMove(context.Background(), cfg, filepath.Join(source, "inside"), MoveOptions{})
	if plan.Measured || plan.Bytes != 0 {
		t.Fatalf("a move refused before measuring reports a size: %+v", plan)
	}
}
