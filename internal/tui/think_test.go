package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestCtrlKShowsFinalizedThinkingInThread(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	m.ready = true

	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ThinkingDelta{Text: "Rayleigh scattering in air."})
	m.handleEvent(events.ResponseDelta{Text: "Because air scatters blue light."})
	m.handleEvent(events.ResponseReady{Response: "Because air scatters blue light."})

	hidden := stripANSI(m.View())
	if strings.Contains(hidden, "Rayleigh scattering") {
		t.Fatalf("thinking leaked into transcript while hidden:\n%s", hidden)
	}
	if !strings.Contains(hidden, "Because air scatters blue light.") {
		t.Fatalf("spoken reply missing while thinking hidden:\n%s", hidden)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	if !m.showThink {
		t.Fatal("ctrl+k did not enable thinking blocks")
	}
	shown := stripANSI(m.View())
	if !strings.Contains(shown, "thinking") {
		t.Fatalf("thinking label missing after ctrl+k:\n%s", shown)
	}
	if !strings.Contains(shown, "Rayleigh scattering in air.") {
		t.Fatalf("think body missing after ctrl+k:\n%s", shown)
	}
	if !strings.Contains(shown, "Because air scatters blue light.") {
		t.Fatalf("spoken reply missing after ctrl+k:\n%s", shown)
	}
	if !strings.Contains(shown, "think") {
		t.Fatalf("footer chip missing after ctrl+k:\n%s", shown)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.showThink {
		t.Fatal("ctrl+k did not hide thinking blocks")
	}
	hiddenAgain := stripANSI(m.View())
	if strings.Contains(hiddenAgain, "Rayleigh scattering") {
		t.Fatalf("thinking still in thread after hide:\n%s", hiddenAgain)
	}
}

func TestThinkCommandOnOff(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)

	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ThinkingDelta{Text: "plan the greeting"})
	m.handleEvent(events.ResponseReady{Response: "Hello there."})

	m, _ = typeAndEnter(m, "/think on")
	if !m.showThink {
		t.Fatal("/think on did not enable thinking blocks")
	}
	if !strings.Contains(stripANSI(m.View()), "plan the greeting") {
		t.Fatalf("/think on did not reveal think body:\n%s", stripANSI(m.View()))
	}
	if len(runner.texts()) != 0 {
		t.Fatalf("/think reached the brain: %v", runner.texts())
	}

	m, _ = typeAndEnter(m, "/think off")
	if m.showThink {
		t.Fatal("/think off did not hide thinking blocks")
	}
	if strings.Contains(stripANSI(m.View()), "plan the greeting") {
		t.Fatalf("/think off left think body in the thread:\n%s", stripANSI(m.View()))
	}
}

func TestCopyIgnoresThinkingBlocks(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	clip := &testClipboard{}
	m.deps.clipboard = clip

	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ThinkingDelta{Text: "private scratchpad"})
	m.handleEvent(events.ResponseReady{Response: "Public answer."})
	m.toggleShowThink()

	if strings.Contains(m.lastAssistantText, "private scratchpad") {
		t.Fatalf("lastAssistantText included thinking: %q", m.lastAssistantText)
	}
	m.copyLastAssistant()
	if clip.value != "Public answer." {
		t.Fatalf("copy included thinking: %q", clip.value)
	}
	if !strings.Contains(stripANSI(m.View()), "private scratchpad") {
		t.Fatalf("toggle should show thinking in the thread:\n%s", stripANSI(m.View()))
	}
}

func TestThinkingOnlyTurnAppearsWhenToggled(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.ready = true

	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ThinkingDelta{Text: "plan the greeting"})
	m.handleEvent(events.ResponseReady{Response: ""})

	hidden := stripANSI(m.View())
	if strings.Contains(hidden, "no reply") {
		t.Fatalf("thinking-only empty painted no-reply placeholder:\n%s", hidden)
	}
	if strings.Contains(hidden, "plan the greeting") {
		t.Fatalf("thinking-only turn visible while hidden:\n%s", hidden)
	}

	m.toggleShowThink()
	shown := stripANSI(m.View())
	if !strings.Contains(shown, "plan the greeting") {
		t.Fatalf("thinking-only turn missing after toggle:\n%s", shown)
	}
	if strings.Contains(shown, "no reply") {
		t.Fatalf("toggle revealed no-reply placeholder:\n%s", shown)
	}
}

func TestSeededThinkTagsHonorToggle(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.seedTranscript([]brain.Turn{{
		Role:    "assistant",
		Content: "<think>hidden plan</think>\nHello there.",
	}})

	hidden := stripANSI(m.View())
	if strings.Contains(hidden, "hidden plan") {
		t.Fatalf("seeded think visible while hidden:\n%s", hidden)
	}
	if !strings.Contains(hidden, "Hello there.") {
		t.Fatalf("seeded spoken reply missing:\n%s", hidden)
	}

	m.toggleShowThink()
	shown := stripANSI(m.View())
	if !strings.Contains(shown, "hidden plan") {
		t.Fatalf("seeded think missing after toggle:\n%s", shown)
	}
	if strings.Contains(shown, "<think>") {
		t.Fatalf("raw think tags leaked after toggle:\n%s", shown)
	}
}

func TestLiveThinkingStillStreamsWhenToggleOff(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.ready = true

	m.handleEvent(events.ThinkingStarted{})
	m.handleEvent(events.ThinkingDelta{Text: "live scratchpad"})

	view := stripANSI(m.View())
	if !strings.Contains(view, "live scratchpad") {
		t.Fatalf("live thinking missing while toggle off:\n%s", view)
	}
}

func TestThinkToggleLeavesActivity(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	m.activityFocused = true
	m.toggleShowThink()
	if m.activityFocused {
		t.Fatal("enabling thinking should return to chat")
	}
}
