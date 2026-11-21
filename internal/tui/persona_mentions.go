package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/internal/pathcomplete"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// mentionItem is one row of the @ palette: a persona (id, name) or a completed
// @path (path).
type mentionItem struct {
	id, name string
	path     string
	score    int
}

// mentionPalette completes the @token at the cursor. At the start of a message
// it lists personas, then paths; anywhere else only paths.
type mentionPalette struct {
	active   bool
	start    int
	query    string
	selected int
	// matches holds the persona rows first, then the path rows, so one
	// selection runs over both and the persona rows stay a prefix.
	matches  []mentionItem
	personas int
	profiles []*persona.Profile
	// profilesRead is reset whenever the list closes, so each time it opens
	// at the start of a message the roster is read fresh.
	profilesRead bool
	load         func() ([]*persona.Profile, error)
	err          error
	// paths supplies the @path root and campaign shortcuts; nil disables
	// path completion. It is read once per conversation.
	paths     func() (root string, shortcuts map[string]string)
	pathsRead bool
	root      string
	shortcuts map[string]string
	// dismissed names the token Esc closed the list for; the list stays
	// closed until the token text changes.
	dismissed string
}

// mentionAt finds the @token at the cursor: the rune offset of "@", the text
// between "@" and the cursor, and where the token ends. "@" must start the
// message or follow whitespace, so email addresses never match.
func (m conversationModel) mentionAt() (start int, query string, end int, ok bool) {
	value := m.input.Value()
	cursor := m.textareaCursorOffset()
	query, start = pathcomplete.AtQuery(value, cursor)
	if start < 0 {
		return 0, "", 0, false
	}
	text := []rune(value)
	end = cursor
	for end < len(text) && !unicode.IsSpace(text[end]) {
		end++
	}
	return start, query, end, true
}

func (m conversationModel) mentionKey(start, end int) string {
	return fmt.Sprintf("%d:%s", start, string([]rune(m.input.Value())[start:end]))
}

// composerTakesText is false in vim NORMAL and VISUAL, where keys are
// commands rather than text and the path list must not claim them.
func (m conversationModel) composerTakesText() bool {
	return !m.vim.enabled || m.vim.mode == vimInsert
}

// mentionPathsFor completes against the mention root of cfg, with the
// campaign's shortcuts when that root is a campaign.
func mentionPathsFor(cfg *config.Config) func() (string, map[string]string) {
	return func() (string, map[string]string) {
		root, campaign := config.MentionRoot(cfg, "")
		if !campaign {
			return root, nil
		}
		return root, pathcomplete.LoadShortcuts(root)
	}
}

func (m *conversationModel) mentionPaths() (string, map[string]string) {
	if !m.mentions.pathsRead && m.mentions.paths != nil {
		m.mentions.root, m.mentions.shortcuts = m.mentions.paths()
		m.mentions.pathsRead = true
	}
	return m.mentions.root, m.mentions.shortcuts
}

// mentionsCarryRoot is true when the prompt will not say what an @path is
// relative to. A completed path is then written in full. It reads the session
// config, which a persona switch or a Settings reload replaces along with the
// brain's.
func (m conversationModel) mentionsCarryRoot() bool {
	return !config.MentionRootStated(m.cfg)
}

func (m *conversationModel) syncMentions() {
	start, query, end, ok := m.mentionAt()
	if !ok {
		m.mentions.active, m.mentions.dismissed = false, ""
		return
	}
	if key := m.mentionKey(start, end); key == m.mentions.dismissed {
		m.mentions.active = false
		return
	}
	m.mentions.dismissed = ""
	atStart := start == 0
	pathToken := strings.Contains(query, "/")
	if !m.mentions.active || m.mentions.start != 0 {
		m.mentions.profilesRead = false
	}
	if atStart && !pathToken && !m.mentions.profilesRead {
		load := m.mentions.load
		if load == nil {
			load = persona.List
		}
		m.mentions.profiles, m.mentions.err = load()
		m.mentions.profilesRead = true
	}
	if !m.mentions.active || query != m.mentions.query || start != m.mentions.start {
		m.mentions.selected = 0
	}
	m.mentions.start, m.mentions.query = start, query
	m.mentions.matches = nil
	if atStart && !pathToken {
		m.mentions.matches = personaMatches(query, m.mentions.profiles)
	}
	m.mentions.personas = len(m.mentions.matches)
	if m.composerTakesText() {
		if root, shortcuts := m.mentionPaths(); root != "" {
			for _, c := range pathcomplete.Candidates(pathcomplete.Unrooted(query, root), root, shortcuts) {
				m.mentions.matches = append(m.mentions.matches, mentionItem{path: c})
			}
		}
	}
	// A path token with nothing to offer shows no list, even at the start of
	// a message: "No matching personas" would misdescribe it.
	m.mentions.active = len(m.mentions.matches) > 0 || (atStart && !pathToken)
	m.mentions.selected = min(m.mentions.selected, max(len(m.mentions.matches)-1, 0))
}

