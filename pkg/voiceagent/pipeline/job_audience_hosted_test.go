package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func TestReceivesJobOnAPersonaSentOnlyItsShare(t *testing.T) {
	onMuse := &config.Config{BrainProvider: "muse-spark", ActivePersona: "mira"}
	cases := []struct {
		name      string
		admission map[string]string
		want      bool
	}{
		{"its job", map[string]string{"persona": "mira"}, true},
		{"its job recorded on this backend", map[string]string{"persona": "mira", "brain": "muse-spark"}, true},
		{"its job recorded on Claude", map[string]string{"persona": "mira", "brain": "claude"}, true},
		{"its job recorded on Ollama", map[string]string{"persona": "mira", "brain": "ollama"}, true},
		{"its job recorded with the backend unset", map[string]string{"persona": "mira", "brain": ""}, true},
		{"another persona's job", map[string]string{"persona": "ember"}, false},
		{"another persona's job recorded on this backend", map[string]string{"persona": "ember", "brain": "muse-spark"}, false},
		{"a job saved before jobs recorded their persona", map[string]string{"provider": "ollama"}, true},
		{"a job started outside a spawn", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{Brain: museSparkBrain(t)}
			NewAgency(p, job.NewTable(t.Context()), nil, onMuse, nil).Wire()
			if got := p.receivesJob(tc.admission); got != tc.want {
				t.Fatalf("receivesJob = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("a job started outside a spawn after another persona was live", func(t *testing.T) {
		p := &Pipeline{Brain: museSparkBrain(t)}
		NewAgency(p, job.NewTable(t.Context()), nil, &config.Config{BrainProvider: "ollama", ActivePersona: "ember"}, nil).Wire()
		p.Agency.ReplaceWorker(nil, onMuse, nil)
		if p.receivesJob(nil) {
			t.Fatal("Mira was told about a job Ember may have started")
		}
	})
}

func TestJobFollowsThePersonaThatStartedItAcrossBackends(t *testing.T) {
	onClaude := &config.Config{BrainProvider: "claude", ActivePersona: "mira"}
	p := &Pipeline{Brain: &brain.Brain{}}
	NewAgency(p, job.NewTable(t.Context()), nil, &config.Config{BrainProvider: "ollama", ActivePersona: "ember"}, nil).Wire()
	p.Agency.ReplaceWorker(nil, onClaude, nil)
	miras, embers := frozenConfig(onClaude), map[string]string{"persona": "ember"}
	for _, step := range []struct {
		backend string
		live    brain.Provider
		ember   bool
	}{
		{"claude", &brain.Brain{}, false},
		{"muse-spark", museSparkBrain(t), false},
		{"ollama", &fakeBrain{}, true},
	} {
		p.Brain = step.live
		p.Agency.ReplaceWorker(nil, &config.Config{BrainProvider: step.backend, ActivePersona: "mira"}, nil)
		if !p.receivesJob(miras) {
			t.Fatalf("Mira on %s did not receive the job she started on Claude", step.backend)
		}
		if got := p.receivesJob(embers); got != step.ember {
			t.Fatalf("Mira on %s receives Ember's job = %v, want %v", step.backend, got, step.ember)
		}
	}
}

func TestJobStatusNamesTheJobsOfAClaudeOnlyInstallWithActivePersonaUnset(t *testing.T) {
	h := newBudgetHarness(t, job.WithMaxConcurrent(2))
	defer close(h.release)
	h.agency.pipeline.Brain = &brain.Brain{}
	h.agency.ReplaceWorker(gatedHookWorker{release: h.release}, &config.Config{BrainProvider: "claude"}, nil)
	h.spawn("audit the config")
	select {
	case <-h.tool:
	case <-time.After(3 * time.Second):
		t.Fatal("the job never reached a tool")
	}
	h.agency.HandleRoute(context.Background(), h.turn, h.metrics, RouteDecision{Kind: RouteJobStatus, Transcript: "status", Source: "test"})
	calls := h.tts.Calls()
	if got, want := calls[len(calls)-1], "Still working on audit the config. So far: listed 1 folder (1 unknown)."; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}
