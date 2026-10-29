package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Enter while the voice turn is responding (speaking/thinking) barges in:
// cancel the voice turn and park the draft as pending text.
func TestSubmitWhileRespondingBargesIn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := startedConversation(t, runner, true)
	voiceCmd := m.dispatchVoiceTurn()
	voiceDone := make(chan tea.Msg, 1)
	go func() { voiceDone <- voiceCmd() }()

	m.handleEvent(events.UserInput{Text: "spoken words"}) // listening -> responding
	if m.turnState != turnVoiceResponding {
		t.Fatalf("turnState = %d, want responding after UserInput event", m.turnState)
	}

	m, cmd := typeAndEnter(m, "my draft")
	if cmd != nil {
		t.Fatal("barge-in submit should wait for voiceTurnDone, not dispatch immediately")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling after text barge-in", m.turnState)
	}
	if m.pendingText != "my draft" {
		t.Fatalf("pendingText = %q, want my draft", m.pendingText)
	}
	if m.input.Value() != "" {
		t.Errorf("composer should clear after barge-in: %q", m.input.Value())
	}

	// Drain the canceled voice turn; handleVoiceTurnDone should dispatch text.
	select {
	case msg := <-voiceDone:
		m, cmd = m.Update(msg)
	case <-time.After(2 * time.Second):
		t.Fatal("voice turn was not canceled by barge-in")
	}
	if m.pendingText != "" {
		t.Fatalf("pendingText still set after drain: %q", m.pendingText)
	}
	if cmd == nil && m.turnState != turnTextRunning && m.turnState != turnIdle {
		// dispatchTextTurn returns a Cmd; if fake runner is sync it may complete.
		t.Logf("after barge drain: state=%d cmd=%v", m.turnState, cmd != nil)
	}
}

// After the first typed barge-in, the agent reply is a text turn with TTS.
// Enter during that reply must cancel again — otherwise barge-in only works once.
func TestSubmitWhileTextTurnBargesInAgain(t *testing.T) {
	runner := &fakeTurnRunner{blockText: true, textStarted: make(chan struct{}, 1)}
	m, _ := startedConversation(t, runner, true)
	m.turnState = turnIdle

	// First typed message (idle path) starts a long text turn (speaking).
	m, textCmd := typeAndEnter(m, "first barge")
	if textCmd == nil {
		t.Fatal("idle submit must dispatch text turn")
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState = %d, want text running", m.turnState)
	}
	textDone := make(chan tea.Msg, 1)
	go func() { textDone <- textCmd() }()
	// dispatchTextTurn short-circuits on an already-canceled context, so the
	// first turn must actually reach the runner before we barge in — otherwise
	// the cancel races the goroutine and "first barge" is never recorded.
	waitForTexts(t, runner, 1)

	// Wait until the first turn actually records its input; otherwise the second
	// Enter can cancel the turn before RunTurnTextMode runs, dropping "first barge".
	select {
	case <-runner.textStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first text turn never entered RunTurnTextMode")
	}

	// Second Enter while the agent is still on the text turn.
	m, cmd := typeAndEnter(m, "second barge")
	if cmd != nil {
		t.Fatal("barge during text turn should wait for cancel drain")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling after second barge", m.turnState)
	}
	if m.pendingText != "second barge" {
		t.Fatalf("pendingText = %q, want second barge", m.pendingText)
	}
	if runner.stopped < 1 {
		t.Fatal("StopPlayback should run on barge-in so TTS stops immediately")
	}

	select {
	case msg := <-textDone:
		m, cmd = m.Update(msg)
	case <-time.After(2 * time.Second):
		t.Fatal("text turn was not canceled by second barge-in")
	}
	if m.pendingText != "" {
		t.Fatalf("pendingText still set after drain: %q", m.pendingText)
	}
	if cmd == nil {
		t.Fatal("cancel drain must dispatch the second text turn")
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState = %d, want text running for second barge", m.turnState)
	}
	// Run the newly dispatched text turn (non-blocking path: unblock runner).
	runner.mu.Lock()
	runner.blockText = false
	runner.mu.Unlock()
	if msg := cmd(); msg == nil {
		t.Fatal("second text turn cmd returned nil")
	}
	got := runner.texts()
	if len(got) < 2 || got[len(got)-1] != "second barge" {
		t.Fatalf("text inputs = %v, want … second barge", got)
	}
}

