package tui

import (
	"context"
	"testing"
)

type tuiBackgroundRunner struct {
	fakeTurnRunner
	ready    chan struct{}
	pending  bool
	captured bool
	calls    int
}

type pausingBackgroundRunner struct {
	tuiBackgroundRunner
	pauses int
	ack    bool
}

type captureReadinessRunner struct {
	tuiBackgroundRunner
	ready bool
}

func (r *captureReadinessRunner) BackgroundCaptureReady() bool { return r.ready }

func (r *pausingBackgroundRunner) PauseForCompletion(context.Context) (bool, error) {
	r.pauses++
	return r.ack, nil
}

func (r *tuiBackgroundRunner) BackgroundReady() <-chan struct{} { return r.ready }
func (r *tuiBackgroundRunner) BackgroundPending() bool          { return r.pending }
func (r *tuiBackgroundRunner) RunBackgroundTurn(context.Context) error {
	r.calls++
	r.pending = false
	return nil
}
func TestBackgroundWaitsForVoiceOwnershipAndRunsAtBoundary(t *testing.T) {
	runner := &tuiBackgroundRunner{ready: make(chan struct{}, 1), pending: true}
	m := conversationModel{deps: conversationDeps{runner: runner, ctx: context.Background()}, turnState: turnVoiceListening, pendingBackground: true}
	if cmd := m.resumeListening(); cmd != nil {
		t.Fatal("background interrupted live STT")
	}
	if !m.pendingBackground {
		t.Fatal("pending result was lost")
	}
	m.turnState = turnVoiceResponding
	if cmd := m.resumeListening(); cmd != nil {
		t.Fatal("background interrupted spoken reply")
	}
	m.turnState = turnIdle
	cmd := m.resumeListening()
	if cmd == nil {
		t.Fatal("idle result not dispatched")
	}
	if _, ok := cmd().(textTurnDoneMsg); !ok {
		t.Fatal("background bypassed ordinary turn completion")
	}
	if runner.calls != 1 {
		t.Fatalf("background calls=%d", runner.calls)
	}
	if len(runner.texts()) != 0 {
		t.Fatal("background fabricated a text user turn")
	}
}

func (r *tuiBackgroundRunner) HasCapturedInput() bool { return r.captured }
func TestCapturedBargeInBeatsDeferredBackgroundBrief(t *testing.T) {
	runner := &tuiBackgroundRunner{ready: make(chan struct{}, 1), pending: true, captured: true}
	m := conversationModel{deps: conversationDeps{runner: runner, ctx: context.Background(), voice: true}, voiceEnabled: true, turnState: turnIdle, pendingBackground: true}
	cmd := m.resumeListening()
	if cmd == nil || m.turnState != turnVoiceListening {
		t.Fatal("retained speech did not win next pipeline turn")
	}
	if runner.calls != 0 {
		t.Fatal("background spoke before captured utterance")
	}
	if !m.pendingBackground {
		t.Fatal("captured utterance cleared the deferred background result")
	}
	_ = cmd()
	// The captured turn ends without another BackgroundReady edge. The next
	// idle boundary must observe the still-pending result and deliver it.
	runner.captured = false
	m.turnState = turnIdle
	background := m.resumeListening()
	if background == nil || m.turnState != turnTextRunning {
		t.Fatal("pending result was not delivered at the next boundary")
	}
	msg := background()
	if _, ok := msg.(textTurnDoneMsg); !ok || runner.calls != 1 {
		t.Fatalf("background delivery = %T, calls=%d; want one text turn", msg, runner.calls)
	}
}

func TestCapturedSpeechPrecedesQueuedTextAndThenBackground(t *testing.T) {
	runner := &tuiBackgroundRunner{ready: make(chan struct{}, 1), captured: true}
	m := conversationModel{
		deps:         conversationDeps{runner: runner, ctx: context.Background(), voice: true},
		voiceEnabled: true,
		turnState:    turnIdle,
		pendingText:  "queued typed turn",
	}
	voice := m.resumeListening()
	if voice == nil || m.turnState != turnVoiceListening || m.pendingText == "" {
		t.Fatalf("captured speech did not precede queued text: state=%v pending=%q", m.turnState, m.pendingText)
	}
	if _, ok := voice().(voiceTurnDoneMsg); !ok {
		t.Fatal("captured voice command did not complete as a voice turn")
	}
	runner.captured = false
	m.turnState = turnIdle
	text := m.resumeListening()
	if text == nil || m.turnState != turnTextRunning {
		t.Fatalf("queued text did not follow captured speech: state=%v", m.turnState)
	}
	if _, ok := text().(textTurnDoneMsg); !ok {
		t.Fatal("queued text command did not complete as a text turn")
	}
	if got := runner.texts(); len(got) != 1 || got[0] != "queued typed turn" {
		t.Fatalf("queued text inputs = %v", got)
	}
	runner.pending = true
	m.turnState = turnIdle
	background := m.handleTextTurnDone(textTurnDoneMsg{})
	if background == nil || m.turnState != turnTextRunning {
		t.Fatalf("background result did not follow queued text: state=%v", m.turnState)
	}
	msg := background()
	if _, ok := msg.(textTurnDoneMsg); !ok || runner.calls != 1 {
		t.Fatalf("background completion = %T, calls=%d", msg, runner.calls)
	}
}

