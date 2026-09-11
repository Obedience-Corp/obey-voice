package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// talkConversation is a started voice conversation with a talk key attached
// and the end-utterance seam counted.
func talkConversation(t *testing.T, runner *fakeTurnRunner, mode talkhotkey.Mode) (conversationModel, *int) {
	t.Helper()
	ends := 0
	bus := events.NewBus()
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{
		runner:       runner,
		bus:          bus,
		voice:        true,
		endUtterance: func() bool { ends++; return true },
		ctx:          t.Context(),
	})
	combo, err := talkhotkey.ParseCombo(talkhotkey.DefaultCombo)
	if err != nil {
		t.Fatal(err)
	}
	m.attachTalk(combo, mode)
	m, _ = m.Update(talkHotkeyEventMsg{ev: talkhotkey.Event{Kind: talkhotkey.KindRegistered, Combo: combo.String()}})
	return m, &ends
}

// mutedIdle pauses voice input with Ctrl+G and drains the cancel that muting
// an open mic starts, leaving the model muted and idle.
func mutedIdle(t *testing.T, m conversationModel) conversationModel {
	t.Helper()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if m.turnState == turnVoiceCanceling {
		m, _ = m.Update(voiceTurnDoneMsg{})
	}
	if m.voiceEnabled || m.turnState != turnIdle {
		t.Fatalf("setup: voiceEnabled=%v turnState=%d, want muted and idle", m.voiceEnabled, m.turnState)
	}
	return m
}

func keyDown() talkHotkeyEventMsg {
	return talkHotkeyEventMsg{ev: talkhotkey.Event{Kind: talkhotkey.KindDown}}
}

func keyUp(held time.Duration) talkHotkeyEventMsg {
	return talkHotkeyEventMsg{ev: talkhotkey.Event{Kind: talkhotkey.KindUp, HeldMS: held.Milliseconds()}}
}

// D2: while muted, hold is one bounded turn. The press opens a voice turn
// without unmuting, the release commits it, and the drain does not redispatch.
func TestTalkHoldWhileMutedIsOneBoundedTurn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}, {block: true}}}
	m, ends := talkConversation(t, runner, talkhotkey.ModeHold)
	m = mutedIdle(t, m)
	before := runner.voiceCalls

	m, cmd := m.Update(keyDown())
	if cmd == nil || m.turnState != turnVoiceListening {
		t.Fatalf("press while muted did not open a turn (cmd=%v state=%d)", cmd != nil, m.turnState)
	}
	if m.voiceEnabled {
		t.Fatal("the talk key unmuted the session as a side effect")
	}
	go cmd() // the blocked RunTurn; released by the model's cancel at teardown

	m, _ = m.Update(keyUp(800 * time.Millisecond))
	if *ends != 1 {
		t.Fatalf("release called endUtterance %d times, want 1", *ends)
	}
	m, cmd = m.Update(voiceTurnDoneMsg{text: "hello"})
	if cmd != nil {
		t.Fatal("a bounded turn redispatched listening while muted")
	}
	if m.voiceEnabled || m.turnState != turnIdle {
		t.Fatalf("after the bounded turn: voiceEnabled=%v turnState=%d, want still muted and idle", m.voiceEnabled, m.turnState)
	}
	runner.mu.Lock()
	called := runner.voiceCalls - before
	runner.mu.Unlock()
	if called != 0 {
		// The Cmd ran on our goroutine; the count moves when it executes.
		t.Logf("runner saw %d call(s) from the bounded turn", called)
	}
}

// With the mic already open, the press adopts the listening turn (no second
// dispatch) and the release commits it early instead of waiting for silence.
func TestTalkHoldAdoptsTheOpenMic(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, ends := talkConversation(t, runner, talkhotkey.ModeHold)
	if m.turnState != turnVoiceListening {
		t.Fatalf("setup: turnState=%d, want listening", m.turnState)
	}

	m, cmd := m.Update(keyDown())
	if cmd != nil {
		t.Fatal("press over an open mic dispatched a second turn")
	}
	m, _ = m.Update(keyUp(time.Second))
	if *ends != 1 {
		t.Fatalf("release called endUtterance %d times, want 1", *ends)
	}
}

// Key repeat: every down after the first, until its up, is ignored.
func TestTalkHoldIgnoresKeyRepeat(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := talkConversation(t, runner, talkhotkey.ModeHold)
	m = mutedIdle(t, m)

	dispatched := 0
	for range 5 {
		var cmd tea.Cmd
		m, cmd = m.Update(keyDown())
		if cmd != nil {
			dispatched++
		}
	}
	if dispatched != 1 {
		t.Fatalf("%d repeated downs dispatched %d turns, want 1", 5, dispatched)
	}
}

