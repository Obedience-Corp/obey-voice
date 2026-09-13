package personaruntime

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestSelectionPreservesNewTierPolicyWithoutConstructingLegacyWorker(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	a := pipeline.NewAgency(f.pipeline, jobs, nil, f.base, nil)
	a.Wire()
	workers := config.DefaultVoiceWorkers()
	workers.Mode, workers.Revision = config.WorkerModeTiered, 5
	if err := a.ApplyWorkerSettings(workers); err != nil {
		t.Fatal(err)
	}
	f.selector.opts.NewWorker = func(context.Context, *config.Config) (brain.Provider, error) {
		t.Error("persona switch constructed a legacy worker in tiered mode")
		return nil, nil
	}
	if _, err := f.selector.Select(context.Background(), "santa", "explicit"); err != nil {
		t.Fatal(err)
	}
	if f.selector.Snapshot().PersonaID != "santa" {
		t.Fatal("selection did not change responder")
	}
	if a.WorkerSettings().Revision != 5 || f.selector.Config().VoiceWorkers.Revision != 5 {
		t.Fatal("selection restored obsolete policy")
	}
}

func TestTierSaveDoesNotReloadResponderAtNextBoundary(t *testing.T) {
	f := newSelectorTestFixture(t)
	defer f.close(t)
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	a := pipeline.NewAgency(f.pipeline, jobs, nil, f.base, nil)
	a.Wire()
	w := config.DefaultVoiceWorkers()
	w.Mode, w.Revision = config.WorkerModeTiered, 1
	if err := a.ApplyWorkerSettings(w); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Select(context.Background(), "samantha", "response_boundary"); err != nil {
		t.Fatal(err)
	}
	before := f.pipeline.Brain
	w.Revision++
	w.Policy.QueuedWorkers = 4
	if err := a.ApplyWorkerSettings(w); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Select(context.Background(), "samantha", "response_boundary"); err != nil {
		t.Fatal(err)
	}
	if f.pipeline.Brain != before {
		t.Fatal("worker settings recreated responder resources")
	}
}
