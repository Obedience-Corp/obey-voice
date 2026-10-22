package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionDirectoryPrecedenceAndStrictFailures(t *testing.T) {
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	dirs := make([]string, 5)
	for i := range dirs {
		dirs[i], _ = filepath.EvalSymlinks(t.TempDir())
	}
	cfg := &Config{WorkDir: dirs[0]}
	for _, tc := range []struct{ name, override, persona, saved, env, legacy, want, source string }{
		{name: "app", want: dirs[0], source: "config"},
		{name: "persona", persona: dirs[1], want: dirs[1], source: "persona"},
		{name: "env", persona: dirs[1], env: dirs[2], want: dirs[2], source: "env"},
		{name: "legacy", persona: dirs[1], legacy: dirs[2], want: dirs[2], source: "env"},
		{name: "primary env", env: dirs[2], legacy: dirs[1], want: dirs[2], source: "env"},
		{name: "flag", override: dirs[3], persona: dirs[1], env: dirs[2], want: dirs[3], source: "flag"},
		{name: "resume", override: dirs[3], persona: dirs[1], env: dirs[2], saved: dirs[4], want: dirs[4], source: "session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OBEY_VOICE_WORK_DIR", tc.env)
			t.Setenv("WORK_DIR", tc.legacy)
			got, err := ResolveSessionDirectory(cfg, tc.override, tc.persona, tc.saved, "")
			if err != nil || got.Dir != tc.want || string(got.Source) != tc.source {
				t.Fatalf("got %+v, %v; want %s (%s)", got, err, tc.want, tc.source)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, args := range [][4]string{{missing, "", "", ""}, {"", missing, "", ""}, {"", "", missing, "persona"}} {
		if _, err := ResolveSessionDirectory(cfg, args[0], args[1], args[2], args[3]); err == nil {
			t.Fatal("invalid selection silently fell back")
		}
	}
	t.Setenv("OBEY_VOICE_WORK_DIR", missing)
	if _, err := ResolveSessionDirectory(cfg, "", dirs[1], "", ""); err == nil {
		t.Fatal("invalid env silently fell back")
	}
}

func TestStartingDirectoryValidationAndPinnedNoFallback(t *testing.T) {
	root := t.TempDir()
	real, _ := filepath.EvalSymlinks(root)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	got, err := ValidateStartingDirectory(link)
	if err != nil || got != real {
		t.Fatalf("symlink: %q %v", got, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(root, "missing"), string(filepath.Separator)} {
		if _, err := ValidateStartingDirectory(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if got, err := ValidateStartingDirectory(""); err != nil || got != "" {
		t.Fatal(got, err)
	}
	cfg := &Config{WorkDir: real, WorkDirSource: "persona"}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if WorkDirFrom(cfg) != real {
		t.Fatal("pinned directory silently fell back after removal")
	}
	if _, err := ResolveSessionDirectory(cfg, "", "", real, "persona"); err == nil {
		t.Fatal("missing saved directory accepted")
	}
}
