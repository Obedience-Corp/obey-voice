//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/app"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	personaruntime "github.com/Obedience-Corp/obey-voice/internal/persona/runtime"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/internal/transcript"
	appTUI "github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/internal/ui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

var (
	textMode          bool
	noVoice           bool
	skipTUI           bool
	debugAudioDir     string
	transcriptLogPath string
)

var rootCmd = &cobra.Command{
	Use:   "obey-voice",
	Short: "Give Claude a voice",
	Long:  "Obey Voice — ultra-low-latency voice assistant for AI coding, inspired by Her.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// serve outlives the app that spawned it, so its broken-pipe guard is
		// installed in the earliest hook that runs — this one writes below
		// (--debug-audio, prompt seeding) and fang renders usage before RunE.
		// Other commands keep the default disposition, where a `| head` still
		// ends the pipeline.
		if cmd.Name() == serveCommandName {
			dropBrokenPipeSignals()
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		// Resolve the agent's working directory once, before anything can
		// chdir, and stamp it onto every config this process loads. No chdir
		// happens here: every command must still resolve --debug-audio,
		// --log-file and `narrate <file>` against the caller's cwd. The two
		// interactive surfaces (root's RunE and serve) enter the directory
		// themselves, after pinning their own relative path flags.
		resolvedWorkDir = config.WorkDirFrom(cfg)
		debugDir := debugAudioDir
		if debugDir == "auto" {
			debugDir = filepath.Join(config.ConfigDir(), "debug", "audio")
		}
		if err := audio.SetDebugAudioDir(debugDir); err != nil {
			return err
		}
		if debugDir != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "  Audio debug capture enabled under %s\n", debugDir)
		}
		// Best-effort: seed default prompt documents so users have editable
		// starting files. A failure here (e.g. a read-only home) must not
		// block commands — resolution falls back to the embedded defaults.
		if shouldSeedPrompts(cmd) {
			if _, err := prompts.Seed(config.PromptsDir()); err != nil {
				fmt.Fprintf(os.Stderr, "obey-voice: seeding prompt defaults: %v\n", err)
			}
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		stampWorkDir(cfg)
		// --transcript-log is opened later, inside the runtime builder or
		// startPipeline — both after the chdir — so pin it to the caller's cwd.
		absPathFlag(&transcriptLogPath)
		if err := enterWorkDir(cfg.WorkDir); err != nil {
			return err
		}

		// The TUI serves interactive invocations including --text (voice
		// input off, D3). Non-TTY, --no-tui, and --no-voice without --text
		// keep the plain stdout loop (scripts / demos without mic).
		if useConversationTUI() {
			return appTUI.RunWithMeeting(cfg, conversationRuntimeBuilder(nil), meetingRuntimeBuilder())
		}

		return startPipeline(cfg, nil)
	},
}

// useConversationTUI is true for a terminal session that should open the
// Bubble Tea UI. --text enters the TUI even with --no-voice so demos and
// typing-only use still get the conversation screen (and tool visibility).
func useConversationTUI() bool {
	if skipTUI || !stdinIsTerminal() {
		return false
	}
	// Plain --no-voice (no --text): headless stdout loop for scripts.
	if noVoice && !textMode {
		return false
	}
	return true
}

func shouldSeedPrompts(cmd *cobra.Command) bool {
	return cmd.CommandPath() != "obey-voice config migrate"
}

// personaOverride is --persona: a process-scoped identity override. It is
// deliberately not persisted — `obey-voice persona use` is the persisting path —
// so trying a persona never edits config.yaml behind the user's back.
var personaOverride string

