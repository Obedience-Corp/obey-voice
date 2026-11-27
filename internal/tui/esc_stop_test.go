package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func esc(m conversationModel) (conversationModel, tea.Cmd) {
	return m.Update(tea.KeyMsg{Type: tea.KeyEsc})
}

// escRunner records stop sources and any call that would touch background workers.
type escRunner struct {
	sourceRecordingRunner
	workerCalls int
}

func (r *escRunner) Workers() []pipeline.WorkerInfo { return nil }
func (r *escRunner) SetWorkersEnabled(bool)         { r.workerCalls++ }
func (r *escRunner) CancelWork(string) error        { r.workerCalls++; return nil }
func (r *escRunner) StopWorkers() error             { r.workerCalls++; return nil }

func (r *escRunner) stops() []pipeline.StopSource {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pipeline.StopSource(nil), r.sources...)
}

// textTurnRunning leaves one typed message's turn in flight with a counted cancel.
func textTurnRunning(t *testing.T) (conversationModel, *escRunner, *int) {
	t.Helper()
	runner := &escRunner{}
	m := sizedConversation(t, 100, 30)
	m.startConversation(conversationDeps{runner: runner, bus: events.NewBus(), ctx: t.Context()})
	m, cmd := typeAndEnter(m, "summarize the notes")
	if cmd == nil || m.turnState != turnTextRunning {
		t.Fatalf("setup: cmd=%v state=%d, want a running text turn", cmd != nil, m.turnState)
	}
	cancels := 0
	m.turnCancel = func() { cancels++ }
	return m, runner, &cancels
}

func assertStoppedWithEsc(t *testing.T, m conversationModel, runner *escRunner, cancels int) {
	t.Helper()
	if m.turnState != turnVoiceCanceling || cancels != 1 {
		t.Fatalf("after Esc: state=%d cancels=%d; want canceling, 1", m.turnState, cancels)
	}
	if got := runner.stops(); len(got) != 1 || got[0] != (pipeline.StopSource{Name: "escape"}) {
		t.Fatalf("stop sources = %+v, want one escape", got)
	}
}

func assertNotStopped(t *testing.T, m conversationModel, want turnState, cancels int) {
	t.Helper()
	if m.turnState != want || cancels != 0 {
		t.Fatalf("state=%d cancels=%d; want state %d and no cancel", m.turnState, cancels, want)
	}
}

func TestEscStopsARunningTextTurn(t *testing.T) {
	m, runner, cancels := textTurnRunning(t)

	m, cmd := esc(m)
	if cmd != nil {
		t.Fatal("Esc started something")
	}
	assertStoppedWithEsc(t, m, runner, *cancels)
	if !strings.Contains(m.status, "Stopped with Esc") {
		t.Fatalf("status = %q, want the stop named", m.status)
	}
	found := false
	for _, entry := range m.activity {
		found = found || entry.stage == "turn" && entry.detail == "stopped with Esc"
	}
	if !found {
		t.Fatalf("activity does not record the stop: %+v", m.activity)
	}

	m, cmd = m.Update(textTurnDoneMsg{err: context.Canceled})
	if cmd != nil || m.turnState != turnIdle {
		t.Fatalf("after the drain: cmd=%v state=%d; want idle with nothing started", cmd != nil, m.turnState)
	}
	if got := runner.texts(); len(got) > 1 {
		t.Fatalf("the stopped message was sent again: %q", got)
	}
	if runner.workerCalls != 0 {
		t.Fatalf("Esc touched background workers %d times", runner.workerCalls)
	}
}

// With or without a talk key, Esc queues no talk turn; the mic just listens again.
func TestEscStopsAVoiceReply(t *testing.T) {
	for name, attach := range map[string]bool{"no talk key": false, "talk key attached": true} {
		t.Run(name, func(t *testing.T) {
			runner := &escRunner{}
			runner.voiceQueue = []voiceScript{{block: true}}
			m := sizedConversation(t, 100, 30)
			m.startConversation(conversationDeps{runner: runner, bus: events.NewBus(), voice: true, ctx: t.Context()})
			if attach {
				combo, _ := talkhotkey.ParseCombo(talkhotkey.DefaultCombo)
				m.attachTalk(combo, talkhotkey.ModeToggle)
			} else if m.talk != nil {
				t.Fatal("setup: a talk key is attached")
			}
			m = recogniserEnded(t, m, "read me the summary")
			cancels := 0
			m.turnCancel = func() { cancels++ }

			m, _ = esc(m)
			assertStoppedWithEsc(t, m, runner, cancels)
			if m.talk != nil && m.talk.pendingTurn {
				t.Fatal("Esc queued a talk turn")
			}

			m, _ = m.Update(voiceTurnDoneMsg{err: context.Canceled})
			if m.turnState != turnVoiceListening {
				t.Fatalf("after the drain: state=%d, want listening again", m.turnState)
			}
			if m.talkKeyTurn.Load() {
				t.Fatal("the reopened mic was handed to the talk key")
			}
		})
	}
}

