package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type voiceAnimationTerminalSnapshot struct {
	screen   screen
	frame    int
	input    string
	activity bool
	view     string
}

type voiceAnimationTerminalHarness struct {
	app       App
	snapshots chan voiceAnimationTerminalSnapshot
}

func (h *voiceAnimationTerminalHarness) Init() tea.Cmd {
	return voiceTickCmd()
}

func (h *voiceAnimationTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := h.app.Update(msg)
	h.app = model.(App)
	switch msg.(type) {
	case voiceTickMsg, tea.KeyMsg, settingsDoneMsg, busEventMsg:
		select {
		case h.snapshots <- voiceAnimationTerminalSnapshot{
			screen:   h.app.screen,
			frame:    h.app.conversation.voiceFrame,
			input:    h.app.conversation.input.Value(),
			activity: h.app.conversation.activityFocused,
			view:     stripANSI(h.app.View()),
		}:
		default:
		}
	}
	return h, cmd
}

func TestBackgroundWorkerAnimationRendersInTerminalBesideListening(t *testing.T) {
	app := newApp(&config.Config{}, nil, nil)
	app.screen = screenConversation
	app.width, app.height = 120, 24
	app.conversation = sizedConversation(t, 120, 24)
	harness := &voiceAnimationTerminalHarness{app: app, snapshots: make(chan voiceAnimationTerminalSnapshot, 64)}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	var terminal strings.Builder
	program := tea.NewProgram(harness, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := program.Run(); done <- err }()
	stopped := false
	defer func() {
		if stopped {
			return
		}
		program.Quit()
		if err := <-done; err != nil {
			t.Errorf("terminal shutdown: %v", err)
		}
	}()

	program.Send(busEventMsg{event: events.JobStarted{ID: "job-1", Task: "inspect files"}})
	first := awaitVoiceAnimationSnapshot(t, harness.snapshots, "worker start", func(s voiceAnimationTerminalSnapshot) bool {
		return strings.Contains(s.view, "◐ Working · ^W")
	})
	awaitVoiceAnimationSnapshot(t, harness.snapshots, "worker animation tick", func(s voiceAnimationTerminalSnapshot) bool {
		return s.frame > first.frame && strings.Contains(s.view, "◓ Working · ^W")
	})
	program.Send(busEventMsg{event: events.STTPhase{Phase: "listening"}})
	awaitVoiceAnimationSnapshot(t, harness.snapshots, "work beside listening", func(s voiceAnimationTerminalSnapshot) bool {
		header := strings.SplitN(s.view, "\n", 2)[0]
		return strings.Contains(header, "Working · ^W") && strings.Contains(header, "Listening")
	})
	for _, step := range []struct {
		event events.Event
		label string
	}{
		{events.GeneratingVoice{Sentence: "Checking the files."}, "Synthesizing voice"},
		{events.SpeakingStarted{}, "Speaking"},
		{events.STTPhase{Phase: "listening"}, "Listening"},
	} {
		program.Send(busEventMsg{event: step.event})
		snapshot := awaitVoiceAnimationSnapshot(t, harness.snapshots, step.label, func(s voiceAnimationTerminalSnapshot) bool {
			return strings.Contains(strings.SplitN(s.view, "\n", 2)[0], step.label)
		})
		if rows := len(strings.Split(snapshot.view, "\n")); rows != app.height {
			t.Fatalf("%s frame has %d rows in %d-row terminal", step.label, rows, app.height)
		}
	}
	program.Send(busEventMsg{event: events.JobFinished{ID: "job-1", Task: "inspect files"}})
	awaitVoiceAnimationSnapshot(t, harness.snapshots, "worker completion", func(s voiceAnimationTerminalSnapshot) bool {
		return !strings.Contains(s.view, "Working · ^W") && strings.Contains(s.view, "Listening")
	})
	program.Quit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stopped = true
	output := stripANSI(terminal.String())
	if !strings.Contains(output, "Working · ^W") || !strings.Contains(output, "inspect files · done") {
		t.Fatal("Bubble Tea terminal output omitted work or completion")
	}
}

func (h *voiceAnimationTerminalHarness) View() string { return h.app.View() }

func awaitVoiceAnimationSnapshot(t *testing.T, snapshots <-chan voiceAnimationTerminalSnapshot, label string, want func(voiceAnimationTerminalSnapshot) bool) voiceAnimationTerminalSnapshot {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case snapshot := <-snapshots:
			if want(snapshot) {
				return snapshot
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", label)
			return voiceAnimationTerminalSnapshot{}
		}
	}
}

