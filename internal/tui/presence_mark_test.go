package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// hasMark reports whether a rendered screen contains the presence mark, which
// is the only braille in this program.
func hasMark(view string) bool {
	for _, r := range view {
		if r >= 0x2800 && r <= 0x28FF {
			return true
		}
	}
	return false
}

func launcherAt(w, h int) launcherModel {
	m := newLauncher(&config.Config{
		BrainProvider: "ollama", OllamaModel: "qwen3:8b",
		TTSProvider: "kokoro", TTSVoice: "af_heart",
		AgentName: "Samantha", ActivePersona: "samantha",
	}, nil)
	m.width, m.height = w, h
	return m
}

// The front door wears the mark, but only where the menu can still spare the
// rows — and never at a size where the concentric arcs blur together.
func TestLauncherShowsThePresenceMark(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want bool
	}{
		{120, 40, true},
		{96, 34, true},
		{80, 24, true},
		{70, 22, false}, // too short: the menu needs the rows
		{60, 34, false}, // too narrow: the mark would crowd the wordmark
	} {
		got := hasMark(launcherAt(tc.w, tc.h).View())
		if got != tc.want {
			t.Errorf("%dx%d: mark present = %v, want %v", tc.w, tc.h, got, tc.want)
		}
	}
}

// Adding rows to the brand plate must not push the menu off the screen.
//
// The sizes here are the ones where the mark can appear, plus the two just
// under its thresholds. Smaller terminals are left out on purpose: the full
// launcher already overflows a 40x16 window on main, before any of this, and
// fixing that is not this change's business.
func TestLauncherFitsItsTerminal(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 40}, {96, 34}, {80, 24}, {72, 24}, {70, 22}, {60, 24}} {
		view := launcherAt(size.w, size.h).View()
		lines := strings.Split(view, "\n")
		if len(lines) > size.h {
			t.Errorf("%dx%d: launcher is %d rows, exceeds the %d available", size.w, size.h, len(lines), size.h)
		}
		for _, line := range lines {
			if got := ansi.StringWidth(line); got > size.w {
				t.Errorf("%dx%d: line %q is %d cells wide", size.w, size.h, stripANSI(line), got)
			}
		}
		if !strings.Contains(stripANSI(view), "New conversation") {
			t.Errorf("%dx%d: the menu was pushed off the screen", size.w, size.h)
		}
	}
}

// The launcher is where the app sits idle: its mark must be static, or the
// program holds a redraw loop open for as long as it is running.
func TestLauncherMarkIsStatic(t *testing.T) {
	m := launcherAt(96, 34)
	first := m.View()
	for range 5 {
		if m.View() != first {
			t.Fatal("the launcher mark changes between renders; it would need a tick")
		}
	}
}

// The recorder waits on the same kind of build as a conversation — models and
// a microphone — and used to show one static status line while it did.
func TestMeetingStartShowsTheWaitingScreen(t *testing.T) {
	app := NewApp(&config.Config{})
	app.runCtx = t.Context()
	app.progress = newEventBridge(16)
	app.width, app.height = 90, 30

	model, cmd := app.Update(startMeetingMsg{Description: "Standup", RoutePlan: routePlanLocal})
	app = model.(App)
	if app.screen != screenMeeting {
		t.Fatalf("screen = %v, want meeting", app.screen)
	}
	if !app.meeting.startup.active {
		t.Fatal("the recorder did not enter the waiting state")
	}
	if cmd == nil {
		t.Fatal("no command returned; the orb would never turn")
	}
	view := stripANSI(app.meeting.View())
	if !strings.Contains(view, "Starting Standup") {
		t.Fatalf("waiting screen does not name the meeting:\n%s", view)
	}
	if !strings.Contains(view, "0:00") {
		t.Fatalf("waiting screen has no clock:\n%s", view)
	}
	if !hasMark(app.meeting.View()) {
		t.Fatalf("waiting screen has no mark:\n%s", view)
	}
}

