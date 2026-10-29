package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPersonaDirectoryOverridesAndImmutableBindings(t *testing.T) {
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	launch, _ := filepath.EvalSymlinks(t.TempDir())
	pinned, _ := filepath.EvalSymlinks(t.TempDir())
	for _, source := range []string{"launch-dir", "config", "flag", "env"} {
		t.Run(source, func(t *testing.T) {
			base := &Config{WorkDir: launch, WorkDirSource: source}
			if err := BindPersonaDirectory(base, "alpha", pinned); err != nil {
				t.Fatal(err)
			}
			want := pinned
			if source == "flag" || source == "env" {
				want = launch
			}
			if base.WorkDir != want {
				t.Fatalf("directory = %q, want %q", base.WorkDir, want)
			}
			before := base.Clone()
			next := base.Clone()
			if err := BindPersonaDirectory(next, "beta", ""); err != nil {
				t.Fatal(err)
			}
			if next.WorkDir != launch {
				t.Fatal("default inherited previous persona directory")
			}
			if !reflect.DeepEqual(before, base) {
				t.Fatal("binding mutated admitted configuration")
			}
		})
	}
	cfg := &Config{WorkDir: launch, WorkDirSource: "launch-dir"}
	if err := BindPersonaDirectory(cfg, "alpha", pinned); err != nil {
		t.Fatal(err)
	}
	before := cfg.Clone()
	if err := os.RemoveAll(pinned); err != nil {
		t.Fatal(err)
	}
	if err := BindPersonaDirectory(cfg, "alpha", launch); err == nil {
		t.Fatal("missing pinned directory silently redirected")
	}
	if !reflect.DeepEqual(before, cfg) {
		t.Fatal("failed bind mutated configuration")
	}
}

func TestPersonaDirectoryUsesCapturedDefaultAfterEnvironmentChanges(t *testing.T) {
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	launch, _ := filepath.EvalSymlinks(t.TempDir())
	cfg := &Config{WorkDir: launch, WorkDirSource: "launch-dir"}
	if err := BindPersonaDirectory(cfg, "alpha", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBEY_VOICE_WORK_DIR", t.TempDir())
	if err := BindPersonaDirectory(cfg, "beta", ""); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkDir != launch {
		t.Fatal("environment replaced saved launch default", cfg.WorkDir)
	}
}
