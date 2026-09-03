package tui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// settingsEditKind names the value being typed. editNone means the list has
// the keys.
type settingsEditKind int

const (
	editNone settingsEditKind = iota
	editWorkerURL
	editWorkerToken
	editUserDisplayName
)

// settingsEditor is a one-line value editor for settings a list cannot express
// — an address, a shared secret. A secret is typed, never displayed: the
// editor starts empty and masks what is typed, so no stored token is readable
// from this screen.
type settingsEditor struct {
	kind  settingsEditKind
	label string
	input textinput.Model
}

func (m *settingsModel) beginEdit(kind settingsEditKind, label, initial string) tea.Cmd {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 512
	input.Width = 48
	input.SetValue(initial)
	input.CursorEnd()
	if kind == editWorkerToken {
		input.EchoMode = textinput.EchoPassword
		input.Placeholder = "paste the token from speech-serve"
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

// editorLine renders the open editor above the list.
func (m settingsModel) editorLine() string {
	if m.edit.kind == editNone {
		return ""
	}
	return "  " + selectedStyle.Render(m.edit.label+": ") + m.edit.input.View()
}