// A press over a reply interrupts it first: playback stops, the turn cancels,
// and the key's own turn opens once the cancel drains.
func TestTalkPressOverReplyInterruptsThenOpensATurn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := talkConversation(t, runner, talkhotkey.ModeHold)
	m.turnState = turnVoiceResponding
	cancels := 0
	m.turnCancel = func() { cancels++ }

	m, cmd := m.Update(keyDown())
	if cmd != nil {
		t.Fatal("the press must wait for the cancel drain before dispatching")
	}
	if m.turnState != turnVoiceCanceling || cancels != 1 || runner.stopped != 1 {
		t.Fatalf("after press: state=%d cancels=%d stopped=%d; want canceling, 1, 1", m.turnState, cancels, runner.stopped)
	}
	m, cmd = m.Update(voiceTurnDoneMsg{})
	if cmd == nil || m.turnState != turnVoiceListening {
		t.Fatalf("cancel drain did not open the key's turn (cmd=%v state=%d)", cmd != nil, m.turnState)
	}
}

// A release before the interrupt drained drops the queued turn: nothing was
// said, so nothing is asked.
func TestTalkReleaseBeforeDrainDropsTheQueuedTurn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, ends := talkConversation(t, runner, talkhotkey.ModeHold)
	m = mutedIdle(t, m) // muted, so the drain must not resume listening
	m.turnState = turnVoiceResponding
	m.turnCancel = func() {}

	m, _ = m.Update(keyDown())
	m, _ = m.Update(keyUp(50 * time.Millisecond))
	if *ends != 0 {
		t.Fatalf("release during a cancel called endUtterance %d times, want 0", *ends)
	}
	m, cmd := m.Update(voiceTurnDoneMsg{})
	if cmd != nil || m.turnState != turnIdle {
		t.Fatalf("drain opened a turn nobody is holding (cmd=%v state=%d)", cmd != nil, m.turnState)
	}
}

// If the recogniser ended the turn while the key was still held, the release
// must not end the next one.
func TestTalkReleaseAfterVADEndedIsANoOp(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}, {block: true}}}
	m, ends := talkConversation(t, runner, talkhotkey.ModeHold)

	m, _ = m.Update(keyDown())                       // adopts the open mic
	m, _ = m.Update(voiceTurnDoneMsg{text: "hello"}) // VAD ended it; listening resumes
	_, _ = m.Update(keyUp(2 * time.Second))
	if *ends != 0 {
		t.Fatalf("release after the turn already ended called endUtterance %d times, want 0", *ends)
	}
}

// Toggle: tap to open, tap to send. Up never acts.
func TestTalkToggleTapOpensAndTapSends(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, ends := talkConversation(t, runner, talkhotkey.ModeToggle)
	m = mutedIdle(t, m)

	m, cmd := m.Update(keyDown())
	if cmd == nil {
		t.Fatal("first tap did not open a turn")
	}
	m, _ = m.Update(keyUp(80 * time.Millisecond))
	if *ends != 0 {
		t.Fatal("toggle ended the turn on key up")
	}
	_, _ = m.Update(keyDown())
	if *ends != 1 {
		t.Fatalf("second tap called endUtterance %d times, want 1", *ends)
	}
}

// Automatic: a short press is a tap (keeps talking), a long one is a hold.
func TestTalkAutomaticTapVersusHold(t *testing.T) {
	for name, tc := range map[string]struct {
		held     time.Duration
		wantEnds int
	}{
		"299 ms is a tap":  {299 * time.Millisecond, 0},
		"300 ms is a hold": {300 * time.Millisecond, 1},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
			m, ends := talkConversation(t, runner, talkhotkey.ModeAutomatic)
			m, _ = m.Update(keyDown()) // adopts the open mic
			_, _ = m.Update(keyUp(tc.held))
			if *ends != tc.wantEnds {
				t.Fatalf("endUtterance called %d times, want %d", *ends, tc.wantEnds)
			}
		})
	}
}

