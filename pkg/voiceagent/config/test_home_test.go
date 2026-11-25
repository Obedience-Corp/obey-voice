package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func insideDir(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestTestBinaryNeverResolvesTheInheritedInstall(t *testing.T) {
	if !testing.Testing() {
		t.Fatal("testing.Testing() is false inside a test binary")
	}
	home, err := os.UserHomeDir()
	if err != nil || home != inheritedHome {
		t.Fatalf("os.UserHomeDir() = %q, %v; want the HOME the binary started with %q", home, err, inheritedHome)
	}
	install := filepath.Join(home, ".obey")
	underTestHome := map[string]string{
		"DefaultConfigDir": DefaultConfigDir(),
		"ConfigDir":        ConfigDir(),
		"ConfigFile":       ConfigFile(),
		"PersonasDir":      PersonasDir(),
		"SessionsDir":      SessionsDir(),
		"CapturesDir":      CapturesDir(),
		"~ expansion":      expandHomePath("~/.obey"),
	}
	for name, p := range underTestHome {
		if !insideDir(p, testHomeDir()) {
			t.Errorf("%s = %q is outside the per-process test home %q", name, p, testHomeDir())
		}
	}
	underTestHome["MeetingsDir"] = MeetingsDir()
	underTestHome["PromptsDir"] = PromptsDir()
	underTestHome["SkillsDir"] = SkillsDir()
	for name, p := range underTestHome {
		if insideDir(p, install) {
			t.Errorf("%s = %q is inside the install of the HOME the test binary started with (%q)", name, p, install)
		}
	}
	if got := ConfigDir(); got != filepath.Join(testHomeDir(), ".obey", "agents", "voice", AppSlug) {
		t.Errorf("ConfigDir() = %q, want the install layout under the test home", got)
	}
}

func TestTestBinaryHonorsAHomeTheTestSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got := homeDir(); got != home {
		t.Fatalf("homeDir() = %q, want the HOME the test set %q", got, home)
	}
	if got := expandHomePath("~/notes"); got != filepath.Join(home, "notes") {
		t.Fatalf("expandHomePath = %q", got)
	}
}

func TestConfigDirForTestStillWins(t *testing.T) {
	dir := t.TempDir()
	SetConfigDirForTest(t, dir)
	if ConfigDir() != dir || ConfigFile() != filepath.Join(dir, "config.yaml") {
		t.Fatalf("ConfigDir() = %q, ConfigFile() = %q", ConfigDir(), ConfigFile())
	}
}

func TestPruneTestHomesRemovesOnlyExitedBinariesHomes(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	mkdir := func(name string) string {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Join(dir, ".obey"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	dead := mkdir("101-0123456789abcdef")
	live := mkdir("202-0123456789abcdef")
	other := mkdir("notes")
	file := filepath.Join(parent, "103-0123456789abcdef")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "104-0123456789abcdef")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	pruneTestHomes(parent, func(pid int) bool { return pid == 202 })

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("home of an exited binary kept: %v", err)
	}
	for _, kept := range []string{live, other, file, link, outside} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s removed: %v", kept, err)
		}
	}
}

func TestPruneTestHomesSkipsASymlinkedParent(t *testing.T) {
	target := t.TempDir()
	home := filepath.Join(target, "101-0123456789abcdef")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(t.TempDir(), "homes")
	if err := os.Symlink(target, parent); err != nil {
		t.Fatal(err)
	}
	pruneTestHomes(parent, func(int) bool { return false })
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("pruned through a symlinked parent: %v", err)
	}
}

func TestTestHomeSitsUnderTheSharedParent(t *testing.T) {
	if got, want := filepath.Dir(testHomeDir()), filepath.Join(os.TempDir(), AppSlug+"-test-homes"); got != want {
		t.Fatalf("test home parent = %q, want %q", got, want)
	}
	if !testHomeName.MatchString(filepath.Base(testHomeDir())) {
		t.Fatalf("test home name %q does not match what pruning removes", filepath.Base(testHomeDir()))
	}
}
