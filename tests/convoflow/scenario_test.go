package main

import (
	"path/filepath"
	"testing"
)

func TestLoadStockScenarios(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("scenarios")
	for _, name := range []string{"search-web-intent.yaml", "chat-then-work.yaml", "job-status.yaml", "live-router-spawn.yaml"} {
		scn, err := loadScenario(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if scn.Name == "" || len(scn.Turns) == 0 {
			t.Fatalf("%s: empty", name)
		}
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := osWrite(path, "name: x\nturns: []\nbogus: 1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadScenario(path); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestEmptyUserRequiresTerminalOrIdleWait(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := osWrite(path, "name: x\nturns:\n  - id: a\n    user: \"\"\n    wait: job_started\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadScenario(path); err == nil {
		t.Fatal("expected empty-user wait error")
	}
}
