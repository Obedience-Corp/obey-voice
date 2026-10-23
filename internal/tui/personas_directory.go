package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/persona/campdirs"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// personaDirectoryPicker holds the Starting directory row: registered camps
// discovered on form open plus the (custom) free-text state. The chosen path
// itself lives in personasModel.directoryInput — one source of truth for the
// picker chip and the text field, as with the Brain model row.
type personaDirectoryPicker struct {
	// discover is the injection seam; production shells out to `camp list`.
	discover func(context.Context) campdirs.Choices

	seq     int
	loading bool
	loaded  bool
	notice  string
	camps   []meeting.Campaign
	custom  bool
	// customText survives cycling off (custom) and back within one form-open.
	customText string
	// initial is the path the form opened with. Save validates only a changed
	// value, so a persona whose folder vanished still opens and saves its
	// other fields; initialMissing (checked once, on open) makes the row say so.
	initial        string
	initialMissing bool
	cancel         context.CancelFunc
}

// personaDirectoriesMsg carries an async camp scan back to the form.
type personaDirectoriesMsg struct {
	seq     int
	choices campdirs.Choices
}

// directoryChoice is one entry of the picker list: a label for the chip and
// the path it stands for ("" for the app default and for (custom)).
type directoryChoice struct {
	label string
	path  string
}

func newPersonaDirectoryInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "~/projects/app or /absolute/path"
	ti.CharLimit = 1024
	ti.Width = 40
	return ti
}

// loadDirectories starts the camp scan for the open form. The row renders
// (default) and (custom) immediately with a scanning note until it lands.
func (m *personasModel) loadDirectories() tea.Cmd {
	discover := m.directory.discover
	if discover == nil {
		return nil
	}
	m.cancelDirectories()
	m.directory.seq++
	m.directory.loading = true
	m.directory.loaded = false
	m.directory.notice = ""
	m.directory.camps = nil

	seq := m.directory.seq
	ctx, cancel := context.WithCancel(context.Background())
	m.directory.cancel = cancel
	return func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return personaDirectoriesMsg{seq: seq, choices: campdirs.Choices{Notice: err.Error()}}
		}
		return personaDirectoriesMsg{seq: seq, choices: discover(ctx)}
	}
}

func (m *personasModel) cancelDirectories() {
	if m.directory.cancel != nil {
		m.directory.cancel()
		m.directory.cancel = nil
	}
}

// resetDirectories drops scan and custom state when the form closes.
func (m *personasModel) resetDirectories() {
	m.cancelDirectories()
	m.directory.seq++
	m.directory.loading = false
	m.directory.loaded = false
	m.directory.notice = ""
	m.directory.camps = nil
	m.directory.custom = false
	m.directory.customText = ""
	m.directory.initial = ""
	m.directory.initialMissing = false
}

// applyDirectories folds a scan result into the row, dropping stale results.
func (m *personasModel) applyDirectories(msg personaDirectoriesMsg) {
	if msg.seq != m.directory.seq {
		return
	}
	m.cancelDirectories()
	m.directory.loading = false
	m.directory.loaded = true
	m.directory.notice = msg.choices.Notice
	m.directory.camps = msg.choices.Campaigns
}

// directoryValue is the path recorded on the persona ("" = inherit app default).
func (m personasModel) directoryValue() string {
	return strings.TrimSpace(m.directoryInput.Value())
}

// directoryList builds the row: (default) + discovered camps + any current
// path that matches no camp (so edit never drops a hand-written folder) +
// (custom) last.
func (m personasModel) directoryList() []directoryChoice {
	out := []directoryChoice{{label: stackDefaultLabel}}
	for _, c := range m.directory.camps {
		label := strings.TrimSpace(c.Name)
		if label == "" {
			label = c.Path
		}
		out = append(out, directoryChoice{label: label, path: c.Path})
	}
	// The sticky entry stays while (custom) owns the row, so leaving (custom)
	// returns to the saved folder instead of the nearest camp.
	if v := m.directoryValue(); v != "" {
		found := false
		for _, item := range out[1:] {
			if item.path == v {
				found = true
				break
			}
		}
		if !found {
			// A hand-written folder shows its last segment on the chip; the line
			// below carries the whole path.
			out = append(out, directoryChoice{label: filepath.Base(v), path: v})
		}
	}
	return append(out, directoryChoice{label: stackCustomLabel})
}

// directoryIndex maps the current value onto directoryList.
func (m personasModel) directoryIndex() int {
	list := m.directoryList()
	if m.directory.custom {
		return len(list) - 1
	}
	v := m.directoryValue()
	if v == "" {
		return 0
	}
	for i, item := range list[1:] {
		if item.path == v {
			return i + 1
		}
	}
	return 0
}

