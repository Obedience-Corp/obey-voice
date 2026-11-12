package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
)

func TestWorkerReplyDoesNotConsumeForegroundStream(t *testing.T) {
	m := sizedConversation(t, 100, 28)
	m.agentName = "Current persona"
	m.handleEvent(events.ResponseDelta{Text: "A foreground reply in progress"})
	e := events.JobFinished{ID: "job", Summary: "The worker found three files.", Envelope: &events.ResponseEnvelope{DisplayName: "Jarvis"}}
	m.handleEvent(e)
	m.handleEvent(e)
	if m.streamingAgent != "A foreground reply in progress" {
		t.Fatalf("worker consumed foreground text: %q", m.streamingAgent)
	}
	if len(m.plainTurns) != 1 || m.plainTurns[0].text != e.Summary {
		t.Fatalf("worker reply duplicated or missing: %+v", m.plainTurns)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Jarvis") || !strings.Contains(view, e.Summary) {
		t.Fatalf("worker reply misattributed:\n%s", view)
	}
	m.handleEvent(events.ResponseReady{Response: "The foreground answer."})
	if len(m.plainTurns) != 2 || m.plainTurns[1].text != "The foreground answer." {
		t.Fatal("foreground reply lost")
	}
}

func TestWorkerReplyRejectsRetiredConversationScope(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	current := work.WorkScope{SessionID: "same", RuntimeEpoch: "runtime", ConversationEpoch: "new"}
	m.deps.workScope = func() work.WorkScope { return current }
	old := current
	old.ConversationEpoch = "old"
	m.handleEvent(events.JobFinished{ID: "old", Summary: "Stale worker result", Scope: old})
	if len(m.plainTurns) != 0 {
		t.Fatal("stale result entered the new thread")
	}
}

func TestCtrlWRetainsCompletedWorkerThread(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	m.deps.workers = func() []pipeline.WorkerInfo {
		return []pipeline.WorkerInfo{{ID: "finished", State: "done", Thread: []brain.Turn{{Role: "user", Content: "Inspect files"}, {Role: "assistant", Content: "Three files found"}}}}
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	if cmd == nil {
		t.Fatal("Ctrl-W did not navigate")
	}
	if opened, ok := cmd().(openWorkerMsg); !ok || opened.jobID != "finished" {
		t.Fatalf("completed worker shortcut = %+v", opened)
	}
}

// The actual terminal renderer must show an asynchronous reply that is not
// queued for speech while the conversation remains in listening mode, without
// waiting for speech events.
func TestWorkerReplyTerminalWhileListening(t *testing.T) {
	h := &workerReplyTerminal{model: sizedConversation(t, 100, 26)}
	h.model.turnState = turnVoiceListening
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var output bytes.Buffer
	if _, err := tea.NewProgram(h, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutSignalHandler()).Run(); err != nil {
		t.Fatal(err)
	}
	plain := stripANSI(output.String())
	if !strings.Contains(plain, "Worker answer is visible while listening.") || !strings.Contains(plain, "Jarvis") {
		t.Fatalf("terminal missing worker reply:\n%s", plain)
	}
	if h.model.turnState != turnVoiceListening {
		t.Fatal("worker reply took over microphone ownership")
	}
}

type workerReplyTerminal struct{ model conversationModel }

func (h *workerReplyTerminal) Init() tea.Cmd {
	return tea.Tick(30*time.Millisecond, func(time.Time) tea.Msg {
		return events.JobFinished{ID: "worker", Summary: "Worker answer is visible while listening.", Envelope: &events.ResponseEnvelope{DisplayName: "Jarvis"}}
	})
}
func (h *workerReplyTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if e, ok := msg.(events.JobFinished); ok {
		h.model.handleEvent(e)
		return h, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return tea.QuitMsg{} })
	}
	return h, nil
}
func (h *workerReplyTerminal) View() string { return h.model.View() }
