package tui

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Resolve once per catalog reload, never from View or from the active persona's
// overlaid config: otherwise one persona's override leaks into inherited rows.
type personaListStack struct {
	provider, model, voice      string
	providerSource, modelSource string
	note                        string
}

func (m *personasModel) reloadStacks() {
	m.stacks = make(map[string]personaListStack, len(m.items))
	m.defaultsErr = ""
	load := m.loadDefaults
	if load == nil {
		load = config.LoadRaw
	}
	defaults, err := load()
	if err != nil {
		defaults = nil
		m.defaultsErr = "Settings unavailable; inherited values cannot be resolved"
	}
	m.listDefaults = defaults
	for _, p := range m.items {
		if p == nil {
			continue
		}
		m.stacks[p.ID] = resolvePersonaListStack(defaults, p)
	}
}

func resolvePersonaListStack(defaults *config.Config, p *persona.Profile) personaListStack {
	effective := &config.Config{}
	if defaults != nil {
		effective = defaults.Clone()
	}
	if p.Audio != nil && p.Audio.Mode == persona.AudioNative {
		selected, mode, err := persona.ResolveAudioConfig(effective, p)
		if err != nil {
			return personaListStack{provider: "Native unavailable", note: err.Error()}
		}
		return personaListStack{provider: selected.BrainProvider, model: mode.Model, voice: mode.Voice, providerSource: "Native persona", modelSource: "Native persona", note: "Native audio · experimental"}
	}
	persona.Apply(effective, p)
	s := personaListStack{
		provider:       strings.TrimSpace(effective.BrainProvider),
		model:          persona.ModelForProvider(effective, effective.BrainProvider),
		providerSource: "Settings", modelSource: "Settings",
	}
	if strings.TrimSpace(p.Brain.Provider) != "" {
		s.providerSource = "Persona"
	}
	if strings.TrimSpace(p.Brain.Model) != "" {
		s.modelSource = "Persona"
	}
	if s.provider == "" {
		s.provider = "unavailable"
	}
	if s.model == "" {
		s.model = "provider default"
	}
	if defaults == nil {
		if s.providerSource == "Settings" {
			s.provider = "unavailable"
		}
		if s.modelSource == "Settings" || s.provider == "unavailable" {
			s.model = "unavailable"
		}
	}
	if strings.EqualFold(s.provider, "claude") {
		s.model, s.modelSource = "CLI default", "Claude CLI"
		if strings.TrimSpace(p.Brain.Model) != "" {
			s.note = "Saved model is not routed by Claude; its CLI chooses the model."
		}
	}
	voice := config.TTSVoiceFor(effective, effective.TTSProvider)
	s.voice = strings.Trim(strings.TrimSpace(effective.TTSProvider)+" / "+strings.TrimSpace(voice), " /")
	if s.voice == "" {
		s.voice = "unavailable"
	}
	return s
}

func (m personasModel) screenHeight() int {
	if m.height <= 0 {
		return 24
	}
	return m.height
}
func (m personasModel) screenWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}
func (m personasModel) listBodyRows() int {
	header, footer := 3, 3 // leave one terminal row free, as other TUI lists do
	if m.screenHeight() < 10 {
		header = 2
	}
	if m.screenHeight() >= 16 && m.confirmDeleteID == "" {
		footer++
	}
	if m.loadWarning != "" {
		footer++
	}
	return max(m.screenHeight()-header-footer, 1)
}
func (m personasModel) personaRowHeight() int {
	if m.screenWidth() < 60 {
		return 2
	}
	return 1
}
func (m personasModel) listActiveID() string {
	if m.selectedPersona != nil {
		return m.selectedPersona()
	}
	if m.cfg != nil {
		return persona.ActiveID(m.cfg)
	}
	return ""
}

func personaCell(text string, width int) string {
	text = ansi.Truncate(text, max(width, 1), "…")
	return text + strings.Repeat(" ", max(width-ansi.StringWidth(text), 0))
}

func (m personasModel) personaColumns(name, llm, voice string) string {
	width := m.screenWidth() - 4
	if width < 56 {
		return name
	}
	nameW := min(26, width/3)
	if width < 96 {
		return personaCell(name, nameW) + "  " + personaCell(llm, width-nameW-2)
	}
	voiceW := min(34, width/3)
	return personaCell(name, nameW) + "  " + personaCell(llm, width-nameW-voiceW-4) + "  " + personaCell(voice, voiceW)
}