// cycleDirectory moves ←/→ through the choices. Landing on (custom) focuses
// the free text field and restores what was typed there; leaving it keeps
// that text for the way back.
func (m *personasModel) cycleDirectory(delta int) {
	list := m.directoryList()
	idx := (m.directoryIndex() + delta + len(list)) % len(list)
	custom := idx == len(list)-1
	if m.directory.custom && !custom {
		m.directory.customText = m.directoryValue()
	}
	switch {
	case custom:
		m.directory.custom = true
		// Entering from a listed folder keeps it in the field as the draft;
		// only text typed under (custom) earlier replaces it.
		if text := m.directory.customText; text != "" {
			m.directoryInput.SetValue(text)
		}
		m.directoryInput.Focus()
		m.directoryInput.CursorEnd()
	default:
		m.directory.custom = false
		m.directoryInput.SetValue(list[idx].path)
		m.directoryInput.Blur()
	}
	m.message = m.formRowHelp()
}

// prefillDirectory seeds the row from a profile (nil = app default). The saved
// value is loaded as-is: a folder that has since vanished must still open in
// the editor; only a changed value is validated on save.
func (m *personasModel) prefillDirectory(p *persona.Profile) {
	m.directory.custom = false
	m.directory.customText = ""
	value := ""
	if p != nil {
		value = strings.TrimSpace(p.StartingDirectory)
	}
	m.directory.initial = value
	m.directory.initialMissing = false
	if value != "" {
		_, err := config.ValidateStartingDirectory(value)
		m.directory.initialMissing = err != nil
	}
	m.directoryInput.SetValue(value)
}

// directoryChanged reports whether save has to validate and persist the row.
func (m personasModel) directoryChanged() bool {
	return m.directoryValue() != m.directory.initial
}

func (m personasModel) directoryRowHelp() string {
	if m.directory.custom {
		return "Type a folder path (~ allowed) · ←/→ leaves custom · must exist · empty = (default)"
	}
	return "Starting directory · new conversations run file & shell tools here · (default) inherits app work_dir"
}

// directoryNote explains the row when the camp list is not a list.
func (m personasModel) directoryNote() string {
	switch {
	case m.directory.loading:
		return "scanning camps…"
	case m.directory.notice != "":
		return "camp list unavailable · (custom) still works"
	case m.directory.loaded && len(m.directory.camps) == 0:
		return "no registered camps · (custom) for any folder"
	}
	return ""
}

// directoryLines renders the Starting directory box: the picker row, then a
// dim line naming the folder the choice stands for (or why it is missing).
func (m personasModel) directoryLines() []string {
	mark, labelStyle := "  ", dimStyle
	if m.formRow == formRowDirectory {
		mark, labelStyle = selectedStyle.Render("▸ "), selectedStyle
	}
	remaining := max(m.formBoxWidth()-(4+2+stackLabelWidth+1), 12)
	note := m.directoryNote()
	list := m.directoryList()
	choice := list[m.directoryIndex()]

	chipRoom := remaining
	if m.directory.custom || note != "" {
		chipRoom = max(remaining/2, 12)
	}
	value := "‹ " + ansi.Truncate(choice.label, max(chipRoom-4, 4), "…") + " ›"
	remaining -= ansi.StringWidth(value)
	if m.directory.custom && remaining >= 10 {
		field := m.directoryInput
		width := remaining - 1
		if note != "" {
			if field.Value() == "" {
				field.Placeholder = ""
			}
			width = max(min(width, 24), 8)
		}
		field.Width = width
		rendered := ansi.Truncate(trimTrailingSpaces(field.View()), width, "…")
		value += " " + rendered
		remaining -= ansi.StringWidth(rendered) + 1
	}
	if note != "" && remaining >= 6 {
		value += dimStyle.Render("  " + ansi.Truncate(note, remaining-2, "…"))
	}
	row := mark + labelStyle.Render(fmt.Sprintf("%-*s", stackLabelWidth, "Directory")) + " " + value

	detail := m.directoryDetail(max(m.formBoxWidth()-4-2, 8))
	return []string{row, dimStyle.Render("  " + detail)}
}

// truncateLeft keeps the tail of a path, which is the part that tells folders
// apart; formBox would otherwise cut exactly that off.
func truncateLeft(s string, width int) string {
	if ansi.StringWidth(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && ansi.StringWidth("…"+string(runes)) > width {
		runes = runes[1:]
	}
	return "…" + string(runes)
}

// directoryDetail is the second line: the folder this choice resolves to, or
// why the saved one no longer works.
func (m personasModel) directoryDetail(width int) string {
	v := m.directoryValue()
	switch {
	case m.directory.custom && v == "":
		return "type a folder · saved with the persona"
	case v == "":
		if m.cfg != nil && strings.TrimSpace(m.cfg.WorkDir) != "" {
			return truncateLeft("app default · "+strings.TrimSpace(m.cfg.WorkDir), width)
		}
		return "app default · the folder obey-voice is launched from"
	}
	if v == m.directory.initial && m.directory.initialMissing {
		prefix := "folder missing · pick (default) or another · "
		return prefix + truncateLeft(v, max(width-ansi.StringWidth(prefix), 8))
	}
	return truncateLeft(v, width)
}
