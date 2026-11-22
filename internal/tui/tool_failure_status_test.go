package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func failedForegroundTool() events.ToolCallFinished {
	return events.ToolCallFinished{
		ID:   "call-1",
		Name: "run_command",
		Kind: "execute",
		Outcome: &events.ToolOutcome{
			Status:        events.ToolOutcomeFailed,
			ExitCode:      intPtr(1),
			Reason:        "exit status 1",
			OutputPreview: "ls: /no/such/dir: No such file or directory",
		},
	}
}

func assertNotErrorState(t *testing.T, m conversationModel, when string) {
	t.Helper()
	if m.voiceMode == anim.ModeError {
		t.Fatalf("%s: voiceMode = ModeError, want the turn to carry on", when)
	}
	if m.statusErr {
		t.Fatalf("%s: status %q is an error status", when, m.status)
	}
}

func TestFailedToolKeepsTurnThinking(t *testing.T) {
	m := sizedConversation(t, 120, 30)
	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ToolCallStarted{ID: "call-1", Name: "run_command", Kind: "execute", Summary: "ls /no/such/dir"})
	m.handleEvent(failedForegroundTool())

	assertNotErrorState(t, m, "after failed tool")
	if m.voiceMode != anim.ModeThinking {
		t.Fatalf("voiceMode = %v, want ModeThinking while the model works on", m.voiceMode)
	}
	if want := m.agentName + " thinking"; m.status != want {
		t.Fatalf("status = %q, want %q", m.status, want)
	}

	m.handleEvent(events.ToolCallStarted{ID: "call-2", Name: "run_command", Kind: "execute", Summary: "ls ."})
	assertNotErrorState(t, m, "retry started")
	m.handleEvent(events.ToolCallFinished{ID: "call-2", Name: "run_command", Kind: "execute", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeSucceeded, ExitCode: intPtr(0)}})
	assertNotErrorState(t, m, "retry finished")
}

func TestFailedToolThenSpokenReplyEndsIdle(t *testing.T) {
	m := sizedConversation(t, 120, 30)
	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(failedForegroundTool())
	m.handleEvent(events.ResponseReady{Response: "That folder does not exist."})
	m.handleEvent(events.GeneratingVoice{Sentence: "That folder does not exist."})
	assertNotErrorState(t, m, "synthesizing")
	m.handleEvent(events.SpeakingStarted{Text: "That folder does not exist."})
	assertNotErrorState(t, m, "speaking")
	m.handleEvent(events.SpeakingComplete{})

	if m.voiceMode != anim.ModeIdle {
		t.Fatalf("voiceMode after playback = %v, want ModeIdle", m.voiceMode)
	}
	if m.statusErr || m.status != "" {
		t.Fatalf("status after playback = %q (err=%v), want cleared", m.status, m.statusErr)
	}
}

func TestFailedToolThenTextReplyEndsIdle(t *testing.T) {
	m := sizedConversation(t, 120, 30)
	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(failedForegroundTool())
	m.handleEvent(events.ResponseReady{Response: "That folder does not exist."})

	if m.voiceMode != anim.ModeIdle {
		t.Fatalf("voiceMode after text reply = %v, want ModeIdle", m.voiceMode)
	}
	if m.statusErr || m.status != "" {
		t.Fatalf("status after text reply = %q (err=%v), want cleared", m.status, m.statusErr)
	}
}

func TestTimedOutToolKeepsTurnThinking(t *testing.T) {
	m := sizedConversation(t, 120, 30)
	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ToolCallFinished{ID: "call-1", Name: "run_command", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeTimedOut, Reason: "command timed out"}})

	assertNotErrorState(t, m, "after timed-out tool")
	if m.voiceMode != anim.ModeThinking {
		t.Fatalf("voiceMode = %v, want ModeThinking", m.voiceMode)
	}
}

// A canceled tool means the turn itself was canceled; an interrupted turn
// emits no event that would clear a thinking state.
func TestCanceledToolDoesNotLeaveTurnThinking(t *testing.T) {
	m := sizedConversation(t, 120, 30)
	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ToolCallFinished{ID: "call-1", Name: "run_command", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeCanceled, Reason: "command canceled"}})
	m.handleEvent(events.TurnMetrics{Outcome: "interrupted"})

	assertNotErrorState(t, m, "after canceled tool")
	if m.voiceMode != anim.ModeIdle {
		t.Fatalf("voiceMode = %v, want ModeIdle", m.voiceMode)
	}
	if strings.Contains(m.status, "thinking") {
		t.Fatalf("status = %q, want no thinking status after a canceled turn", m.status)
	}
}

func TestFailedToolStaysInTranscriptAndActivity(t *testing.T) {
	m := sizedConversation(t, 160, 30)
	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(failedForegroundTool())

	var line string
	for _, item := range m.items {
		if item.kind == chatItemSys && strings.Contains(stripANSI(item.sys), "✗ tool run_command failed") {
			line = item.sys
		}
	}
	if line == "" {
		t.Fatal("transcript missing the failed tool line")
	}
	plain := stripANSI(line)
	if strings.Contains(plain, "\n") {
		t.Fatalf("failed tool line spans several lines: %q", plain)
	}
	for _, want := range []string{"/no/such/dir", "exit status 1", "exit code 1"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("failed tool line %q missing %q", plain, want)
		}
	}
	if !strings.Contains(line, errorStyle.Render("✗")) {
		t.Fatalf("failed tool marker is not drawn in the error colour: %q", line)
	}

	found := false
	for _, a := range m.activity {
		if a.stage == "tool" && strings.Contains(a.detail, "run_command failed") && strings.Contains(a.detail, "exit status 1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("activity missing the failed tool entry: %+v", m.activity)
	}
}

func TestRealErrorStillSticksAfterToolActivity(t *testing.T) {
	m := sizedConversation(t, 120, 30)
	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(failedForegroundTool())
	m.handleEvent(events.Error{Stage: "brain", Message: "stream broke"})
	m.handleEvent(events.ResponseReady{Response: "Partial.", Degraded: true})
	m.handleEvent(events.SpeakingStarted{Text: "Partial."})
	m.handleEvent(events.SpeakingComplete{})

	if m.voiceMode != anim.ModeError {
		t.Fatalf("voiceMode = %v, want ModeError after a real error", m.voiceMode)
	}
	if !m.statusErr || !strings.Contains(m.status, "stream broke") {
		t.Fatalf("status = %q (err=%v), want the error to stick", m.status, m.statusErr)
	}
}