func init() {
	rootCmd.Flags().BoolVarP(&textMode, "text", "t", false, "Text-only input mode (no microphone)")
	rootCmd.Flags().BoolVarP(&noVoice, "no-voice", "n", false, "Disable TTS output")
	rootCmd.Flags().BoolVar(&skipTUI, "no-tui", false, "Skip TUI launcher, start directly")
	rootCmd.Flags().StringVar(&personaOverride, "persona", "", "Run as this persona for this process only (never persisted; see `obey-voice persona use` to persist)")
	rootCmd.PersistentFlags().StringVar(&debugAudioDir, "debug-audio", "", "Record TTS source WAVs, exact device-output WAV, and callback timing (optional DIR)")
	rootCmd.PersistentFlags().Lookup("debug-audio").NoOptDefVal = "auto"
	rootCmd.PersistentFlags().StringVar(&transcriptLogPath, "transcript-log", "", "Append conversation events as JSONL to FILE (harnesses, field debugging)")
}

// attachTranscriptLog starts the JSONL event tap when --transcript-log is
// set. The returned closer is a no-op otherwise. A tap that cannot open is a
// startup error: a harness must never run blind believing it is recording.
func attachTranscriptLog(bus *events.Bus) (func(), error) {
	if transcriptLogPath == "" {
		return func() {}, nil
	}
	w, err := transcript.NewWriter(transcriptLogPath)
	if err != nil {
		return nil, err
	}
	w.Attach(bus)
	return func() { _ = w.Close() }, nil
}

// Execute runs the root command.
func Execute() error {
	return fang.Execute(context.Background(), rootCmd)
}

func modelName(cfg *config.Config) string {
	switch cfg.BrainProvider {
	case "claude":
		return "claude"
	case "grok":
		if cfg.GrokModel != "" {
			return cfg.GrokModel
		}
		return "grok"
	case "cursor":
		if cfg.CursorModel != "" {
			return cfg.CursorModel
		}
		return "cursor"
	default:
		return cfg.OllamaModel
	}
}

// loadConfigForSession reloads config for a conversation start. "Use defaults"
// reads Settings without the active-persona overlay so the models and
// providers from Settings actually take effect.
func loadConfigForSession(personaID string) (*config.Config, error) {
	return config.LoadRaw()
}

// bindSession resolves the conversation identity for a start. StartDefaultsID
// keeps Settings routing and TTS; named personas still overlay their stack.
func bindSession(cfg *config.Config, personaID string) (*persona.SessionBinding, error) {
	if personaID == appTUI.StartDefaultsID {
		return persona.ResolveDefaultsBinding(cfg)
	}
	if personaID == "" {
		personaID = personaOverride
	}
	return persona.ResolveBinding(cfg, personaID)
}

