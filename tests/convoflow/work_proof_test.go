package main

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type deterministicWorkRunner struct {
	rec                  *recorder
	coordinator          *work.Coordinator
	scope                work.WorkScope
	completeBeforeSwitch bool
	selection            events.PersonaSelection
	workerCalls          int
}

func newDeterministicWorkRunner(rec *recorder, completeBeforeSwitch bool) *deterministicWorkRunner {
	scope := work.WorkScope{SessionID: "deterministic-session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	return &deterministicWorkRunner{
		rec: rec, scope: scope, completeBeforeSwitch: completeBeforeSwitch,
		coordinator: work.NewCoordinator(scope),
		selection:   events.PersonaSelection{SessionID: scope.SessionID, ConversationEpoch: scope.ConversationEpoch, PersonaID: "samantha", DisplayName: "Samantha"},
	}
}

func (r *deterministicWorkRunner) runTurn(_ context.Context, _ string) error {
	r.workerCalls++
	key, err := r.coordinator.Register(r.scope, work.Admission{Task: "local fixture", Origin: "agency", Provider: "ollama", Model: "qwen3:8b", Config: map[string]string{"persona": "samantha"}})
	if err != nil {
		return err
	}
	if err := r.coordinator.Started(key); err != nil {
		return err
	}
	r.rec.add(events.JobStarted{ID: key.JobID, Task: "local fixture"})
	if r.completeBeforeSwitch {
		if err := r.publish(key); err != nil {
			return err
		}
		r.rec.add(events.JobFinished{ID: key.JobID, Task: "local fixture"})
	}
	return nil
}

func (r *deterministicWorkRunner) publish(key work.JobKey) error {
	_, err := r.coordinator.Publish(key, work.Outcome{Scope: key.Scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "created worker-proof.txt", Origin: "agency", Provider: "ollama", Model: "qwen3:8b", Config: map[string]string{"persona": "samantha"}})
	return err
}

func (r *deterministicWorkRunner) selectPersona(_ context.Context, id string) error {
	if id != "santa" {
		return nil
	}
	r.selection.PersonaID, r.selection.DisplayName = "santa", "Santa"
	if !r.completeBeforeSwitch {
		jobs := r.coordinator.Snapshot(r.scope).Jobs
		if len(jobs) > 0 {
			job := jobs[0]
			if err := r.publish(job.Key); err != nil {
				return err
			}
			r.rec.add(events.JobFinished{ID: job.Key.JobID, Task: job.Task})
		}
	}
	return nil
}

func (r *deterministicWorkRunner) runBackground(context.Context) error {
	snapshot := r.coordinator.Snapshot(r.scope)
	if len(snapshot.Pending) == 0 {
		return nil
	}
	result := snapshot.Pending[0]
	permit, err := r.coordinator.AcquireCapturePermit(r.scope, "deterministic-host", 1)
	if err != nil {
		return err
	}
	lease, err := r.coordinator.Claim(r.scope, "attempt-1", "turn-1", []work.ResultRef{{Scope: r.scope, JobID: result.DeliveryKey.JobID, ResultID: result.Outcome.ResultID, Revision: result.Outcome.TerminalRevision}}, permit)
	if err != nil {
		return err
	}
	receipt := work.DeliveryReceipt{Scope: r.scope, AttemptID: lease.AttemptID, TurnID: lease.TurnID, CaptureEpoch: lease.CaptureEpoch, Mode: "voice", ResponseCommitted: true, PlaybackComplete: true, Audible: true, Segments: 1}
	if err := r.coordinator.FinishAttempt(lease, []string{result.Outcome.ResultID}, receipt); err != nil {
		return err
	}
	return r.coordinator.AcknowledgePersisted(r.scope, r.coordinator.Snapshot(r.scope).Revision)
}

func (r *deterministicWorkRunner) waitWork(context.Context, bool) error {
	snapshot := r.coordinator.Snapshot(r.scope)
	if len(snapshot.Communicated) != 1 || snapshot.Revision != snapshot.PersistedRevision {
		return errDeterministicWorkNotDurable{}
	}
	return nil
}

func (r *deterministicWorkRunner) history() []brain.Turn { return nil }
func (r *deterministicWorkRunner) playing() bool         { return false }
func (r *deterministicWorkRunner) livePolicy() string    { return "route_only" }
func (r *deterministicWorkRunner) workerPolicy() string  { return "legacy" }
func (r *deterministicWorkRunner) workerTools() *bool    { v := true; return &v }
func (r *deterministicWorkRunner) liveCalled() bool      { return false }
func (r *deterministicWorkRunner) workerCalled() bool    { return r.workerCalls > 0 }
func (r *deterministicWorkRunner) workSnapshot() work.Snapshot {
	return r.coordinator.Snapshot(r.scope)
}
func (r *deterministicWorkRunner) personaState() personaTurnState {
	return personaTurnState{Selection: r.selection, WorkerCalls: r.workerCalls, Work: r.workSnapshot()}
}
func (r *deterministicWorkRunner) writePersonaArtifacts(string) error { return nil }
func (r *deterministicWorkRunner) validatePersonaRun() error          { return nil }

type errDeterministicWorkNotDurable struct{}

func (errDeterministicWorkNotDurable) Error() string { return "deterministic work is not durable" }

func workProofScenarioForTest() *Scenario {
	return &Scenario{Name: "deterministic-work-proof", Turns: []Turn{
		{ID: "start", User: "research the local fixture", Wait: waitJobStarted},
		{ID: "select", Op: "select_persona", Persona: "santa", Wait: waitIdle, Expect: []Expectation{{PersonaID: "santa"}, {WorkRunningAtSelection: boolPtr(true)}, {WorkCompletedBeforeSelection: boolPtr(false)}}},
		{ID: "terminal", Wait: waitJobTerminal},
		{ID: "deliver", Op: "run_background", Wait: waitWorkDurable, Expect: []Expectation{{WorkCommunicated: boolPtr(true)}, {WorkReceipt: boolPtr(true)}, {WorkPersisted: boolPtr(true)}, {WorkOriginPersona: "samantha"}, {WorkCurrentResponder: "santa"}}},
	}}
}

func boolPtr(v bool) *bool { return &v }

func TestDeterministicWorkProofExecutesSelectionAndDurableVoiceReceipt(t *testing.T) {
	rec := &recorder{}
	runner := newDeterministicWorkRunner(rec, false)
	eng := &engine{scn: workProofScenarioForTest(), runDir: t.TempDir(), rec: rec, runner: runner}
	if err := eng.run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDeterministicWorkProofRejectsCompletedBeforeSelection(t *testing.T) {
	rec := &recorder{}
	runner := newDeterministicWorkRunner(rec, true)
	eng := &engine{scn: workProofScenarioForTest(), runDir: t.TempDir(), rec: rec, runner: runner}
	if err := eng.run(context.Background()); err == nil {
		t.Fatal("completed-before-selection case unexpectedly passed")
	}
}
