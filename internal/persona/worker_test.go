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

func TestWorkerSelectionSurvivesSaveReloadAndApply(t *testing.T) {
	setConfigDir(t, t.TempDir())
	if err := Write(&Profile{Schema: Schema, ID: "guide", DisplayName: "Guide", Prompts: PromptRefs{Persona: "guide"}}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []Worker{
		{Provider: "codex", Model: "fixture-codex"},
		{Provider: "ollama", Model: "fixture-local", OllamaHost: "http://worker:11434"},
		{}, // Clearing the selection restores inheritance.
	} {
		if _, err := UpdateStack("guide", Brain{Provider: "ollama", Worker: want}, TTS{}); err != nil {
			t.Fatal(err)
		}
		p, err := Load("guide")
		if err != nil {
			t.Fatal(err)
		}
		if p.Brain.Worker != want {
			t.Fatalf("saved worker = %+v, want %+v", p.Brain.Worker, want)
		}
		cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "global-worker"}
		Apply(cfg, p)
		if want.Provider == "" {
			if cfg.VoiceWorkerModel != "global-worker" {
				t.Fatal("cleared worker did not inherit")
			}
		} else if config.VoiceWorkerProviderOf(cfg) != want.Provider || cfg.VoiceWorkerModel != want.Model || cfg.VoiceWorkerOllamaHost != want.OllamaHost {
			t.Fatalf("runtime worker = %s/%s/%s, want %+v", config.VoiceWorkerProviderOf(cfg), cfg.VoiceWorkerModel, cfg.VoiceWorkerOllamaHost, want)
		}
	}
}

func TestPersonaTierOverridesSurviveSaveAndUnrelatedStackEdit(t *testing.T) {
	setConfigDir(t, t.TempDir())
	p := &Profile{Schema: Schema, ID: "guide", DisplayName: "Guide", Prompts: PromptRefs{Persona: "guide"}, Brain: Brain{WorkerTiers: &config.WorkerTierOverrides{Simple: &config.WorkerTierOverride{Assignment: &config.WorkerAssignment{Provider: "ollama", Model: "private-worker", Billing: config.WorkerBillingLocal}}}}}
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateStack("guide", Brain{Provider: "ollama", Model: "new-live"}, TTS{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load("guide")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Brain.WorkerTiers, p.Brain.WorkerTiers) {
		t.Fatal("unrelated edit lost worker tiers")
	}
	w := config.DefaultVoiceWorkers()
	w.Mode = config.WorkerModeTiered
	cfg := &config.Config{VoiceWorkers: w}
	Apply(cfg, loaded)
	if cfg.PersonaWorkerTiers.Simple.Assignment.Model != "private-worker" || cfg.VoiceWorkers.Tiers.Simple.Assignment != nil {
		t.Fatal("persona tier contaminated defaults or was ignored")
	}
	Apply(cfg, &Profile{ID: "other"})
	if cfg.PersonaWorkerTiers != nil {
		t.Fatal("previous persona override leaked")
	}
}

func TestWorkSettingSurvivesSaveReloadApplyAndUnrelatedStackEdits(t *testing.T) {
	setConfigDir(t, t.TempDir())
	if err := Write(&Profile{Schema: Schema, ID: "mira", DisplayName: "Mira", Prompts: PromptRefs{Persona: "mira"}}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{config.WorkWorker, config.WorkOwn, ""} {
		if _, err := UpdateStack("mira", Brain{Provider: "claude", Work: want, Worker: Worker{Provider: "ollama"}}, TTS{}); err != nil {
			t.Fatal(err)
		}
		p, err := Load("mira")
		if err != nil {
			t.Fatal(err)
		}
		if p.Brain.Work != want {
			t.Fatalf("saved work = %q, want %q", p.Brain.Work, want)
		}
		cfg := &config.Config{PersonaWork: "stale"}
		Apply(cfg, p)
		if cfg.PersonaWork != want {
			t.Fatalf("bound work = %q, want %q", cfg.PersonaWork, want)
		}
		// The profile names its own worker, so an empty setting is worker.
		wantEffective := want
		if want == "" {
			wantEffective = config.WorkWorker
		}
		if got := config.WorkOf(cfg); got != wantEffective {
			t.Fatalf("effective work = %q, want %q", got, wantEffective)
		}
	}
	bad := &Profile{Schema: Schema, ID: "mira", DisplayName: "Mira", Prompts: PromptRefs{Persona: "mira"}, Brain: Brain{Work: "decide"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("an unknown work value validated")
	}
}

// Naming a worker in the persona's profile makes worker its default; a
// worker chosen only in Settings does not.
func TestAPersonaThatNamesItsWorkerDefaultsToWorker(t *testing.T) {
	global := func() *config.Config {
		return &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "global:8b", PersonaNamesWorker: true}
	}
	for _, tc := range []struct {
		name   string
		worker Worker
		work   string
		want   string
	}{
		{"no worker of its own", Worker{}, "", config.WorkOwn},
		{"a worker provider", Worker{Provider: "codex"}, "", config.WorkWorker},
		{"only a worker model", Worker{Model: "qwen3:8b"}, "", config.WorkWorker},
		{"only a worker host", Worker{OllamaHost: "http://box:11434"}, "", config.WorkWorker},
		{"a worker but set to own", Worker{Provider: "codex"}, config.WorkOwn, config.WorkOwn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := global()
			Apply(cfg, &Profile{Schema: Schema, ID: "mira", DisplayName: "Mira", Brain: Brain{Provider: "claude", Work: tc.work, Worker: tc.worker}})
			if got := config.WorkOf(cfg); got != tc.want {
				t.Fatalf("work = %q (%s), want %q", got, config.WorkReason(cfg), tc.want)
			}
		})
	}
}
