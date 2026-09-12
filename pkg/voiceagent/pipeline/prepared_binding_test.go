package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestPreparedBrainReplacementPersistenceFailureKeepsBindingAndJobs(t *testing.T) {
	oldRuntime := newPreparedTestRuntime(t)
	old := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "old"}, runtime: oldRuntime, closed: make(chan struct{})}
	p, jobs := newPreparedTestPipeline(t, old)
	defer jobs.Close()
	defer func() { _ = p.CloseWork() }()

	scope := p.WorkSnapshot().Scope
	c := p.WorkCoordinator()
	if _, err := c.Register(scope, workAdmission("surviving-job")); err != nil {
		t.Fatal(err)
	}
	before := c.Snapshot(scope)

	nextRuntime := newPreparedTestRuntime(t)
	next := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "next"}, runtime: nextRuntime, closed: make(chan struct{})}
	prepared, err := p.PrepareConversationBrain(next, &lifecycleWorker{reply: "worker"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("disk full")
	if err := prepared.Commit(func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("Commit error = %v, want %v", err, wantErr)
	}
	if p.Brain != old {
		t.Fatal("persistence failure published candidate brain")
	}
	if got := c.Snapshot(scope); !reflect.DeepEqual(got, before) {
		t.Fatalf("work binding changed on persistence failure:\n got %+v\nwant %+v", got, before)
	}
	if !oldRuntime.Enabled() {
		t.Fatal("old runtime was retired on persistence failure")
	}
	if nextRuntime.Enabled() {
		t.Fatal("prepared candidate runtime was not cleaned up")
	}
}

func TestPreparedBrainReplacementAttachFailureKeepsOldSelection(t *testing.T) {
	oldRuntime := newPreparedTestRuntime(t)
	old := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "old"}, runtime: oldRuntime, closed: make(chan struct{})}
	p, jobs := newPreparedTestPipeline(t, old)
	defer jobs.Close()
	defer func() { _ = p.CloseWork() }()

	nextRuntime := newPreparedTestRuntime(t)
	next := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "next"}, runtime: nextRuntime, closed: make(chan struct{}), rejectAttach: true}
	if _, err := p.PrepareConversationBrain(next, &lifecycleWorker{reply: "worker"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil); err == nil {
		t.Fatal("PrepareConversationBrain succeeded despite candidate attach failure")
	}
	if p.Brain != old {
		t.Fatal("candidate attach failure changed old brain")
	}
	if !oldRuntime.Enabled() {
		t.Fatal("old runtime was retired on candidate attach failure")
	}
	if nextRuntime.Enabled() {
		t.Fatal("failed candidate runtime was not cleaned up")
	}
}

func TestPreparedBrainReplacementSupportsPipelineWithoutAgency(t *testing.T) {
	old := &lifecycleWorker{reply: "old"}
	next := &lifecycleWorker{reply: "next"}
	p := &Pipeline{Brain: old}
	prepared, err := p.PrepareConversationBrain(next, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Commit(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.Brain != next {
		t.Fatal("prepared replacement did not publish without an agency")
	}
}

func newPreparedTestPipeline(t *testing.T, old *managedHarnessBrain) (*Pipeline, *job.Table) {
	t.Helper()
	p := &Pipeline{Events: events.NewBus(), Brain: old}
	jobs := job.NewTable(context.Background())
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "worker"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil)
	a.Wire()
	if err := p.EnableConversationWork("prepared-binding", nil, &config.Config{VoiceWorkerProvider: "ollama"}); err != nil {
		t.Fatal(err)
	}
	return p, jobs
}

func newPreparedTestRuntime(t *testing.T) *delegation.Runtime {
	t.Helper()
	runtime, err := delegation.New(context.Background(), &managedHarnessClient{
		started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{}),
	}, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func workAdmission(id string) work.Admission {
	return work.Admission{JobID: id, Task: id, Origin: "test", Provider: "test"}
}

func TestPreparedSameBrainRejectsClosedConversation(t *testing.T) {
	old := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "old"}, runtime: newPreparedTestRuntime(t), closed: make(chan struct{})}
	p, jobs := newPreparedTestPipeline(t, old)
	defer jobs.Close()
	if err := p.CloseWork(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.PrepareConversationBrain(old, nil, nil, nil); !errors.Is(err, work.ErrRetired) {
		t.Fatalf("closed no-op accepted: %v", err)
	}
}
func TestPreparedBrainRejectsChangedResponderBeforePersistence(t *testing.T) {
	old := &lifecycleWorker{reply: "old"}
	p := &Pipeline{Brain: old}
	candidate := &lifecycleWorker{reply: "candidate"}
	prepared, err := p.PrepareConversationBrain(candidate, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Brain = &lifecycleWorker{reply: "newer"}
	called := false
	if err := prepared.Commit(func() error { called = true; return nil }); err == nil {
		t.Fatal("stale candidate accepted")
	}
	if called {
		t.Fatal("stale candidate persisted")
	}
}
