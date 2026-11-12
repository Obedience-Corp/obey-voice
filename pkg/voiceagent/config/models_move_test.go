package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// moveFixture is an isolated install whose models folder holds a nested file,
// a link that points outside the folder, and a config key naming a file in it.
func moveFixture(t *testing.T) (cfg *Config, source, root string) {
	t.Helper()
	t.Setenv("MODELS_DIR", "")
	root = t.TempDir()
	isolateMoveHooks(t, root)
	SetConfigDirForTest(t, filepath.Join(root, "install"))
	source = filepath.Join(root, "models")
	writeMoveFile(t, filepath.Join(source, "silero_vad.onnx"), "vad")
	writeMoveFile(t, filepath.Join(source, "whispercpp", "ggml-base.en.bin"), "whisper-weights")
	outside := filepath.Join(root, "elsewhere")
	writeMoveFile(t, filepath.Join(outside, "voice.bin"), "linked")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(source, "audio8-voices")); err != nil {
			t.Fatal(err)
		}
	}
	setMoveKey(t, "models_dir", source)
	setMoveKey(t, "whispercpp_model_path", filepath.Join(source, "whispercpp", "ggml-base.en.bin"))
	if !within(ConfigDir(), root) || !within(source, root) {
		t.Fatalf("fixture escaped its temp root %s", root)
	}
	return &Config{ModelsDir: source}, source, root
}

// isolateMoveHooks restores the real file operations for the test and points
// the serve probe at a folder inside root.
func isolateMoveHooks(t *testing.T, root string) {
	t.Helper()
	orig, origRoot := moveHooks, serveControlRoot
	moveHooks = defaultMoveHooks()
	// Never look up the real user's home.
	moveHooks.homeFallback = func() string { return "" }
	serveRoot := filepath.Join(root, "serve-control")
	serveControlRoot = func() string { return serveRoot }
	t.Cleanup(func() { moveHooks, serveControlRoot = orig, origRoot })
}

func writeMoveFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setMoveKey(t *testing.T, key, value string) {
	t.Helper()
	if _, err := SetKeyFileValue(key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

// crossVolume fails a rename into another folder the way a rename across
// drives does. A rename within one folder, which is how the old folder is set
// aside, still works.
func crossVolume(oldpath, newpath string) error {
	if filepath.Dir(oldpath) == filepath.Dir(newpath) {
		return os.Rename(oldpath, newpath)
	}
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: errCrossDevice}
}

// copyOpts forces the cross-volume copy path.
func copyOpts(t *testing.T, progress func(MoveProgress)) MoveOptions {
	t.Helper()
	return renameOpts(t, crossVolume, progress)
}

func renameOpts(t *testing.T, rename func(string, string) error, progress func(MoveProgress)) MoveOptions {
	t.Helper()
	moveHooks.rename = rename
	return MoveOptions{Progress: progress}
}

func assertMoved(t *testing.T, cfg *Config, source, dest string, result MoveResult) {
	t.Helper()
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old folder still present: %v", err)
	}
	for rel, want := range map[string]string{"silero_vad.onnx": "vad", "whispercpp/ggml-base.en.bin": "whisper-weights"} {
		got, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", rel, got, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dest, moveIncompleteMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unfinished-copy marker was left in the finished folder: %v", err)
	}
	if runtime.GOOS != "windows" {
		got, err := os.ReadFile(filepath.Join(dest, "audio8-voices", "voice.bin"))
		if err != nil || string(got) != "linked" {
			t.Fatalf("link was not carried over: %q, %v", got, err)
		}
		if info, err := os.Lstat(filepath.Join(dest, "audio8-voices")); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("audio8-voices should still be a link, not a copy of its target: %v", err)
		}
	}
	if ModelsDir() != dest || cfg.ModelsDir != dest {
		t.Fatalf("models_dir = %q / %q, want %q", ModelsDir(), cfg.ModelsDir, dest)
	}
	want := filepath.Join(dest, "whispercpp", "ggml-base.en.bin")
	mu.RLock()
	got := v.GetString("whispercpp_model_path")
	mu.RUnlock()
	if got != want {
		t.Fatalf("whispercpp_model_path = %q, want %q", got, want)
	}
	if cfg.WhisperCPPModelPath != want {
		t.Fatalf("loaded config still has whispercpp_model_path = %q", cfg.WhisperCPPModelPath)
	}
	kept := &Config{ModelsDir: source, WhisperCPPModelPath: "stale"}
	result.ApplyTo(kept)
	if kept.ModelsDir != dest || kept.WhisperCPPModelPath != want {
		t.Fatalf("ApplyTo left %q / %q", kept.ModelsDir, kept.WhisperCPPModelPath)
	}
	if len(result.RewrittenKeys) != 1 || result.RewrittenKeys[0] != "whispercpp_model_path" {
		t.Fatalf("rewritten keys = %v", result.RewrittenKeys)
	}
	if result.Files != 2 || result.Bytes != int64(len("vad")+len("whisper-weights")) {
		t.Fatalf("result counted %d files, %d bytes", result.Files, result.Bytes)
	}
	if len(result.Warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", result.Warnings)
	}
	// Nothing was left beside the old folder.
	siblings, _ := filepath.Glob(source + ".obey-voice-old-*")
	if len(siblings) > 0 {
		t.Fatalf("set-aside old folder left behind: %v", siblings)
	}
}