// conversationRuntimeBuilder returns the RuntimeBuilder the TUI invokes on
// entering the conversation screen: assets are ensured with in-screen
// progress and the mic goes hot here, not in the launcher (D2). A non-nil
// resume session seeds both the brain history and the viewport.
func conversationRuntimeBuilder(resumeSession *session.Session) appTUI.RuntimeBuilder {
	return func(ctx context.Context, progress appTUI.BuildProgress, sessionID, personaID string) (*appTUI.ConversationRuntime, error) {
		// CLI resume/continue seeds only the first chat. Later launcher choices
		// must load their selected session, or start fresh for New conversation.
		sess := resumeSession
		resumeSession = nil
		if sessionID != "" && (sess == nil || sess.ID != sessionID) {
			var err error
			sess, err = session.Load(sessionID)
			if err != nil {
				return nil, fmt.Errorf("load session: %w", err)
			}
		}
		if sess != nil && sess.SelectedPersonaID != "" && personaID == "" {
			personaID = sess.SelectedPersonaID
		}
		// Reload config in case settings changed inside the TUI.
		progress.Stage("Reading settings")
		cfg, err := loadConfigForSession(personaID)
		if err != nil {
			return nil, fmt.Errorf("reload config: %w", err)
		}
		stampWorkDir(cfg)
		baseCfg := *cfg

		// Resolve the initial responder before constructing capture/model work.
		// Shared selection refreshes this immutable binding at response boundaries.
		sessionBindID := personaID
		binding, err := bindSession(cfg, personaID)
		if err != nil {
			failedSessionID := sessionID
			if sess != nil {
				failedSessionID = sess.ID
			}
			return nil, &appTUI.PersonaBindingError{Err: fmt.Errorf("resolve persona: %w", err), SessionID: failedSessionID}
		}
		cfg = binding.Config()
		if _, err := brain.PersonaPromptHash(cfg); err != nil {
			failedSessionID := sessionID
			if sess != nil {
				failedSessionID = sess.ID
			}
			return nil, &appTUI.PersonaBindingError{Err: err, SessionID: failedSessionID}
		}

		spCfg := speaker.FromAppConfig(cfg)
		req := config.AssetRequest{
			NeedTTS:     !noVoice,
			NeedSTT:     !textMode,
			NeedVAD:     !textMode && cfg.VADEnabled,
			NeedSpeaker: !textMode && spCfg.LiveActive(),
		}
		progress.Stage("Checking models")
		if err := config.EnsureRuntimeAssets(ctx, cfg, req, progress.Downloading); err != nil {
			return nil, fmt.Errorf("ensure runtime assets: %w", err)
		}

		bus := events.NewBus()
		closeTap, err := attachTranscriptLog(bus)
		if err != nil {
			return nil, fmt.Errorf("transcript log: %w", err)
		}
		liveTTS := &voiceagent.LiveTTSManager{}
		// The pipeline names its own steps: each model load is seconds, and the
		// screen waiting on them has nothing else to report.
		p, cleanup, err := buildPipeline(ctx, cfg, bus, textMode, noVoice, liveTTS, progress.Stage)
		if err != nil {
			liveTTS.Close()
			closeTap()
			return nil, fmt.Errorf("init pipeline: %w", err)
		}

		resumed := sess != nil
		if sess == nil {
			sess = session.New(cfg.BrainProvider, modelName(cfg))
		} else {
			p.Brain.LoadHistory(sess.Turns)
		}

		checkpoint, err := bindWorkSession(p, cfg, sess, session.DefaultStore())
		if err != nil {
			liveTTS.Close()
			cleanup()
			closeTap()
			return nil, err
		}

		// Preload the model while the user reads the empty screen so their
		// first turn isn't the cold one. Best-effort — failures are ignored.
		// After the session is restored: LoadHistory resets the delegation
		// runtime whose tools warmup sends.
		if w, ok := p.Brain.(brain.Warmer); ok {
			go w.Warmup(ctx)
		}

		var captureSrc speaker.CaptureSource
		if p.Capture != nil {
			captureSrc = p.Capture
		}
		progress.Stage("Preparing speaker identification")
		liveSpeaker, stopLive, liveDetail := prepareLiveSpeaker(ctx, cfg, captureSrc)
		if liveDetail != "" {
			bus.Emit(events.Info{Message: liveDetail})
		}

		// Session-local rename table: UI bubbles + model prompt attribution.
		// user_display_name overlays speaker-1 for the desk case (one human,
		// no enrollment yet). speaker-2+ stay anonymous until named or enrolled.
		speakerNames := speaker.NewNameMap()
		speakerNames.SeedPrimary(cfg.UserDisplayName)
		brain.AttachSpeakerNames(p.Brain, speakerNames)
		if liveSpeaker != nil {
			liveCfg := speaker.FromAppConfig(cfg).Normalize()
			if liveCfg.LiveActive() && liveCfg.Live.Mode == speaker.LiveModeOwnerVerify {
				p.SpeakerGate = ownerVerifyGate(liveSpeaker.Stats)
			}
			// barge_in_speakers=enrolled_only is a separate axis from
			// speaker.live.mode (D7): wired regardless of mode, and even
			// when speaker.live is currently disabled — a later /speakers
			// on should not need a session restart to start gating. The
			// verdict source itself fails open until live analysis is
			// actually healthy and producing verdicts (see doctor's
			// barge-in-speakers-gate warning for the disabled/unenrolled
			// case).
			if config.BargeInSpeakersOf(cfg) == config.BargeInSpeakersEnrolledOnly {
				p.BargeInSpeakerVerdict = bargeInSpeakerVerdict(liveSpeaker.Stats)
			}
		}
		p.CurrentSpeaker = func() string {
			if liveSpeaker == nil {
				return ""
			}
			st := liveSpeaker.Stats()
			switch st.Status {
			case speaker.LiveHealthy, speaker.LiveRunning, speaker.LiveDegraded:
				return strings.TrimSpace(st.LastLabel)
			default:
				return ""
			}
		}

		var selector *personaruntime.Selector
		if _, supported := p.Brain.(brain.HistoryProjectionProvider); supported {
			var voices *voiceagent.LiveTTSManager
			if !noVoice {
				voices = liveTTS
			}
			selector, err = personaruntime.NewSelector(personaruntime.SelectionOptions{
				Base: &baseCfg, Pipeline: p, Voices: voices, SessionID: sess.ID, Initial: binding,
				UseDefaults: sessionBindID == appTUI.StartDefaultsID,
				Persist: func(turns []brain.Turn, selected events.PersonaSelection) error {
					return session.DefaultStore().SaveSelection(sess, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash)
				},
				NewBrain: func(_ context.Context, next *config.Config) (brain.Provider, error) {
					provider, err := brain.NewProvider(next)
					if err == nil {
						brain.AttachSpeakerNames(provider, speakerNames)
					}
					return provider, err
				},
			})
			if err != nil {
				_ = closeWorkSession(p, checkpoint)
				if stopLive != nil {
					stopLive()
				}
				liveTTS.Close()
				cleanup()
				closeTap()
				return nil, err
			}
			p.AdmitInput = selector.Route
		}

		p.OnTurn = func() {
			checkpoint.retry()
			if err := sess.Save(p.Brain.History()); err != nil {
				bus.Emit(events.Error{Stage: "session", Message: fmt.Sprintf("save session: %v", err)})
			}
		}
		wireCompact(p, cfg, sess, bus)

		// Capture the bind id (StartDefaultsID or a persona id) so Settings →
		// ReloadVoice cannot swap this conversation onto a different identity's
		// voice (e.g. uncle-fu mid-session must not become samantha/kokoro just
		// because that is the live active_persona on disk). Defaults sessions
		// keep loading Settings without the default persona's saved stack.
		if sessionBindID == "" {
			sessionBindID = binding.PersonaID
		}

		// Declare first so ReloadVoice can update SessionCfg after install.
		var rt *appTUI.ConversationRuntime
		rt = &appTUI.ConversationRuntime{
			Pipeline:     p,
			Bus:          bus,
			Voice:        p.STT != nil,
			Output:       p.HasTTS() && p.Player != nil,
			PersonaID:    binding.PersonaID,
			AgentName:    binding.AgentName,
			SessionCfg:   binding.Config(),
			SessionID:    sess.ID,
			InputDevice:  cfg.InputDevice,
			OutputDevice: cfg.OutputDevice,
			LiveSpeaker:  liveSpeaker,
			SpeakerNames: speakerNames,
			ReloadVoice: func(reloadCtx context.Context) error {
				if noVoice {
					return nil
				}
				reloadID := sessionBindID
				if selector != nil {
					reloadID = selector.Snapshot().PersonaID
				}
				reloaded, err := loadConfigForSession(reloadID)
				if err != nil {
					return fmt.Errorf("reload config: %w", err)
				}
				if selector != nil {
					stampWorkDir(reloaded)
					if err := selector.Reload(reloadCtx, reloaded); err != nil {
						return err
					}
					rt.SessionCfg = selector.Config()
					return nil
				}
				// Re-apply the session persona on top of the reloaded app config
				// so device/settings changes take effect without replacing the
				// bound voice identity. Defaults sessions re-bind Settings, not
				// the default persona's saved TTS.
				rebound, err := bindSession(reloaded, reloadID)
				if err != nil {
					return fmt.Errorf("resolve session persona: %w", err)
				}
				sessionCfg := rebound.Config()
				if err := config.EnsureRuntimeAssets(reloadCtx, sessionCfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
					return fmt.Errorf("ensure TTS assets: %w", err)
				}
				set, err := voiceagent.NewTTSSet(sessionCfg)
				if err != nil {
					return fmt.Errorf("init TTS: %w", err)
				}
				if reloadCtx.Err() != nil || !liveTTS.Install(p, set) {
					set.Close()
					if err := reloadCtx.Err(); err != nil {
						return err
					}
					return fmt.Errorf("conversation is shutting down")
				}
				// SessionCfg is only published from ReloadVoice after a successful
				// install and is consumed on the TUI update thread after
				// voiceReloadedMsg — do not read it from other goroutines.
				rt.SessionCfg = sessionCfg
				if set.VoiceNotice != nil {
					bus.Emit(events.Error{Stage: "tts-voice", Message: set.VoiceNotice.Error()})
				}
				if set.FallbackWarning != nil {
					bus.Emit(events.Error{Stage: events.StageTTSFallback, Message: set.FallbackWarning.Error()})
				}
				return nil
			},
			Cleanup: func() {
				if selector != nil {
					selector.Close()
				}
				if err := closeWorkSession(p, checkpoint); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: work cleanup: %v\n", err)
				}
				// stopLive closes feed, adapter, and analyzer/engine.
				if stopLive != nil {
					stopLive()
				}
				if err := sess.Save(p.Brain.History()); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: failed to save session %s: %v\n", sess.ID, err)
				}
				liveTTS.Close()
				cleanup()
				closeTap()
			},
		}
		if selector != nil {
			rt.SelectPersona = func(ctx context.Context, id string) (events.PersonaSelection, *config.Config, error) {
				selected, err := selector.Select(ctx, id, "ui")
				return selected, selector.Config(), err
			}
			rt.SelectionSnapshot = selector.Snapshot
			rt.SelectionConfig = selector.Config
		}
		if resumed {
			rt.Seed = sess.Turns
		}
		return rt, nil
	}
}

