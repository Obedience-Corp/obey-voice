package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workDirEnv redirects home, the binary folder and the config root so the
// resolver's guards can be driven without depending on where `go test` ran.
type workDirEnv struct {
	home   string
	exeDir string
}

func newWorkDirEnv(t *testing.T) workDirEnv {
	t.Helper()
	SetConfigDirForTest(t, t.TempDir())

	home := t.TempDir()
	t.Setenv("HOME", home)

	exeDir := t.TempDir()
	origExecutableDir := executableDir
	executableDir = func() string { return exeDir }
	t.Cleanup(func() { executableDir = origExecutableDir })

	return workDirEnv{home: home, exeDir: exeDir}
}

// TestWorkDirFrom locks the forgiving half of the resolver: a configured value
// must never stop the agent from starting. The failure cases come first — they
// are the reason it exists, since a serve spawned by the Mac app starts in the
// binary's own folder, which can never become the workspace.
func TestWorkDirFrom(t *testing.T) {
	env := newWorkDirEnv(t)

	exeLink := filepath.Join(t.TempDir(), "bin-link")
	if err := os.Symlink(env.exeDir, exeLink); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(env.home, "workspace")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := t.TempDir()
	file := filepath.Join(existing, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"missing directory falls back to home", &Config{WorkDir: filepath.Join(existing, "gone")}, env.home},
		{"a file is not a workspace", &Config{WorkDir: file}, env.home},
		{"root falls back to home", &Config{WorkDir: "/"}, env.home},
		{"binary folder falls back to home", &Config{WorkDir: env.exeDir}, env.home},
		{"symlink to the binary folder falls back to home", &Config{WorkDir: exeLink}, env.home},
		{"tilde expands", &Config{WorkDir: "~/workspace"}, nested},
		{"configured directory wins", &Config{WorkDir: existing}, existing},
		{"nothing configured uses the cwd", nil, cwd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WorkDirFrom(tt.cfg)
			if got != tt.want {
				t.Errorf("WorkDirFrom() = %q, want %q", got, tt.want)
			}
			assertUsableWorkDir(t, got)
		})
	}
}

// TestResolveWorkDirRejectsABadOverride is the other half of the split: an
// explicit --work-dir is the user's own words, so a typo has to be reported.
// Silently starting in $HOME would point every file and shell tool — and, with
// remote_tools_enabled, every network client — at their home folder.
func TestResolveWorkDirRejectsABadOverride(t *testing.T) {
	env := newWorkDirEnv(t)
	existing := t.TempDir()
	configured := t.TempDir()
	file := filepath.Join(existing, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(env.home, "workspace")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		override string
		wantErr  bool
		want     string
	}{
		{"a typo is an error, not a fallback", filepath.Join(existing, "projct"), true, ""},
		{"a file is an error", file, true, ""},
		{"an existing directory is taken literally", existing, false, existing},
		{"tilde expands in an override", "~/workspace", false, nested},
		{"no override defers to the configured value", "", false, configured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWorkDir(&Config{WorkDir: configured}, tt.override)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveWorkDir(%q) = %q, want an error", tt.override, got)
				}
				if got != "" {
					t.Errorf("ResolveWorkDir returned %q alongside an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveWorkDir(%q) error = %v", tt.override, err)
			}
			if got != tt.want {
				t.Errorf("ResolveWorkDir(%q) = %q, want %q", tt.override, got, tt.want)
			}
			assertUsableWorkDir(t, got)
		})
	}
}

// The error has to name the path the user typed, or "not a directory" is
// useless in a serve log.
func TestResolveWorkDirErrorNamesThePath(t *testing.T) {
	newWorkDirEnv(t)
	missing := filepath.Join(t.TempDir(), "projct")

	_, err := ResolveWorkDir(nil, missing)
	if err == nil {
		t.Fatal("want an error for a missing override")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name %q", err, missing)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkDir(nil, file); !errors.Is(err, ErrNotADirectory) {
		t.Errorf("error for a file = %v, want ErrNotADirectory", err)
	}
}

// Without a home the resolver must not answer "." — os.Chdir(".") would leave a
// spawned serve in the binary's folder, which is the whole defect.
func TestWorkDirFallbackNeverReturnsRelativeOrBinaryDir(t *testing.T) {
	env := newWorkDirEnv(t)

	origHomeDir := homeDir
	homeDir = func() string { return "." }
	t.Cleanup(func() { homeDir = origHomeDir })

	// The cwd is the binary folder, exactly as a spawned serve sees it, so the
	// candidate is rejected and the fallback has to answer.
	t.Chdir(env.exeDir)

	got := WorkDirFrom(nil)
	if got == "." {
		t.Fatal("resolver returned \".\"")
	}
	if sameDir(got, env.exeDir) {
		t.Fatalf("resolver returned the binary folder %q", got)
	}
	assertUsableWorkDir(t, got)
}

// The global twin must answer from the same resolver, so paths and doctor
// cannot report a directory the brains do not use.
func TestWorkDirReadsTheConfiguredSetting(t *testing.T) {
	newWorkDirEnv(t)
	dir := t.TempDir()
	Set("work_dir", dir)
	if got := WorkDir(); got != dir {
		t.Errorf("WorkDir() = %q, want %q", got, dir)
	}
}

// assertUsableWorkDir pins the contract every caller relies on: chdir-able and
// printable, which means non-empty, absolute, and an existing directory.
func assertUsableWorkDir(t *testing.T, dir string) {
	t.Helper()
	if dir == "" {
		t.Fatal("work dir is empty")
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("work dir %q is not absolute", dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("work dir %q is not an existing directory (err %v)", dir, err)
	}
}
