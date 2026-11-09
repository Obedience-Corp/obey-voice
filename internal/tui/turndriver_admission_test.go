package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestAdmissionFailureNoticeShowsCategoryAndOriginalInput(t *testing.T) {
	runner := &fakeTurnRunner{}
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: context.Background()})
	m.handleEvent(events.UserInput{Text: "Santa what do you think"})

	m, _ = m.Update(voiceTurnDoneMsg{
		text:        "Santa what do you think",
		err:         admissionFailure(admission.FailureClassifierLocalTimeout, "classifier timed out"),
		noticeEpoch: m.noticeEpoch,
	})

	view := stripANSI(m.View())
	if !strings.Contains(view, "Santa what do you think") {
		t.Fatalf("original input missing from transcript:\n%s", view)
	}
	found := false
	for _, info := range infos {
		if strings.Contains(info, "classifier timeout") &&
			strings.Contains(info, "Resubmit this request manually") &&
			strings.Contains(info, `Original input: "Santa what do you think"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("manual retry notice missing or incomplete: %v", infos)
	}
}

func TestAdmissionFailureManualResubmissionDispatchesTypedTurn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{
		{text: "Santa what do you think", err: admissionFailure(admission.FailureClassifierUnavailable, "classifier offline")},
		{block: true},
	}}
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: context.Background()})

	first := m.dispatchVoiceTurn()
	m, retryTick := m.Update(first())
	if retryTick == nil {
		t.Fatal("response-stage refusal did not schedule a retry")
	}
	m, retryDispatch := m.Update(voiceRetryMsg{})
	if retryDispatch == nil {
		t.Fatal("voice retry message did not resume listening")
	}
	voiceDone := make(chan tea.Msg, 1)
	go func() { voiceDone <- retryDispatch() }()

	resubmitted := "Santa what do you think"
	m, cmd := typeAndEnter(m, resubmitted)
	if cmd != nil {
		t.Fatal("manual retry should wait for the canceled listening turn")
	}
	if m.turnState != turnVoiceCanceling || m.pendingText != resubmitted {
		t.Fatalf("turn state=%d pending=%q", m.turnState, m.pendingText)
	}

	var canceled tea.Msg
	select {
	case canceled = <-voiceDone:
	case <-time.After(2 * time.Second):
		t.Fatal("manual retry did not cancel the in-flight listening turn")
	}
	m, cmd = m.Update(canceled)
	if cmd == nil {
		t.Fatal("cancel drain did not dispatch the typed retry")
	}
	if m.turnState != turnTextRunning {
		t.Fatalf("turnState=%d want text running", m.turnState)
	}
	if done := cmd(); done == nil {
		t.Fatal("typed retry cmd returned nil")
	}
	if got := runner.texts(); len(got) != 1 || got[0] != resubmitted {
		t.Fatalf("typed retries=%v", got)
	}

	foundNotice := false
	for _, info := range infos {
		if strings.Contains(info, "Resubmit this request manually") &&
			strings.Contains(info, `Original input: "Santa what do you think"`) {
			foundNotice = true
		}
	}
	if !foundNotice {
		t.Fatalf("manual retry notice missing in terminal path: %v", infos)
	}
}

func TestAdmissionFailureNoticeRetiredByClearSuppressesLateCallbacks(t *testing.T) {
	runner := &fakeTurnRunner{}
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: context.Background()})
	staleEpoch := m.noticeEpoch
	m.handleEvent(events.ConversationCleared{})

	m, _ = m.Update(voiceTurnDoneMsg{
		text:        "Santa what do you think",
		err:         admissionFailure(admission.FailureClassifierUnavailable, "offline"),
		noticeEpoch: staleEpoch,
	})

	for _, info := range infos {
		if strings.Contains(info, "Resubmit this request manually") {
			t.Fatalf("late callback resurrected retired notice: %v", infos)
		}
	}
}

func TestAdmissionFailureNoticeRetiredBySessionRestartSuppressesLateCallbacks(t *testing.T) {
	runner := &fakeTurnRunner{}
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: context.Background(), sessionID: "first"})
	staleEpoch := m.noticeEpoch
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: context.Background(), sessionID: "restored"})

	m, _ = m.Update(voiceTurnDoneMsg{
		text:        "Santa what do you think",
		err:         admissionFailure(admission.FailureClassifierUnavailable, "offline"),
		noticeEpoch: staleEpoch,
	})

	for _, info := range infos {
		if strings.Contains(info, "Resubmit this request manually") {
			t.Fatalf("restored session accepted stale notice callback: %v", infos)
		}
	}
}

func TestRecognizedVoiceResponseErrorsRetryWithoutDisablingVoice(t *testing.T) {
	runner := &fakeTurnRunner{}
	bus := events.NewBus()
	var errMsgs []string
	events.Subscribe(bus, func(e events.Error) { errMsgs = append(errMsgs, e.Message) })
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: context.Background()})
	m.voiceFailures = 2 // a prior pair of genuine input failures

	for i := 0; i < 3; i++ {
		var cmd tea.Cmd
		m, cmd = m.Update(voiceTurnDoneMsg{text: "Veronica", err: errors.New("admit response: roster collision")})
		if cmd == nil {
			t.Fatalf("response failure %d did not schedule a retry", i+1)
		}
		if !m.voiceEnabled || m.voiceFailures != 0 {
			t.Fatalf("response failure %d changed input fallback state: enabled=%v failures=%d", i+1, m.voiceEnabled, m.voiceFailures)
		}
		m.turnState = turnVoiceListening // simulate the retry dispatch
	}
	if len(errMsgs) != 3 || !strings.Contains(errMsgs[0], "roster collision") {
		t.Fatalf("response errors were not surfaced on the event bus: %v", errMsgs)
	}
}

// TestVoiceFailurePolicyTerminalProof drives the real Bubble Tea program and
// event bridge. Response failures retain listening, while empty transcripts
// consume the bounded input-failure budget and switch to text.
func TestVoiceFailurePolicyTerminalProof(t *testing.T) {
	for _, tc := range []struct {
		name         string
		text         string
		wantEnabled  bool
		wantFallback bool
	}{
		{name: "response failure keeps voice", text: "Veronica", wantEnabled: true},
		{name: "input failure falls back", wantFallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			bus := events.NewBus()
			model := sizedConversation(t, 120, 30)
			failure := errors.New("admit response: roster collision")
			if tc.wantFallback {
				failure = errors.New("STT: microphone unavailable")
			}
			runner := &fakeTurnRunner{voiceQueue: []voiceScript{
				{text: tc.text, err: failure}, {text: tc.text, err: failure}, {text: tc.text, err: failure},
			}}
			model.deps = conversationDeps{runner: runner, bus: bus, voice: true, ctx: ctx}
			harness := &voiceFailureTerminalHarness{model: model, wantFallback: tc.wantFallback}
			var terminal bytes.Buffer
			program := tea.NewProgram(harness, tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithContext(ctx), tea.WithoutSignalHandler())
			if _, err := program.Run(); err != nil {
				t.Fatal(err)
			}
			if harness.model.voiceEnabled != tc.wantEnabled {
				t.Fatalf("voice enabled=%v, want %v", harness.model.voiceEnabled, tc.wantEnabled)
			}
			rendered := stripANSI(terminal.String())
			if !strings.Contains(rendered, "admit response: roster collision") && !tc.wantFallback {
				t.Fatalf("response error missing from terminal:\n%s", rendered)
			}
			if tc.wantFallback != strings.Contains(rendered, "switching to text") {
				t.Fatalf("fallback notice mismatch in terminal:\n%s", rendered)
			}
			runner.mu.Lock()
			calls := runner.voiceCalls
			runner.mu.Unlock()
			if calls != 3 {
				t.Fatalf("voice attempts = %d, want 3", calls)
			}
			if !tc.wantFallback && !harness.retryScheduled {
				t.Fatal("third response error did not schedule another voice attempt")
			}
			t.Logf("terminal output:\n%s", rendered)
		})
	}
}

type voiceFailureTerminalHarness struct {
	model          conversationModel
	wantFallback   bool
	errors         int
	completed      int
	retryScheduled bool
}

func TestVoiceFailureTerminalHarnessStopsAfterThirdResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{"retry tick", voiceRetryMsg{}},
		{"background recovery tick", backgroundRecoveryMsg{}},
		{"background ready notification", backgroundReadyMsg{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := sizedConversation(t, 80, 24)
			model.startConversation(conversationDeps{
				runner: &fakeTurnRunner{}, bus: events.NewBus(), voice: true, ctx: t.Context(),
			})
			// The third response failure has completed, but its error event has
			// not crossed the terminal bridge yet. Exercise each queued wakeup
			// deterministically instead of relying on the 500ms timer racing it.
			model.turnState = turnIdle
			harness := &voiceFailureTerminalHarness{model: model, completed: 3}
			_, cmd := harness.Update(tc.msg)
			if cmd != nil || harness.model.turnState != turnIdle {
				t.Fatal("terminal proof dispatched beyond its three scripted turns")
			}
			if !harness.model.voiceEnabled {
				t.Fatal("terminal proof changed the voice-enabled result being observed")
			}
		})
	}
}

func (h *voiceFailureTerminalHarness) Init() tea.Cmd {
	return h.model.startConversation(h.model.deps)
}

func (h *voiceFailureTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Retry and background-recovery wakeups can both resume an idle listener.
	// Once the three scripted response failures finish, keep draining the event
	// bridge for rendering without dispatching another fixture turn. Leave the
	// model's voice-enabled state intact: that is the result this proof checks.
	if h.completed >= 3 && !h.wantFallback {
		switch msg.(type) {
		case voiceRetryMsg, backgroundRecoveryMsg, backgroundReadyMsg:
			return h, nil
		}
	}
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if _, ok := msg.(voiceTurnDoneMsg); ok {
		h.completed++
		if h.completed == 3 && !h.wantFallback {
			// Observe the third retry without executing a fourth fixture turn while
			// the event bridge catches up with the third error for rendering.
			h.retryScheduled = cmd != nil
			return h, nil
		}
	}
	bridged, ok := msg.(busEventMsg)
	if !ok {
		return h, cmd
	}
	if _, ok := bridged.event.(events.Error); ok {
		h.errors++
	}
	if h.wantFallback {
		if info, ok := bridged.event.(events.Info); ok && strings.Contains(info.Message, "switching to text") {
			h.model, _ = h.model.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
			return h, tea.Quit
		}
	} else if h.errors == 3 {
		h.model, _ = h.model.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
		return h, tea.Quit
	}
	return h, cmd
}

func (h *voiceFailureTerminalHarness) View() string { return h.model.View() }

func TestShutdownClassificationQuits(t *testing.T) {
	runner := &fakeTurnRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	bus := events.NewBus()
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: true, ctx: ctx})

	cancel() // parent ctx gone: the program is shutting down
	m, cmd := m.Update(voiceTurnDoneMsg{err: context.Canceled})
	if !m.quitting || cmd == nil {
		t.Fatal("canceled parent ctx did not quit the conversation")
	}
}

func TestTurnWaitGroupTracksInFlightTurns(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	bus := events.NewBus()
	m := sizedConversation(t, 80, 24)
	// voice=false so startConversation doesn't dispatch a turn this test
	// never executes; the dispatch under test happens explicitly below.
	m.startConversation(conversationDeps{runner: runner, bus: bus, voice: false, ctx: ctx, wg: &wg})
	m.deps.voice = true
	m.voiceEnabled = true

	cmd := m.dispatchVoiceTurn()
	done := make(chan struct{})
	go func() { cmd(); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight turn did not drain on parent cancel")
	}
	wg.Wait() // must not deadlock: Done ran when the turn drained
}

// A bare printable rune while a response is in flight must stop playback
// without canceling the turn — the composer label's "type to barge in"
// promise (WI-dc9e33 B2). Only Enter escalates to a full barge-in.
func TestKeystrokeBargesInWithoutCancelingTurn(t *testing.T) {
	for _, state := range []turnState{turnVoiceResponding, turnTextRunning} {
		runner := &fakeTurnRunner{}
		m, _ := startedConversation(t, runner, true)
		m.turnState = state
		canceled := false
		m.turnCancel = func() { canceled = true }

		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})

		if runner.stopped != 1 {
			t.Fatalf("state %d: StopPlayback calls = %d, want 1", state, runner.stopped)
		}
		if canceled {
			t.Fatalf("state %d: keystroke barge-in must not cancel the turn", state)
		}
		if m.turnState != state {
			t.Fatalf("state %d: turnState changed to %d", state, m.turnState)
		}
		if m.input.Value() != "w" {
			t.Fatalf("state %d: rune should land in composer, got %q", state, m.input.Value())
		}
	}
}

func TestKeystrokeBargeInIgnoresIdleAndNonTyping(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)

	// Idle: typing is just drafting.
	m.turnState = turnIdle
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if runner.stopped != 0 {
		t.Fatal("idle typing must not stop playback")
	}

	// Alt-chords are commands, not drafting.
	m.turnState = turnVoiceResponding
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}, Alt: true})
	if runner.stopped != 0 {
		t.Fatal("alt chord must not stop playback")
	}

	// vim-normal navigation scrolls the transcript; it is not drafting.
	m.vim.enabled = true
	m.vim.mode = vimNormal
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if runner.stopped != 0 {
		t.Fatal("vim-normal keys must not stop playback")
	}
	_ = m
}
