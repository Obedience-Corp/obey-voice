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

// A file rewritten in place after it was copied, same length and with its
// modification time put back, used to pass the size-and-time compare and be
// deleted with only the old bytes in the new folder.
func TestMoveModelsCatchesASameSizeRewriteWithItsTimeRestored(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	vad := filepath.Join(source, "silero_vad.onnx")
	opts := copyOpts(t, nil)
	moveHooks.afterCopy = func(_ context.Context, rel string) {
		if rel != "silero_vad.onnx" {
			return
		}
		info, err := os.Stat(vad)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vad, []byte("NEW"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(vad, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	result, err := MoveModels(context.Background(), cfg, dest, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "silero_vad.onnx")); string(got) != "NEW" {
		t.Fatalf("new silero_vad.onnx = %q, want the rewritten bytes (warnings %v)", got, result.Warnings)
	}
}

// The home check walked only the home path as spelled, so a home that is a
// link into the models folder went unseen.
func TestMoveModelsRefusesAHomeReachedThroughALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	cfg, _, root := moveFixture(t)
	data := filepath.Join(root, "data")
	writeMoveFile(t, filepath.Join(data, "realhome", "notes.txt"), "mine")
	writeMoveFile(t, filepath.Join(data, "silero_vad.onnx"), "vad")
	homeLink := filepath.Join(root, "home-link")
	if err := os.Symlink(filepath.Join(data, "realhome"), homeLink); err != nil {
		t.Fatal(err)
	}
	orig := homeDir
	homeDir = func() string { return homeLink }
	t.Cleanup(func() { homeDir = orig })
	setMoveKey(t, "models_dir", data)
	cfg.ModelsDir = data
	_, err := MoveModels(context.Background(), cfg, filepath.Join(root, "fast", "data"), copyOpts(t, nil))
	if err == nil || !strings.Contains(err.Error(), "home folder") {
		t.Fatalf("err = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(data, "realhome", "notes.txt")); err != nil || string(got) != "mine" {
		t.Fatalf("the home folder was touched: %q, %v", got, err)
	}
}

func TestMoveModelsRefusesWhenTheHomeFolderIsUnknown(t *testing.T) {
	cfg, source, root := moveFixture(t)
	orig := homeDir
	homeDir = func() string { return "." }
	t.Cleanup(func() { homeDir = orig })
	dest := filepath.Join(root, "fast", "models")
	_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err == nil || !strings.Contains(err.Error(), "home folder") {
		t.Fatalf("err = %v", err)
	}
	assertNothingMoved(t, cfg, source, dest)
}

// failingSave makes the config write fail after the move has renamed or
// copied the folder.
func failingSave(t *testing.T) error {
	t.Helper()
	failure := errors.New("disk full")
	moveHooks.writeConfig = func(func(func(string) (string, bool)) []keyValue) ([]keyValue, error) {
		return nil, failure
	}
	return failure
}

// The undo after a failed save was never reached by a test: the only
// save-failure test was refused by the plan before anything moved.
func TestMoveModelsFailedSaveAfterTheRenamePutsTheFolderBack(t *testing.T) {
	for name, premade := range map[string]bool{"new destination": false, "empty folder made in advance": true} {
		t.Run(name, func(t *testing.T) {
			cfg, source, root := moveFixture(t)
			dest := filepath.Join(root, "fast", "models")
			if premade {
				if err := os.MkdirAll(dest, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(ConfigFile())
			failure := failingSave(t)
			renamed := false
			moveHooks.afterRename = func(context.Context) { renamed = true }
			_, err := MoveModels(context.Background(), cfg, dest, MoveOptions{})
			if !errors.Is(err, failure) || !renamed {
				t.Fatalf("err = %v, renamed = %v", err, renamed)
			}
			if premade {
				if entries, err := os.ReadDir(dest); err != nil || len(entries) != 0 {
					t.Fatalf("the empty folder was not put back: %v, %d entries", err, len(entries))
				}
				if err := os.Remove(dest); err != nil {
					t.Fatal(err)
				}
			}
			assertNothingMoved(t, cfg, source, dest)
			if info, err := os.Lstat(source); err != nil || !info.IsDir() {
				t.Fatalf("the old folder is not a plain folder again: %v", err)
			}
			if after, _ := os.ReadFile(ConfigFile()); string(after) != string(before) {
				t.Fatalf("config.yaml changed:\n%s", after)
			}
		})
	}
}

func TestMoveModelsFailedSaveAfterTheCopyRemovesIt(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	before, _ := os.ReadFile(ConfigFile())
	opts := copyOpts(t, nil)
	failure := failingSave(t)
	copied := false
	moveHooks.afterCopy = func(context.Context, string) { copied = true }
	_, err := MoveModels(context.Background(), cfg, dest, opts)
	if !errors.Is(err, failure) || !copied {
		t.Fatalf("err = %v, copied = %v", err, copied)
	}
	assertNothingMoved(t, cfg, source, dest)
	if after, _ := os.ReadFile(ConfigFile()); string(after) != string(before) {
		t.Fatalf("config.yaml changed:\n%s", after)
	}
}

// The check that honours a cancel just before the save had no test: the
// existing one cancelled inside the copy loop, which stops on its own.
func TestMoveModelsCancelAfterTheLastFileStopsBeforeTheSave(t *testing.T) {
	t.Run("copy", func(t *testing.T) {
		cfg, source, root := moveFixture(t)
		dest := filepath.Join(root, "fast", "models")
		ctx, cancel := context.WithCancel(context.Background())
		opts := copyOpts(t, nil)
		moveHooks.afterCopy = func(_ context.Context, rel string) {
			if rel == filepath.Join("whispercpp", "ggml-base.en.bin") {
				cancel()
			}
		}
		_, err := MoveModels(ctx, cfg, dest, opts)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want cancellation", err)
		}
		assertNothingMoved(t, cfg, source, dest)
	})
	t.Run("rename", func(t *testing.T) {
		cfg, source, root := moveFixture(t)
		dest := filepath.Join(root, "fast", "models")
		ctx, cancel := context.WithCancel(context.Background())
		moveHooks.afterRename = func(context.Context) { cancel() }
		_, err := MoveModels(ctx, cfg, dest, MoveOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want cancellation", err)
		}
		assertNothingMoved(t, cfg, source, dest)
	})
}

// A same-drive move renames first and saves second. In between, the old path
// must still lead to the models, so a kill there loses nothing anyone can find.
func TestMoveModelsKeepsTheOldPathWorkingUntilTheSave(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	write := moveHooks.writeConfig
	moveHooks.writeConfig = func(build func(func(string) (string, bool)) []keyValue) ([]keyValue, error) {
		if got, err := os.ReadFile(filepath.Join(source, "silero_vad.onnx")); err != nil || string(got) != "vad" {
			t.Errorf("the old path did not lead to the models before the save: %q, %v", got, err)
		}
		return write(build)
	}
	result, err := MoveModels(context.Background(), cfg, dest, MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertMoved(t, cfg, source, dest, result)
}

// The settings to repoint were chosen from this process's snapshot, which a
// long-running app may hold from before another process changed the file.
func TestMoveModelsRepointsWhatTheFileSaysNotAStaleSnapshot(t *testing.T) {
	cfg, source, root := moveFixture(t)
	inside := filepath.Join(source, "whispercpp", "ggml-base.en.bin")
	outside := filepath.Join(root, "elsewhere", "voice.bin")
	// The file says the model is inside; this process still believes outside.
	Set("whispercpp_model_path", outside)
	dest := filepath.Join(root, "fast", "models")
	result, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	cfgFile, _ := os.ReadFile(ConfigFile())
	want := filepath.Join(dest, "whispercpp", "ggml-base.en.bin")
	if !strings.Contains(string(cfgFile), want) || strings.Contains(string(cfgFile), inside) {
		t.Fatalf("config.yaml (rewritten %v):\n%s", result.RewrittenKeys, cfgFile)
	}
}

// Files obey-voice does not recognise are moved along, but a copied old
// folder holding any of them is kept, with a note, never deleted.
func TestMoveModelsKeepsAnOldFolderWithUnrecognisedFiles(t *testing.T) {
	cfg, source, root := moveFixture(t)
	writeMoveFile(t, filepath.Join(source, "lmstudio", "weights.gguf"), "someone else's")
	dest := filepath.Join(root, "fast", "models")
	plan, err := PlanModelsMove(context.Background(), cfg, dest, MoveOptions{})
	if err != nil || len(plan.Unrecognized) == 0 || plan.Unrecognized[0].Name != "lmstudio" {
		t.Fatalf("unrecognized = %+v, %v", plan.Unrecognized, err)
	}
	result, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(source + ".obey-voice-old-*")
	if len(kept) != 1 || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "lmstudio") {
		t.Fatalf("kept = %v, warnings = %v", kept, result.Warnings)
	}
	if got, _ := os.ReadFile(filepath.Join(kept[0], "lmstudio", "weights.gguf")); string(got) != "someone else's" {
		t.Fatalf("kept folder lost the file: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "lmstudio", "weights.gguf")); string(got) != "someone else's" {
		t.Fatalf("the file was not moved along: %q", got)
	}
	if note, err := os.ReadFile(filepath.Join(kept[0], quarantineReadme)); err != nil || !strings.Contains(string(note), dest) {
		t.Fatalf("no note in the kept folder: %q, %v", note, err)
	}
	if _, err := os.Lstat(filepath.Join(dest, quarantineReadme)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the note was copied into the new folder: %v", err)
	}
}

// A healthy install with the default settings, plus files the installers
// write, is recognised in full, so its old folder can be removed. A model
// nothing uses any more is not.
func TestMoveModelsRecognisesWhatTheAppWrites(t *testing.T) {
	_, source, _ := moveFixture(t)
	cfg, err := LoadRaw()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelsDir = source
	for _, name := range []string{".archive-kokoro.tar.bz2.part", ".extract-123", ".samantha-asset-kokoro.json", ".DS_Store"} {
		writeMoveFile(t, filepath.Join(source, name), "x")
	}
	writeMoveFile(t, filepath.Join(source, "decider", "native-install.lock"), "")
	manifest, err := ManifestFor(cfg, DefaultAssetRequest(cfg))
	if err != nil {
		t.Fatal(err)
	}
	installed := 0
	for _, a := range manifest.Assets {
		paths, _ := a.ownedPaths(source)
		for _, p := range paths {
			if a.IsArchive() && a.TargetDir != "" {
				writeMoveFile(t, filepath.Join(p, "model.onnx"), "m")
			} else {
				writeMoveFile(t, p, "m")
			}
			installed++
		}
	}
	if installed < 3 {
		t.Fatalf("the default manifest names only %d asset paths", installed)
	}
	writeMoveFile(t, filepath.Join(source, "old-tier-nobody-uses", "model.onnx"), "unused")
	plan, err := PlanModelsMove(context.Background(), cfg, filepath.Join(t.TempDir(), "fast"), MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range plan.Unrecognized {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "old-tier-nobody-uses" {
		t.Fatalf("unrecognized = %v, want only the unused tier (installed %d asset paths)", names, installed)
	}
}

// A failed config flush after the save must keep the old folder.
func TestMoveModelsKeepsTheOldFolderWhenTheConfigFlushFails(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	opts := copyOpts(t, nil)
	moveHooks.syncConfig = func(string) error { return errors.New("I/O error") }
	result, err := MoveModels(context.Background(), cfg, dest, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "silero_vad.onnx")); err != nil {
		t.Fatalf("the old folder was deleted though the setting may not be on disk: %v", err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "flushed") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

// A copy failure while bringing a late folder over must not leave its
// half-written temp copy in the new folder.
func TestMoveModelsLeavesNoTempFileWhenALateCopyFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	cfg, _, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	opts := copyOpts(t, nil)
	moveHooks.beforeVerify = func(old string, pass int) {
		if pass != 1 {
			return
		}
		late := filepath.Join(old, "late-voices")
		writeMoveFile(t, filepath.Join(late, "a-voice.bin"), "late")
		if err := os.Symlink(filepath.Join(root, "elsewhere", "voice.bin"), filepath.Join(late, "z-link")); err != nil {
			t.Fatal(err)
		}
		moveHooks.symlink = func(string, string) error { return errors.New("operation not supported") }
	}
	result, err := MoveModels(context.Background(), cfg, dest, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "kept") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dest, "*.obey-voice-move.part"))
	if len(leftovers) != 0 {
		t.Fatalf("temp copies left in the new folder: %v", leftovers)
	}
}

// Another drive mounted inside the models folder is refused before anything
// moves, and a mount that appears later keeps the old folder.
func TestMoveModelsRefusesADriveMountedInside(t *testing.T) {
	cfg, source, root := moveFixture(t)
	mounted := filepath.Join(source, "whispercpp")
	sourceDev, _ := deviceOf(source)
	moveHooks.deviceOf = func(path string) (uint64, bool) {
		if samePath(path, mounted) {
			return sourceDev + 1, true
		}
		return deviceOf(path)
	}
	dest := filepath.Join(root, "fast", "models")
	_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err == nil || !strings.Contains(err.Error(), "mounted inside") {
		t.Fatalf("err = %v", err)
	}
	assertNothingMoved(t, cfg, source, dest)

	moveHooks.deviceOf = deviceOf
	moveHooks.beforeVerify = func(old string, pass int) {
		if pass != 2 {
			return
		}
		inside := filepath.Join(old, "whispercpp")
		moveHooks.deviceOf = func(path string) (uint64, bool) {
			if path == inside {
				return sourceDev + 1, true
			}
			return deviceOf(path)
		}
	}
	result, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "mounted inside") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

// A retry after a stopped copy names the folder that holds the partial files
// and says to empty it, not to delete a link or a drive.
func TestMoveModelsRetryNamesWhereThePartialCopyIs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	cfg, _, root := moveFixture(t)
	real := filepath.Join(root, "external", "models")
	writeMoveFile(t, filepath.Join(real, moveIncompleteMarker), "")
	writeMoveFile(t, filepath.Join(real, "silero_vad.onnx"), "va")
	link := filepath.Join(root, "models-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	_, err := MoveModels(context.Background(), cfg, link, MoveOptions{})
	realDest := resolveExisting(real)
	if err == nil || !strings.Contains(err.Error(), "delete everything inside "+realDest) || !strings.Contains(err.Error(), filepath.Join(realDest, moveIncompleteMarker)) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal(err)
	}
}

// A drive that cannot hold links (exFAT, FAT32) gets no link at the old
// path. The settings must still be repointed, though the old path is gone by
// the time they are saved.
func TestMoveModelsRepointsOnADriveWithoutLinks(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	moveHooks.symlink = func(string, string) error { return errors.New("operation not supported") }
	result, err := MoveModels(context.Background(), cfg, dest, MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Copied {
		t.Fatal("expected a same-drive rename")
	}
	assertMoved(t, cfg, source, dest, result)
}

// The installer lock a move takes is created if missing, and taken away
// again: a folder the user never had a decider folder in gets none.
func TestMoveModelsLeavesNoLockFileBehind(t *testing.T) {
	t.Run("moved", func(t *testing.T) {
		cfg, _, root := moveFixture(t)
		dest := filepath.Join(root, "fast", "models")
		if _, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(dest, "decider")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the move left its lock folder in the new folder: %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		cfg, source, root := moveFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		opts := copyOpts(t, nil)
		moveHooks.afterCopy = func(context.Context, string) { cancel() }
		if _, err := MoveModels(ctx, cfg, filepath.Join(root, "fast", "models"), opts); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(source, "decider")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the move left its lock folder in the old folder: %v", err)
		}
	})
	t.Run("an existing lock is kept", func(t *testing.T) {
		cfg, _, root := moveFixture(t)
		cfgSource := cfg.ModelsDir
		writeMoveFile(t, filepath.Join(cfgSource, "decider", "native-install.lock"), "")
		dest := filepath.Join(root, "fast", "models")
		if _, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(dest, "decider", "native-install.lock")); err != nil {
			t.Fatalf("the user's own install lock was removed: %v", err)
		}
	})
}