// wireCompact equips the pipeline for /compact: the resolved summarize
// instruction and a pre-compact session backup. A prompt-resolution failure
// disables /compact with a visible error instead of blocking startup.
func wireCompact(p *pipeline.Pipeline, cfg *config.Config, sess *session.Session, bus *events.Bus) {
	prompt, err := brain.CompactInstruction(cfg)
	if err != nil {
		bus.Emit(events.Error{Stage: "compact", Message: fmt.Sprintf("compact prompt: %v (/compact disabled)", err)})
		return
	}
	p.CompactPrompt = prompt
	if cfg != nil && cfg.MaxHistory > 0 {
		p.AutoCompactAfter = cfg.MaxHistory * 2
	}
	p.OnCompactBackup = func(turns []brain.Turn) {
		if err := sess.SaveBackup(turns); err != nil {
			bus.Emit(events.Error{Stage: "compact", Message: fmt.Sprintf("pre-compact backup: %v", err)})
		}
	}
}

func startPipeline(cfg *config.Config, resumeSession *session.Session) error {
	ctx, cancel := signalContext()
	defer cancel()

	// Same binding rule as the TUI runtime: the CLI conversation runs on an
	// identity snapshot, not the live global overlay. --persona selects which
	// identity, for this process only.
	binding, err := persona.ResolveBinding(cfg, personaOverride)
	if err != nil {
		return fmt.Errorf("resolve persona: %w", err)
	}
	cfg = binding.Config()

	req := config.AssetRequest{
		NeedTTS: !noVoice,
		NeedSTT: !textMode,
		NeedVAD: !textMode && cfg.VADEnabled,
	}
	if err := config.EnsureRuntimeAssets(ctx, cfg, req, modelProgress); err != nil {
		return fmt.Errorf("ensure runtime assets: %w", err)
	}

	bus := events.NewBus()
	closeTap, err := attachTranscriptLog(bus)
	if err != nil {
		return fmt.Errorf("transcript log: %w", err)
	}
	defer closeTap()
	display := ui.New(bus, cfg.AgentName)

	p, cleanup, err := buildPipeline(ctx, cfg, bus, textMode, noVoice, nil, printStage)
	if err != nil {
		return fmt.Errorf("init pipeline: %w", err)
	}
	defer cleanup()
	display.SetWorkSnapshot(p.WorkSnapshot)

	// Create or resume session.
	sess := resumeSession
	if sess == nil {
		sess = session.New(cfg.BrainProvider, modelName(cfg))
	} else {
		// Restore conversation history.
		if sess.Provider != "" && sess.Provider != cfg.BrainProvider {
			fmt.Printf("  Note: session was recorded with provider %q, resuming with %q\n", sess.Provider, cfg.BrainProvider)
		}
		p.Brain.LoadHistory(sess.Turns)
		fmt.Printf("  Resuming session %s (%d turns)\n", sess.ID, len(sess.Turns))
	}

	checkpoint, err := bindWorkSession(p, cfg, sess, session.DefaultStore())
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeWorkSession(p, checkpoint); closeErr != nil {
			fmt.Fprintf(os.Stderr, "  warning: work cleanup: %v\n", closeErr)
		}
	}()

	// Preload the model while the welcome/setup is shown so the user's first
	// turn isn't the cold one. Best-effort — failures are ignored. After the
	// session is restored: LoadHistory resets the delegation runtime whose
	// tools warmup sends.
	if w, ok := p.Brain.(brain.Warmer); ok {
		go w.Warmup(ctx)
	}

	// Auto-save after each turn.
	p.OnTurn = func() {
		checkpoint.retry()
		if err := sess.Save(p.Brain.History()); err != nil {
			bus.Emit(events.Error{Stage: "session", Message: fmt.Sprintf("save session: %v", err)})
		}
	}

	display.ShowWelcome()
	display.ShowProviders(cfg.TTSProvider, cfg.STTProvider)
	defer display.ShowGoodbye()

	err = app.Run(ctx, p, os.Stdin, textMode, noVoice)

	// Final save on exit.
	if saveErr := sess.Save(p.Brain.History()); saveErr != nil {
		fmt.Fprintf(os.Stderr, "  warning: failed to save session %s: %v\n", sess.ID, saveErr)
	}

	return err
}

