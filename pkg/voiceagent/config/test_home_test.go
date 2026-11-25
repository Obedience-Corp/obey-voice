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