func TestBackgroundRecoveryFindsPendingResultWithoutReadyEdge(t *testing.T) {
	runner := &tuiBackgroundRunner{ready: make(chan struct{}), pending: true}
	m := conversationModel{
		deps:      conversationDeps{runner: runner, ctx: context.Background()},
		turnState: turnIdle,
	}

	// No BackgroundReady notification is sent. The recovery message must read
	// the current inbox level and enter the existing serialized turn path.
	m, _ = m.Update(backgroundRecoveryMsg{})
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState = %v, want background text turn after lost wake", m.turnState)
	}
	if m.pendingBackground {
		t.Fatal("claimed background result remained marked pending")
	}
	if runner.calls != 0 {
		t.Fatal("recovery must only claim work; command has not run yet")
	}
}

func TestBackgroundRecoveryDoesNotPreemptQueuedText(t *testing.T) {
	runner := &tuiBackgroundRunner{ready: make(chan struct{}), pending: true}
	m := conversationModel{
		deps:        conversationDeps{runner: runner, ctx: context.Background()},
		turnState:   turnIdle,
		pendingText: "queued input",
	}
	cmd := m.reconcileBackground()
	if cmd == nil || m.turnState != turnTextRunning {
		t.Fatal("background recovery did not dispatch queued text first")
	}
	if _, ok := cmd().(textTurnDoneMsg); !ok || runner.calls != 0 {
		t.Fatalf("recovery changed queued text state: calls=%d state=%v", runner.calls, m.turnState)
	}
}

func TestBackgroundPauseWaitsForVoiceCompletion(t *testing.T) {
	runner := &pausingBackgroundRunner{
		tuiBackgroundRunner: tuiBackgroundRunner{ready: make(chan struct{}), pending: true},
		ack:                 true,
	}
	m := conversationModel{
		deps:         conversationDeps{runner: runner, ctx: context.Background(), voice: true},
		voiceEnabled: true,
		turnState:    turnVoiceListening,
	}
	cmd := m.reconcileBackground()
	if cmd == nil || runner.pauses != 0 || m.turnState != turnVoiceListening {
		t.Fatalf("pause request: cmd=%v pauses=%d state=%v", cmd != nil, runner.pauses, m.turnState)
	}
	msg := cmd()
	if runner.pauses != 1 {
		t.Fatalf("pause command calls=%d, want 1", runner.pauses)
	}
	if _, ok := msg.(backgroundPauseDoneMsg); !ok {
		t.Fatalf("pause command returned %T", msg)
	}
	m, _ = m.Update(msg)
	if !m.backgroundPauseAcknowledged || m.turnState != turnVoiceListening {
		t.Fatal("acknowledged pause forced idle or was not retained")
	}
	if cmd := m.handleVoiceTurnDone(voiceTurnDoneMsg{}); cmd == nil || m.turnState != turnTextRunning {
		t.Fatal("voice completion did not deliver pending background result")
	}
}

func TestBackgroundWaitsForCaptureOwnerReadiness(t *testing.T) {
	runner := &captureReadinessRunner{
		tuiBackgroundRunner: tuiBackgroundRunner{ready: make(chan struct{}), pending: true},
	}
	m := conversationModel{
		deps:         conversationDeps{runner: runner, ctx: context.Background(), voice: true},
		voiceEnabled: true,
		turnState:    turnIdle,
	}
	cmd := m.resumeListening()
	if cmd == nil || m.turnState != turnVoiceListening || !m.pendingBackground {
		t.Fatalf("capture-unready result was claimed: cmd=%v state=%v pending=%v", cmd != nil, m.turnState, m.pendingBackground)
	}
}
