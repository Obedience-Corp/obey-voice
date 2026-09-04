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
	_ = cmd()
}
