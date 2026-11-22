package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// spawnAs starts a job as a persona would, with the display name its
// acknowledgement carries.
func (h *budgetHarness) spawnAs(id, name, backend, task string) {
	h.agency.ReplaceWorker(gatedHookWorker{release: h.release}, &config.Config{ActivePersona: id, BrainProvider: backend, PersonaWork: config.WorkWorker}, nil)
	h.metrics.response.DisplayName = name
	h.spawn(task)
}

func (h *budgetHarness) awaitTool(t *testing.T) {
	t.Helper()
	select {
	case <-h.tool:
	case <-time.After(3 * time.Second):
		t.Fatal("the job never reached a tool")
	}
}

// The host writes the reply that starts a job, and that exchange leaves the
// conversation window while a long job is still running. The request has to
// carry the job itself or the persona answering denies having one.
func TestLiveRequestDescribesRunningAndQueuedJobs(t *testing.T) {
	h := newBudgetHarness(t)
	h.spawnAs("ember", "Ember", "ollama", "read the release notes")
	h.awaitTool(t)
	h.spawnAs("sol", "", "ollama", "tidy the diary")
	h.spawnAs("mira", "Mira", "ollama", "audit the config")

	facts := h.agency.pipeline.workerFacts(true)
	if len(facts) != 3 {
		t.Fatalf("facts = %+v, want the running job and both queued ones", facts)
	}
	own, other, unnamed := facts[0], facts[1], facts[2]
	if unnamed.Own || unnamed.Starter != "sol" || unnamed.State != "queued" {
		t.Fatalf("a job started without a display name should carry its persona id: %+v", unnamed)
	}
	if !own.Own || own.Starter != "" || own.State != "queued" || own.Task != "audit the config" || own.Running != 0 || own.Tier != "legacy" {
		t.Fatalf("the live persona's own job should come first, waiting: %+v", own)
	}
	if other.Own || other.Starter != "Ember" || other.State != "running" || other.Task != "read the release notes" || other.Running <= 0 ||
		!strings.Contains(other.Progress, "listed 1 folder") {
		t.Fatalf("another persona's running job: %+v", other)
	}
	if got := h.agency.pipeline.workerFacts(false); len(got) != 0 {
		t.Fatalf("a turn that answers no one was given the job list: %+v", got)
	}

	close(h.release)
	deadline := time.Now().Add(3 * time.Second)
	for h.agency.jobs.Active() {
		if time.Now().After(deadline) {
			t.Fatal("the jobs never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := h.agency.pipeline.workerFacts(true); len(got) != 0 {
		t.Fatalf("finished jobs are still described as active: %+v", got)
	}
}

func TestLiveRequestOnAHostedBackendDescribesOnlyItsOwnJobs(t *testing.T) {
	h := newBudgetHarness(t, job.WithMaxConcurrent(2))
	defer close(h.release)
	h.agency.pipeline.Brain = museSparkBrain(t)
	h.spawnAs("ember", "Ember", "ollama", "read my private tax notes")
	h.spawnAs("mira", "Mira", "muse-spark", "audit the config")
	h.awaitTool(t)
	h.awaitTool(t)

	facts := h.agency.pipeline.workerFacts(true)
	if len(facts) != 1 || !facts[0].Own || facts[0].Task != "audit the config" {
		t.Fatalf("facts = %+v, want only the job the live persona started", facts)
	}
}

func TestOnlyATurnAnsweringAPersonCarriesTheJobList(t *testing.T) {
	h := newBudgetHarness(t)
	defer close(h.release)
	live := &backgroundBrain{}
	p := h.agency.pipeline
	p.Brain = live
	h.spawn("audit the config")
	h.awaitTool(t)

	if err := p.RunTurnTextMode(context.Background(), "What have you been doing all this time?"); err != nil {
		t.Fatal(err)
	}
	if got := live.opts.WorkerFacts; len(got) != 1 || got[0].Task != "audit the config" || got[0].State != "running" {
		t.Fatalf("the reply to a person was not told about the running job: %+v", got)
	}
	live.opts = brain.StreamOptions{}
	p.STT, p.VAD, p.Capture = &fakeSTT{text: "And what is it doing now?"}, &fakeVAD{}, newFakeCapture()
	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := live.opts.WorkerFacts; !live.opts.VoiceMode || len(got) != 1 || got[0].Task != "audit the config" {
		t.Fatalf("the reply to a spoken question was not told about the running job: %+v", live.opts)
	}
	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !live.opts.Background || len(live.opts.WorkerFacts) != 0 {
		t.Fatalf("a result notification carried the job list: %+v", live.opts)
	}
}

// Every unfinished job reaches the renderer, which lists some and counts the
// rest; a job dropped here would be one the persona denies.
func TestLiveRequestIsGivenEveryUnfinishedJob(t *testing.T) {
	h := newBudgetHarness(t)
	defer close(h.release)
	for range 11 {
		h.spawn("audit the config")
	}
	h.awaitTool(t)
	if got := len(h.agency.pipeline.workerFacts(true)); got != 11 {
		t.Fatalf("facts = %d, want all 11 unfinished jobs", got)
	}
}

func TestRecentProgressKeepsTheNewestRows(t *testing.T) {
	for summary, want := range map[string]string{
		"":                             "",
		"read 3 files · ran 1 command": "read 3 files, ran 1 command",
		"read 3 files · ran 1 command · searched 2 times · ran 9 commands": "earlier work, then ran 1 command, searched 2 times, ran 9 commands",
	} {
		if got := recentProgress(summary); got != want {
			t.Errorf("recentProgress(%q) = %q, want %q", summary, got, want)
		}
	}
}