func TestEscInVimInsertGoesToNormalWithoutStopping(t *testing.T) {
	m, _, cancels := textTurnRunning(t)
	m.vim.enabled, m.vim.mode = true, vimInsert

	m, _ = esc(m)
	if m.vim.mode != vimNormal {
		t.Fatalf("vim mode = %v, want NORMAL", m.vim.mode)
	}
	assertNotStopped(t, m, turnTextRunning, *cancels)
}

func TestEscInVimNormalClearsAPendingKeyBeforeStopping(t *testing.T) {
	m, runner, cancels := textTurnRunning(t)
	m.vim.enabled, m.vim.mode = true, vimNormal
	m, _ = m.Update(keyRune('d'))
	if m.vim.pending.kind != vimPendingOperator {
		t.Fatal("setup: d did not leave an operator pending")
	}

	m, _ = esc(m)
	if m.vim.pending.kind != vimPendingNone {
		t.Fatalf("pending = %+v, want cleared", m.vim.pending)
	}
	assertNotStopped(t, m, turnTextRunning, *cancels)

	m, _ = esc(m)
	assertStoppedWithEsc(t, m, runner, *cancels)
}

// Everything Esc dismissed before is still dismissed first, and the turn keeps running.
func TestEscDismissalsWinOverStop(t *testing.T) {
	cases := map[string]struct {
		setup     func(t *testing.T, m conversationModel) conversationModel
		dismissed func(m conversationModel) bool
	}{
		"path mentions": {
			setup: func(t *testing.T, m conversationModel) conversationModel {
				root := pathFixture(t)
				m.mentions.paths = func() (string, map[string]string) { return root, map[string]string{"p": "projects/"} }
				setMentionDraft(&m, "see @p")
				if !m.pathMentionsShowing() {
					t.Fatal("setup: no path list")
				}
				return m
			},
			dismissed: func(m conversationModel) bool { return !m.mentionsShowing() },
		},
		"vim visual": {
			setup: func(t *testing.T, m conversationModel) conversationModel {
				m.vim.enabled, m.vim.mode = true, vimNormal
				m.input.SetValue("draft")
				m, _ = m.Update(keyRune('v'))
				return m
			},
			dismissed: func(m conversationModel) bool { return m.vim.mode == vimNormal },
		},
		"selection": {
			setup: func(t *testing.T, m conversationModel) conversationModel {
				m.input.SetValue("draft")
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
				if !m.editor.selectionActive() {
					t.Fatal("setup: nothing selected")
				}
				return m
			},
			dismissed: func(m conversationModel) bool { return !m.editor.selectionActive() },
		},
		"activity filter": {
			setup: func(t *testing.T, m conversationModel) conversationModel {
				m.activityFocused, m.workFilter = true, "job-1"
				return m
			},
			dismissed: func(m conversationModel) bool { return m.workFilter == "" && m.activityFocused },
		},
		"activity panel": {
			setup: func(t *testing.T, m conversationModel) conversationModel {
				m.activityFocused = true
				return m
			},
			dismissed: func(m conversationModel) bool { return !m.activityFocused },
		},
		"picture panel": {
			setup: func(t *testing.T, m conversationModel) conversationModel {
				m.pictures.open = true
				return m
			},
			dismissed: func(m conversationModel) bool { return !m.pictures.open },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, runner, cancels := textTurnRunning(t)
			m = tc.setup(t, m)
			m, _ = esc(m)
			if !tc.dismissed(m) {
				t.Fatal("Esc did not dismiss what it dismissed before")
			}
			assertNotStopped(t, m, turnTextRunning, *cancels)
			if got := runner.stops(); len(got) != 0 {
				t.Fatalf("playback stopped by %+v", got)
			}
		})
	}
}

// Listening, an in-flight cancel and idle are left alone.
func TestEscIsANoOpWithoutARunningTurn(t *testing.T) {
	for _, state := range []turnState{turnIdle, turnVoiceListening, turnVoiceCanceling} {
		runner := &escRunner{}
		m := sizedConversation(t, 100, 30)
		m.startConversation(conversationDeps{runner: runner, bus: events.NewBus(), ctx: t.Context()})
		m.turnState = state
		cancels := 0
		m.turnCancel = func() { cancels++ }

		m, cmd := esc(m)
		if cmd != nil {
			t.Fatalf("state %d: Esc returned a command", state)
		}
		assertNotStopped(t, m, state, cancels)
		if got := runner.stops(); len(got) != 0 {
			t.Fatalf("state %d: playback stopped by %+v", state, got)
		}
	}
}

func TestEscStopLeavesTheDraftAlone(t *testing.T) {
	m, runner, cancels := textTurnRunning(t)
	m.input.SetValue("half a thought")

	m, _ = esc(m)
	assertStoppedWithEsc(t, m, runner, *cancels)
	if got := m.input.Value(); got != "half a thought" {
		t.Fatalf("draft = %q, want it untouched", got)
	}
	m, _ = m.Update(textTurnDoneMsg{err: context.Canceled, input: "summarize the notes"})
	if got := m.input.Value(); got != "half a thought" {
		t.Fatalf("draft after the drain = %q, want it untouched", got)
	}
}