func (m personasModel) listLines(bodyRows int) []string {
	if m.loadErr != "" {
		return padPersonasLines([]string{errorStyle.Render("  Error loading personas: " + m.loadErr)}, bodyRows)
	}
	heading := m.personaColumns("PERSONA", "LLM PROVIDER / MODEL", "VOICE")
	if m.personaRowHeight() == 2 {
		heading = "PERSONA / LLM"
	}
	lines := []string{dimStyle.Render("  " + heading)}
	visible := m.visibleRows()
	start := min(max(m.offset, 0), max(m.listLen()-1, 0))
	end := min(start+visible, m.listLen())
	active := m.listActiveID()
	for i := start; i < end; i++ {
		prefix, style := "  ", normalStyle
		if i == m.cursor {
			prefix, style = "▸ ", selectedStyle
		}
		if i == len(m.items) {
			lines = append(lines, style.Render(prefix+personasCreateLabel))
			if m.personaRowHeight() == 2 {
				lines = append(lines, "")
			}
			continue
		}
		p := m.items[i]
		if p == nil {
			lines = append(lines, "")
			if m.personaRowHeight() == 2 {
				lines = append(lines, "")
			}
			continue
		}
		name := displayName(p)
		if p.ID == active {
			name = "✓ " + name
		}
		if p.Inactive {
			name += " [inactive]"
			if i != m.cursor {
				style = dimStyle
			}
		}
		stack := m.stacks[p.ID]
		llm := stack.provider + " / " + stack.model
		lines = append(lines, style.Render(prefix+m.personaColumns(name, llm, stack.voice)))
		if m.personaRowHeight() == 2 {
			lines = append(lines, "    "+normalStyle.Render(llm))
		}
	}
	detail := m.personaDetailLines()
	if len(detail) > 0 {
		// The range indicator makes a scrollable catalog distinct from an empty pane.
		lines = padPersonasLines(lines, bodyRows-len(detail)-1)
		rangeLabel := fmt.Sprintf("  %d–%d of %d", start+1, end, m.listLen())
		lines = append(lines, dimStyle.Render(rangeLabel))
		lines = append(lines, detail...)
	}
	return padPersonasLines(lines, bodyRows)
}

func (m personasModel) personaDetailLines() []string {
	if m.screenHeight() < 18 || m.screenWidth() < 36 || m.loadErr != "" || m.confirmDeleteID != "" {
		return nil
	}
	width := m.screenWidth() - 2
	var title string
	body := []string{}
	if m.cursor >= 0 && m.cursor < len(m.items) && m.items[m.cursor] != nil {
		p := m.items[m.cursor]
		s := m.stacks[p.ID]
		state := "enabled"
		if p.Inactive {
			state = "[inactive]"
		}
		if p.ID == m.listActiveID() {
			state = "✓ active"
		}
		title = headerStyle.Render(displayName(p))
		body = append(body, dimStyle.Render(p.ID+" · "+state))
		value := func(label, text, source string) {
			line := label + text
			if source != "" {
				line += "  · " + source
			}
			// Wrap model names rather than silently hiding their distinguishing suffix.
			for _, part := range strings.Split(ansi.Hardwrap(line, max(width-4, 1), false), "\n") {
				body = append(body, normalStyle.Render(part))
			}
		}
		value("LLM provider  ", s.provider, s.providerSource)
		value("LLM model     ", s.model, s.modelSource)
		value("Voice         ", s.voice, "")
		directory := strings.TrimSpace(p.StartingDirectory)
		if directory == "" {
			directory = "(app default)"
		}
		value("Directory     ", directory, "")
		if p.Builtin && p.ID == persona.DefaultID {
			body = append(body, dimStyle.Render("built-in default"))
		}
		if s.note != "" {
			body = append(body, warningStyle.Render(s.note))
		}
		if m.defaultsErr != "" {
			body = append(body, warningStyle.Render(m.defaultsErr))
		}
	} else {
		title = headerStyle.Render("Create a persona")
		body = []string{normalStyle.Render("Give it a name, a model, a voice, and a system prompt."), dimStyle.Render("Press enter to create · empty fields inherit Settings.")}
	}
	// Keep the selected row visible even on a short terminal with long names.
	maxBody := max(m.screenHeight()/2-2, 4)
	if len(body) > maxBody {
		body = body[:maxBody]
	}
	title = ansi.Truncate(title, max(width-6, 1), "…")
	return formBoxStyled(title, body, width, lipgloss.NewStyle().Foreground(colorAccent))
}