func TestMoveModelsRenamesOnTheSameVolume(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	result, err := MoveModels(context.Background(), cfg, dest, MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Copied {
		t.Fatal("a same-volume move should rename, not copy")
	}
	assertMoved(t, cfg, source, dest, result)
}

func TestMoveModelsCopiesAcrossVolumes(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	if err := os.MkdirAll(dest, 0o755); err != nil { // an empty folder made in advance is accepted
		t.Fatal(err)
	}
	var phases []string
	result, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, func(p MoveProgress) {
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
		if p.Done > p.Total {
			t.Errorf("progress ran past the total: %d of %d", p.Done, p.Total)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Copied {
		t.Fatal("expected the copy path")
	}
	assertMoved(t, cfg, source, dest, result)
	if got := strings.Join(phases, ","); got != strings.Join([]string{MovePhaseMeasure, MovePhaseCopy, MovePhaseSave, MovePhaseVerify, MovePhaseRemove}, ",") {
		t.Fatalf("phases = %s", got)
	}
}

func TestMoveModelsCancelledMidCopyLeavesTheOldFolderInUse(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	ctx, cancel := context.WithCancel(context.Background())
	_, err := MoveModels(ctx, cfg, dest, copyOpts(t, func(p MoveProgress) {
		if p.Phase == MovePhaseCopy && p.Done > 0 {
			cancel()
		}
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want cancellation", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial copy was left behind: %v", err)
	}
	for _, rel := range []string{"silero_vad.onnx", "whispercpp/ggml-base.en.bin"} {
		if _, err := os.Stat(filepath.Join(source, rel)); err != nil {
			t.Fatalf("old folder lost %s: %v", rel, err)
		}
	}
	if ModelsDir() != source || cfg.ModelsDir != source {
		t.Fatalf("models_dir changed to %q / %q after a cancelled move", ModelsDir(), cfg.ModelsDir)
	}
}

func TestMoveModelsFollowsALinkedModelsFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	cfg, source, root := moveFixture(t)
	link := filepath.Join(root, "cache-models")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	setMoveKey(t, "models_dir", link)
	setMoveKey(t, "whispercpp_model_path", filepath.Join(link, "whispercpp", "ggml-base.en.bin"))
	cfg.ModelsDir = link
	dest := filepath.Join(root, "fast", "models")
	result, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	assertMoved(t, cfg, source, dest, result)
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old link should be removed rather than left dangling: %v", err)
	}
}

func TestMoveModelsRefusesUnsafeTargets(t *testing.T) {
	cfg, source, root := moveFixture(t)
	occupied := filepath.Join(root, "occupied")
	writeMoveFile(t, filepath.Join(occupied, "keep.txt"), "mine")
	for name, tc := range map[string]struct{ dest, want string }{
		"same folder":       {source, "already in"},
		"inside the source": {filepath.Join(source, "sub"), "inside the current models folder"},
		"parent of source":  {root, "is inside"},
		"has files":         {occupied, "already has files"},
		"empty":             {"  ", "choose a folder"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := MoveModels(context.Background(), cfg, tc.dest, MoveOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	if got, err := os.ReadFile(filepath.Join(occupied, "keep.txt")); err != nil || string(got) != "mine" {
		t.Fatalf("a refused move touched the target: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(source, "silero_vad.onnx")); err != nil {
		t.Fatalf("a refused move touched the source: %v", err)
	}
}

func TestMoveModelsRefusesWhenTheEnvironmentDecidesTheFolder(t *testing.T) {
	cfg, source, root := moveFixture(t)
	t.Setenv("MODELS_DIR", source)
	_, err := MoveModels(context.Background(), cfg, filepath.Join(root, "fast"), MoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "MODELS_DIR") {
		t.Fatalf("err = %v, want it to name MODELS_DIR", err)
	}
}

func TestMoveModelsWithNothingDownloaded(t *testing.T) {
	t.Setenv("MODELS_DIR", "")
	root := t.TempDir()
	isolateMoveHooks(t, root)
	SetConfigDirForTest(t, filepath.Join(root, "install"))
	missing := filepath.Join(root, "never-created")
	_, err := MoveModels(context.Background(), &Config{ModelsDir: missing}, filepath.Join(root, "fast"), MoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "no models to move") {
		t.Fatalf("err = %v", err)
	}
	if bytes, files, err := ModelsUsage(context.Background(), missing); err != nil || bytes != 0 || files != 0 {
		t.Fatalf("usage of a missing folder = %d, %d, %v", bytes, files, err)
	}
}

// A cancel that lands after the last file is copied but before the setting is
// saved must still undo the copy, not finish the move.
func TestMoveModelsCancelledBeforeSaveUndoesTheCopy(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	ctx, cancel := context.WithCancel(context.Background())
	total := int64(len("vad") + len("whisper-weights"))
	_, err := MoveModels(ctx, cfg, dest, copyOpts(t, func(p MoveProgress) {
		if p.Phase == MovePhaseCopy && p.Done == total {
			cancel()
		}
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want cancellation", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the finished copy was left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "silero_vad.onnx")); err != nil || ModelsDir() != source {
		t.Fatalf("old folder not in use after cancel: %v, models_dir=%q", err, ModelsDir())
	}
}
