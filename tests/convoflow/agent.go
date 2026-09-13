package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// runProfile is what the run actually used: the report header and
// config.resolved.yaml carry these knobs, never the whole Config (it holds
// credentials and the run dir is a cache, not a vault).
type runProfile struct {
	Profile        string `yaml:"profile" json:"profile"`
	LiveProvider   string `yaml:"live_provider" json:"live_provider"`
	LiveModel      string `yaml:"live_model" json:"live_model"`
	LiveHost       string `yaml:"live_host,omitempty" json:"live_host,omitempty"`
	WorkerProvider string `yaml:"worker_provider" json:"worker_provider"`
	WorkerModel    string `yaml:"worker_model" json:"worker_model"`
	WorkerHost     string `yaml:"worker_host,omitempty" json:"worker_host,omitempty"`
	Input          string `yaml:"input" json:"input"`
	RouterTools    string `yaml:"router_tools" json:"router_tools"`
	ToolsEnabled   bool   `yaml:"voice_tools_enabled" json:"voice_tools_enabled"`
	TTSProvider    string `yaml:"tts_provider" json:"tts_provider"`
	TTSVoice       string `yaml:"tts_voice,omitempty" json:"tts_voice,omitempty"`
	WorkDir        string `yaml:"work_dir" json:"work_dir"`
	FixtureConfig  string `yaml:"fixture_config,omitempty" json:"fixture_config,omitempty"`
	PersonaDir     string `yaml:"persona_dir,omitempty" json:"persona_dir,omitempty"`
}

func (p runProfile) silent() bool { return strings.EqualFold(p.TTSProvider, "none") }

// resolveConfig overlays the run's knobs on a copy of the operator's config.
// The knobs are first made effective with withScenario (CLI > non-empty
// YAML), the same values probeOllama was given; the live fields then fall
// back to the operator's config. The two worker fields never do: an empty
// value means "inherit the live model / host", which is what the probe
// checked, and a pinned voice_worker_model in the operator's file must not
// put a model into the run that the probe never looked for.
func resolveConfig(base *config.Config, scn *Scenario, k knobs, runDir string) (*config.Config, runProfile, error) {
	if base == nil {
		return nil, runProfile{}, fmt.Errorf("resolveConfig: nil base config")
	}
	k = k.withScenario(scn)
	local := *base
	local.BrainProvider = firstNonEmpty(k.liveProvider, local.BrainProvider)
	local.OllamaModel = firstNonEmpty(k.liveModel, local.OllamaModel)
	local.OllamaHost = firstNonEmpty(k.ollamaHost, local.OllamaHost)
	if !k.workerIsStub() {
		local.VoiceWorkerProvider = firstNonEmpty(k.workerProvider, local.VoiceWorkerProvider)
	}
	local.VoiceWorkerModel = k.workerModel
	local.VoiceWorkerOllamaHost = k.workerOllamaHost

	if strings.EqualFold(strings.TrimSpace(os.Getenv("VOICE_TOOLS_ENABLED")), "false") {
		return nil, runProfile{}, skipError{"VOICE_TOOLS_ENABLED=false in the environment; the gate cannot prove legacy tools on the worker"}
	}
	local.VoiceToolsEnabled = true
	local.VoiceRouterTools = string(brain.RouterToolsRouteOnly)
	if strings.EqualFold(scn.RouterTools, string(brain.RouterToolsLegacy)) {
		local.VoiceRouterTools = string(brain.RouterToolsLegacy)
	}

	workDir := filepath.Join(runDir, "work")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, runProfile{}, fmt.Errorf("work dir: %w", err)
	}
	local.WorkDir = workDir

	if scn.TTS.Provider != "" {
		local.TTSProvider = scn.TTS.Provider
	}
	if scn.TTS.Voice != "" {
		local.TTSVoice = scn.TTS.Voice
	}

	prof := runProfile{
		LiveProvider:   local.BrainProvider,
		LiveModel:      local.OllamaModel,
		WorkerProvider: local.VoiceWorkerProvider,
		WorkerModel:    local.VoiceWorkerModel,
		Input:          scn.Input,
		RouterTools:    local.VoiceRouterTools,
		ToolsEnabled:   local.VoiceToolsEnabled,
		TTSProvider:    local.TTSProvider,
		TTSVoice:       local.TTSVoice,
		WorkDir:        workDir,
	}
	liveOllama := strings.EqualFold(local.BrainProvider, "ollama")
	if liveOllama {
		prof.LiveHost = local.OllamaHost
	}
	switch {
	case k.workerIsStub():
		prof.Profile = "stub"
		prof.WorkerProvider = "stub"
		prof.WorkerModel = ""
	case strings.EqualFold(local.VoiceWorkerProvider, "ollama"):
		if prof.WorkerModel == "" {
			prof.WorkerModel = local.OllamaModel
		}
		prof.WorkerHost = firstNonEmpty(local.VoiceWorkerOllamaHost, local.OllamaHost)
		prof.Profile = "api-worker"
		if liveOllama {
			prof.Profile = "local"
		}
	default:
		prof.Profile = "api-worker"
	}
	return &local, prof, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func writeResolvedConfig(runDir string, prof runProfile) error {
	data, err := yaml.Marshal(prof)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "config.resolved.yaml"), data, 0o644)
}

