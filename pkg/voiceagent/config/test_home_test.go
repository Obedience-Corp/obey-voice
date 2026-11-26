package config

import (
	"fmt"
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
	home := osHomeDir()
	if home != inheritedHome {
		t.Fatalf("osHomeDir() = %q, want the HOME the binary started with %q", home, inheritedHome)
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

func TestClaimTestHomesParentCreatesItForThisUserAlone(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "homes")
	asked := false
	private := func(info os.FileInfo) bool {
		asked = true
		return info.IsDir()
	}
	if !claimTestHomesParent(parent, private) {
		t.Fatal("a parent that did not exist was not claimed")
	}
	if !asked {
		t.Fatal("the new parent was never checked")
	}
	if !claimTestHomesParent(parent, private) {
		t.Fatal("the parent this user created was refused the second time")
	}
}

func TestClaimTestHomesParentRefusesWhatItCannotTrust(t *testing.T) {
	private := func(os.FileInfo) bool { return true }
	t.Run("a parent that is not private to this user", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "homes")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if claimTestHomesParent(parent, func(os.FileInfo) bool { return false }) {
			t.Fatal("claimed")
		}
	})
	t.Run("a link to a folder", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "homes")
		if err := os.Symlink(t.TempDir(), parent); err != nil {
			t.Skip(err)
		}
		if claimTestHomesParent(parent, private) {
			t.Fatal("claimed")
		}
	})
	t.Run("a file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "homes")
		if err := os.WriteFile(parent, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if claimTestHomesParent(parent, private) {
			t.Fatal("claimed")
		}
	})
	t.Run("a temp folder that does not exist", func(t *testing.T) {
		if claimTestHomesParent(filepath.Join(t.TempDir(), "missing", "homes"), private) {
			t.Fatal("claimed")
		}
	})
}

func TestNewTestHomeLeavesAnotherUsersParentAlone(t *testing.T) {
	tmp := t.TempDir()
	parent := filepath.Join(tmp, testHomesParentName())
	theirs := filepath.Join(parent, "101-0123456789abcdef")
	if err := os.MkdirAll(theirs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(theirs+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}

	home, lock := newTestHome(tmp, func(os.FileInfo) bool { return false })

	if lock != nil {
		lock.Close()
		t.Error("a lock was taken under a parent that is not this user's")
	}
	if insideDir(home, parent) {
		t.Errorf("home %q is under a parent that is not this user's", home)
	}
	if !insideDir(home, tmp) || home == tmp {
		t.Errorf("home %q is not a folder of its own under the temp folder %q", home, tmp)
	}
	if info, err := os.Lstat(home); err != nil || !info.IsDir() {
		t.Errorf("fallback home %q is not a folder: %v", home, err)
	}
	for _, kept := range []string{theirs, theirs + ".lock"} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s removed from a parent that is not this user's: %v", kept, err)
		}
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 2 {
		t.Errorf("parent that is not this user's was written to: %v, %v", entries, err)
	}
}

func TestTestHomeSitsUnderThisUsersParent(t *testing.T) {
	want := filepath.Join(os.TempDir(), testHomesParentName())
	if !claimTestHomesParent(want, ownedPrivateDir) {
		t.Skipf("%s is not this user's, so the test binary keeps a home of its own", want)
	}
	if got := filepath.Dir(testHomeDir()); got != want {
		t.Fatalf("test home parent = %q, want %q", got, want)
	}
	if uid := os.Getuid(); uid >= 0 && !strings.HasSuffix(want, fmt.Sprintf("-test-homes-%d", uid)) {
		t.Fatalf("parent %q does not carry the uid %d", want, uid)
	}
}
