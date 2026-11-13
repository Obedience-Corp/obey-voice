package tui

import (
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The terminal reports the mouse as escape sequences such as ESC[<65;60;15M.
// Bubble Tea reads input in fixed chunks and parses each chunk alone, so a
// burst of wheel events that crosses a chunk boundary leaves one sequence cut
// in two. The halves arrive as typed keys: alt+[ and then "<65;60;15M", or a
// run starting "[<". Left alone they land in the composer, and because typing
// cancels speech they also silence the reply that was about to play.

var (
	sgrMouseSequence = regexp.MustCompile(`^\[<(\d+);\d+;\d+[Mm]`)
	sgrMousePrefix   = regexp.MustCompile(`^\[(<(\d+(;(\d+(;(\d+)?)?)?)?)?)?$`)
)

const (
	sgrWheelUp   = 64
	sgrWheelDown = 65
)

// absorbMouseFragment swallows the typed remains of a split mouse report and
// applies the wheel movement it carried. handled is false for ordinary typing.
func (m conversationModel) absorbMouseFragment(msg tea.KeyMsg) (next conversationModel, cmd tea.Cmd, handled bool) {
	if msg.Type != tea.KeyRunes || msg.Paste {
		m.mouseFragment = ""
		return m, nil, false
	}
	text := string(msg.Runes)
	if m.mouseFragment == "" && !(msg.Alt && text == "[") && !strings.HasPrefix(text, "[<") {
		return m, nil, false
	}
	rest := m.mouseFragment + text
	m.mouseFragment = ""
	var cmds []tea.Cmd
	for {
		match := sgrMouseSequence.FindStringSubmatch(rest)
		if match == nil {
			break
		}
		rest = rest[len(match[0]):]
		button, _ := strconv.Atoi(match[1])
		var wheel tea.MouseButton
		switch button {
		case sgrWheelUp:
			wheel = tea.MouseButtonWheelUp
		case sgrWheelDown:
			wheel = tea.MouseButtonWheelDown
		default:
			continue
		}
		var scroll tea.Cmd
		m, scroll = m.updateScroll(tea.MouseMsg{Button: wheel, Action: tea.MouseActionPress})
		cmds = append(cmds, scroll)
	}
	switch {
	case rest == "":
	case sgrMousePrefix.MatchString(rest):
		m.mouseFragment = rest
	default:
		// Not a mouse report after all: let the key through as typed.
		return m, tea.Batch(cmds...), false
	}
	return m, tea.Batch(cmds...), true
}