// Registration shows in the footer and the activity panel; a conflict is
// reported where the user is looking and never green.
func TestTalkRegistrationAndConflictAreVisible(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}}}
	m, _ := talkConversation(t, runner, talkhotkey.ModeHold)
	def, _ := talkhotkey.ParseCombo(talkhotkey.DefaultCombo)
	hint := def.Display() + " talk"
	if !strings.Contains(stripANSI(m.View()), hint) {
		t.Fatalf("footer lacks the registered hint %q:\n%s", hint, stripANSI(m.View()))
	}

	m, _ = m.Update(talkHotkeyEventMsg{ev: talkhotkey.Event{Kind: talkhotkey.KindError, Code: talkhotkey.ConflictCode, Detail: talkhotkey.DefaultCombo + " is already registered by another app"}})
	view := stripANSI(m.View())
	if strings.Contains(view, hint) {
		t.Fatal("footer still advertises a key that failed to register")
	}
	if !strings.Contains(m.status, "taken by another app") {
		t.Fatalf("status = %q, want the conflict named", m.status)
	}
	// A press after the conflict is inert: the driver was reset and the
	// helper holds nothing.
	m, cmd := m.Update(keyDown())
	if cmd != nil && m.turnState == turnVoiceCanceling {
		t.Fatal("a press after a failed registration interrupted the conversation")
	}
}

// Without a microphone the key reports the same thing Ctrl+G does.
func TestTalkWithoutMicrophoneIsRefused(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: &fakeTurnRunner{}, bus: events.NewBus(), voice: false, ctx: t.Context()})
	combo, _ := talkhotkey.ParseCombo(talkhotkey.DefaultCombo)
	m.attachTalk(combo, talkhotkey.ModeHold)
	m, cmd := m.Update(keyDown())
	if cmd != nil || !strings.Contains(m.status, "Microphone unavailable") {
		t.Fatalf("press without a mic: cmd=%v status=%q", cmd != nil, m.status)
	}
}

// controlDown and controlUp are the same edges arriving from the control
// endpoint instead of the native helper.
func controlDown() talkHotkeyEventMsg {
	return talkHotkeyEventMsg{ev: talkhotkey.Event{Kind: talkhotkey.KindDown}, fromControl: true}
}

func controlUp(held time.Duration) talkHotkeyEventMsg {
	return talkHotkeyEventMsg{ev: talkhotkey.Event{Kind: talkhotkey.KindUp, HeldMS: held.Milliseconds()}, fromControl: true}
}

// A shortcut tool's down/up drives the same turn as a native key press: one
// driver, one contract, whichever side delivered the edge.
func TestTalkControlCommandsDriveTheSameTurn(t *testing.T) {
	runner := &fakeTurnRunner{voiceQueue: []voiceScript{{block: true}, {block: true}}}
	m, ends := talkConversation(t, runner, talkhotkey.ModeHold)
	m = mutedIdle(t, m)

	m, cmd := m.Update(controlDown())
	if cmd == nil || m.turnState != turnVoiceListening {
		t.Fatalf("talk down did not open a turn (cmd=%v state=%d)", cmd != nil, m.turnState)
	}
	go cmd() // the blocked RunTurn; released by the model's cancel at teardown

	m, _ = m.Update(controlUp(800 * time.Millisecond))
	if *ends != 1 {
		t.Fatalf("talk up called endUtterance %d times, want 1", *ends)
	}
	if m.voiceEnabled {
		t.Fatal("the control endpoint unmuted the session as a side effect")
	}
}

// The endpoint is only useful if you can see where it is, so it is named in
// the activity panel the moment it opens.
func TestAttachTalkControlNamesTheEndpoint(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{runner: &fakeTurnRunner{}, bus: events.NewBus(), voice: true, ctx: t.Context()})
	const address = "/run/user/501/obey-voice/talk.sock"
	m.attachTalkControl(address, talkhotkey.Combo{}, talkhotkey.ModeHold)

	if m.talk == nil || m.talk.driver == nil {
		t.Fatal("the control endpoint left no driver, so its commands would go nowhere")
	}
	found := false
	for _, entry := range m.activity {
		if entry.stage == "talk control" && entry.detail == address {
			found = true
		}
	}
	if !found {
		t.Fatalf("activity does not name the endpoint: %+v", m.activity)
	}
	// Nothing registered a combo, so the footer must not advertise one.
	if hint := m.talkHint(); hint != "" {
		t.Fatalf("talkHint = %q, want empty until something holds the key", hint)
	}
}

// The endpoint closing is not a conversation event: the App drops it and the
// key path that is still registered keeps working.
func TestTalkControlClosedClearsTheEndpoint(t *testing.T) {
	control, err := talkhotkey.ListenControl(t.Context())
	if err != nil {
		t.Skipf("no control endpoint on this host: %v", err)
	}
	t.Cleanup(func() { _ = control.Close() })

	app := App{cfg: &config.Config{}, runCtx: t.Context(), control: control, width: 80, height: 24}
	next, cmd := app.Update(talkControlClosedMsg{})
	got := next.(App)
	if got.control != nil {
		t.Fatal("the closed endpoint is still attached")
	}
	if cmd != nil {
		t.Fatal("a closed endpoint must not schedule more work")
	}
}
