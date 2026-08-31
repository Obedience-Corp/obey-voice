package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

// beginDelete arms the confirmation for the persona under the cursor. Nothing
// is removed here: the answer keystroke is a separate, deliberate one.
//
// The two refusals persona.Delete owns are checked up front so the prompt is
// never shown for a deletion that cannot happen.
func (m *personasModel) beginDelete() {
	if m.loadErr != "" {
		return
	}
	if m.cursor < 0 || m.cursor >= len(m.items) {
		// Trailing "+ Create" row: nothing to delete.
		return
	}
	p := m.items[m.cursor]
	if p == nil {
		return
	}
	name := personaDisplayName(p)
	if p.Builtin {
		m.message = fmt.Sprintf("%s is built-in and cannot be deleted", name)
		return
	}
	if len(m.items) <= 1 {
		m.message = fmt.Sprintf("%s is the only persona: create another before deleting it", name)
		return
	}
	m.confirmDeleteID = p.ID
	m.confirmDeleteName = name
	m.message = ""
}

// updateDeleteConfirm answers a pending delete. Only "y" deletes; every other
// key cancels, so a stray enter or space on this screen cannot destroy a
// profile the user meant to keep.
func (m personasModel) updateDeleteConfirm(msg tea.KeyMsg) (personasModel, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.performDelete()
	default:
		m.message = fmt.Sprintf("Kept %s", m.confirmDeleteName)
		m.confirmDeleteID, m.confirmDeleteName = "", ""
	}
	return m, nil
}

// performDelete runs the confirmed removal and re-reads the list.
func (m *personasModel) performDelete() {
	id, name := m.confirmDeleteID, m.confirmDeleteName
	m.confirmDeleteID, m.confirmDeleteName = "", ""
	if id == "" {
		return
	}
	del := m.deletePersona
	if del == nil {
		del = persona.Delete
	}
	res, err := del(m.cfg, id)
	if err != nil {
		m.message = fmt.Sprintf("Failed to delete %s: %v", name, personaDeleteReason(err))
		return
	}
	m.reload()
	m.cursor = min(m.cursor, max(m.listLen()-1, 0))
	m.ensureVisible()
	m.message = personaDeleteMessage(name, res)
}

// personaDeleteReason unwraps the package's sentinel refusals into the phrasing
// this screen uses; anything else is reported as-is.
func personaDeleteReason(err error) error {
	switch {
	case errors.Is(err, persona.ErrBuiltinProtected):
		return errors.New("built-in personas cannot be deleted")
	case errors.Is(err, persona.ErrLastPersona):
		return errors.New("it is the only persona left")
	default:
		return err
	}
}

// personaDeleteMessage reports what the deletion actually did — including the
// two outcomes a user would otherwise discover later: a prompt document left
// behind because another persona owns it, and a forced switch of the active
// persona.
func personaDeleteMessage(name string, res *persona.DeleteResult) string {
	msg := fmt.Sprintf("Deleted %s", name)
	if res == nil {
		return msg
	}
	if len(res.Kept) > 0 {
		kept := make([]string, 0, len(res.Kept))
		for _, path := range res.Kept {
			kept = append(kept, filepath.Base(path))
		}
		msg += " · kept shared prompt " + strings.Join(kept, ", ")
	}
	if res.Reactivated && res.ActivePersona != "" {
		msg += " · active persona is now " + res.ActivePersona
	}
	return msg
}

// personaDisplayName is the human label for a profile: its display name when it
// has one, else the id.
func personaDisplayName(p *persona.Profile) string {
	if p == nil {
		return ""
	}
	if name := strings.TrimSpace(p.DisplayName); name != "" {
		return name
	}
	return p.ID
}

// confirmDeleteLines renders the confirmation in place of the list, padded to
// the same height so the screen does not jump.
func (m personasModel) confirmDeleteLines(rows int) []string {
	body := []string{
		normalStyle.Render(fmt.Sprintf("Delete %s?", m.confirmDeleteName)),
		"",
		dimStyle.Render("Removes personas/" + m.confirmDeleteID + "/ and the system prompt it owns."),
		dimStyle.Render("A prompt document another persona points at is kept."),
	}
	if m.cfg != nil && persona.ActiveID(m.cfg) == m.confirmDeleteID {
		body = append(body, dimStyle.Render("This is the active persona — another one will be activated."))
	}
	body = append(body,
		"",
		warningStyle.Render("This cannot be undone."),
		"",
		normalStyle.Render("Press ")+errorStyle.Render("y")+normalStyle.Render(" to delete · any other key cancels"),
	)
	box := formBoxStyled(errorStyle.Render("Confirm delete"), body, m.formBoxWidth(), errorStyle)
	return padPersonasLines(append([]string{""}, box...), rows)
}
