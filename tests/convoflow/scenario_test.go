package main

import (
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestLoadStockScenarios(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("scenarios")
	for _, name := range []string{"search-web-intent.yaml", "chat-then-work.yaml", "job-status.yaml", "live-router-spawn.yaml", "self-test.yaml", "persona-proof.yaml", "work-survival.yaml"} {
		scn, err := loadScenario(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if scn.Name == "" || len(scn.Turns) == 0 {
			t.Fatalf("%s: empty", name)
		}
	}
}

func TestScenarioWorkProofOperationsAndFrozenOutcomeExpectations(t *testing.T) {
	scn, err := loadScenarioString("work.yaml", `name: work
turns:
  - id: start
    user: "research this local fixture"
    wait: job_started
  - id: switch
    op: select_persona
    persona: santa
    wait: idle
    expect:
      - work_running_at_selection: true
      - work_completed_before_selection: false
      - work_origin_persona: samantha
      - work_current_responder: santa
  - id: deliver
    op: run_background
    wait: work_durable
    expect:
      - work_pending: false
      - work_communicated: true
      - work_receipt: true
      - work_persisted: true
`)
	if err != nil {
		t.Fatal(err)
	}
	if scn.Turns[1].Op != "select_persona" || scn.Turns[2].Wait != waitWorkDurable {
		t.Fatalf("work sequence = %+v", scn.Turns)
	}
	scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "epoch"}
	snapshot := work.Snapshot{Scope: scope, Revision: 3, PersistedRevision: 3,
		Jobs:         []work.JobView{{State: work.JobTerminal}},
		Communicated: []work.ResultView{{Outcome: work.Outcome{Config: map[string]string{"persona": "samantha"}}, LastAttempt: &work.DeliveryEvidence{Receipt: work.DeliveryReceipt{Mode: "voice", ResponseCommitted: true, PlaybackComplete: true, Audible: true, Segments: 1}}}}}
	if got := workOriginPersona(snapshot); got != "samantha" || workState(snapshot) != "communicated" || !workHasReceipt(snapshot) {
		t.Fatalf("outcome projection origin=%q state=%q receipt=%t", got, workState(snapshot), workHasReceipt(snapshot))
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
