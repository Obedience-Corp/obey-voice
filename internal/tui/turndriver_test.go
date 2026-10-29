package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// fakeTurnRunner scripts RunTurn results in order; a blockNext entry parks
// the turn until its context is canceled, like a silent listening session.
type fakeTurnRunner struct {
	mu          sync.Mutex
	voiceQueue  []voiceScript
	voiceCalls  int
	textInputs  []string
	textErr     error
	blockText   bool          // park RunTurnTextMode until ctx cancel (simulates TTS)
	textStarted chan struct{} // signaled once per entry into RunTurnTextMode
	stopped     int           // StopPlayback call count
}

type voiceScript struct {
	text  string
	err   error
	block bool
}

func (f *fakeTurnRunner) RunTurn(ctx context.Context) (string, error) {
	f.mu.Lock()
	f.voiceCalls++
	var s voiceScript
	if len(f.voiceQueue) > 0 {
		s = f.voiceQueue[0]
		f.voiceQueue = f.voiceQueue[1:]
	}
	f.mu.Unlock()

	if s.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return s.text, s.err
}

func (f *fakeTurnRunner) RunTurnTextMode(ctx context.Context, input string) error {
	f.mu.Lock()
	f.textInputs = append(f.textInputs, input)
	block := f.blockText
	err := f.textErr
	started := f.textStarted
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeTurnRunner) StopPlayback() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
}

func (f *fakeTurnRunner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.voiceCalls
}

func (f *fakeTurnRunner) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.textInputs...)
}

// waitForTexts blocks until the runner has recorded at least n text turns, so a
// test can barge into a turn that is provably running instead of racing it.
func waitForTexts(t *testing.T, f *fakeTurnRunner, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.texts()) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runner recorded %d text turns, want %d", len(f.texts()), n)
}

func startedConversation(t *testing.T, runner *fakeTurnRunner, voice bool) (conversationModel, *events.Bus) {
	t.Helper()
	bus := events.NewBus()
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{
		runner: runner,
		bus:    bus,
		voice:  voice,
		ctx:    context.Background(),
	})
	return m, bus
}

func typeAndEnter(m conversationModel, text string) (conversationModel, tea.Cmd) {
	m.input.SetValue(text)
	return m.Update(tea.KeyMsg{Type: tea.KeyEnter})
}

func admissionFailure(category admission.FailureCategory, detail string) error {
	return &pipeline.AdmissionError{
		Err: admission.WrapFailure(category, "route", errors.New(detail)),
	}
}

func TestVoiceTurnCompletesAndRedispatches(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{text: "hello"}}}
	m, _ := startedConversation(t, runner, true)

	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want listening after start", m.turnState)
	}

	m, cmd := m.Update(voiceTurnDoneMsg{text: "hello"})
	if cmd == nil {
		t.Fatal("completed voice turn did not redispatch")
	}
	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want listening after redispatch", m.turnState)
	}
}

func TestSilenceKeepsListening(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)

	m, cmd := m.Update(voiceTurnDoneMsg{text: ""})
	if cmd == nil || m.turnState != turnVoiceListening {
		t.Fatal("silent turn must keep listening")
	}
}

// D1: submitting text while a voice turn is listening cancels the voice turn,
// then dispatches the text turn once the canceled turn drains.
func TestTextSubmitCancelsListeningVoiceTurn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := startedConversation(t, runner, true)

	voiceCmd := m.dispatchVoiceTurn() // execute the in-flight turn ourselves
	voiceDone := make(chan tea.Msg, 1)
	go func() { voiceDone <- voiceCmd() }()

	m, cmd := typeAndEnter(m, "typed instead")
	if cmd != nil {
		t.Fatal("submit during listening must wait for the canceled turn, not dispatch")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling", m.turnState)
	}
	if m.input.Value() != "" {
		t.Error("input not cleared on submit")
	}
	// Composer is empty immediately — the bubble must already be in Chat so
	// the user does not think Enter dropped the message during cancel.
	view := stripANSI(m.View())
	if !strings.Contains(view, "typed instead") {
		t.Fatalf("typed message missing from chat during cancel wait:\n%s", view)
	}
	if m.activityFocused {
		t.Fatal("submit must switch focus back to Chat")
	}

	var msg tea.Msg
	select {
	case msg = <-voiceDone:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled voice turn never returned — cancel not propagated")
	}

	m, cmd = m.Update(msg)
	if cmd == nil {
		t.Fatal("drained cancel did not dispatch the pending text turn")
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState = %d, want text running", m.turnState)
	}

	if textMsg := cmd(); textMsg == nil {
		t.Fatal("text turn cmd returned nil msg")
	}
	if got := runner.texts(); len(got) != 1 || got[0] != "typed instead" {
		t.Fatalf("RunTurnTextMode inputs = %v, want [typed instead]", got)
	}
	// Bus UserInput after dispatch must not double the optimistic bubble.
	m.handleEvent(events.UserInput{Text: "typed instead"})
	if got := strings.Count(stripANSI(m.View()), "typed instead"); got != 1 {
		t.Fatalf("user text rendered %d times after UserInput, want 1", got)
	}
}

