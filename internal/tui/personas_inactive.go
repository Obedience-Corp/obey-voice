package tui

import (
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

func (m *personasModel) toggleInactive() {
	if m.loadErr != "" || m.cursor < 0 || m.cursor >= len(m.items) {
		return
	}
	p := m.items[m.cursor]
	if p == nil {
		return
	}
	if !p.Inactive && m.selectedPersona != nil && m.selectedPersona() == p.ID {
		m.message = "Select another persona in this conversation before disabling " + displayName(p)
		return
	}
	set := m.setInactive
	if set == nil {
		set = persona.SetInactive
	}
	updated, err := set(m.cfg, p.ID, !p.Inactive)
	if err != nil {
		m.message = fmt.Sprintf("Failed to change persona state: %v", err)
		return
	}
	m.reload()
	if updated.Inactive {
		m.message = displayName(updated) + " is inactive; excluded from name routing"
	} else {
		m.message = displayName(updated) + " is enabled"
	}
}
