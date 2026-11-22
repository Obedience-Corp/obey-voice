package tui

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	tea "github.com/charmbracelet/bubbletea"
)

type speechStopBrain struct {
	pauseTerminalBrain
	started chan struct{}
	release chan struct{}
}

func (b *speechStopBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	close(b.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return b.pauseTerminalBrain.ThinkStream(ctx, input, opts)
	}
}

type speechStopTTS struct {
	matrixTTS
	calls atomic.Int32
}

func (s *speechStopTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	s.calls.Add(1)
	return s.matrixTTS.Synthesize(ctx, text)
}

type speechStopReadyMsg struct{}
type speechStopSnapshot struct {
	response    events.ResponseReady
	view, draft string
}
type speechStopHarness struct {
	t         *testing.T
	model     conversationModel
	deps      conversationDeps
	ready     chan struct{}
	typed     chan string
	responses chan speechStopSnapshot
	heard     chan struct{} // optional: the TUI has taken the voice turn's input
	speaking  chan struct{} // optional: the TUI has seen the reply start speaking
	settled   chan string   // optional: the view once the turn's metrics land
}

func (h *speechStopHarness) Init() tea.Cmd {
	return tea.Batch(h.model.startConversation(h.deps), func() tea.Msg { return speechStopReadyMsg{} })
}
func (h *speechStopHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	switch msg := msg.(type) {
	case speechStopReadyMsg:
		close(h.ready)
	case tea.KeyMsg:
		if msg.Type == tea.KeyRunes && string(msg.Runes) == "x" {
			h.typed <- h.model.input.Value()
		}
	case busEventMsg:
		if _, ok := msg.event.(events.UserInput); ok && h.heard != nil {
			select {
			case h.heard <- struct{}{}:
			default:
				h.t.Error("the voice turn delivered more than one UserInput")
			}
		}
		if response, ok := msg.event.(events.ResponseReady); ok {
			h.responses <- speechStopSnapshot{response: response, view: stripANSI(h.model.View()), draft: h.model.input.Value()}
		}
		if _, ok := msg.event.(events.SpeakingStarted); ok && h.speaking != nil {
			select {
			case h.speaking <- struct{}{}:
			default:
			}
		}
		if e, ok := msg.event.(events.TurnMetrics); ok && h.settled != nil && e.Outcome != "" && e.Outcome != "timed_out" {
			select {
			case h.settled <- stripANSI(h.model.View()):
			default:
			}
		}
	}
	return h, cmd
}
func (h *speechStopHarness) View() string { return h.model.View() }

func TestTypingDuringThinkingLeavesTypedReplySpokenInTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	bus := events.NewBus()
	provider := &speechStopBrain{started: make(chan struct{}), release: make(chan struct{})}
	voice := &speechStopTTS{}
	player := &matrixPlayer{}
	defer player.Close()
	runner := &pipeline.Pipeline{Brain: provider, TTS: voice, Player: player, Events: bus}
	var failures atomic.Int32
	events.Subscribe(bus, func(events.Error) { failures.Add(1) })
	wg := new(sync.WaitGroup)
	h := &speechStopHarness{
		model: sizedConversation(t, 110, 30),
		deps:  conversationDeps{runner: runner, bus: bus, output: true, ctx: ctx, wg: wg},
		ready: make(chan struct{}), typed: make(chan string, 1), responses: make(chan speechStopSnapshot, 1),
	}
	var terminal strings.Builder
	program := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithoutSignalHandler())
	finished := make(chan error, 1)
	go func() { _, err := program.Run(); finished <- err }()
	defer func() {
		program.Quit()
		if err := <-finished; err != nil {
			t.Errorf("terminal shutdown: %v", err)
		}
		cancel()
		wg.Wait()
	}()
	select {
	case <-h.ready:
	case <-ctx.Done():
		t.Fatal("terminal did not start")
	}
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("status")})
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal("model did not start")
	}
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	select {
	case draft := <-h.typed:
		if draft != "x" {
			t.Fatalf("typed draft=%q", draft)
		}
	case <-ctx.Done():
		t.Fatal("terminal did not process the stop keystroke")
	}
	close(provider.release)
	select {
	case snapshot := <-h.responses:
		if snapshot.response.Interrupted || snapshot.response.Degraded || snapshot.draft != "x" || !strings.Contains(snapshot.view, "canonical context") {
			t.Fatalf("reply lost its text, draft or outcome: %+v", snapshot)
		}
	case <-ctx.Done():
		t.Fatal("keystroke canceled text generation or held the turn open")
	}
	if voice.calls.Load() == 0 || failures.Load() != 0 {
		t.Fatalf("reply was not spoken or emitted errors: synthesis=%d errors=%d", voice.calls.Load(), failures.Load())
	}
}

