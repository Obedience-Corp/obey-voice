package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	personaruntime "github.com/Obedience-Corp/obey-voice/internal/persona/runtime"
	"github.com/Obedience-Corp/obey-voice/internal/worksession"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	tea "github.com/charmbracelet/bubbletea"
)

// gatedFixtureSource gates the first acoustic frame, not provider Start. The
// real native session returns immediately and owns cancellation/completion
// negotiation; its Close cancels ReadFrame even for detached work capture.
type gatedFixtureSource struct {
	*audio.FixtureSource
	permit    <-chan struct{}
	released  chan<- providerFixtureRelease
	dropped   *atomic.Int64
	trace     events.TraceContext
	waiting   bool
	closed    chan struct{}
	closeOnce sync.Once
}

func (s *gatedFixtureSource) ReadFrame(ctx context.Context) (audio.Frame, error) {
	if s.waiting {
		select {
		case <-ctx.Done():
			return audio.Frame{}, ctx.Err()
		case <-s.closed:
			return audio.Frame{}, audio.ErrSourceClosed
		case <-s.permit:
		}
		s.waiting = false
		s.FixtureSource.Reset() // restart realtime pacing at permit, not provider construction
		at := time.Now()
		release := providerFixtureRelease{At: at, Trace: s.trace}
		if !s.trace.Origin.IsZero() {
			value := float64(at.Sub(s.trace.Origin)) / float64(time.Millisecond)
			release.MonoMS = &value
		}
		select {
		case s.released <- release:
		default:
			s.dropped.Add(1)
		}
	}
	return s.FixtureSource.ReadFrame(ctx)
}
func (s *gatedFixtureSource) Read() []float32 {
	if s.waiting {
		return nil
	}
	return s.FixtureSource.Read()
}
func (s *gatedFixtureSource) Close() error {
	s.closeOnce.Do(func() { close(s.closed); _ = s.FixtureSource.Close() })
	return nil
}

type gatedFixtureSTT struct {
	source   *gatedFixtureSource
	provider stt.Provider
}

func (s *gatedFixtureSTT) Available() bool { return s.provider.Available() }
func (s *gatedFixtureSTT) Start(ctx context.Context) (stt.Session, error) {
	s.source.FixtureSource.Reset()
	s.source.trace = events.TraceFromContext(ctx)
	s.source.waiting = true
	return s.provider.Start(ctx)
}

type providerRecordTerminal struct {
	app        App
	permit     chan struct{}
	metrics    <-chan events.TurnMetrics
	tokens     <-chan events.TokenUsage
	log        *json.Encoder
	logErr     *error
	released   <-chan providerFixtureRelease
	population *providerPopulation
}