// The composer label names Esc only while Esc would stop the turn.
func TestComposerLabelNamesEscOnlyWhenItStops(t *testing.T) {
	const hint = "esc to stop"
	m := sizedConversation(t, 110, 30)
	m.turnState = turnTextRunning
	if view := stripANSI(m.View()); !strings.Contains(view, "Responding — send a message to interrupt, "+hint) {
		t.Fatalf("responding label lacks the Esc hint:\n%s", view)
	}
	m.vim.enabled, m.vim.mode = true, vimInsert
	if view := stripANSI(m.View()); strings.Contains(view, hint) {
		t.Fatalf("vim INSERT label offers Esc as stop:\n%s", view)
	}
	m.vim.enabled = false
	m.turnState = turnVoiceListening
	if view := stripANSI(m.View()); strings.Contains(view, hint) {
		t.Fatalf("listening label offers Esc as stop:\n%s", view)
	}
}

// An open command or persona list keeps Esc from stopping the turn under it.
func TestEscWithAListOpenDoesNotStop(t *testing.T) {
	for name, draft := range map[string]string{"command list": "/", "persona list": "@"} {
		t.Run(name, func(t *testing.T) {
			m, runner, cancels := textTurnRunning(t)
			setMentionDraft(&m, draft)
			if !m.mentionsShowing() && len(matchingSlashCommands(m.input.Value())) == 0 {
				t.Fatal("setup: no list showing")
			}
			if strings.Contains(stripANSI(m.View()), "esc to stop") {
				t.Fatal("label offers Esc as stop with a list open")
			}
			m, _ = esc(m)
			assertNotStopped(t, m, turnTextRunning, *cancels)

			setMentionDraft(&m, "")
			m, _ = esc(m)
			assertStoppedWithEsc(t, m, runner, *cancels)
		})
	}
}

// pendingResultRunner keeps reporting its result as pending, the way an interrupted delivery does.
type pendingResultRunner struct {
	escRunner
	deliveries int
}

func (r *pendingResultRunner) BackgroundReady() <-chan struct{} { return nil }
func (r *pendingResultRunner) BackgroundPending() bool          { return true }
func (r *pendingResultRunner) RunBackgroundTurn(ctx context.Context) error {
	r.deliveries++
	return ctx.Err()
}

// A result delivery stopped with Esc is not started again until the user has had a turn.
func TestEscOnAResultDeliveryHoldsItUntilTheNextUserTurn(t *testing.T) {
	runner := &pendingResultRunner{}
	m := sizedConversation(t, 100, 30)
	m.startConversation(conversationDeps{runner: runner, bus: events.NewBus(), ctx: t.Context()})
	if cmd := m.resumeListening(); cmd == nil || m.turnState != turnTextRunning {
		t.Fatalf("setup: state=%d, want the pending result being delivered", m.turnState)
	}

	m, _ = esc(m)
	if m.turnState != turnVoiceCanceling || !strings.Contains(m.status, "waits for your next message") {
		t.Fatalf("after Esc: state=%d status=%q", m.turnState, m.status)
	}
	m, _ = m.Update(textTurnDoneMsg{err: context.Canceled})
	if m.turnState != turnIdle {
		t.Fatalf("state after the drain = %d; the stopped delivery started again", m.turnState)
	}
	m, _ = m.Update(backgroundReadyMsg{})
	m, _ = m.Update(backgroundRecoveryMsg{})
	if m.turnState != turnIdle {
		t.Fatalf("state = %d; a held result was delivered before the user's turn", m.turnState)
	}

	m, cmd := typeAndEnter(m, "thanks")
	if cmd == nil || m.turnState != turnTextRunning {
		t.Fatalf("typed turn did not start: state=%d", m.turnState)
	}
	m, _ = m.Update(textTurnDoneMsg{input: "thanks"})
	if m.turnState != turnTextRunning || !m.backgroundTurn {
		t.Fatalf("state=%d delivery=%v; the held result was not delivered after the user's turn", m.turnState, m.backgroundTurn)
	}
}

// Esc on an ordinary turn leaves a pending result free to be delivered next.
func TestEscOnAnOrdinaryTurnDoesNotHoldAPendingResult(t *testing.T) {
	runner := &pendingResultRunner{}
	m := sizedConversation(t, 100, 30)
	m.startConversation(conversationDeps{runner: runner, bus: events.NewBus(), ctx: t.Context()})
	m, cmd := typeAndEnter(m, "summarize the notes")
	if cmd == nil || m.turnState != turnTextRunning || m.backgroundTurn {
		t.Fatalf("setup: state=%d delivery=%v", m.turnState, m.backgroundTurn)
	}
	m, _ = esc(m)
	m, _ = m.Update(textTurnDoneMsg{err: context.Canceled, input: "summarize the notes"})
	if m.turnState != turnTextRunning || !m.backgroundTurn {
		t.Fatalf("state=%d delivery=%v; want the pending result delivered", m.turnState, m.backgroundTurn)
	}
}