// Stages go to whichever screen is waiting.
func TestMeetingBuildStagesReachTheRecorder(t *testing.T) {
	app := NewApp(&config.Config{})
	app.runCtx = t.Context()
	app.progress = newEventBridge(16)
	app.width, app.height = 90, 30
	model, _ := app.Update(startMeetingMsg{Description: "Standup"})
	app = model.(App)

	model, _ = app.Update(buildStageMsg{stage: BuildStage{Label: "Checking models"}})
	app = model.(App)
	model, _ = app.Update(buildStageMsg{stage: BuildStage{Label: "Opening the microphone"}})
	app = model.(App)

	view := stripANSI(app.meeting.View())
	if !strings.Contains(view, "▸ Opening the microphone…") {
		t.Fatalf("current stage missing:\n%s", view)
	}
	if !strings.Contains(view, "✓ Checking models") {
		t.Fatalf("finished stage missing:\n%s", view)
	}
	if app.conversation.startup.active {
		t.Fatal("a meeting stage leaked into the conversation screen")
	}
}

// A build that fails returns to setup; the waiting screen must not survive it.
func TestMeetingWaitingScreenClearsOnError(t *testing.T) {
	app := NewApp(&config.Config{})
	app.runCtx = t.Context()
	app.progress = newEventBridge(16)
	model, _ := app.Update(startMeetingMsg{Description: "Standup"})
	app = model.(App)

	model, _ = app.Update(meetingReadyMsg{err: errFakeBuild})
	app = model.(App)
	if app.meeting.startup.active {
		t.Fatal("the waiting screen outlived a failed build")
	}
	if app.screen != screenMeetingSetup {
		t.Fatalf("screen = %v, want meeting setup", app.screen)
	}
}

// Recorder keys act on a capture that does not exist yet.
func TestMeetingWaitingScreenIgnoresInput(t *testing.T) {
	m := newEmbeddedMeeting()
	m.width, m.height = 90, 30
	m.beginStartup("Standup")

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'h', 'i'}},
		{Type: tea.KeyEnter},
		{Type: tea.KeyCtrlB},
	} {
		next, _ := m.Update(key)
		m = next.(meetingModel)
	}
	if !m.startup.active {
		t.Fatal("a keystroke left the waiting state")
	}
	if got := strings.TrimSpace(m.note.Value()); got != "" {
		t.Fatalf("the note field took input while starting: %q", got)
	}
}

// The orb turns while the recorder is being built, and stops when it is ready.
func TestMeetingStartupTickStopsWhenReady(t *testing.T) {
	m := newEmbeddedMeeting()
	m.width, m.height = 90, 30
	m.beginStartup("Standup")

	before := m.startup.frame
	next, cmd := m.Update(startupTickMsg(time.Now()))
	m = next.(meetingModel)
	if m.startup.frame == before {
		t.Fatal("tick did not advance the orb")
	}
	if cmd == nil {
		t.Fatal("tick did not reschedule itself")
	}

	m.finishStartup()
	next, cmd = m.Update(startupTickMsg(time.Now()))
	m = next.(meetingModel)
	if cmd != nil {
		t.Fatal("tick kept running after the recorder was ready")
	}
}

// errFakeBuild stands in for a failed meeting build.
var errFakeBuild = errors.New("no microphone")

// The waiting screen offers exactly one key, so it had better work: the
// recorder owns ctrl+c as "stop recording", but there is nothing to stop while
// it is still being built.
func TestCtrlCQuitsWhileTheRecorderIsStarting(t *testing.T) {
	app := NewApp(&config.Config{})
	app.runCtx = t.Context()
	app.progress = newEventBridge(16)
	model, _ := app.Update(startMeetingMsg{Description: "Standup"})
	app = model.(App)

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	app = model.(App)
	if !app.quitting || cmd == nil {
		t.Fatal("ctrl+c did nothing while the recorder was starting")
	}

	// Once recording, ctrl+c goes back to meaning "stop", not "quit".
	running := NewApp(&config.Config{})
	running.runCtx = t.Context()
	running.progress = newEventBridge(16)
	running.screen = screenMeeting
	running.meeting = newEmbeddedMeeting()
	model, _ = running.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if model.(App).quitting {
		t.Fatal("ctrl+c quit the app instead of stopping the recording")
	}
}

