package personaruntime

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fixtureScripts holds the fake CLI scripts, each set written once per test
// binary. macOS checks a newly written executable on its first run, which
// takes about 100 ms, so writing them per test made that a per-test cost.
var fixtureScripts struct {
	mu   sync.Mutex
	root string
	dirs map[string]string
}

func TestMain(m *testing.M) {
	code := m.Run()
	fixtureScripts.mu.Lock()
	if fixtureScripts.root != "" {
		_ = os.RemoveAll(fixtureScripts.root)
	}
	fixtureScripts.mu.Unlock()
	os.Exit(code)
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
	if fixtureScripts.root == "" {
		root, err := os.MkdirTemp("", "personaruntime-fixtures-")
		if err != nil {
			t.Fatal(err)
		}
		fixtureScripts.root, fixtureScripts.dirs = root, map[string]string{}
	}
	dir := filepath.Join(fixtureScripts.root, key)
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
