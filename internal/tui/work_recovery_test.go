package tui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
)

type recoveryTerminal struct {
	model conversationModel
	mu    sync.Mutex
	out   bytes.Buffer
	views chan string
}

func (h *recoveryTerminal) Init() tea.Cmd { return nil }
func (h *recoveryTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	if h.views != nil {
		select {
		case h.views <- stripANSI(h.model.View()):
		default:
		}
	}
	return h, cmd
}
func (h *recoveryTerminal) View() string { return h.model.View() }
func (h *recoveryTerminal) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.out.Write(p)
}

func TestRecoveryPollRendersDroppedFinalSnapshotAndStickyGap(t *testing.T) {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	snap := work.Snapshot{Scope: scope, Revision: 2, Pending: []work.ResultView{{Outcome: work.Outcome{ResultID: "result", JobID: "job", Scope: scope, Status: work.OutcomeSucceeded, Text: "Recovered answer"}, State: work.Ready}}}
	health := events.WorkJournalHealth{Scope: scope, Incomplete: true}
	m := sizedConversation(t, 100, 24)
	m.deps.workScope = func() work.WorkScope { return scope }
	m.deps.workSnapshot = func() work.Snapshot { return snap }
	m.deps.journalHealth = func() events.WorkJournalHealth { return health }
	h := &recoveryTerminal{model: m, views: make(chan string, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	firstClosed := false
	defer func() {
		if !firstClosed {
			p.Quit()
			<-done
		}
	}()
	p.Send(voiceTickMsg(time.Now().Add(time.Second)))
	view := ""
	deadline := time.After(time.Second)
	for !strings.Contains(view, "Recovered answer") {
		select {
		case view = <-h.views:
		case <-deadline:
			t.Fatal("poll did not render recovered result")
		}
	}
	if strings.Count(view, "Recovered answer") != 1 {
		t.Fatalf("recovered result count/view=%d/%q", strings.Count(view, "Recovered answer"), view)
	}
	if !strings.Contains(view, "Diagnostic history incomplete") {
		t.Fatalf("missing persistent diagnostic warning: %q", view)
	}
	p.Quit()
	<-done
	firstClosed = true
	if !strings.Contains(stripANSI(h.out.String()), "Recovered answer") || !strings.Contains(stripANSI(h.out.String()), "Diagnostic history incomplete") {
		t.Fatal("terminal renderer omitted recovery evidence")
	}
	health = events.WorkJournalHealth{Scope: scope}
	h.model.setStatus("transient status", false)
	p2 := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done2 := make(chan error, 1)
	go func() { _, err := p2.Run(); done2 <- err }()
	defer func() { p2.Quit(); <-done2 }()
	p2.Send(voiceTickMsg(time.Now().Add(2 * time.Second)))
	view = <-h.views
	if !strings.Contains(view, "Diagnostic history incomplete") {
		t.Fatal("warning cleared after healthy poll")
	}
	if strings.Count(view, "Recovered answer") != 1 {
		t.Fatal("result duplicated")
	}
}

func TestRecoveryFocusFetchesAndNewRuntimeClearsWarning(t *testing.T) {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	next := scope
	next.RuntimeEpoch = "new"
	current := scope
	health := events.WorkJournalHealth{Scope: scope, Incomplete: true}
	m := sizedConversation(t, 100, 24)
	m.deps.workScope = func() work.WorkScope { return current }
	m.deps.workSnapshot = func() work.Snapshot { return work.Snapshot{Scope: current, Revision: 1} }
	m.deps.journalHealth = func() events.WorkJournalHealth { return health }
	m, _ = m.Update(tea.FocusMsg{})
	if m.workScope != scope || !m.journalIncomplete {
		t.Fatalf("focus recovery scope=%+v warning=%v", m.workScope, m.journalIncomplete)
	}
	m.journalIncomplete = true
	current = next
	health = events.WorkJournalHealth{Scope: next}
	m.startConversation(conversationDeps{bus: events.NewBus(), ctx: context.Background(), workScope: func() work.WorkScope { return current }, workSnapshot: func() work.Snapshot { return work.Snapshot{Scope: current} }, journalHealth: func() events.WorkJournalHealth { return health }})
	if m.journalIncomplete {
		t.Fatal("new runtime retained old warning")
	}
	if m.workScope != next {
		t.Fatalf("focus/start scope=%+v", m.workScope)
	}
}

func TestRecoveryTimerRunsWithReducedMotionIdle(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.reducedMotion = true
	m.deps.ctx = context.Background()
	m.deps.workSnapshot = func() work.Snapshot { return work.Snapshot{} }
	cmd := m.workProjectionPollCmd()
	if cmd == nil {
		t.Fatal("recovery timer disabled in reduced-motion idle mode")
	}
	if _, ok := cmd().(workProjectionPollMsg); !ok {
		t.Fatal("recovery timer did not emit poll message")
	}
}
