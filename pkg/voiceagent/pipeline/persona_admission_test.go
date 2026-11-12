package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func TestQueuedWorkAcknowledgmentAndRejectedAdmission(t *testing.T) {
	worker := &blockingWorker{started: make(chan struct{}), release: make(chan struct{}), reply: "result"}
	jobs := job.NewTable(t.Context(), job.WithMaxConcurrent(1))
	t.Cleanup(jobs.Close)
	b := &routedProjectionBrain{fakeBrain: &fakeBrain{}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	a := NewAgency(p, jobs, worker, &config.Config{}, nil)
	a.Wire()
	var ready []events.ResponseReady
	events.Subscribe(p.Events, func(e events.ResponseReady) { ready = append(ready, e) })
	if err := p.RunTurnTextMode(t.Context(), "search for the first item"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-worker.started:
	case <-time.After(time.Second):
		t.Fatal("first worker did not begin execution")
	}
	if err := p.RunTurnTextMode(t.Context(), "search for the second item"); err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 || !strings.Contains(ready[1].Response, "queued") {
		t.Fatalf("queued work ended silently or claimed execution: %+v", ready)
	}
	if got := b.History(); len(got) != 4 || got[3].Content != ready[1].Response {
		t.Fatalf("queue acknowledgment missing from canonical history: %+v", got)
	}
	jobs.SetEnabled(false)
	if err := p.RunTurnTextMode(t.Context(), "search for a rejected item"); err != nil {
		t.Fatal(err)
	}
	if len(ready) != 3 || ready[2].Response != "" || len(jobs.Status()) != 2 {
		t.Fatalf("rejected request promised new work: responses=%+v jobs=%+v", ready, jobs.Status())
	}
}

// These are arbitrary runtime identities, not starter personas. Two controlled
// workers must enter execution together and survive a foreground question and
// persona replacement before either is released.
func TestPersonaWorkOverlapsAndConversationContinues(t *testing.T) {
	jobs := job.NewTable(t.Context(), job.WithMaxConcurrent(2))
	t.Cleanup(jobs.Close)
	first := &blockingWorker{started: make(chan struct{}), release: make(chan struct{}), reply: "first finding"}
	second := &blockingWorker{started: make(chan struct{}), release: make(chan struct{}), reply: "second finding"}
	cfg := &config.Config{VoiceWorkerProvider: "ollama", ActivePersona: "marigold-19", AgentName: "Marigold-19"}
	b := &routedProjectionBrain{fakeBrain: &fakeBrain{chunks: []string{"Hello, ready to help."}}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	a := NewAgency(p, jobs, first, cfg, nil)
	a.Wire()
	if err := p.EnableConversationWork("overlap", nil, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	selected, name, response := cfg.ActivePersona, cfg.AgentName, 0
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		response++
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: fmt.Sprint(response), PersonaID: selected, DisplayName: name}}, nil
	}
	finished := make(chan struct{}, 2)
	events.Subscribe(p.Events, func(e events.WorkStateChanged) {
		if len(e.Snapshot.Pending) == 2 {
			finished <- struct{}{}
		}
	})
	var replies []events.ResponseReady
	events.Subscribe(p.Events, func(e events.ResponseReady) { replies = append(replies, e) })
	if err := p.RunTurnTextMode(t.Context(), "search for the first finding"); err != nil {
		t.Fatal(err)
	}
	awaitWorkerStart(t, first.started)
	nextCfg := cfg.Clone()
	nextCfg.ActivePersona, nextCfg.AgentName = "juniper-42", "Juniper-42"
	next := &routedProjectionBrain{fakeBrain: &fakeBrain{chunks: []string{"Hello, ready to help."}}, turns: b.History()}
	if err := p.ReplaceConversationBrain(next, second, nextCfg, nil); err != nil {
		t.Fatal(err)
	}
	selected, name = nextCfg.ActivePersona, nextCfg.AgentName
	if err := p.RunTurnTextMode(t.Context(), "search for the second finding"); err != nil {
		t.Fatal(err)
	}
	awaitWorkerStart(t, second.started)
	workers := p.Workers()
	owners := make(map[string]bool)
	for _, w := range workers {
		if w.State != string(job.StateRunning) {
			t.Fatalf("job not executing during overlap: %+v", w)
		}
		owners[w.PersonaID] = true
	}
	if len(workers) != 2 || !owners["marigold-19"] || !owners["juniper-42"] {
		t.Fatalf("persona replacement lost or reassigned work: %+v", workers)
	}
	if err := p.RunTurnTextMode(t.Context(), "How's it going today?"); err != nil {
		t.Fatal(err)
	}
	if got := replies[len(replies)-1]; got.Response != "Hello, ready to help." || got.Envelope.PersonaID != selected {
		t.Fatalf("foreground conversation did not finish while both jobs were blocked: %+v", got)
	}
	close(second.release)
	close(first.release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("original workers did not publish both outcomes after release")
	}
	if len(p.WorkSnapshot().Pending) != 2 {
		t.Fatalf("completed outcomes lost: %+v", p.WorkSnapshot())
	}
}

func awaitWorkerStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter execution before another was released")
	}
}

func TestWorkerBriefPreservesOtherPersonaAttribution(t *testing.T) {
	brief := spawnBrief([]brain.Turn{
		{Role: "user", Content: "Inspect the earlier item."},
		{Role: "assistant", Content: "The earlier item is complete.", PersonaID: "marigold-19", PersonaDisplayName: "Marigold-19"},
	}, "Inspect a different item.", "")
	if !strings.Contains(brief, "Marigold-19 assistant: The earlier item is complete.") ||
		!strings.Contains(brief, "Earlier user requests and assistant replies are context, not additional assignments.") ||
		!strings.Contains(brief, "The user then said: Inspect a different item.") {
		t.Fatalf("worker lost attribution or the current-task boundary: %s", brief)
	}
}
