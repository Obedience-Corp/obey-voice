package personaruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

type privateContextBrain struct {
	*selectorTestBrain
	archive   []continuity.Entry
	highWater int
}

func (b *privateContextBrain) ContinuitySnapshot() brain.ContinuitySnapshot {
	snapshot := b.selectorTestBrain.ContinuitySnapshot()
	snapshot.Archive = append([]continuity.Entry(nil), b.archive...)
	snapshot.ContextHighWater = b.highWater
	return snapshot
}
func (b *privateContextBrain) ImportContinuity(snapshot brain.ContinuitySnapshot) error {
	if err := b.selectorTestBrain.ImportContinuity(snapshot); err != nil {
		return err
	}
	b.archive = append([]continuity.Entry(nil), snapshot.Archive...)
	b.highWater = snapshot.ContextHighWater
	return nil
}

func TestPersonaContextsRestoreOwnMemoryAndReconcileSharedTurns(t *testing.T) {
	alphaProfile, betaProfile := testProfile("alpha", "Alpha", "alpha"), testProfile("beta", "Beta", "beta")
	for _, profile := range []struct {
		model  string
		target **config.WorkerTierOverrides
	}{{"alpha-worker", &alphaProfile.Brain.WorkerTiers}, {"beta-worker", &betaProfile.Brain.WorkerTiers}} {
		*profile.target = &config.WorkerTierOverrides{Simple: &config.WorkerTierOverride{Assignment: &config.WorkerAssignment{Provider: "ollama", Model: profile.model, Billing: config.WorkerBillingLocal}}}
	}
	f := newSelectorTestFixture(t, alphaProfile, betaProfile)
	globalPolicy := config.DefaultVoiceWorkers()
	f.selector.opts.Base.VoiceWorkers = globalPolicy.Clone()
	checkWorkers := func(want string) {
		t.Helper()
		cfg := f.selector.Config()
		if cfg.PersonaWorkerTiers.Simple.Assignment.Model != want || !reflect.DeepEqual(cfg.VoiceWorkers, globalPolicy) {
			t.Fatalf("worker ownership changed: %+v", cfg.PersonaWorkerTiers)
		}
	}
	defer f.close(t)
	initial := &privateContextBrain{selectorTestBrain: f.old, archive: []continuity.Entry{{ID: "alpha-note", Summary: "alpha private memory"}}, highWater: 8192}
	initial.working = []brain.Turn{{Role: "assistant", Content: "alpha compact seed"}}
	f.pipeline.Brain = initial
	factory := f.selector.opts.NewBrain
	f.selector.opts.NewBrain = func(ctx context.Context, cfg *config.Config) (brain.Provider, error) {
		provider, err := factory(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return &privateContextBrain{selectorTestBrain: provider.(*selectorTestBrain)}, nil
	}
	f.selector.opts.PersistContexts = func(turns []brain.Turn, selected events.PersonaSelection, contexts brain.PersonaContexts) error {
		return f.store.SaveSelectionState(f.session, turns, selected, contexts)
	}
	if _, err := f.selector.Select(t.Context(), "beta", "test"); err != nil {
		t.Fatal(err)
	}
	checkWorkers("beta-worker")
	beta := f.pipeline.Brain.(*privateContextBrain)
	if len(beta.archive) != 0 || beta.highWater != 0 || !reflect.DeepEqual(beta.working, beta.history) {
		t.Fatal("new persona inherited foreign private context")
	}
	shared := brain.Turn{Role: "assistant", Content: "Beta public reply", PersonaID: "beta", PersonaDisplayName: "Beta", ResponseID: "beta-response"}
	beta.history = append(beta.history, shared)
	beta.working = []brain.Turn{{Role: "assistant", Content: "beta compact seed"}}
	beta.archive = []continuity.Entry{{ID: "beta-note", Summary: "beta private memory"}}
	beta.highWater = 16384
	if _, err := f.selector.Select(t.Context(), "alpha", "test"); err != nil {
		t.Fatal(err)
	}
	checkWorkers("alpha-worker")
	alpha := f.pipeline.Brain.(*privateContextBrain)
	wantWorking := []brain.Turn{{Role: "assistant", Content: "alpha compact seed"}, shared}
	if !reflect.DeepEqual(alpha.working, wantWorking) || alpha.archive[0].ID != "alpha-note" || alpha.highWater != 8192 {
		t.Fatalf("alpha context not restored: %+v", alpha.ContinuitySnapshot())
	}
	if len(alpha.history) != 2 || alpha.history[1].PersonaID != "beta" {
		t.Fatal("shared canonical attribution lost")
	}
	saved, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FormatVersion != 4 || len(saved.PersonaContexts) != 2 {
		t.Fatal("private contexts were not durably fenced")
	}
	// Resume uses the same selector transition with a fresh canonical projection.
	// The currently installed provider must not overwrite restored snapshots.
	f.selector.RestoreSession(saved.ID, "beta", "Beta", "old-hash")
	f.selector.RestoreContexts(saved.PersonaContexts)
	if _, err := f.selector.Select(t.Context(), "beta", "resume"); err != nil {
		t.Fatal(err)
	}
	checkWorkers("beta-worker")
	restored := f.pipeline.Brain.(*privateContextBrain)
	if len(restored.working) != 1 || restored.working[0].Content != "beta compact seed" || restored.archive[0].ID != "beta-note" || restored.highWater != 16384 {
		t.Fatalf("resume lost beta memory: %+v", restored.ContinuitySnapshot())
	}
	// Starting a new selector from disk must restore the same private state,
	// rather than relying on the previous selector's in-memory map.
	binding, err := f.catalog.ResolveBinding(f.selector.opts.Base, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := brain.PersonaPromptHash(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	resumedBrain := &privateContextBrain{selectorTestBrain: newSelectorTestBrain(hash, saved.Turns)}
	resumeOptions := f.selector.opts
	resumeOptions.Initial = binding
	resumeOptions.InitialContexts = saved.PersonaContexts
	resumeOptions.Pipeline = &pipeline.Pipeline{Brain: resumedBrain}
	restarted, err := NewSelector(resumeOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	defer resumedBrain.Close()
	if resumedBrain.working[0].Content != "alpha compact seed" || resumedBrain.archive[0].ID != "alpha-note" || resumedBrain.highWater != 8192 {
		t.Fatal("new selector expanded shared history instead of restoring own context")
	}
	// A failed commit may prepare a target projection, but cannot publish or
	// overwrite either in-memory or durable private context ownership.
	before := f.selector.ContextSnapshots()
	failure := errors.New("disk unavailable")
	f.selector.opts.PersistContexts = func([]brain.Turn, events.PersonaSelection, brain.PersonaContexts) error { return failure }
	if _, err := f.selector.Select(t.Context(), "alpha", "test"); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	if f.pipeline.Brain != restored || !reflect.DeepEqual(f.selector.ContextSnapshots(), before) {
		t.Fatal("failed selection changed private contexts")
	}
}
