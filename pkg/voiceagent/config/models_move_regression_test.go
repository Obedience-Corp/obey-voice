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

// assertNothingMoved checks a refused or undone move left everything as it
// was: the old folder whole and in use, and no destination.
func assertNothingMoved(t *testing.T, cfg *Config, source, dest string) {
	t.Helper()
	for rel, want := range map[string]string{"vad.onnx": "vad", "whispercpp/ggml-base.en.bin": "whisper-weights"} {
		got, err := os.ReadFile(filepath.Join(source, rel))
		if err != nil || string(got) != want {
			t.Fatalf("old folder lost %s: %q, %v", rel, got, err)
		}
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination %s exists after a move that did not happen: %v", dest, err)
	}
	if ModelsDir() != source || cfg.ModelsDir != source {
		t.Fatalf("models_dir = %q / %q, want it unchanged at %q", ModelsDir(), cfg.ModelsDir, source)
	}
}

// A file that appears in an already-listed folder while the copy runs used to
// be in neither folder after a move that reported success.
func TestMoveModelsKeepsAFileWrittenDuringTheCopy(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	wrote := false
	_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, func(p MoveProgress) {
		if p.Phase == MovePhaseCopy && p.Done > 0 && !wrote {
			wrote = true
			writeMoveFile(t, filepath.Join(source, "late-voice.bin"), "registered voice")
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "late-voice.bin")); err != nil || string(got) != "registered voice" {
		t.Fatalf("late-voice.bin was lost: %q, %v", got, err)
	}
}

// Bytes appended to a file after it was copied used to be dropped.
func TestMoveModelsKeepsBytesAppendedAfterAFileWasCopied(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	appended := false
	_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, func(p MoveProgress) {
		if p.Phase == MovePhaseCopy && p.Done >= int64(len("vad")) && !appended {
			appended = true
			f, err := os.OpenFile(filepath.Join(source, "vad.onnx"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.WriteString("+appended")
			_ = f.Close()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "vad.onnx")); string(got) != "vad+appended" {
		t.Fatalf("dest vad.onnx = %q, the appended bytes are gone", got)
	}
}

// A destination that is a link to an empty folder used to have the link
// deleted and replaced by a plain folder.
func TestMoveModelsIntoALinkLandsInItsTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	for name, opts := range map[string]func(*testing.T) MoveOptions{
		"rename": func(*testing.T) MoveOptions { return MoveOptions{} },
		"copy":   func(t *testing.T) MoveOptions { return copyOpts(t, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _, root := moveFixture(t)
			target := filepath.Join(root, "external", "models")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "models-on-external")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if _, err := MoveModels(context.Background(), cfg, link, opts(t)); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the link was replaced: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(target, "vad.onnx")); err != nil || string(got) != "vad" {
				t.Fatalf("the models did not reach the link's target: %q, %v", got, err)
			}
			if ModelsDir() != link {
				t.Fatalf("models_dir = %q, want the link %q", ModelsDir(), link)
			}
		})
	}
}

func TestMoveModelsRefusesALinkToNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	cfg, source, root := moveFixture(t)
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "not-there"), link); err != nil {
		t.Fatal(err)
	}
	_, err := MoveModels(context.Background(), cfg, link, MoveOptions{})
	if err == nil {
		t.Fatal("moved onto a link to nothing")
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "vad.onnx")); err != nil || ModelsDir() != source {
		t.Fatalf("the old folder is not in use: %v, models_dir=%q", err, ModelsDir())
	}
}

// A case variant of a folder inside the source, or a relative path, used to
// slip past the nesting checks.
func TestMoveModelsSeesThroughCaseVariantsAndRelativePaths(t *testing.T) {
	t.Run("case variant", func(t *testing.T) {
		cfg, source, root := moveFixture(t)
		variant := filepath.Join(root, "MODELS", "sub")
		if _, err := os.Stat(filepath.Join(root, "MODELS")); err != nil {
			t.Skip("case-sensitive volume")
		}
		_, err := MoveModels(context.Background(), cfg, variant, MoveOptions{})
		if err == nil || !strings.Contains(err.Error(), "inside the current models folder") {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(source, "sub")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a move into the source started: %v", err)
		}
	})
	t.Run("relative destination", func(t *testing.T) {
		cfg, source, root := moveFixture(t)
		t.Chdir(root)
		_, err := MoveModels(context.Background(), cfg, "fast-models", MoveOptions{})
		if err == nil || !strings.Contains(err.Error(), "not a full path") {
			t.Fatalf("err = %v", err)
		}
		assertNothingMoved(t, cfg, source, filepath.Join(root, "fast-models"))
	})
	t.Run("relative models_dir", func(t *testing.T) {
		cfg, source, root := moveFixture(t)
		t.Chdir(root)
		setMoveKey(t, "models_dir", "models")
		cfg.ModelsDir = "models"
		_, err := MoveModels(context.Background(), cfg, filepath.Join(source, "sub"), MoveOptions{})
		if err == nil || !strings.Contains(err.Error(), "relative path") {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(source, "sub")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a move into the source started: %v", err)
		}
	})
}

