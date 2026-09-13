package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
)

type workActionRunner struct {
	*fakeTurnRunner
	scope   work.WorkScope
	actions []string
	cancels []string
}

func (r *workActionRunner) WorkResultAction(s work.WorkScope, id, action string) error {
	r.scope = s
	r.actions = append(r.actions, action+":"+id)
	return nil
}
func (r *workActionRunner) CancelWork(id string) error { r.cancels = append(r.cancels, id); return nil }
func (r *workActionRunner) StopWorkers() error         { r.cancels = append(r.cancels, "all"); return nil }

func TestWorkCommandsRemainLocalAndKeepDetailFocus(t *testing.T) {
	for _, state := range []turnState{turnIdle, turnVoiceListening, turnVoiceResponding, turnTextRunning} {
		m := sizedConversation(t, 100, 24)
		runner := &workActionRunner{fakeTurnRunner: &fakeTurnRunner{}}
		m.deps.runner = runner
		m.turnState = state
		m.handleEvent(events.WorkStateChanged{Snapshot: stripSnapshot()})
		m.input.SetValue("keep draft")
		m.runWorkCommand([]string{"weather"})
		if !m.activityFocused || m.workFilter != "weather" || m.input.Value() != "keep draft" || m.turnState != state {
			t.Fatal("inspect changed focus/draft/turn")
		}
		m.runWorkCommand([]string{"dismiss", "weather"})
		m.runWorkCommand([]string{"cancel", "weather"})
		if len(runner.actions) != 1 || runner.actions[0] != "dismiss:weather" || len(runner.cancels) != 1 || runner.scope != stripSnapshot().Scope {
			t.Fatalf("actions=%v cancel=%v", runner.actions, runner.cancels)
		}
		if len(runner.texts()) != 0 || m.workFilter != "weather" || !m.activityFocused {
			t.Fatal("control reached brain or stole focus")
		}
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.workFilter != "" || !m.activityFocused {
			t.Fatal("first escape shouldclearfilter")
		}
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.activityFocused {
			t.Fatal("second escape should return Chat")
		}
		if m.workActivity {
			t.Fatal("second escape left the work inspector sticky")
		}
	}
}

func TestWorkInspectorExitRestoresTimelineForToggleAndClear(t *testing.T) {
	for _, name := range []string{"ctrl+t", "/activity", "/clear"} {
		t.Run(name, func(t *testing.T) {
			m := sizedConversation(t, 100, 24)
			m.handleEvent(events.WorkStateChanged{Snapshot: stripSnapshot()})
			m.runWorkCommand([]string{"all"})
			if !m.workActivity || !m.activityFocused {
				t.Fatal("work inspection did not take focus")
			}

			switch name {
			case "ctrl+t":
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
			case "/activity":
				m.executeSlashCommand(slashCommand{id: commandActivity}, nil)
			case "/clear":
				m.executeSlashCommand(slashCommand{id: commandClear}, nil)
			}
			if m.workActivity {
				t.Fatalf("%s left work inspector active", name)
			}
			if name != "/clear" && m.activityViewport.TotalLineCount() == 0 {
				t.Fatalf("%s did not restore the activity timeline", name)
			}
		})
	}
}
func TestWorkCommandShowsOriginAndCanonicalDisposition(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	m.handleEvent(events.WorkStateChanged{Snapshot: stripSnapshot()})
	m.runWorkCommand([]string{"weather"})
	text := stripANSI(m.activityViewport.View())
	for _, want := range []string{"samantha", "held", "muted", "forecast", "Simple"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %q", want, text)
		}
	}
	if m.workCounts().unseen != 0 || m.workCounts().updates != 1 {
		t.Fatal("detail visibility confused with delivery")
	}
}
func TestWorkCopyIncludesOutcomeOnlyOnce(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	s := stripSnapshot()
	m.handleEvent(events.WorkStateChanged{Snapshot: s})
	m.handleEvent(events.WorkStateChanged{Snapshot: s})
	if text := m.latestReadableWorkResult(); !strings.Contains(text, "Fixture forecast") || !strings.Contains(text, "Weather") {
		t.Fatalf("copylast=%q", text)
	}
	if len(m.readableWorkResults()) != 1 {
		t.Fatal("duplicatecopyresult")
	}
}

func TestWorkInspectionRefreshesSavedWatermarkWithoutNewRevision(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	s := stripSnapshot()
	m.handleEvent(events.WorkStateChanged{Snapshot: s})
	s.PersistedRevision = s.Revision
	m.deps.workSnapshot = func() work.Snapshot { return s }
	m.recoverWork(time.Now(), true)
	if m.workDisplay.snapshot.PersistedRevision != s.Revision {
		t.Fatal("same revision saved watermark stayed stale")
	}
}
