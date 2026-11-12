package personaruntime

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// fixtureScripts holds the fake CLI scripts, each set written once per test
// binary. macOS checks a newly written executable on its first run, which
// takes about 100 ms, so writing them per test made that a per-test cost.
//
// The scripts re-run this test package. A race-enabled binary takes about a
// second just to start, so under -race they run a plain build of the package
// instead; only the fakes run there, never the code under test.
var fixtureScripts struct {
	mu   sync.Mutex
	root string
	dirs map[string]string
	// binary is what the scripts run, and env, dir and goBin how a race run
	// builds it: the environment and directory the binary started with,
	// before any test changed them.
	binary     string
	env        []string
	dir, goBin string
}

func TestMain(m *testing.M) {
	if raceEnabled {
		fixtureScripts.env = os.Environ()
		fixtureScripts.dir, _ = os.Getwd()
		fixtureScripts.goBin, _ = exec.LookPath("go")
	}
	code := m.Run()
	fixtureScripts.mu.Lock()
	if fixtureScripts.root != "" {
		_ = os.RemoveAll(fixtureScripts.root)
	}
	fixtureScripts.mu.Unlock()
	os.Exit(code)
}

// fixtureRoot returns the directory the scripts live in, creating it on first
// use. The caller holds fixtureScripts.mu.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	if fixtureScripts.root == "" {
		root, err := os.MkdirTemp("", "personaruntime-fixtures-")
		if err != nil {
			t.Fatal(err)
		}
		fixtureScripts.root, fixtureScripts.dirs = root, map[string]string{}
	}
	return fixtureScripts.root
}

// fixtureBinary returns the test binary the fake CLI scripts run: this one,
// or under -race a plain build of this package, made once. A failed build
// falls back to this binary.
func fixtureBinary(t *testing.T) string {
	t.Helper()
	fixtureScripts.mu.Lock()
	defer fixtureScripts.mu.Unlock()
	if fixtureScripts.binary != "" {
		return fixtureScripts.binary
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureScripts.binary = self
	if !raceEnabled || fixtureScripts.goBin == "" {
		return self
	}
	plain := filepath.Join(fixtureRoot(t), "fixture.test")
	build := exec.CommandContext(t.Context(), fixtureScripts.goBin, "test", "-c", "-race=false", "-vet=off", "-o", plain, ".")
	build.Dir, build.Env = fixtureScripts.dir, fixtureScripts.env
	if out, err := build.CombinedOutput(); err != nil {
		t.Logf("fake CLIs run the race-enabled binary; plain build failed: %v\n%s", err, out)
		return self
	}
	fixtureScripts.binary = plain
	return plain
}

// fixtureScriptDir returns the directory holding the scripts written for key,
// writing them on first use. Each key has its own directory, so a test puts
// on PATH only the fakes it installs. Scripts must not depend on the test;
// per-test state goes in environment variables.
func fixtureScriptDir(t *testing.T, key string, scripts map[string]string) string {
	t.Helper()
	fixtureScripts.mu.Lock()
	defer fixtureScripts.mu.Unlock()
	if dir, ok := fixtureScripts.dirs[key]; ok {
		return dir
	}
	dir := filepath.Join(fixtureRoot(t), key)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixtureScripts.dirs[key] = dir
	return dir
}