// buildPipeline constructs the interactive pipeline through the library, so the
// CLI exercises exactly the surface an embedder gets. If something the CLI needs
// cannot be expressed as voiceagent.Options, that is the library's problem to
// fix, not the CLI's to work around.
func buildPipeline(ctx context.Context, cfg *config.Config, bus *events.Bus, text, silent bool, liveTTS *voiceagent.LiveTTSManager, stage func(string)) (*pipeline.Pipeline, func(), error) {
	agent, cleanup, err := voiceagent.New(ctx, voiceagent.Options{
		Config:   cfg,
		Events:   bus,
		TextOnly: text,
		Silent:   silent,
		LiveTTS:  liveTTS,
		Stage:    stage,
		Logf:     func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	if err != nil {
		return nil, nil, err
	}
	return agent.Pipeline, cleanup, nil
}

// printStage narrates pipeline startup on the plain CLI, which otherwise sits
// silent through every model load.
func printStage(label string) {
	fmt.Printf("  %s...\n", label)
}

var lastProgressPct int

func modelProgress(name string, pct float64) {
	iPct := int(pct)
	if pct == 0 {
		lastProgressPct = -1
		fmt.Printf("  Downloading %s...\n", name)
		return
	}
	if iPct != lastProgressPct {
		lastProgressPct = iPct
		fmt.Printf("\r  %s: %d%%", name, iPct)
		if iPct >= 100 {
			fmt.Println()
		}
	}
}