// oneUtteranceSTT hears one utterance, then listens silently until canceled.
type oneUtteranceSTT struct {
	text   string
	starts atomic.Int32
}

type silentSTTSession struct{ events chan stt.Event }

func (s *silentSTTSession) Events() <-chan stt.Event { return s.events }
func (*silentSTTSession) Close() error               { return nil }

func (s *oneUtteranceSTT) Start(context.Context) (stt.Session, error) {
	session := &silentSTTSession{events: make(chan stt.Event, 2)}
	session.events <- stt.PhaseEvent{Phase: "listening"}
	if s.starts.Add(1) == 1 {
		session.events <- stt.FinalTranscript{Text: s.text}
		close(session.events)
	}
	return session, nil
}
func (*oneUtteranceSTT) Available() bool { return true }

// A keystroke while the persona is still working, before the reply has said
// anything, must not cost the reply its voice.
func TestKeystrokeDuringVoiceThinkingLeavesReplySpoken(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	bus := events.NewBus()
	provider := &speechStopBrain{started: make(chan struct{}), release: make(chan struct{})}
	voice := &speechStopTTS{}
	player := &matrixPlayer{}
	defer player.Close()
	runner := &pipeline.Pipeline{STT: &oneUtteranceSTT{text: "Obey, what do you think about Dylan?"}, Brain: provider, TTS: voice, Player: player, Events: bus}
	metrics := make(chan events.TurnMetrics, 8)
	events.Subscribe(bus, func(e events.TurnMetrics) {
		if e.Outcome != "" && e.Outcome != "timed_out" {
			metrics <- e
		}
	})
	wg := new(sync.WaitGroup)
	h := &speechStopHarness{
		t:     t,
		model: sizedConversation(t, 110, 30),
		deps:  conversationDeps{runner: runner, bus: bus, voice: true, output: true, ctx: ctx, wg: wg},
		ready: make(chan struct{}), typed: make(chan string, 1), responses: make(chan speechStopSnapshot, 1),
		heard: make(chan struct{}, 1),
	}
	var terminal strings.Builder
	program := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithoutSignalHandler())
	finished := make(chan error, 1)
	go func() { _, err := program.Run(); finished <- err }()
	defer func() {
		program.Quit()
		if err := <-finished; err != nil {
			t.Errorf("terminal shutdown: %v", err)
		}
		cancel()
		wg.Wait()
	}()
	for _, wait := range []<-chan struct{}{provider.started, h.heard} {
		select {
		case <-wait:
		case <-ctx.Done():
			t.Fatal("voice turn never reached the model")
		}
	}
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	select {
	case <-h.typed:
	case <-ctx.Done():
		t.Fatal("terminal did not process the keystroke")
	}
	close(provider.release)
	select {
	case snapshot := <-h.responses:
		if snapshot.response.Interrupted {
			t.Fatalf("an early keystroke silenced the reply: %+v", snapshot.response)
		}
	case <-ctx.Done():
		t.Fatal("voice turn did not finish")
	}
	var got events.TurnMetrics
	select {
	case got = <-metrics:
	case <-ctx.Done():
		t.Fatal("no turn metrics")
	}
	if got.Outcome != "completed" || got.Interrupted || got.SpeechStop != nil {
		t.Fatalf("outcome=%q interrupted=%v speech stop=%+v", got.Outcome, got.Interrupted, got.SpeechStop)
	}
	if voice.calls.Load() == 0 {
		t.Fatal("reply was never synthesized")
	}
}

// speakingPlayer keeps each playback audible until it is stopped.
type speakingPlayer struct {
	mu      sync.Mutex
	cancels []context.CancelFunc
	playing atomic.Bool
}