// liveRunner drives a production voiceagent.Agent: brain.NewProvider for the
// live brain, brain.NewWorkerProvider for the worker (the same-daemon path
// PR #119 opened), both wrapped so the harness can read the StreamOptions
// each was given. Construction order follows the design: providers, then
// the TTS set (checked here — Available is a method on a built provider),
// then New with everything injected.
type liveRunner struct {
	agent *voiceagent.Agent
	// cfg is the run's private snapshot. WAV replay must use the same resolved
	// provider/model settings even if the caller's config is subsequently
	// changed while the long-lived agent is running.
	cfg            *config.Config
	cleanup        func()
	tts            *voiceagent.TTSSet
	voices         *voiceagent.LiveTTSManager
	live           *observing
	liveProvider   brain.Provider
	worker         *observing
	workerProvider brain.Provider
	player         *recordingPlayer
	persona        *personaRuntime
	observations   []*observing
	workerFactory  func(*config.Config) (brain.Provider, error)

	mu         sync.Mutex
	markStream int
	markFull   int
}

func newLiveRunner(ctx context.Context, cfg *config.Config, k knobs, bus *events.Bus, rec *recorder, player *recordingPlayer, silent bool, logf func(string, ...any)) (*liveRunner, error) {
	liveInner, err := brain.NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("live brain: %w", err)
	}
	live, liveObs := observeProvider(liveInner)
	configSnapshot := *cfg
	r := &liveRunner{cfg: &configSnapshot, live: liveObs, liveProvider: live, player: player, observations: []*observing{liveObs}}

	var workerInner brain.Provider
	if k.workerIsStub() {
		workerInner = newStubWorker()
	} else {
		workerInner, err = brain.NewWorkerProvider(cfg)
		if err != nil {
			r.closeBrains()
			return nil, fmt.Errorf("worker brain: %w", err)
		}
	}
	r.workerProvider, r.worker = observeProvider(workerInner)

	opts := voiceagent.Options{
		Config:   cfg,
		Events:   bus,
		Brain:    r.liveProvider,
		Worker:   r.workerProvider,
		TextOnly: true,
		Silent:   silent,
		Logf:     logf,
	}
	if !silent {
		set, err := voiceagent.NewTTSSet(cfg)
		if err != nil {
			r.closeBrains()
			return nil, skipError{fmt.Sprintf("tts: %v", err)}
		}
		if set.Primary == nil || !set.Primary.Available() {
			set.Close()
			r.closeBrains()
			return nil, skipError{fmt.Sprintf("tts provider %q is not available", cfg.TTSProvider)}
		}
		r.tts = set
		opts.TTS = set.Primary
		opts.TTSFallback = set.Fallback
		opts.Player = player
	}

	agent, cleanup, err := voiceagent.New(ctx, opts)
	if err != nil {
		r.close()
		return nil, fmt.Errorf("voiceagent.New: %w", err)
	}
	r.agent = agent
	r.cleanup = cleanup
	if r.tts != nil {
		r.voices = &voiceagent.LiveTTSManager{RetireDelay: -1}
		if !r.voices.Adopt(r.tts) {
			r.close()
			return nil, fmt.Errorf("persona TTS manager is closed")
		}
	}
	agent.OnRoute = rec.route
	return r, nil
}

func (r *liveRunner) close() {
	if r.persona != nil {
		r.persona.selector.Close()
		if r.agent != nil && r.agent.Pipeline != nil {
			r.persona.recordSaveError(r.agent.Pipeline.CloseWork())
		}
		if r.persona.checkpoint != nil {
			r.persona.recordSaveError(r.persona.checkpoint.Close())
			r.persona.checkpoint = nil
		}
	}
	if r.cleanup != nil {
		r.cleanup()
		r.cleanup = nil
	}
	if r.voices != nil {
		r.voices.Close()
		r.voices = nil
		r.tts = nil
	} else if r.tts != nil {
		r.tts.Close()
		r.tts = nil
	}
	r.closeBrains()
}