// Quitting during a meeting build is newly reachable — the waiting screen
// hands Ctrl+C back to the app — so the recorder needs the ownership slot the
// conversation has. A runtime built after the quit must be torn down even
// though its ready message never reaches Update. (PR #84 review.)
func TestMeetingBuildCancelsCleanupViaSlot(t *testing.T) {
	cleaned := false
	rt := &MeetingRuntime{Cleanup: func() { cleaned = true }}

	ctx, cancel := context.WithCancel(context.Background())
	slot := newMeetingSlot()
	build := func(context.Context, MeetingSpec, BuildProgress) (*MeetingRuntime, error) { return rt, nil }
	cancel() // quit while the builder is still opening the mic

	msg := buildMeeting(build, ctx, newEventBridge(4), slot, MeetingSpec{Description: "Standup"})()
	ready, ok := msg.(meetingReadyMsg)
	if !ok {
		t.Fatalf("buildMeeting returned %#v", msg)
	}
	if ready.err == nil {
		t.Fatal("expected a cancelled-context error when the build finishes after quit")
	}
	if !cleaned {
		t.Fatal("recorder Cleanup not called when ctx was cancelled after the build")
	}
}

// The slot is the backstop, not the primary path: a recorder that reached
// Update is still owned by the slot so run() can release it at exit, and
// releasing twice is safe by MeetingRuntime's contract.
func TestMeetingSlotReleasesAtExit(t *testing.T) {
	var calls int
	rt := &MeetingRuntime{Cleanup: func() { calls++ }}
	slot := newMeetingSlot()

	msg := buildMeeting(
		func(context.Context, MeetingSpec, BuildProgress) (*MeetingRuntime, error) { return rt, nil },
		context.Background(), newEventBridge(4), slot, MeetingSpec{Description: "Standup"},
	)()
	if ready := msg.(meetingReadyMsg); ready.err != nil || ready.rt != rt {
		t.Fatalf("build did not deliver the runtime: %+v", ready)
	}
	if calls != 0 {
		t.Fatal("a live recorder was cleaned up while the build succeeded")
	}

	slot.cleanup()
	if calls != 1 {
		t.Fatalf("Cleanup ran %d times at exit, want 1", calls)
	}
	slot.cleanup() // the slot is empty now; exit paths may run twice
	if calls != 1 {
		t.Fatalf("Cleanup ran again on an empty slot (%d)", calls)
	}
}

// Storing a second recorder retires the first: a rebuild must not strand the
// mic the previous build opened.
func TestMeetingSlotReleasesTheRuntimeItReplaces(t *testing.T) {
	first, second := 0, 0
	slot := newMeetingSlot()
	slot.store(&MeetingRuntime{Cleanup: func() { first++ }})
	slot.store(&MeetingRuntime{Cleanup: func() { second++ }})
	if first != 1 {
		t.Fatalf("replaced recorder cleaned %d times, want 1", first)
	}
	if second != 0 {
		t.Fatal("the current recorder was cleaned on store")
	}
}

// A recorder released through the slot never reached meetingRT, so nothing
// else closes its bundle. Cleanup stops the mic; only Close writes the
// trailer, and an opened-but-never-finished meeting is the one part of a
// quit-during-build that outlives the process.
func TestMeetingSlotClosesAnAbandonedBundle(t *testing.T) {
	w := recordedBundleWriter(t)
	t.Cleanup(func() { _, _ = w.Close() })

	slot := newMeetingSlot()
	slot.store(&MeetingRuntime{Writer: w})
	slot.cleanup()

	events, err := os.ReadFile(w.JSONLPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), meetinglog.TypeSessionEnd) {
		t.Fatalf("bundle left unfinished after the slot released it:\n%s", events)
	}
}