// An environment setting naming a file in the old folder used to get only a
// warning, and the file was then deleted under it.
func TestMoveModelsRefusesWhenAnEnvironmentSettingPointsIntoTheFolder(t *testing.T) {
	cfg, source, root := moveFixture(t)
	t.Setenv("WHISPERCPP_MODEL_PATH", filepath.Join(source, "whispercpp", "ggml-base.en.bin"))
	dest := filepath.Join(root, "fast", "models")
	_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err == nil || !strings.Contains(err.Error(), "WHISPERCPP_MODEL_PATH") {
		t.Fatalf("err = %v, want it to name WHISPERCPP_MODEL_PATH", err)
	}
	assertNothingMoved(t, cfg, source, dest)
}

// A setting that cannot be rewritten in config.yaml used to be warned about
// after models_dir was saved, and the old folder was deleted anyway.
func TestMoveModelsRefusesWhenASettingCannotBeSaved(t *testing.T) {
	cfg, source, root := moveFixture(t)
	data, err := os.ReadFile(ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("whispercpp_model_path: "+filepath.Join(source, "whispercpp", "ggml-base.en.bin")+"\n")...)
	if err := os.WriteFile(ConfigFile(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "fast", "models")
	_, err = MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err == nil || !strings.Contains(err.Error(), "whispercpp_model_path") {
		t.Fatalf("err = %v, want it to name whispercpp_model_path", err)
	}
	assertNothingMoved(t, cfg, source, dest)
	if after, _ := os.ReadFile(ConfigFile()); string(after) != string(data) {
		t.Fatalf("config.yaml changed:\n%s", after)
	}
}

// A models_dir that holds far more than models used to be moved and deleted
// wholesale: the home folder, a standard folder, or the settings folder.
func TestMoveModelsRefusesFoldersThatHoldMoreThanModels(t *testing.T) {
	t.Run("parent of the settings folder", func(t *testing.T) {
		t.Setenv("MODELS_DIR", "")
		root := t.TempDir()
		isolateMoveHooks(t, root)
		source := filepath.Join(root, "obey")
		install := filepath.Join(source, "agents", "voice", "obey-voice")
		SetConfigDirForTest(t, install)
		writeMoveFile(t, filepath.Join(source, "vad.onnx"), "vad")
		writeMoveFile(t, filepath.Join(install, "personas", "veronica", "persona.yaml"), "id: veronica")
		setMoveKey(t, "models_dir", source)
		setMoveKey(t, "ollama_model", "qwen3:8b")
		before, _ := os.ReadFile(filepath.Join(install, "config.yaml"))
		dest := filepath.Join(root, "fast", "obey")
		_, err := MoveModels(context.Background(), &Config{ModelsDir: source}, dest, copyOpts(t, nil))
		if err == nil || !strings.Contains(err.Error(), "settings folder") {
			t.Fatalf("err = %v", err)
		}
		if after, _ := os.ReadFile(filepath.Join(install, "config.yaml")); string(after) != string(before) {
			t.Fatalf("config.yaml changed:\n%s", after)
		}
		if _, err := os.Stat(filepath.Join(install, "personas", "veronica", "persona.yaml")); err != nil {
			t.Fatalf("persona moved: %v", err)
		}
		if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("destination created: %v", err)
		}
	})
	t.Run("home folder", func(t *testing.T) {
		cfg, source, root := moveFixture(t)
		orig := homeDir
		homeDir = func() string { return filepath.Join(source, "home") }
		t.Cleanup(func() { homeDir = orig })
		dest := filepath.Join(root, "fast", "models")
		_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
		if err == nil || !strings.Contains(err.Error(), "home folder") {
			t.Fatalf("err = %v", err)
		}
		assertNothingMoved(t, cfg, source, dest)
	})
	t.Run("a standard folder", func(t *testing.T) {
		cfg, _, root := moveFixture(t)
		home := filepath.Join(root, "home")
		docs := filepath.Join(home, "Documents")
		writeMoveFile(t, filepath.Join(docs, "taxes.pdf"), "private")
		orig := homeDir
		homeDir = func() string { return home }
		t.Cleanup(func() { homeDir = orig })
		setMoveKey(t, "models_dir", docs)
		cfg.ModelsDir = docs
		dest := filepath.Join(root, "fast", "models")
		_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
		if err == nil || !strings.Contains(err.Error(), "Documents") {
			t.Fatalf("err = %v", err)
		}
		if got, err := os.ReadFile(filepath.Join(docs, "taxes.pdf")); err != nil || string(got) != "private" {
			t.Fatalf("Documents was touched: %q, %v", got, err)
		}
	})
}

