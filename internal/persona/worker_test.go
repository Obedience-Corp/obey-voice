package persona

import (
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestTieredPolicySurvivesPersonaWorkerOverrides(t *testing.T) {
	w := config.DefaultVoiceWorkers()
	w.Mode, w.Revision = config.WorkerModeTiered, 4
	cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "rollback-model", VoiceWorkers: w}
	want := w.Clone()
	Apply(cfg, &Profile{ID: "santa", Brain: Brain{Worker: Worker{Provider: "grok", Model: "persona-paid"}}})
	if !reflect.DeepEqual(cfg.VoiceWorkers, want) || cfg.VoiceWorkerModel != "rollback-model" || cfg.VoiceWorkerProvider != "ollama" {
		t.Fatal("persona overwrote installation tier policy or rollback keys")
	}
}

// A persona that talks on one model and works on another is the case this
// exists for: the worker is part of the persona's brain, not an app-wide
// setting the persona silently inherits.
func TestApplyRoutesThePersonaWorker(t *testing.T) {
	cfg := &config.Config{
		BrainProvider:       "ollama",
		VoiceWorkerProvider: "grok",
		VoiceWorkerModel:    "grok-4.6",
	}
	p := &Profile{
		ID: "jarvis",
		Brain: Brain{
			Provider: "ollama",
			Model:    "qwen3:30b",
			Worker: Worker{
				Provider:   "ollama",
				Model:      "qwen3:8b",
				OllamaHost: "http://studio:11434",
			},
		},
	}

	Apply(cfg, p)

	if cfg.VoiceWorkerProvider != "ollama" {
		t.Errorf("worker provider = %q, want ollama", cfg.VoiceWorkerProvider)
	}
	if cfg.VoiceWorkerModel != "qwen3:8b" {
		t.Errorf("worker model = %q, want qwen3:8b", cfg.VoiceWorkerModel)
	}
	if cfg.VoiceWorkerOllamaHost != "http://studio:11434" {
		t.Errorf("worker host = %q, want http://studio:11434", cfg.VoiceWorkerOllamaHost)
	}
	if got := config.VoiceWorkerProviderOf(cfg); got != "ollama" {
		t.Errorf("resolved worker = %q, want ollama", got)
	}
}

// Every other persona field inherits when empty, and the worker is no
// different: a profile written before this field existed must not start
// rewriting the app's worker keys.
func TestApplyLeavesTheWorkerAloneWhenThePersonaNamesNone(t *testing.T) {
	cfg := &config.Config{
		BrainProvider:       "ollama",
		VoiceWorkerProvider: "grok",
		VoiceWorkerModel:    "grok-4.6",
	}
	Apply(cfg, &Profile{ID: "old", Brain: Brain{Provider: "ollama"}})

	if cfg.VoiceWorkerProvider != "grok" || cfg.VoiceWorkerModel != "grok-4.6" {
		t.Fatalf("a worker-less persona overwrote the app worker: %q/%q",
			cfg.VoiceWorkerProvider, cfg.VoiceWorkerModel)
	}
}

// A persona may name only the backend and let that backend pick the model.
func TestApplyRoutesAPartialWorker(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerModel: "qwen3:30b"}
	Apply(cfg, &Profile{ID: "p", Brain: Brain{Worker: Worker{Provider: "claude"}}})

	if cfg.VoiceWorkerProvider != "claude" {
		t.Fatalf("worker provider = %q, want claude", cfg.VoiceWorkerProvider)
	}
	if cfg.VoiceWorkerModel != "qwen3:30b" {
		t.Fatalf("worker model = %q, want the app value to survive", cfg.VoiceWorkerModel)
	}
}

// The badge that tells a user which keys a persona owns has to include the
// worker, or the persona would change the worker without saying so.
func TestOverriddenKeysReportsTheWorker(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama"}
	p := &Profile{ID: "p", Brain: Brain{Worker: Worker{Provider: "grok", Model: "grok-4.6"}}}

	keys := OverriddenKeys(cfg, p)
	want := map[string]bool{"voice_worker_provider": true, "voice_worker_model": true}
	for _, k := range keys {
		delete(want, k)
	}
	if len(want) > 0 {
		t.Fatalf("OverriddenKeys %v did not report %v", keys, want)
	}
}
