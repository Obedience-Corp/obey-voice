package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// aliasFixture is moveFixture with a second spelling of the temp root: alias
// is a link to root, the way /var is a link to /private/var on macOS or a
// home folder can be reached through a link.
func aliasFixture(t *testing.T) (cfg *Config, source, root, alias string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks")
	}
	cfg, source, root = moveFixture(t)
	alias = filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	return cfg, source, root, alias
}

// Every refusal compares folders by identity, so a path reached through a
// link is caught the same as the plain spelling.
func TestMoveModelsRefusalsSeeThroughALinkedParent(t *testing.T) {
	t.Run("destination inside the source", func(t *testing.T) {
		cfg, source, _, alias := aliasFixture(t)
		dest := filepath.Join(alias, "models", "sub")
		_, err := MoveModels(context.Background(), cfg, dest, MoveOptions{})
		if err == nil || !strings.Contains(err.Error(), "inside the current models folder") {
			t.Fatalf("err = %v", err)
		}
		assertNothingMoved(t, cfg, source, filepath.Join(source, "sub"))
	})
	t.Run("home folder", func(t *testing.T) {
		cfg, _, _, alias := aliasFixture(t)
		orig := homeDir
		homeDir = func() string { return filepath.Join(alias, "models") }
		t.Cleanup(func() { homeDir = orig })
		_, err := MoveModels(context.Background(), cfg, filepath.Join(t.TempDir(), "fast"), copyOpts(t, nil))
		if err == nil || !strings.Contains(err.Error(), "home folder") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("settings folder", func(t *testing.T) {
		cfg, source, _, alias := aliasFixture(t)
		SetConfigDirForTest(t, filepath.Join(alias, "models", "settings"))
		setMoveKey(t, "models_dir", source)
		_, err := MoveModels(context.Background(), cfg, filepath.Join(t.TempDir(), "fast"), copyOpts(t, nil))
		if err == nil || !strings.Contains(err.Error(), "settings folder") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("top of a drive", func(t *testing.T) {
		cfg, _, root, alias := aliasFixture(t)
		setMoveKey(t, "models_dir", filepath.Join(alias, "models"))
		cfg.ModelsDir = filepath.Join(alias, "models")
		moveHooks.isMountPoint = func(p string) bool { return samePath(p, filepath.Join(root, "models")) }
		_, err := MoveModels(context.Background(), cfg, filepath.Join(t.TempDir(), "fast"), copyOpts(t, nil))
		if err == nil || !strings.Contains(err.Error(), "top of a drive") {
			t.Fatalf("err = %v", err)
		}
	})
}

// A setting that names a file in the folder through another spelling of its
// path used to be missed: not repointed, and the file deleted under it.
func TestMoveModelsRepointsASettingSpelledThroughALink(t *testing.T) {
	cfg, source, _, alias := aliasFixture(t)
	viaAlias := filepath.Join(alias, "models", "whispercpp", "ggml-base.en.bin")
	setMoveKey(t, "whispercpp_model_path", viaAlias)
	dest := filepath.Join(t.TempDir(), "fast", "models")
	result, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	mu.RLock()
	got := v.GetString("whispercpp_model_path")
	mu.RUnlock()
	if want := filepath.Join(dest, "whispercpp", "ggml-base.en.bin"); got != want {
		t.Fatalf("whispercpp_model_path = %q, want %q (rewritten: %v)", got, want, result.RewrittenKeys)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("the setting names a missing file: %v", err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("old folder still present: %v", err)
	}
}

func TestMoveModelsRefusesAnEnvironmentSettingSpelledThroughALink(t *testing.T) {
	cfg, source, _, alias := aliasFixture(t)
	// The file names a model elsewhere; only the environment points in here.
	setMoveKey(t, "whispercpp_model_path", filepath.Join(t.TempDir(), "other.bin"))
	viaAlias := filepath.Join(alias, "models", "whispercpp", "ggml-base.en.bin")
	t.Setenv("WHISPERCPP_MODEL_PATH", viaAlias)
	dest := filepath.Join(t.TempDir(), "fast", "models")
	_, err := MoveModels(context.Background(), cfg, dest, copyOpts(t, nil))
	if err == nil || !strings.Contains(err.Error(), "WHISPERCPP_MODEL_PATH") || !strings.Contains(err.Error(), viaAlias) {
		t.Fatalf("err = %v, want it to name WHISPERCPP_MODEL_PATH and %s", err, viaAlias)
	}
	assertNothingMoved(t, cfg, source, dest)
}

// A link inside the folder whose full-path target reaches the folder through
// another spelling would point into the deleted old folder after the move.
func TestMoveModelsRefusesALinkIntoTheFolderSpelledThroughALink(t *testing.T) {
	cfg, source, _, alias := aliasFixture(t)
	if err := os.Symlink(filepath.Join(alias, "models", "vad.onnx"), filepath.Join(source, "vad-current.onnx")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "fast", "models")
	_, err := MoveModels(context.Background(), cfg, dest, MoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "vad-current.onnx") {
		t.Fatalf("err = %v, want it to name vad-current.onnx", err)
	}
	assertNothingMoved(t, cfg, source, dest)
}