func TestIdleTextSubmitEchoesImmediately(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	m.turnState = turnIdle

	m, cmd := typeAndEnter(m, "hello chat")
	if cmd == nil {
		t.Fatal("idle submit must dispatch a text turn")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "hello chat") {
		t.Fatalf("idle typed message missing from chat before pipeline events:\n%s", view)
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState = %d, want text running", m.turnState)
	}
}

// After a text turn finishes, the background voice turn resumes.
func TestVoiceResumesAfterTextTurn(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)
	m.turnState = turnTextRunning

	m, cmd := m.Update(textTurnDoneMsg{})
	if cmd == nil || m.turnState != turnVoiceListening {
		t.Fatal("voice listening did not resume after text turn")
	}
}

func TestRuntimeMuteControls(t *testing.T) {
	runner := &fakeTurnRunner{}
	bus := events.NewBus()
	outputMuted := false
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{
		runner: runner, bus: bus, voice: true, output: true,
		setOutputMuted: func(muted bool) { outputMuted = muted },
		ctx:            context.Background(),
	})

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if m.voiceEnabled || m.voiceOn() {
		t.Fatal("ctrl+g did not mute microphone input")
	}
	m.turnState = turnIdle
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if !m.voiceEnabled || cmd == nil {
		t.Fatal("second ctrl+g did not resume microphone input")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.outputMuted || !outputMuted {
		t.Fatal("ctrl+o did not mute pipeline output")
	}
}

func TestAbsoluteMuteUnmuteCommands(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)
	// startConversation dispatches a listening turn; park in idle so absolute
	// unmute can redispatch cleanly after force-mute.
	m.turnState = turnIdle
	m.voiceEnabled = true

	m, cmd := typeAndEnter(m, "/mute")
	if m.voiceEnabled {
		t.Fatal("/mute did not force voice input off")
	}
	if cmd != nil {
		t.Fatal("/mute while idle must not dispatch a turn")
	}

	// Already muted: absolute /mute must stay muted (not toggle back on).
	m, cmd = typeAndEnter(m, "/mute")
	if m.voiceEnabled || cmd != nil {
		t.Fatal("muted → /mute must stay muted without redispach")
	}

	m, cmd = typeAndEnter(m, "/unmute")
	if !m.voiceEnabled || cmd == nil {
		t.Fatal("/unmute did not force voice input on and resume listening")
	}
	// Drain the resume cmd so the fake runner is not left mid-turn.
	_ = cmd()

	// Already unmuted: absolute /unmute must stay unmuted (not toggle off).
	before := runner.calls()
	m.turnState = turnIdle
	m, cmd = typeAndEnter(m, "/unmute")
	if !m.voiceEnabled {
		t.Fatal("unmuted → /unmute must stay unmuted")
	}
	if cmd != nil {
		_ = cmd()
	}
	if runner.calls() != before {
		t.Fatal("unmuted → /unmute must not redispatch another listening turn")
	}

	// /mic remains a toggle (Ctrl+G alias).
	m.turnState = turnIdle
	m, _ = typeAndEnter(m, "/mic")
	if m.voiceEnabled {
		t.Fatal("/mic did not toggle voice input off")
	}
}

// Muting while STT is still listening cancels the in-flight turn and does not
// redispatch voice listening while input stays paused. Ctrl+G mutes immediately;
// a typed /mute is parked as pending text and applied after the cancel drains.
func TestMuteWhileListeningCancelsWithoutRedispatch(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := startedConversation(t, runner, true)

	voiceCmd := m.dispatchVoiceTurn()
	voiceDone := make(chan tea.Msg, 1)
	go func() { voiceDone <- voiceCmd() }()

	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want listening", m.turnState)
	}

	// Immediate mute (Ctrl+G): cancel listening now; no redispatch while off.
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if m.voiceEnabled {
		t.Fatal("ctrl+g did not pause voice input")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling after mute while listening", m.turnState)
	}
	if cmd != nil {
		t.Fatal("mute while listening must wait for cancel drain, not dispatch")
	}

	var msg tea.Msg
	select {
	case msg = <-voiceDone:
	case <-time.After(2 * time.Second):
		t.Fatal("listening turn was not canceled by mute")
	}

	m, cmd = m.Update(msg)
	if m.voiceEnabled {
		t.Fatal("voice re-enabled after cancel drain")
	}
	if m.turnState != turnIdle {
		t.Fatalf("turnState = %d after cancel drain, want idle", m.turnState)
	}
	if cmd != nil {
		t.Fatal("canceled listening mute must not redispatch while voice input is off")
	}
}
