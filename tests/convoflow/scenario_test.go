package main

import (
	"path/filepath"
	"testing"
)

func TestLoadStockScenarios(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("scenarios")
	for _, name := range []string{"search-web-intent.yaml", "chat-then-work.yaml", "job-status.yaml", "live-router-spawn.yaml", "self-test.yaml", "persona-proof.yaml"} {
		scn, err := loadScenario(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if scn.Name == "" || len(scn.Turns) == 0 {
			t.Fatalf("%s: empty", name)
		}
	}
}

func TestScenarioSelectionSchemaRejectsUnknownOpAndMisplacedError(t *testing.T) {
	t.Parallel()
	if _, err := loadScenarioString("bad-op.yaml", "name: x\nturns:\n  - id: a\n    op: select_santa\n"); err == nil {
		t.Fatal("expected unknown op error")
	}
	if _, err := loadScenarioString("bad-error.yaml", "name: x\nturns:\n  - id: a\n    expect_error: nope\n"); err == nil {
		t.Fatal("expected misplaced expect_error error")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	if _, err := loadScenarioString("bad.yaml", "name: x\nturns: []\nbogus: 1\n"); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestEmptyUserRequiresTerminalOrIdleWait(t *testing.T) {
	t.Parallel()
	if _, err := loadScenarioString("bad.yaml", "name: x\nturns:\n  - id: a\n    user: \"\"\n    wait: job_started\n"); err == nil {
		t.Fatal("expected empty-user wait error")
	}
}
