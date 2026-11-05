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
	model     conversationModel
	deps      conversationDeps
	ready     chan struct{}
	typed     chan string
	responses chan speechStopSnapshot
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
		if response, ok := msg.event.(events.ResponseReady); ok {
			h.responses <- speechStopSnapshot{response: response, view: stripANSI(h.model.View()), draft: h.model.input.Value()}
		}
	}
	return h, cmd
}
func (h *speechStopHarness) View() string { return h.model.View() }

func TestTypingDuringThinkingSilencesSpeechInTerminal(t *testing.T) {
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
		if !snapshot.response.Interrupted || snapshot.response.Degraded || snapshot.draft != "x" || !strings.Contains(snapshot.view, "canonical context") {
			t.Fatalf("stopped response lost its text, draft or outcome: %+v", snapshot)
		}
	case <-ctx.Done():
		t.Fatal("typed stop canceled text generation or held the turn open")
	}
	if voice.calls.Load() != 0 || failures.Load() != 0 {
		t.Fatalf("stopped turn synthesized speech or emitted errors: synthesis=%d errors=%d", voice.calls.Load(), failures.Load())
	}
}
