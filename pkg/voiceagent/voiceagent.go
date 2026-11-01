// Package voiceagent assembles obey-voice's voice pipeline so it can be embedded.
//
// The CLI is one consumer of this package, not its owner. Everything a host
// application needs — brain, speech-to-text, text-to-speech, capture, playback —
// is constructed here from a config, and every piece can be replaced through
// Options.
//
// There is no API stability promise yet. That is deliberate: the surface is
// being learned by using it, and the first two users are obey-voice's own
// interactive and serve paths.
package voiceagent

import (
	"context"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

// Agent is a constructed voice pipeline.
//
// The embedded *pipeline.Pipeline is exposed on purpose. Hosts still reach for
// fields this constructor does not cover — speaker gating, turn callbacks, the
// compact hook — and hiding the pipeline behind a partial facade would mean
// inventing a pass-through method for each one before knowing which are actually
// part of the library's surface. Narrowing comes after the surface is known.
type Agent struct {
	*pipeline.Pipeline

	// Turn cancellation for Interrupt. turnSeq identifies the turn that owns
	// cancelTurn, so a finishing turn cannot clear a newer turn's cancel.
	mu         sync.Mutex
	cancelTurn context.CancelFunc
	turnSeq    uint64
}

// Options configures New. Every provider field is optional: nil means "build it
// from Config", which is what the CLI does.
//
// Injecting a provider is how a host supplies its own implementation, and how
// integration tests supply fakes without a build tag excluding the wiring they
// are meant to exercise.
type Options struct {
	// Config drives every default. Required.
	Config *config.Config

	// Events is the bus the pipeline publishes to. Required — a pipeline with
	// no bus is silent in the UI sense, which is never what a caller wants.
	Events *events.Bus

	// Brain, STT, TTS, TTSFallback and Player each replace the provider that
	// Config would otherwise select.
	//
	// An injected Brain assembled its prompts before New was called, so Env and
	// PromptsDir cannot reach it; New rejects those combinations rather than
	// accept an option it would ignore.
	Brain brain.Provider
	// Input replaces recognition with finalized modality-aware input. InputFactory
	// constructs that provider over the same finalized capture/VAD owner after
	// device setup. Neither path constructs a Standard recognizer. These fields
	// are mutually exclusive with each other and STT; injected lifetimes remain
	// the host's responsibility.
	Input        utterance.Provider
	InputFactory func(audio.FrameSource, *audio.VAD) (utterance.Provider, error)
	STT          stt.Provider
	TTS          tts.Provider
	TTSFallback  tts.Provider
	Player       audio.Engine

	// VAD replaces the config-built voice detector. Any implementation of
	// pipeline.VoiceDetector works — a host feeding audio from a browser or a
	// phone supplies its own, which is the extension point serve's Ingress
	// already proves is needed.
	VAD pipeline.VoiceDetector

	// Capture replaces the local microphone. Same reasoning: serve substitutes a
	// network ingress for a capture device, so the seam is demonstrated, not
	// speculative.
	Capture pipeline.CaptureMonitor

	// Worker replaces the async job brain New would construct from
	// voice_worker_provider. Nil means NewWorkerProvider(cfg). When Worker is
	// non-nil, WorkerErr is ignored. WorkerErr without a Worker records
	// Agency's construction failure without calling NewWorkerProvider — tests
	// and hosts that already failed construction.
	Worker    brain.Provider
	WorkerErr error

	// TextOnly skips capture, VAD and STT: the agent reads text turns instead of
	// listening. Silent skips TTS and playback: it thinks and writes but does not
	// speak. Both are false for a full voice agent.
	TextOnly bool
	Silent   bool

	// Env describes the machine the agent tells the model it is running on. A
	// zero Env falls back to the host, which is what the CLI wants. A server
	// answering for many users should set it: telling the model it is running as
	// whoever started the process is worse than saying nothing. Only reaches a
	// brain New builds from Config — rejected with an injected Brain.
	Env brain.Env

	// PromptsDir is where user prompt documents are read from. Empty falls back
	// to the configured directory, so an embedder can ship its own prompts from
	// anywhere without touching the host's config. Only reaches a brain New
	// builds from Config — rejected with an injected Brain.
	PromptsDir string

	// WorkDir is the directory the agent's file and shell tools start in, so an
	// embedder points the agent at a workspace without chdir'ing its host.
	// Empty falls back to the configured work_dir, then the process cwd.
	//
	// It reaches the tools of every provider: ollama runs them in-process and
	// resolves paths against this directory, while claude and grok receive it
	// as the cwd of the CLI subprocess that runs their own. All three are also
	// told about it in the system prompt's grounding block.
	//
	// A default, not a sandbox: a tool may still use an absolute path outside
	// it. Only reaches a brain New builds from Config — rejected with an
	// injected Brain.
	WorkDir string

	// Logf receives non-fatal setup diagnostics — a configured TTS fallback that
	// could not be loaded, for instance. **Nil discards them**, which is the
	// important half: a library that writes to a process stderr its caller does
	// not control is badly behaved. The CLI passes a function that writes to
	// stderr; a host may route them anywhere.
	Logf func(format string, args ...any)

	// LiveTTS, when set, takes ownership of the TTS set New constructs so a
	// later Install can retire the worker instead of stacking them until
	// shutdown. Injected TTS (Options.TTS) is still the caller's to Adopt.
	LiveTTS *LiveTTSManager

	// Stage names the step New is about to take — loading a model, opening the
	// microphone. **Nil discards them**, like Logf. A UI that makes the user
	// wait through this function has nothing else to show them: model loads
	// take seconds each, and a screen that says nothing reads as a hang.
	// Called on New's goroutine, before the step it names.
	Stage func(string)
}

// New builds a voice agent and returns it with a cleanup function.
//
// **The cleanup must be called, and its ordering is load-bearing.** Resources
// are released in reverse order of acquisition: the audio device closes after
// the models that feed it, the capture stops before the VAD it writes into.
// Every error path below runs cleanup before returning, so a partially built
// agent never leaks a device or a loaded model.
func New(ctx context.Context, opts Options) (*Agent, func(), error) {
	if opts.Config == nil {
		return nil, nil, fmt.Errorf("voiceagent: Config is required")
	}
	if opts.Events == nil {
		return nil, nil, fmt.Errorf("voiceagent: Events is required")
	}
	inputChoices := 0
	if opts.Input != nil {
		inputChoices++
	}
	if opts.InputFactory != nil {
		inputChoices++
	}
	if opts.STT != nil {
		inputChoices++
	}
	if inputChoices > 1 {
		return nil, nil, fmt.Errorf("voiceagent: Input, InputFactory and STT are mutually exclusive")
	}
	if opts.TextOnly && opts.InputFactory != nil {
		return nil, nil, fmt.Errorf("voiceagent: InputFactory requires capture mode")
	}
	// Env and PromptsDir only reach a brain New builds from Config; an injected
	// Brain assembled its prompts before New was called. Rejecting the
	// combination keeps both options impossible to set-and-ignore — the same
	// contract as an injected Capture requiring an injected STT.
	if opts.Brain != nil {
		if opts.Env != (brain.Env{}) {
			return nil, nil, fmt.Errorf("voiceagent: Options.Env cannot reach an injected Brain — give the brain its environment context directly")
		}
		if opts.PromptsDir != "" {
			return nil, nil, fmt.Errorf("voiceagent: Options.PromptsDir cannot reach an injected Brain — point the brain at its prompts directly")
		}
		if opts.WorkDir != "" {
			return nil, nil, fmt.Errorf("voiceagent: Options.WorkDir cannot reach an injected Brain — give the brain its working directory directly")
		}
	}
	cfg := opts.Config
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// An unrecognized barge_in_speakers value is a hard startup error (D7) —
	// see config.ValidateBargeInSpeakers for why a typo must not silently
	// become the more permissive "anyone".
	if err := config.ValidateBargeInSpeakers(cfg); err != nil {
		return nil, nil, fmt.Errorf("voiceagent: %w", err)
	}
	stage := opts.Stage
	if stage == nil {
		stage = func(string) {}
	}

	// PromptsDir, WorkDir and Env reach the brain through the config value it is
	// built from, so a copy is taken rather than mutating the caller's config —
	// a host that reuses one *Config across agents must not have it rewritten
	// underneath.
	if opts.PromptsDir != "" || opts.WorkDir != "" || opts.Env != (brain.Env{}) {
		local := *cfg
		if opts.PromptsDir != "" {
			local.PromptsDir = opts.PromptsDir
		}
		if opts.WorkDir != "" {
			local.WorkDir = opts.WorkDir
		}
		local.RuntimeEnvUser = opts.Env.User
		local.RuntimeEnvHostname = opts.Env.Hostname
		local.RuntimeEnvOS = opts.Env.OS
		cfg = &local
	}

	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	p := &pipeline.Pipeline{
		Events:                      opts.Events,
		VoiceToolsEnabled:           cfg.VoiceToolsEnabled,
		RouterToolsPolicy:           brain.NormalizeRouterToolsPolicy(cfg.VoiceRouterTools),
		VoiceFailedRouteRepair:      cfg.VoiceFailedRouteRepair,
		BargeInTailGuard:            config.BargeInTailGuardOf(cfg),
		BargeInSpeakerVerdictMaxAge: config.BargeInSpeakerVerdictMaxAgeOf(cfg),
		TTSLookahead:                config.TTSLookaheadOf(cfg),
		CapturePreRoll:              time.Duration(cfg.VADPreRollMS) * time.Millisecond,
	}

	if config.BargeInSpeakersOf(cfg) == config.BargeInSpeakersEnrolledOnly {
		// Hosts replace this with their live analyzer. Until then, unavailable
		// recognition must not silently downgrade enrolled_only to anyone.
		p.BargeInSpeakerVerdict = func() pipeline.SpeakerVerdict { return pipeline.SpeakerVerdict{} }
	}

	cleanups = append(cleanups, bindDecisions(p, cfg))

	// Brain — injected, or selected by config.
	if opts.Brain != nil {
		p.Brain = opts.Brain
	} else {
		stage("Connecting the brain")
		b, err := brain.NewProvider(cfg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init brain: %w", err)
		}
		p.Brain = b
		// Persona switches replace p.Brain; teardown owns the current provider.
		cleanups = append(cleanups, func() {
			if err := p.CloseBrains(); err != nil {
				fmt.Fprintf(os.Stderr, "obey-voice: brain cleanup: %v\n", err)
			}
		})
	}

	// TTS + Player (skipped in silent mode).
	if !opts.Silent {
		if opts.Player != nil {
			p.Player = opts.Player
		} else {
			player := playback.NewPlayerWithDevice(cfg.OutputDevice)
			player.SetVolume(config.PlaybackVolumeOf(cfg))
			cleanups = append(cleanups, func() { _ = player.Close() })
			p.Player = player
		}

		switch {
		case opts.TTS != nil:
			p.ReplaceTTS(opts.TTS, opts.TTSFallback)
		default:
			stage("Loading the voice")
			ttsSet, err := NewTTSSet(cfg)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init TTS: %w", err)
			}
			if opts.LiveTTS != nil {
				if !opts.LiveTTS.Adopt(ttsSet) {
					ttsSet.Close()
					cleanup()
					return nil, nil, fmt.Errorf("init TTS: conversation is shutting down")
				}
			} else {
				cleanups = append(cleanups, ttsSet.Close)
			}
			p.ReplaceTTS(ttsSet.Primary, ttsSet.Fallback)
			if ttsSet.VoiceNotice != nil {
				logf("warning: %v", ttsSet.VoiceNotice)
			}
			if ttsSet.FallbackWarning != nil {
				logf("warning: %v", ttsSet.FallbackWarning)
			}
		}
	}

	// Backchannel, if enabled: synthesizes its phrase pool once, here, so a turn
	// never pays for it. Off by default; see pipeline/backchannel.go.
	if cfg.BackchannelEnabled && !opts.Silent && p.HasTTS() {
		stage("Synthesizing backchannel phrases")
		p.EnableBackchannel(ctx)
	}

	// Stage tags are stripped. Recorded persona clips are not spliced into
	// TTS — they do not match the live mouth.
	if !opts.Silent {
		sounds := filepath.Join(config.ConfigDir(), "personas", cfg.ActivePersona, "sounds")
		p.EnableVocals(sounds)
	}

	// Capture + VAD + STT (skipped in text mode).
	if !opts.TextOnly {
		var frontend audio.Frontend
		if opts.Capture == nil {
			if front := config.VoiceFrontendOf(cfg); front != config.VoiceFrontendOff {
				built, err := NewVoiceFrontend(cfg)
				if err != nil {
					cleanup()
					return nil, nil, fmt.Errorf("init voice front-end: %w", err)
				}
				frontend = built
				cleanups = append(cleanups, func() { _ = frontend.Close() })
				if !cfg.BargeInEnabled {
					switch front {
					case config.VoiceFrontendNLMS:
						logf("warning: voice_frontend=nlms without barge-in; the noise suppressor over-gates normal speech so VAD starts late and STT hears fragments")
					case config.VoiceFrontendWebRTC:
						logf("warning: voice_frontend=webrtc without barge-in; echo cancellation runs but nothing consumes it")
					}
				}
			}
		}

		var localCapture *audio.Capture
		if opts.Capture != nil {
			// The host owns injected capture lifetime; New must neither start a
			// device nor stop the source during cleanup.
			p.Capture = opts.Capture
		} else {
			stage("Opening the microphone")
			localCapture = audio.NewCaptureWithDevice(cfg.InputDevice)
			// Every TUI entry point into the conversation goes through
			// tui.Run, which redirects file descriptor 2 into the diagnostics
			// log; the headless paths own stderr outright.
			localCapture.Logf = log.Printf
			if frontend != nil {
				localCapture.SetFrontend(frontend)
			}
			if err := localCapture.Start(ctx); err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("start capture: %w", err)
			}
			cleanups = append(cleanups, localCapture.Stop)
			p.Capture = localCapture
		}

		if !opts.Silent && frontend != nil {
			if player, ok := p.Player.(*playback.Player); ok {
				player.SetFrontend(frontend)
			}
		}

		// sttVAD is the CONCRETE detector, because stt.NewProvider still takes
		// *audio.VAD. An injected VoiceDetector satisfies the pipeline but cannot
		// be handed to the STT factory, so a caller injecting one is expected to
		// inject an STT provider too — which is exactly what serve does.
		var sttVAD *audio.VAD
		switch {
		case opts.VAD != nil:
			// Caller owns the lifetime of an injected detector, so it is not
			// added to cleanups — deleting a VAD the host still holds would be
			// a use-after-free waiting to happen.
			p.VAD = opts.VAD
			if concrete, ok := opts.VAD.(*audio.VAD); ok {
				sttVAD = concrete
			}
		case cfg.VADEnabled:
			stage("Loading voice activity detection")
			built, err := audio.NewVAD(cfg)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init VAD: %w", err)
			}
			cleanups = append(cleanups, built.Delete)
			p.VAD = built
			sttVAD = built
		}
		if p.VAD != nil {

			// Barge-in stays nil (watchBargeIn no-ops) unless explicitly enabled.
			if !opts.Silent && cfg.BargeInEnabled {
				bargeInVAD, err := audio.NewBargeInVAD(cfg)
				if err != nil {
					cleanup()
					return nil, nil, fmt.Errorf("init barge-in VAD: %w", err)
				}
				cleanups = append(cleanups, bargeInVAD.Delete)
				p.BargeInVAD = bargeInVAD
			}
		}

		if opts.Input != nil {
			p.Input = opts.Input
		} else if opts.InputFactory != nil {
			var source audio.FrameSource
			if localCapture != nil {
				endable := audio.NewFinalizable(localCapture)
				source = endable
				p.Utterance = endable
			} else {
				source, _ = opts.Capture.(audio.FrameSource)
			}
			if source == nil {
				cleanup()
				return nil, nil, fmt.Errorf("voiceagent: InputFactory requires a frame-capable capture")
			}
			input, err := opts.InputFactory(source, sttVAD)
			if err != nil || input == nil || !input.Available() {
				cleanup()
				return nil, nil, fmt.Errorf("init finalized input: %w", errors.Join(err, errors.New("input must be available")))
			}
			p.Input = input
		} else if opts.STT != nil {
			p.STT = opts.STT
		} else {
			if localCapture == nil {
				cleanup()
				return nil, nil, fmt.Errorf("voiceagent: an injected Capture requires an injected STT provider")
			}
			stage("Loading speech recognition")
			// The recogniser reads the microphone through a wrapper the
			// pipeline can finalize, so a talk key can end a turn the way
			// serve's voice_end ends a network one.
			endable := audio.NewFinalizable(localCapture)
			sttProvider, sttCleanup, err := stt.NewProvider(cfg, endable, sttVAD)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init STT: %w", err)
			}
			if sttCleanup != nil {
				cleanups = append(cleanups, sttCleanup)
			}
			p.STT = sttProvider
			p.Utterance = endable
		}
	} else if opts.Input != nil {
		p.Input = opts.Input
	} else if opts.STT != nil {
		// A caller that injects an STT provider in text mode means it: the
		// provider reads from somewhere other than a local microphone, which is
		// exactly what serve does with a network ingress.
		p.STT = opts.STT
	}

	jobs := job.NewTable(ctx,
		job.WithMaxConcurrent(cfg.VoiceWorkerMaxConcurrent),
		job.WithBudget(time.Duration(cfg.VoiceWorkerJobBudget)*time.Second),
	)
	var worker brain.Provider
	var workerErr error
	if cfg.VoiceWorkers != nil && cfg.VoiceWorkers.Mode == config.WorkerModeTiered {
		// Each tiered job constructs its own provider from its frozen assignment.
	} else if opts.Worker != nil {
		worker = opts.Worker
	} else if opts.WorkerErr != nil {
		workerErr = opts.WorkerErr
	} else if cfg.VoiceWorkEnabled && config.VoiceWorkerProviderOf(cfg) != "ollama" {
		workerErr = fmt.Errorf("experimental voice work refuses paid worker provider %q", config.VoiceWorkerProviderOf(cfg))
	} else {
		worker, workerErr = brain.NewWorkerProvider(cfg)
		if workerErr != nil {
			logf("warning: worker provider: %v", workerErr)
		}
	}
	agency := pipeline.NewAgency(p, jobs, worker, cfg, workerErr)
	agency.Wire()
	cleanups = append(cleanups, jobs.Close)
	cleanups = append(cleanups, func() { _ = agency.CloseWork() })

	return &Agent{Pipeline: p}, cleanup, nil
}
