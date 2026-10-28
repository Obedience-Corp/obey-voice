package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestRecordTalkSealsAdmitError(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	m, bus := startedConversation(t, &fakeTurnRunner{}, false)
	m.sessionID = "sess-record"
	m.deps.sessionID = "sess-record"

	m.configureRecord([]string{"talk"})
	if m.recordMode() != "talk" {
		t.Fatalf("mode = %q", m.recordMode())
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "rec") {
		t.Fatalf("missing rec chip:\n%s", view)
	}

	bus.Emit(events.Error{Stage: "admit", Message: "admit response: persona name \"veronica\" is ambiguous between veronica, veronica-hype"})
	recording := m.capture
	dir := recording.Dir()
	m.configureRecord([]string{"off"})
	if m.recordMode() != "" {
		t.Fatal("still recording after off")
	}

	<-recording.Done()
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ambiguous") {
		t.Fatalf("events.jsonl missing admit error:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCtrlRTogglesTalkNotInVim(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	m, _ := startedConversation(t, &fakeTurnRunner{}, false)
	m.sessionID = "sess-r"
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if m.recordMode() != "talk" {
		t.Fatalf("ctrl+r mode = %q want talk", m.recordMode())
	}
	recording := m.capture
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	<-recording.Done()
	if m.recordMode() != "" {
		t.Fatalf("second ctrl+r mode = %q want off", m.recordMode())
	}
}

func TestRecordStopsOnClearAndNewRuntime(t *testing.T) {
	for _, reset := range []string{"clear", "new runtime"} {
		t.Run(reset, func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			m, bus := startedConversation(t, &fakeTurnRunner{}, false)
			m.startCapture("talk")
			recording := m.capture
			if reset == "clear" {
				m.handleEvent(events.ConversationCleared{})
			} else {
				m.startConversation(m.deps)
			}
			if m.capture != nil || m.capturePending == nil {
				t.Fatal("capture survived conversation identity reset")
			}
			bus.Emit(events.UserInput{Text: "late after reset"})
			<-recording.Done()
			raw, err := os.ReadFile(filepath.Join(recording.Dir(), "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "late after reset") {
				t.Fatal("late event recorded after detach")
			}
			m.pollCapture()
			if m.capturePending != nil {
				t.Fatal("completed seal retained pending writer")
			}
		})
	}
}