func (m providerRecordTerminal) Init() tea.Cmd { return tea.Batch(m.app.Init(), providerPollCmd()) }
func (m providerRecordTerminal) View() string  { return m.app.View() }
func (m providerRecordTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if message, ok := msg.(busEventMsg); ok {
		switch event := message.event.(type) {
		case events.Info:
			text := event.Message
			if len(text) > 8192 {
				text = text[:8192] + "… (bounded diagnostic)"
			}
			if err := m.log.Encode(map[string]any{"type": "diagnostic", "observed_at": time.Now().UTC(), "trace": event.Trace, "message": text}); err != nil {
				*m.logErr = err
			}
		case events.Error:
			text := event.Message
			if len(text) > 8192 {
				text = text[:8192] + "… (bounded diagnostic)"
			}
			if err := m.log.Encode(map[string]any{"type": "diagnostic_error", "observed_at": time.Now().UTC(), "trace": event.Trace, "stage": event.Stage, "message": text}); err != nil {
				*m.logErr = err
			}
		}
	}

	if _, ok := msg.(providerPollMsg); ok {
		return m.populationTick()
	}

	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyF6 {
		if m.population != nil {
			return m, nil
		}
		if m.app.runtime != nil {
			if err := quietProviderOutput(m.app.runtime.Pipeline.Player, m.app.cfg.OutputDevice, false); err != nil {
				*m.logErr = err
				next, cmd := m.app.Update(quitMsg{})
				m.app = next.(App)
				return m, cmd
			}
		}
		if m.app.runtime != nil && m.app.conversation.turnState == turnVoiceListening {
			select {
			case m.permit <- struct{}{}:
			default:
			}
		}
		return m, nil
	}
	if done, ok := msg.(voiceTurnDoneMsg); ok {
		row := map[string]any{"type": "turn", "observed_at": time.Now().UTC(), "input_kind": "recorded_wav_fixture", "physical_microphone": false, "transcript": done.text, "capture": m.app.conversation.capture != nil, "metrics_available": false}
		var capturedMetric events.TurnMetrics
		select {
		case metric := <-m.metrics:
			capturedMetric = metric
			row["metrics_available"] = true
			row["trace"] = metric.Trace
			row["timing"] = metric.Timing
			row["audible_sink"] = metric.AudibleSink
			row["outcome"] = metric.Outcome
			row["segments"] = metric.Segments
			row["envelope"] = metric.Envelope
		default:
			row["outcome"] = "metrics_unavailable"
		}
		counts := []events.TokenUsage{}
		maxGenerated := 0
		for len(counts) < 64 {
			select {
			case usage := <-m.tokens:
				counts = append(counts, usage)
				maxGenerated = max(maxGenerated, usage.Gen)
			default:
				goto accounted
			}
		}
	accounted:
		row["token_usage"] = counts
		row["configured_token_cap_possible"] = maxGenerated >= 64
		row["degraded"] = capturedMetric.Degraded
		row["interrupted"] = capturedMetric.Interrupted
		quietErr := fmt.Errorf("native runtime unavailable")
		if m.app.runtime != nil && m.app.runtime.Pipeline != nil {
			row["group_participation_enabled"] = m.app.runtime.Pipeline.GroupParticipationEnabled
			quietErr = quietProviderOutput(m.app.runtime.Pipeline.Player, m.app.cfg.OutputDevice, true)
		}
		row["quiet_output_verified"] = quietErr == nil
		if m.app.runtime != nil && m.app.runtime.Pipeline != nil {
			if observer, ok := m.app.runtime.Pipeline.Player.(interface {
				NegotiatedDevice() audio.NegotiatedDevice
				Volume() float64
			}); ok {
				row["output_device"] = observer.NegotiatedDevice()
				row["playback_gain"] = observer.Volume()
			}
		}
		if quietErr != nil {
			row["output_verification_error"] = quietErr.Error()
		}
		if done.err != nil {
			row["error"] = done.err.Error()
		}
		if capture := m.app.conversation.capture; capture != nil {
			row["capture_id"] = capture.ID()
			row["capture_path"] = capture.EventsPath()
		}
		if pop := m.population; pop != nil {
			row["work_enabled"] = m.app.cfg.VoiceWorkEnabled
			if pop.Active && pop.Index < len(pop.Entries) {
				row["population"] = pop.Entries[pop.Index]
				pop.Observed++
				valid := quietErr == nil && done.err == nil && capturedMetric.Outcome == "completed" && !capturedMetric.Degraded && !capturedMetric.Interrupted && capturedMetric.AudibleSink == "local_speaker" && capturedMetric.Timing.Origin == "finalized_input" && capturedMetric.Timing.Normalized.PlaybackStartMS != nil && len(counts) > 0 && maxGenerated < 64 && (m.app.conversation.capture != nil) == pop.Entries[pop.Index].Capture
				row["population_valid"] = valid
				if valid {
					pop.Valid++
				}
				pop.Index++
				pop.Awaiting = true
				pop.Active = false
			} else {
				row["outside_planned_population"] = true
			}
		}
		if err := m.log.Encode(row); err != nil {
			*m.logErr = err
		}
	}
	next, command := m.app.Update(msg)
	m.app = next.(App)
	return m, command
}