// A cancelled copy used to delete the destination folder even when the user
// had made it, empty, in advance, and left behind parent folders it created.
func TestMoveModelsCancelRemovesOnlyWhatItCreated(t *testing.T) {
	cancelMidCopy := func(t *testing.T, cfg *Config, dest string) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		_, err := MoveModels(ctx, cfg, dest, copyOpts(t, func(p MoveProgress) {
			if p.Phase == MovePhaseCopy && p.Done > 0 {
				cancel()
			}
		}))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want cancellation", err)
		}
	}
	t.Run("an empty folder the user made", func(t *testing.T) {
		cfg, source, root := moveFixture(t)
		dest := filepath.Join(root, "fast", "models")
		if err := os.MkdirAll(dest, 0o750); err != nil {
			t.Fatal(err)
		}
		cancelMidCopy(t, cfg, dest)
		entries, err := os.ReadDir(dest)
		if err != nil || len(entries) != 0 {
			t.Fatalf("the user's folder was removed or left dirty: %v, %d entries", err, len(entries))
		}
		if _, err := os.Stat(filepath.Join(source, "vad.onnx")); err != nil || ModelsDir() != source {
			t.Fatalf("the old folder is not in use: %v", err)
		}
	})
	t.Run("folders the move made", func(t *testing.T) {
		cfg, source, root := moveFixture(t)
		cancelMidCopy(t, cfg, filepath.Join(root, "new", "deep", "models"))
		if _, err := os.Lstat(filepath.Join(root, "new")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("parent folders the move created were left: %v", err)
		}
		assertNothingMoved(t, cfg, source, filepath.Join(root, "new", "deep", "models"))
	})
}

// Any rename failure used to fall back to a copy; only a move across drives
// should.
func TestMoveModelsOnlyCopiesWhenTheRenameCrossesDrives(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	denied := errors.New("operation not permitted")
	_, err := MoveModels(context.Background(), cfg, dest, renameOpts(t, func(string, string) error { return denied }, nil))
	if !errors.Is(err, denied) {
		t.Fatalf("err = %v, want the rename failure", err)
	}
	assertNothingMoved(t, cfg, source, dest)
	if _, err := os.Lstat(filepath.Join(root, "fast")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a parent folder was left behind: %v", err)
	}
}

// A copy that was stopped leaves a marker; a retry must not offer that partial
// copy as a models folder to switch to.
func TestMoveModelsRetryAfterAStoppedCopySaysToDeleteIt(t *testing.T) {
	cfg, source, root := moveFixture(t)
	dest := filepath.Join(root, "fast", "models")
	writeMoveFile(t, filepath.Join(dest, ".obey-voice-move-incomplete"), "")
	writeMoveFile(t, filepath.Join(dest, "vad.onnx"), "va")
	_, err := MoveModels(context.Background(), cfg, dest, MoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "unfinished copy") || strings.Contains(err.Error(), "config set") {
		t.Fatalf("err = %v, want it to call the folder an unfinished copy and not suggest using it", err)
	}
	if _, err := os.Stat(filepath.Join(source, "vad.onnx")); err != nil {
		t.Fatal(err)
	}
}

// Nothing stopped a move while another process was writing into the folder.
func TestMoveModelsRefusesWhileAModelsLockIsHeld(t *testing.T) {
	hold := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		release, err := acquireLockGuard(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	for name, tc := range map[string]struct {
		lock func(source string) string
		want string
	}{
		"a decision model install": {func(source string) string { return filepath.Join(source, "decider", "native-install.lock") }, "decision model"},
		"another move":             {func(string) string { return filepath.Join(ConfigDir(), "models-move.lock") }, "another models move"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, source, root := moveFixture(t)
			hold(t, tc.lock(source))
			dest := filepath.Join(root, "fast", "models")
			_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			assertNothingMoved(t, cfg, source, dest)
		})
	}
}

// A relative link that leaves the folder would point somewhere else after
// the move.
func TestMoveModelsRefusesLinksThatWouldBreak(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	cfg, source, root := moveFixture(t)
	if err := os.Symlink(filepath.Join("..", "elsewhere", "voice.bin"), filepath.Join(source, "voice.bin")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "fast", "models")
	_, err := MoveModels(context.Background(), cfg, dest, MoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "voice.bin") {
		t.Fatalf("err = %v, want it to name voice.bin", err)
	}
	assertNothingMoved(t, cfg, source, dest)
}

// A save that fails must leave the old folder in use on both paths.
func TestMoveModelsSaveFailureLeavesTheOldFolderInUse(t *testing.T) {
	for name, opts := range map[string]func(*testing.T) MoveOptions{
		"rename": func(*testing.T) MoveOptions { return MoveOptions{} },
		"copy":   func(t *testing.T) MoveOptions { return copyOpts(t, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			cfg, source, root := moveFixture(t)
			if err := os.WriteFile(ConfigFile(), []byte("models_dir: [unclosed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(root, "fast", "models")
			if _, err := MoveModels(context.Background(), cfg, dest, opts(t)); err == nil {
				t.Fatal("moved with an unreadable config")
			}
			assertNothingMoved(t, cfg, source, dest)
		})
	}
}
