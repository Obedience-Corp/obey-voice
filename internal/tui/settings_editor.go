package tui

import (
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
)

// settingsEditKind names the value being typed. editNone means the list has
// the keys.
type settingsEditKind int

const (
	editNone settingsEditKind = iota
	editObey
	editWorkerURL
	editWorkerToken
	editUserDisplayName
	editSpeakerEnrollName
	editTalkHotkey
	editWorkerAssignment
	editWorkerReference
	editWorkerLimit
	editWorkerPolicy
	editDecision
	editDecisionKey
)

// settingsEditor is a one-line value editor for settings a list cannot express
// — an address, a shared secret. A secret is typed, never displayed: the
// editor starts empty and masks what is typed, so no stored token is readable
// from this screen.
type settingsEditor struct {
	kind        settingsEditKind
	label       string
	workerTier  string
	workerField string
	decisionKey string
	input       textinput.Model
}

func (m *settingsModel) beginEdit(kind settingsEditKind, label, initial string) tea.Cmd {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 512
	input.Width = 48
	input.SetValue(initial)
	input.CursorEnd()
	if kind == editWorkerToken || kind == editDecisionKey {
		input.EchoMode = textinput.EchoPassword
		input.Placeholder = "paste the token from speech-serve"
		if kind == editDecisionKey {
			input.Placeholder = "paste the selected provider's API key"
		}
	}
	if kind == editTalkHotkey {
		input.Placeholder = talkhotkey.DefaultComboFor(runtime.GOOS) + " (default); e.g. ctrl+alt+t, f13"
	}
	input.Focus()
	m.edit = settingsEditor{kind: kind, label: label, input: input}
	m.message = ""
	return textinput.Blink
}

func (m *settingsModel) cancelEdit() {
	m.edit = settingsEditor{}
}

// updateEditor owns every key while the editor is open, so a value containing
// "q" or "h" cannot navigate Settings out from under the typist.
func (m settingsModel) updateEditor(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.cancelEdit()
		m.enrollAfterName = false
		m.message = "Cancelled"
		return m, nil
	case "enter":
		kind, value := m.edit.kind, m.edit.input.Value()
		if kind == editObey {
			key := m.edit.decisionKey
			m.cancelEdit()
			m.commitObeyEdit(key, value)
			cmd := m.loadObeyCatalogIfSelected()
			return m, cmd
		}
		if kind == editDecision || kind == editDecisionKey {
			key := m.edit.decisionKey
			m.cancelEdit()
			cmd := m.commitDecisionEdit(key, value)
			return m, cmd
		}
		if kind == editSpeakerEnrollName {
			return m.commitSpeakerEnrollName(value)
		}
		if kind == editTalkHotkey {
			return m.commitTalkHotkey(value)
		}
		if kind == editWorkerAssignment {
			tier := m.edit.workerTier
			m.cancelEdit()
			m.commitWorkerAssignment(tier, value)
			return m, nil
		}
		if kind == editWorkerReference {
			tier, field := m.edit.workerTier, m.edit.label
			m.cancelEdit()
			m.commitWorkerReference(tier, field, value)
			return m, nil
		}
		if kind == editWorkerLimit {
			tier, field := m.edit.workerTier, m.edit.workerField
			m.cancelEdit()
			m.commitWorkerLimit(tier, field, value)
			return m, nil
		}
		if kind == editWorkerPolicy {
			field := m.edit.workerField
			m.cancelEdit()
			m.commitWorkerPolicy(field, value)
			return m, nil
		}
		m.cancelEdit()
		if kind == editUserDisplayName {
			return m, m.commitUserDisplayName(value)
		}
		m.commitSpeechEdit(kind, value)
		return m, nil
	}
	input, cmd := m.edit.input.Update(msg)
	m.edit.input = input
	return m, cmd
}

// commitSpeakerEnrollName starts a guest enrollment. Invalid names keep the
// editor open so the user can fix the value without re-selecting the row.
func (m settingsModel) commitSpeakerEnrollName(value string) (settingsModel, tea.Cmd) {
	value = strings.TrimSpace(value)
	if err := speaker.CheckEnrollmentName(value); err != nil {
		m.message = strings.TrimPrefix(err.Error(), "speaker: ")
		return m, nil
	}
	if m.isOwnerName(value) {
		m.message = "That's your voice — use Re-enroll your voice"
		return m, nil
	}
	m.cancelEdit()
	return m, m.startEnroll(value, false)
}

// editorLine renders the open editor above the list.
func (m settingsModel) editorLine() string {
	if m.edit.kind == editNone {
		return ""
	}
	return "  " + selectedStyle.Render(m.edit.label+": ") + m.edit.input.View()
}