func (p *speakingPlayer) IsPlaying() bool { return p.playing.Load() }
func (p *speakingPlayer) Stop() {
	p.mu.Lock()
	cancels := p.cancels
	p.cancels = nil
	p.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
func (p *speakingPlayer) Close() error { p.Stop(); return nil }
func (p *speakingPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	child, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.cancels = append(p.cancels, cancel)
	p.mu.Unlock()
	started, done := make(chan struct{}), make(chan audio.PlaybackResult, 1)
	go func() {
		defer close(done)
		defer cancel()
		began := false
		frames := stream.Frames()
		for frames != nil {
			select {
			case <-child.Done():
				frames = nil
			case _, ok := <-frames:
				if !ok {
					frames = nil
					break
				}
				if !began {
					began = true
					p.playing.Store(true)
					close(started)
				}
			}
		}
		<-child.Done()
		p.playing.Store(false)
		done <- audio.PlaybackResult{Interrupted: true, Err: child.Err()}
	}()
	return audio.NewPlayback(started, done), nil
}

func TestKeystrokeDuringVoiceSpeechIsNamedInTurnMetrics(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	bus := events.NewBus()
	provider := &speechStopBrain{started: make(chan struct{}), release: make(chan struct{})}
	close(provider.release)
	voice := &speechStopTTS{}
	player := &speakingPlayer{}
	defer player.Close()
	runner := &pipeline.Pipeline{STT: &oneUtteranceSTT{text: "What do you think of the song?"}, Brain: provider, TTS: voice, Player: player, Events: bus}
	metrics := make(chan events.TurnMetrics, 8)
	events.Subscribe(bus, func(e events.TurnMetrics) {
		if e.Outcome != "" && e.Outcome != "timed_out" {
			metrics <- e
		}
	})
	wg := new(sync.WaitGroup)
	h := &speechStopHarness{
		t:     t,
		model: sizedConversation(t, 110, 30),
		deps:  conversationDeps{runner: runner, bus: bus, voice: true, output: true, ctx: ctx, wg: wg},
		ready: make(chan struct{}), typed: make(chan string, 1), responses: make(chan speechStopSnapshot, 1),
		heard: make(chan struct{}, 1), speaking: make(chan struct{}, 1), settled: make(chan string, 1),
	}
	var terminal strings.Builder
	program := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithoutSignalHandler())
	finished := make(chan error, 1)
	go func() { _, err := program.Run(); finished <- err }()
	defer func() {
		program.Quit()
		if err := <-finished; err != nil {
			t.Errorf("terminal shutdown: %v", err)
		}
		cancel()
		wg.Wait()
	}()
	select {
	case <-h.speaking:
	case <-ctx.Done():
		t.Fatal("reply never started speaking")
	}
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	select {
	case snapshot := <-h.responses:
		if !snapshot.response.Interrupted {
			t.Fatalf("typing during speech did not silence the reply: %+v", snapshot.response)
		}
	case <-ctx.Done():
		t.Fatal("voice turn did not finish")
	}
	var got events.TurnMetrics
	select {
	case got = <-metrics:
	case <-ctx.Done():
		t.Fatal("no turn metrics")
	}
	want := events.SpeechStop{Source: "keystroke", Count: 1, KeyRunes: 1}
	if got.SpeechStop == nil || *got.SpeechStop != want {
		t.Fatalf("speech stop = %+v, want %+v", got.SpeechStop, want)
	}
	select {
	case view := <-h.settled:
		if !strings.Contains(view, "speech stopped by typing") {
			t.Fatalf("stopped reply does not say what stopped it:\n%s", view)
		}
	case <-ctx.Done():
		t.Fatal("metrics line never rendered")
	}
}

type sourceRecordingRunner struct {
	fakeTurnRunner
	sources []pipeline.StopSource
}

func (r *sourceRecordingRunner) StopPlaybackFrom(source pipeline.StopSource) {
	r.mu.Lock()
	r.sources = append(r.sources, source)
	r.mu.Unlock()
}

func TestTerminalStopsNameTheirSource(t *testing.T) {
	cases := []struct {
		name string
		stop func(*conversationModel)
		want pipeline.StopSource
	}{
		{"typing", func(m *conversationModel) {
			m.keystrokeBargeIn(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
		}, pipeline.StopSource{Name: "keystroke", KeyRunes: 1}},
		{"paste", func(m *conversationModel) {
			m.keystrokeBargeIn(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted text"), Paste: true})
		}, pipeline.StopSource{Name: "keystroke", KeyRunes: 11, KeyPaste: true}},
		{"enter", func(m *conversationModel) { m.beginTextBargeIn("draft") }, pipeline.StopSource{Name: "text_submit"}},
		{"talk key", func(m *conversationModel) {
			m.talk = &talkState{}
			m.interruptForTalk()
		}, pipeline.StopSource{Name: "talk_key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &sourceRecordingRunner{fakeTurnRunner: fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}}
			m := sizedConversation(t, 80, 24)
			m.startConversation(conversationDeps{runner: runner, bus: events.NewBus(), voice: true, ctx: t.Context()})
			m.handleEvent(events.UserInput{Text: "spoken words"})
			tc.stop(&m)
			if m.turnCancel != nil {
				m.turnCancel()
			}
			runner.mu.Lock()
			defer runner.mu.Unlock()
			if len(runner.sources) != 1 || runner.sources[0] != tc.want || runner.stopped != 0 {
				t.Fatalf("sources=%+v plain stops=%d, want one %+v", runner.sources, runner.stopped, tc.want)
			}
		})
	}
}