// UserInput can clear the cancel gate on the pipeline goroutine before the
// bridge delivers the event into Update — turnState may still be listening.
// Text barge-in must still cancel (same as responding) when turnCancel is set.
func TestSubmitAfterTranscriptGateBargesIn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, bus := startedConversation(t, runner, true)
	voiceCmd := m.dispatchVoiceTurn()
	voiceDone := make(chan tea.Msg, 1)
	go func() { voiceDone <- voiceCmd() }()

	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want listening", m.turnState)
	}
	if !m.canCancelVoice.Load() {
		t.Fatal("canCancelVoice should be true while listening")
	}

	// Synchronous bus emit flips the gate without going through handleEvent.
	bus.Emit(events.UserInput{Text: "spoken already in brain history"})
	if m.canCancelVoice.Load() {
		t.Fatal("canCancelVoice still true after UserInput emit")
	}
	if m.turnState != turnVoiceListening {
		t.Fatalf("turnState = %d, want still listening (bridge not drained)", m.turnState)
	}

	m, cmd := typeAndEnter(m, "my draft")
	if cmd != nil {
		t.Fatal("barge-in should wait for cancel drain")
	}
	if m.turnState != turnVoiceCanceling {
		t.Fatalf("turnState = %d, want canceling after barge-in past transcript gate", m.turnState)
	}
	if m.pendingText != "my draft" {
		t.Fatalf("pendingText = %q", m.pendingText)
	}
	if m.input.Value() != "" {
		t.Errorf("composer should clear: %q", m.input.Value())
	}

	select {
	case msg := <-voiceDone:
		m, _ = m.Update(msg)
	case <-time.After(2 * time.Second):
		t.Fatal("voice turn was not canceled")
	}
}

func TestSpokenExitPhraseQuits(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)

	m, cmd := m.Update(voiceTurnDoneMsg{text: "Goodbye."})
	if !m.quitting {
		t.Fatal("spoken exit phrase did not mark quitting")
	}
	if cmd == nil {
		t.Fatal("no quit cmd returned")
	}
	if _, ok := cmd().(quitMsg); !ok {
		t.Fatal("exit did not produce quitMsg")
	}
}

func TestTypedExitQuitsWithoutTurn(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false) // text-only: idle state
	m, cmd := typeAndEnter(m, "/q")

	if !m.quitting || cmd == nil {
		t.Fatal("typed /q did not quit")
	}
	if len(runner.texts()) != 0 {
		t.Error("exit command must not reach the brain")
	}
}

func TestClearCommandClearsHistoryNotBrainTurn(t *testing.T) {
	cleared := false
	bus := events.NewBus()
	runner := &fakeTurnRunner{}
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{
		runner:       runner,
		bus:          bus,
		clearHistory: func() { cleared = true },
		voice:        false,
		ctx:          context.Background(),
	})

	m, _ = typeAndEnter(m, "/clear")
	if !cleared {
		t.Fatal("clearHistory not called")
	}
	if len(runner.texts()) != 0 {
		t.Error("clear command must not reach the brain")
	}
}

func TestVoiceFailureFallsBackAfterThreshold(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)
	failure := errors.New("mic exploded")

	// Two retries, then fallback on the third consecutive failure.
	for i := range 2 {
		var cmd tea.Cmd
		m, cmd = m.Update(voiceTurnDoneMsg{err: failure})
		if cmd == nil {
			t.Fatalf("failure %d: no retry tick scheduled", i+1)
		}
		if !m.voiceEnabled {
			t.Fatalf("failure %d: fell back too early", i+1)
		}
		m.turnState = turnVoiceListening // simulate the retried dispatch
	}

	m, _ = m.Update(voiceTurnDoneMsg{err: failure})
	if m.voiceEnabled {
		t.Fatal("three consecutive failures did not fall back to text")
	}

	// /voice re-enables and dispatches a listening turn.
	before := runner.calls()
	m, cmd := typeAndEnter(m, "/voice")
	if !m.voiceEnabled {
		t.Fatal("/voice did not re-enable voice")
	}
	if cmd == nil {
		t.Fatal("/voice did not resume listening")
	}
	cmd()
	if runner.calls() != before+1 {
		t.Fatal("resumed listening did not call RunTurn")
	}
}

func TestVoiceErrorSurfacesOnBus(t *testing.T) {
	runner := &fakeTurnRunner{}
	bus := events.NewBus()
	var errMsgs []string
	events.Subscribe(bus, func(e events.Error) { errMsgs = append(errMsgs, e.Message) })

	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: context.Background()})

	m.Update(voiceTurnDoneMsg{err: errors.New("transient stt hiccup")})
	if len(errMsgs) != 1 || !strings.Contains(errMsgs[0], "transient stt hiccup") {
		t.Fatalf("voice failure not emitted as events.Error: %v", errMsgs)
	}
}