// TestRecordProviderTerminalHarness extends the existing opt-in /record terminal
// proof with actual App/runProgram, production providers and finite acoustic
// input. It is deliberately excluded from ordinary unit/CI runs.
func TestRecordProviderTerminalHarness(t *testing.T) {
	root := os.Getenv("OBEY_VOICE_CAPTURE_PROVIDER_ROOT")
	if root == "" {
		t.Skip("opt-in actual-provider terminal harness")
	}
	cfgPath, wav := os.Getenv("OBEY_VOICE_CAPTURE_PROVIDER_CONFIG"), os.Getenv("OBEY_VOICE_CAPTURE_PROVIDER_WAV")
	for _, path := range []string{root, cfgPath, wav} {
		if !filepath.IsAbs(path) {
			t.Fatal("provider harness paths must be explicit absolute paths")
		}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	closeErr := input.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if len(raw) > 1<<20 {
		t.Fatal("fixture config too large")
	}
	output, err := os.OpenFile(filepath.Join(root, "config.yaml"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = output.Write(raw); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
	oldExit := processForceExit
	processForceExit = func(code int) {
		if dump, err := os.OpenFile(filepath.Join(root, "quit-goroutines.txt"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); err == nil {
			_ = pprof.Lookup("goroutine").WriteTo(dump, 2)
			_ = dump.Close()
		}
		oldExit(code)
	}
	t.Cleanup(func() { processForceExit = oldExit })
	config.SetConfigDirForTest(t, root)
	cfg, err := config.LoadRaw()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BrainProvider != "ollama" || cfg.TTSProvider != "kokoro" {
		t.Fatal("provider proof requires explicit local ollama/kokoro fixture config")
	}
	if cfg.OutputDevice == "" || config.PlaybackVolumeOf(cfg) != 0 {
		t.Fatal("provider harness requires explicit output_device and playback_volume:0; no audible probes")
	}
	deviceCtx, deviceCancel := context.WithTimeout(context.Background(), 3*time.Second)
	names, deviceErr := audio.NewDeviceChecker().PlaybackDevices(deviceCtx)
	deviceCancel()
	if deviceErr != nil {
		t.Fatal(deviceErr)
	}
	present := false
	for _, name := range names {
		if name == cfg.OutputDevice {
			present = true
		}
	}
	if !present {
		t.Fatal("requested silent device unavailable; refusing default fallback")
	}
	cfg.VoiceWorkEnabled = os.Getenv("OBEY_VOICE_CAPTURE_PROVIDER_WORK") == "on"
	cfg.BargeInEnabled = false // finite input has no physical capture/echo monitor
	cfg.WorkDir = filepath.Join(root, "work")
	if err := os.MkdirAll(cfg.WorkDir, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(root, "observations.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	permit := make(chan struct{}, 1)
	released := make(chan providerFixtureRelease, 8)
	population, err := newProviderPopulation(root)
	if err != nil {
		t.Fatal(err)
	}
	metrics := make(chan events.TurnMetrics, 8)
	tokens := make(chan events.TokenUsage, 64)
	var dropped atomic.Int64
	var logErr error
	build := func(ctx context.Context, progress BuildProgress, _, _ string) (*ConversationRuntime, error) {
		binding, err := persona.ResolveDefaultsBinding(cfg)
		if err != nil {
			return nil, err
		}
		effective := binding.Config()
		bus := events.NewBus()
		events.Subscribe(bus, func(usage events.TokenUsage) {
			select {
			case tokens <- usage:
			default:
				dropped.Add(1)
			}
		})
		events.Subscribe(bus, func(metric events.TurnMetrics) {
			select {
			case metrics <- metric:
			default:
				dropped.Add(1)
			}
		})
		agent, cleanup, err := voiceagent.New(ctx, voiceagent.Options{Config: effective, Events: bus, TextOnly: true, Stage: progress.Stage})
		if err != nil {
			return nil, err
		}
		if err := quietProviderOutput(agent.Player, effective.OutputDevice, false); err != nil {
			cleanup()
			return nil, err
		}
		failed := true
		defer func() {
			if failed {
				cleanup()
			}
		}()
		plainSource, err := audio.NewFixtureSourceFromWAV(wav, audio.ChunkSize, true)
		if err != nil {
			return nil, err
		}
		source := &gatedFixtureSource{FixtureSource: plainSource, permit: permit, released: released, dropped: &dropped, closed: make(chan struct{})}
		vad, err := audio.NewVAD(effective)
		if err != nil {
			_ = source.Close()
			return nil, err
		}
		recognizer, recognizerCleanup, err := stt.NewProvider(effective, source, vad)
		if err != nil {
			vad.Delete()
			_ = source.Close()
			return nil, err
		}
		agent.STT = &gatedFixtureSTT{source: source, provider: recognizer}
		agent.VAD = vad
		sess := session.New(effective.BrainProvider, config.BrainModelFor(effective, effective.BrainProvider))
		store := session.NewStore(filepath.Join(root, "sessions"))
		checkpoint, err := worksession.Bind(agent.Pipeline, effective, sess, store)
		if err != nil {
			recognizerCleanup()
			vad.Delete()
			_ = source.Close()
			return nil, err
		}
		persist := func(turns []brain.Turn, selection events.PersonaSelection) error {
			return store.SaveSelection(sess, turns, selection.PersonaID, selection.DisplayName, selection.PromptHash, selection.Directories)
		}
		selector, err := personaruntime.NewSelector(personaruntime.SelectionOptions{Base: cfg, Pipeline: agent.Pipeline, SessionID: sess.ID, Initial: binding, UseDefaults: true, Persist: persist, NewBrain: func(_ context.Context, next *config.Config) (brain.Provider, error) { return brain.NewProvider(next) }})
		if err != nil {
			_ = agent.CloseWork()
			_ = checkpoint.Close()
			recognizerCleanup()
			vad.Delete()
			_ = source.Close()
			return nil, err
		}
		agent.AdmitInput = selector.Route
		agent.RouteHandoff = selector.RouteHandoff
		failed = false
		return &ConversationRuntime{Pipeline: agent.Pipeline, Bus: bus, Voice: true, Output: agent.HasTTS() && agent.Player != nil, SessionCfg: effective, SessionID: sess.ID, PersonaID: binding.PersonaID, AgentName: binding.DisplayName, InputDevice: "recorded WAV fixture (F6; no physical mic)", OutputDevice: effective.OutputDevice, SelectionSnapshot: selector.Snapshot, Persist: func() {
			if err := persist(agent.Brain.History(), selector.Snapshot()); err != nil {
				bus.Emit(events.Error{Stage: "session", Message: err.Error()})
			}
		}, Cleanup: func() {
			selector.Close()
			_ = agent.CloseWork()
			_ = checkpoint.Close()
			recognizerCleanup()
			vad.Delete()
			_ = source.Close()
			cleanup()
		}}, nil
	}
	if err := writeProviderProvenance(root, cfgPath, wav, cfg); err != nil {
		t.Fatal(err)
	}
	_, err = runProgram(cfg, build, nil, true, func(app App) (tea.Model, error) {
		model := providerRecordTerminal{app: app, permit: permit, metrics: metrics, tokens: tokens, log: json.NewEncoder(file), logErr: &logErr, released: released, population: population}
		result, runErr := tea.NewProgram(model, tea.WithAltScreen()).Run()
		if final, ok := result.(providerRecordTerminal); ok {
			return final.app, runErr
		}
		return result, runErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if population != nil && (population.Index != len(population.Entries) || population.Observed != len(population.Entries) || population.Valid != len(population.Entries)) {
		t.Fatalf("population incomplete/invalid: planned%d completed%d observed%d valid%d; retain all attempts", len(population.Entries), population.Index, population.Observed, population.Valid)
	}
	if population != nil {
		if err := validateProviderCaptureArtifacts(root, population); err != nil {
			t.Fatal(err)
		}
	}
	if logErr != nil {
		t.Fatal(logErr)
	}
	if dropped.Load() != 0 {
		t.Fatal("lost harness metric observations", dropped.Load())
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual TUI provider proof: finite WAV %s; work=%v; no physical mic; output timing is software sink acceptance/Done", wav, cfg.VoiceWorkEnabled)
}

func providerFixtureHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