// runBackground runs the host-owned delivery turn on the serialized engine
// host. The worker itself has already run asynchronously; this call only
// claims a durable terminal result after flushing the current checkpoint.
func (r *liveRunner) runBackground(ctx context.Context) error {
	if r.agent == nil || r.agent.Pipeline == nil {
		return fmt.Errorf("background work: agent is not configured")
	}
	if r.persona != nil && r.persona.checkpoint != nil {
		if err := r.persona.checkpoint.Wait(ctx); err != nil {
			return fmt.Errorf("background checkpoint before claim: %w", err)
		}
	}
	return r.agent.Pipeline.RunBackgroundTurn(ctx)
}

func (r *liveRunner) waitWork(ctx context.Context, durable bool) error {
	if !durable || r.persona == nil || r.persona.checkpoint == nil {
		return nil
	}
	if err := r.persona.checkpoint.Wait(ctx); err != nil {
		return fmt.Errorf("background checkpoint after delivery: %w", err)
	}
	return nil
}

func (r *liveRunner) workSnapshot() work.Snapshot {
	if r.agent == nil {
		return work.Snapshot{}
	}
	return r.agent.WorkSnapshot()
}

func (r *liveRunner) closeBrains() {
	if r.liveProvider != nil {
		_ = brain.CloseProvider(r.liveProvider)
	}
	if r.workerProvider != nil {
		_ = brain.CloseProvider(r.workerProvider)
	}
}

// mark opens a turn window: liveCalled / workerCalled report calls made
// after it, so a skip-brain turn does not inherit the previous chat turn's
// policy and a worker still running from an earlier spawn does not count
// as this turn's.
func (r *liveRunner) mark() {
	s, _ := r.live.calls()
	_, f := r.worker.calls()
	r.mu.Lock()
	r.markStream, r.markFull = s, f
	r.mu.Unlock()
}

func (r *liveRunner) runTurn(ctx context.Context, user string) error {
	r.mark()
	return r.agent.RunTurnTextMode(ctx, user)
}

func (r *liveRunner) history() []brain.Turn {
	if r.agent != nil && r.agent.Pipeline != nil && r.agent.Pipeline.Brain != nil {
		return r.agent.Pipeline.Brain.History()
	}
	return r.liveProvider.History()
}

func (r *liveRunner) playing() bool { return r.player != nil && r.player.IsPlaying() }

func (r *liveRunner) livePolicy() string {
	return string(r.live.streamOptions().RouterToolsPolicy)
}

func (r *liveRunner) workerPolicy() string {
	return string(r.worker.fullOptions().RouterToolsPolicy)
}

func (r *liveRunner) workerTools() *bool {
	if !r.workerCalled() {
		return nil
	}
	v := r.worker.fullOptions().ToolsEnabled
	return &v
}

func (r *liveRunner) liveCalled() bool {
	s, _ := r.live.calls()
	r.mu.Lock()
	defer r.mu.Unlock()
	return s > r.markStream
}

func (r *liveRunner) workerCalled() bool {
	_, f := r.worker.calls()
	r.mu.Lock()
	defer r.mu.Unlock()
	return f > r.markFull
}

func (r *liveRunner) selectPersona(ctx context.Context, id string) error {
	if r.persona == nil {
		return fmt.Errorf("persona selector is not configured")
	}
	before := r.persona.selector.Snapshot()
	workBefore := r.workSnapshot()
	started := time.Now()
	s, err := r.persona.selector.Select(ctx, id, "ui")
	after := r.persona.selector.Snapshot()
	r.persona.recordSelectionTiming(started, before, after, "ui", err, workBefore)
	if err == nil {
		r.persona.recordSelection(s)
	}
	return err
}
func (r *liveRunner) personaState() personaTurnState {
	if r.persona == nil {
		return personaTurnState{}
	}
	s, _ := r.live.calls()
	provider := r.liveProvider
	if r.agent != nil && r.agent.Pipeline != nil && r.agent.Pipeline.Brain != nil {
		provider = r.agent.Pipeline.Brain
	}
	state := r.persona.state(provider, s)
	_, state.WorkerCalls = r.worker.calls()
	state.Work = r.agent.WorkSnapshot()
	return state
}
func (r *liveRunner) writePersonaArtifacts(dir string) error {
	if r.persona == nil {
		return nil
	}
	s, _ := r.live.calls()
	provider := r.liveProvider
	if r.agent != nil && r.agent.Pipeline != nil && r.agent.Pipeline.Brain != nil {
		provider = r.agent.Pipeline.Brain
	}
	return r.persona.writeArtifacts(dir, provider, []map[string]any{{"calls": s}})
}

// stopPlayback invokes the owning host cancellation control.
func (r *liveRunner) stopPlayback() {
	r.agent.Pipeline.StopPlayback()
}
