package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type personaMention struct {
	id, name string
	score    int
}
type personaMentions struct {
	active   bool
	query    string
	selected int
	profiles []*persona.Profile
	matches  []personaMention
	load     func() ([]*persona.Profile, error)
	err      error
}

// Only a leading @token addresses a persona. Email addresses and names in
// ordinary prose stay untouched; completion replaces just the token at cursor.
func (m conversationModel) mentionToken() (string, int, bool) {
	text := []rune(m.input.Value())
	cursor := m.textareaCursorOffset()
	if len(text) == 0 || text[0] != '@' || cursor < 1 || cursor > len(text) {
		return "", 0, false
	}
	end := 1
	for end < len(text) && !unicode.IsSpace(text[end]) {
		end++
	}
	if cursor > end {
		return "", 0, false
	}
	return string(text[1:cursor]), end, true
}

func (m *conversationModel) syncMentions() {
	query, _, active := m.mentionToken()
	if !active {
		m.mentions.active = false
		return
	}
	if !m.mentions.active {
		load := m.mentions.load
		if load == nil {
			load = persona.List
		}
		m.mentions.profiles, m.mentions.err = load()
	}
	if query != m.mentions.query || !m.mentions.active {
		m.mentions.selected = 0
	}
	m.mentions.active, m.mentions.query = true, query
	m.mentions.matches = nil
	for _, p := range m.mentions.profiles {
		if p == nil || p.Inactive {
			continue
		}
		score := mentionScore(query, p.ID)
		for _, name := range append([]string{p.DisplayName}, p.Aliases...) {
			score = min(score, mentionScore(query, name))
		}
		if score < 1000000 {
			m.mentions.matches = append(m.mentions.matches, personaMention{p.ID, p.DisplayName, score})
		}
	}
	slices.SortFunc(m.mentions.matches, func(a, b personaMention) int {
		return cmp.Or(cmp.Compare(a.score, b.score), strings.Compare(a.id, b.id))
	})
	m.mentions.selected = min(m.mentions.selected, max(len(m.mentions.matches)-1, 0))
}

func mentionScore(query, candidate string) int {
	q, c := []rune(strings.ToLower(query)), []rune(strings.ToLower(candidate))
	if strings.HasPrefix(string(c), string(q)) {
		return 0
	}
	pos, score := 0, 1000
	for _, r := range q {
		found := false
		for pos < len(c) {
			i := pos
			pos++
			if c[i] == r {
				found = true
				break
			}
			score++
		}
		if !found {
			return 1000000
		}
	}
	return score
}

func (m *conversationModel) handleMentionKey(msg tea.KeyMsg) bool {
	if !m.mentions.active || len(m.mentions.matches) == 0 {
		return false
	}
	n := len(m.mentions.matches)
	switch msg.String() {
	case "tab":
		_, end, ok := m.mentionToken()
		if !ok {
			return false
		}
		p := m.mentions.matches[m.mentions.selected]
		previous := m.input.Value()
		tail := string([]rune(previous)[end:])
		prefix := "@" + p.id
		if tail == "" {
			tail = " "
		}
		m.input.SetValue(prefix + tail)
		m.moveTextareaCursorToOffset(len([]rune(prefix)) + 1)
		m.syncEditorFromTextarea()
		m.syncComposer(previous)
	case "up", "shift+tab":
		m.mentions.selected = (m.mentions.selected - 1 + n) % n
	case "down":
		m.mentions.selected = (m.mentions.selected + 1) % n
	default:
		return false
	}
	return true
}

func (m conversationModel) mentionPaletteRows() int {
	if m.height < 13 {
		return 0
	}
	return min(max(len(m.mentions.matches), 1), max(m.height-conversationInputHeight-9, 0))
}

func (m conversationModel) renderMentionPalette() string {
	rows := m.mentionPaletteRows()
	if rows == 0 {
		return ""
	}
	if m.mentions.err != nil {
		return dimStyle.Render("Could not load personas: " + m.mentions.err.Error())
	}
	if len(m.mentions.matches) == 0 {
		return dimStyle.Render("No matching personas")
	}
	start := max(m.mentions.selected-rows+1, 0)
	var lines []string
	for i := start; i < min(start+rows, len(m.mentions.matches)); i++ {
		p := m.mentions.matches[i]
		prefix, style := "  ", dimStyle
		if i == m.mentions.selected {
			prefix, style = "› ", selectedStyle
		}
		line := fmt.Sprintf("%s@%-22s %s", prefix, p.id, p.name)
		lines = append(lines, style.Render(ansi.Truncate(line, max(m.width, 1), "…")))
	}
	return strings.Join(lines, "\n")
}