func personaMatches(query string, profiles []*persona.Profile) []mentionItem {
	var matches []mentionItem
	for _, p := range profiles {
		if p == nil || p.Inactive {
			continue
		}
		score := mentionScore(query, p.ID)
		for _, name := range append([]string{p.DisplayName}, p.Aliases...) {
			score = min(score, mentionScore(query, name))
		}
		if score < 1000000 {
			matches = append(matches, mentionItem{id: p.ID, name: p.DisplayName, score: score})
		}
	}
	slices.SortFunc(matches, func(a, b mentionItem) int {
		return cmp.Or(cmp.Compare(a.score, b.score), strings.Compare(a.id, b.id))
	})
	return matches
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

// visibleMentions drops the path rows while the composer is not taking text.
// Vim mode changes do not resync the composer, so this is decided on read.
func (m conversationModel) visibleMentions() []mentionItem {
	if m.composerTakesText() {
		return m.mentions.matches
	}
	return m.mentions.matches[:m.mentions.personas]
}

func (m conversationModel) mentionsShowing() bool {
	if !m.mentions.active {
		return false
	}
	return len(m.visibleMentions()) > 0 || m.mentions.start == 0 && !strings.Contains(m.mentions.query, "/")
}

func (m conversationModel) pathMentionsShowing() bool {
	return m.mentions.active && len(m.visibleMentions()) > m.mentions.personas
}

// dismissMentions closes the list for the token at the cursor.
func (m *conversationModel) dismissMentions() {
	if start, _, end, ok := m.mentionAt(); ok {
		m.mentions.dismissed = m.mentionKey(start, end)
	}
	m.mentions.active = false
	m.reflow()
}

func (m *conversationModel) handleMentionKey(msg tea.KeyMsg) bool {
	items := m.visibleMentions()
	if !m.mentions.active || len(items) == 0 {
		return false
	}
	n := len(items)
	selected := min(m.mentions.selected, n-1)
	switch msg.String() {
	case "tab":
		start, _, end, ok := m.mentionAt()
		if !ok {
			return false
		}
		if item := items[selected]; item.path != "" {
			m.acceptPathMention(start, item.path)
		} else {
			m.acceptPersonaMention(end, item.id)
		}
	case "up", "shift+tab":
		m.mentions.selected = (selected - 1 + n) % n
	case "down":
		m.mentions.selected = (selected + 1) % n
	default:
		return false
	}
	return true
}

func (m *conversationModel) acceptPersonaMention(end int, id string) {
	previous := m.input.Value()
	tail := string([]rune(previous)[end:])
	prefix := "@" + id
	if tail == "" {
		tail = " "
	}
	m.input.SetValue(prefix + tail)
	m.moveTextareaCursorToOffset(len([]rune(prefix)) + 1)
	m.syncEditorFromTextarea()
	m.syncComposer(previous)
}

// acceptPathMention replaces the token up to the cursor with path and keeps
// whatever follows the cursor. The list shows paths relative to the root; the
// inserted text is absolute when the prompt does not state that root. A directory leaves the cursor after its "/" so
// the list moves into it; a file at the end of the draft gets a space.
func (m *conversationModel) acceptPathMention(start int, path string) {
	previous := m.input.Value()
	runes := []rune(previous)
	tail := string(runes[m.textareaCursorOffset():])
	insert := path
	if root, _ := m.mentionPaths(); m.mentionsCarryRoot() {
		insert = pathcomplete.Rooted(path, root)
	}
	if !strings.HasSuffix(path, "/") && tail == "" {
		insert += " "
	}
	m.input.SetValue(string(runes[:start]) + insert + tail)
	m.moveTextareaCursorToOffset(start + runeLen(insert))
	m.syncEditorFromTextarea()
	m.syncComposer(previous)
}

// expandMentionShortcut rewrites "@p/" to "@projects/" when the "/" just
// typed follows an exact campaign shortcut key.
func (m *conversationModel) expandMentionShortcut() {
	start, query, _, ok := m.mentionAt()
	if !ok || !strings.HasSuffix(query, "/") {
		return
	}
	root, shortcuts := m.mentionPaths()
	path, ok := pathcomplete.ExpandShortcut(strings.TrimSuffix(query, "/"), shortcuts)
	if !ok {
		return
	}
	expanded := "@" + path
	if m.mentionsCarryRoot() {
		expanded = pathcomplete.Rooted(expanded, root)
	}
	runes := []rune(m.input.Value())
	cursor := m.textareaCursorOffset()
	m.input.SetValue(string(runes[:start]) + expanded + string(runes[cursor:]))
	m.moveTextareaCursorToOffset(start + runeLen(expanded))
}

func (m conversationModel) mentionPaletteRows() int {
	if m.height < 13 {
		return 0
	}
	return min(max(len(m.visibleMentions()), 1), max(m.height-conversationInputHeight-9, 0))
}

func (m conversationModel) renderMentionPalette() string {
	rows := m.mentionPaletteRows()
	if rows == 0 {
		return ""
	}
	items := m.visibleMentions()
	if len(items) == 0 {
		if m.mentions.err != nil {
			return dimStyle.Render("Could not load personas: " + m.mentions.err.Error())
		}
		return dimStyle.Render("No matching personas")
	}
	selected := min(m.mentions.selected, len(items)-1)
	start := max(selected-rows+1, 0)
	var lines []string
	for i := start; i < min(start+rows, len(items)); i++ {
		item := items[i]
		prefix, style := "  ", dimStyle
		if i == selected {
			prefix, style = "› ", selectedStyle
		}
		line := prefix + item.path
		if item.path == "" {
			line = fmt.Sprintf("%s@%-22s %s", prefix, item.id, item.name)
		}
		lines = append(lines, style.Render(ansi.Truncate(line, max(m.width, 1), "…")))
	}
	return strings.Join(lines, "\n")
}
