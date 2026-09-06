package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
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
	agent   *voiceagent.Agent
	cleanup func()
	tts     *voiceagent.TTSSet
	live    *observing
	worker  *observing
	player  *recordingPlayer

	mu         sync.Mutex
	markStream int
	markFull   int
}

func newLiveRunner(ctx context.Context, cfg *config.Config, k knobs, bus *events.Bus, rec *recorder, player *recordingPlayer, silent bool, logf func(string, ...any)) (*liveRunner, error) {
	liveInner, err := brain.NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("live brain: %w", err)
	}
	r := &liveRunner{live: &observing{inner: liveInner}, player: player}

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
	r.worker = &observing{inner: workerInner}

	opts := voiceagent.Options{
		Config:   cfg,
		Events:   bus,
		Brain:    r.live,
		Worker:   r.worker,
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
	agent.OnRoute = rec.route
	return r, nil
}

func (r *liveRunner) close() {
	if r.cleanup != nil {
		r.cleanup()
		r.cleanup = nil
	}
	if r.tts != nil {
		r.tts.Close()
		r.tts = nil
	}
	r.closeBrains()
}

func (r *liveRunner) closeBrains() {
	if r.live != nil {
		_ = r.live.Close()
	}
	if r.worker != nil {
		_ = r.worker.Close()
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

func (r *liveRunner) history() []brain.Turn { return r.live.History() }

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