// TestVoiceAnimationSurvivesHiddenScreenTransition runs the top-level App in
// Bubble Tea. A thinking animation remains active while Settings is visible;
// the hidden conversation must consume its tick and keep the chain alive for
// the return to Chat. The final keys prove the real composer and tab
// completion path still receive input after the transition.
func TestVoiceAnimationSurvivesHiddenScreenTransition(t *testing.T) {
	app := newApp(&config.Config{}, nil, nil)
	app.screen = screenConversation
	app.width, app.height = 100, 30
	app.conversation = newConversation("Samantha")
	app.conversation.cfg = app.cfg
	app.conversation.setSize(app.width, app.height)
	app.conversation.ready = true
	app.conversation.deps.voice = true
	app.conversation.voiceEnabled = true
	app.conversation.voiceMode = anim.ModeThinking
	app.conversation.voiceTicking = true

	harness := &voiceAnimationTerminalHarness{
		app:       app,
		snapshots: make(chan voiceAnimationTerminalSnapshot, 64),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var terminal strings.Builder
	program := tea.NewProgram(harness,
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(&terminal),
		tea.WithoutSignalHandler(),
	)
	runErr := make(chan error, 1)
	go func() {
		_, err := program.Run()
		runErr <- err
	}()
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		cancel()
		program.Quit()
		select {
		case err := <-runErr:
			if err != nil && !strings.Contains(err.Error(), "program was killed: context canceled") {
				t.Errorf("terminal shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("timed out waiting for terminal shutdown")
		}
	})

	program.Send(switchScreenMsg(screenSettings))
	awaitVoiceAnimationSnapshot(t, harness.snapshots, "Settings screen", func(snapshot voiceAnimationTerminalSnapshot) bool {
		return snapshot.screen == screenSettings
	})
	hidden := awaitVoiceAnimationSnapshot(t, harness.snapshots, "hidden conversation animation frame", func(snapshot voiceAnimationTerminalSnapshot) bool {
		return snapshot.screen == screenSettings && snapshot.frame > 0
	})

	program.Send(settingsDoneMsg{})
	awaitVoiceAnimationSnapshot(t, harness.snapshots, "return to Chat", func(snapshot voiceAnimationTerminalSnapshot) bool {
		return snapshot.screen == screenConversation
	})
	awaitVoiceAnimationSnapshot(t, harness.snapshots, "post-return animation frame", func(snapshot voiceAnimationTerminalSnapshot) bool {
		return snapshot.screen == screenConversation && snapshot.frame > hidden.frame
	})

	for _, r := range "/he" {
		program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	awaitVoiceAnimationSnapshot(t, harness.snapshots, "tab completion input", func(snapshot voiceAnimationTerminalSnapshot) bool {
		return snapshot.screen == screenConversation && snapshot.input == "/he"
	})
	program.Send(tea.KeyMsg{Type: tea.KeyTab})
	awaitVoiceAnimationSnapshot(t, harness.snapshots, "completed help command", func(snapshot voiceAnimationTerminalSnapshot) bool {
		return snapshot.input == "/help "
	})
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlT})
	awaitVoiceAnimationSnapshot(t, harness.snapshots, "Activity tab", func(snapshot voiceAnimationTerminalSnapshot) bool {
		return snapshot.activity && snapshot.input == "/help "
	})
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlT})
	chat := awaitVoiceAnimationSnapshot(t, harness.snapshots, "Chat tab with preserved draft", func(snapshot voiceAnimationTerminalSnapshot) bool {
		return !snapshot.activity && snapshot.input == "/help "
	})
	animated := awaitVoiceAnimationSnapshot(t, harness.snapshots, "visible animated thinking frame", func(snapshot voiceAnimationTerminalSnapshot) bool {
		return snapshot.frame > chat.frame && snapshot.view != chat.view
	})
	if !strings.Contains(animated.view, "thinking") {
		t.Fatal("thinking indicator missing from rendered Chat")
	}
	program.Quit()
	err := <-runErr
	stopped = true
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stripANSI(terminal.String()), "thinking") || !strings.Contains(stripANSI(terminal.String()), "/help") {
		t.Fatal("terminal output omitted animation or completed draft")
	}
}

// The active meeting has a distinct timer; a delayed conversation tick must
// neither stop the hidden conversation nor advance the meeting's meter.
func TestAppRoutesAnimationTicksToTheirOwners(t *testing.T) {
	app := newApp(&config.Config{}, nil, nil)
	app.screen = screenMeeting
	app.conversation.voiceFrame = 7
	app.conversation.voiceMode = anim.ModeThinking
	app.conversation.voiceTicking = true
	app.meeting.voiceFrame = 3
	app.meeting.voiceMode = anim.ModeListening
	app.meeting.voiceTicking = true
	model, cmd := app.Update(voiceTickMsg(time.Now()))
	app = model.(App)
	if app.conversation.voiceFrame != 8 || app.meeting.voiceFrame != 3 || cmd == nil {
		t.Fatalf("conversation tick affected wrong owner: conversation=%d meeting=%d cmd=%v", app.conversation.voiceFrame, app.meeting.voiceFrame, cmd != nil)
	}
	if _, ok := cmd().(voiceTickMsg); !ok {
		t.Fatal("conversation timer did not renew")
	}
	model, cmd = app.Update(meetingTickMsg(time.Now()))
	app = model.(App)
	if app.conversation.voiceFrame != 8 || app.meeting.voiceFrame != 4 || cmd == nil {
		t.Fatalf("meeting tick affected wrong owner: conversation=%d meeting=%d cmd=%v", app.conversation.voiceFrame, app.meeting.voiceFrame, cmd != nil)
	}
	if _, ok := cmd().(meetingTickMsg); !ok {
		t.Fatal("meeting timer did not renew")
	}
}

func TestAppHiddenAnimationStopsWhenInactive(t *testing.T) {
	for _, target := range []screen{screenSettings, screenPersonas, screenWorkers, screenMeeting} {
		for _, reduced := range []bool{false, true} {
			app := newApp(&config.Config{}, nil, nil)
			app.screen = target
			app.conversation.voiceTicking = true
			app.conversation.reducedMotion = reduced
			if reduced {
				app.conversation.voiceMode = anim.ModeThinking
			}
			model, cmd := app.Update(voiceTickMsg(time.Now()))
			app = model.(App)
			if app.conversation.voiceTicking || cmd != nil {
				t.Fatalf("screen=%v reduced=%v left inactive animation running", target, reduced)
			}
		}
	}
}
