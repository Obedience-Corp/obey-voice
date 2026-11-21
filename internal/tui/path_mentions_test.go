package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

// pathFixture is a disposable campaign: projects/{camp,obey-voice},
// workflow/design, docs and a root README.
func pathFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{".campaign/settings", "projects/camp", "projects/obey-voice", "workflow/design", "workflow/code_reviews", "docs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		".campaign/campaign.yaml":    "name: Fixture\n",
		"README.md":                  "fixture\n",
		"projects/camp/main.go":      "package main\n",
		"projects/obey-voice/go.mod": "module x\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func pathConversation(t *testing.T) (conversationModel, string) {
	t.Helper()
	root := pathFixture(t)
	m := mentionConversation(t)
	m.mentions.paths = func() (string, map[string]string) {
		return root, map[string]string{"p": "projects/", "de": "workflow/design/", "cr": "workflow/code_reviews/"}
	}
	return m, root
}

func typeRunes(t *testing.T, m conversationModel, text string) conversationModel {
	t.Helper()
	for _, r := range text {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func press(m conversationModel, keys ...tea.KeyType) conversationModel {
	for _, k := range keys {
		m, _ = m.Update(tea.KeyMsg{Type: k})
	}
	return m
}

func paths(m conversationModel) []string {
	var out []string
	for _, item := range m.visibleMentions() {
		if item.path != "" {
			out = append(out, item.path)
		}
	}
	return out
}

func TestPathMentionShortcutRewritesAndTabWalksIntoDirectories(t *testing.T) {
	m, _ := pathConversation(t)
	m = typeRunes(t, m, "look at @p/")
	if m.input.Value() != "look at @projects/" {
		t.Fatalf("shortcut not expanded: %q", m.input.Value())
	}
	if got := paths(m); !slices.Equal(got, []string{"@projects/camp/", "@projects/obey-voice/"}) || !m.mentionsShowing() {
		t.Fatalf("directory list = %v", got)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "@projects/camp/") || !strings.Contains(view, "@projects/obey-voice/") {
		t.Fatalf("palette missing entries:\n%s", view)
	}
	m = press(m, tea.KeyTab)
	if m.input.Value() != "look at @projects/camp/" || m.textareaCursorOffset() != runeLen("look at @projects/camp/") {
		t.Fatalf("directory accept = %q cursor %d", m.input.Value(), m.textareaCursorOffset())
	}
	if got := paths(m); !slices.Equal(got, []string{"@projects/camp/main.go"}) {
		t.Fatalf("list did not move into the directory: %v", got)
	}
	m = press(m, tea.KeyTab)
	if m.input.Value() != "look at @projects/camp/main.go " || m.mentionsShowing() {
		t.Fatalf("file accept = %q showing=%v", m.input.Value(), m.mentionsShowing())
	}
}

func TestPathMentionCustomShortcutAndDotSlashFiles(t *testing.T) {
	m, _ := pathConversation(t)
	m = typeRunes(t, m, "see @cr/")
	if m.input.Value() != "see @workflow/code_reviews/" {
		t.Fatalf("custom shortcut = %q", m.input.Value())
	}
	setMentionDraft(&m, "read @./RE")
	if got := paths(m); !slices.Equal(got, []string{"@./README.md", "@./projects/"}) {
		t.Fatalf("./ candidates = %v", got)
	}
	m = press(m, tea.KeyTab)
	if m.input.Value() != "read @./README.md " {
		t.Fatalf("accepted = %q", m.input.Value())
	}
}

func TestPathMentionShortcutOnlyExpandsOnATypedSlash(t *testing.T) {
	m, _ := pathConversation(t)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("see @p/"), Paste: true})
	if m.input.Value() != "see @p/" {
		t.Fatalf("paste expanded a shortcut: %q", m.input.Value())
	}
	m.insertClipboardText(" @de/")
	if !strings.HasSuffix(m.input.Value(), "@de/") {
		t.Fatalf("clipboard insert expanded a shortcut: %q", m.input.Value())
	}
	setMentionDraft(&m, "")
	m = typeRunes(t, m, "see @x/ and @projects/p/")
	if m.input.Value() != "see @x/ and @projects/p/" {
		t.Fatalf("non-shortcut expanded: %q", m.input.Value())
	}
}

func TestPathMentionAcceptKeepsTextAfterTheCursor(t *testing.T) {
	m, _ := pathConversation(t)
	setMentionDraft(&m, "héllo @proj and more")
	m.moveTextareaCursorToOffset(runeLen("héllo @proj"))
	m.syncEditorFromTextarea()
	m.syncComposer(m.input.Value())
	m = press(m, tea.KeyTab)
	if m.input.Value() != "héllo @projects/ and more" || m.textareaCursorOffset() != runeLen("héllo @projects/") {
		t.Fatalf("accept = %q cursor %d", m.input.Value(), m.textareaCursorOffset())
	}
	if got := paths(m); len(got) != 2 {
		t.Fatalf("list after mid-draft accept = %v", got)
	}
}

func TestPathMentionsJoinPersonasAtMessageStart(t *testing.T) {
	m, _ := pathConversation(t)
	setMentionDraft(&m, "@")
	items := m.visibleMentions()
	if m.mentions.personas != 3 || items[0].id != "jarvis" || items[2].id != "uncle-fu" || items[3].path != "@projects/" {
		t.Fatalf("merged list = %+v", items)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "@joker") || !strings.Contains(view, "@projects/") {
		t.Fatalf("palette:\n%s", view)
	}
	setMentionDraft(&m, "@j")
	if m.mentions.personas != 2 || len(paths(m)) == 0 {
		t.Fatalf("@j list = %+v", m.visibleMentions())
	}
	m = press(m, tea.KeyDown, tea.KeyDown, tea.KeyTab)
	if m.input.Value() != "@projects/" {
		t.Fatalf("path accept at message start = %q", m.input.Value())
	}
	if m.mentions.personas != 0 || len(paths(m)) != 2 {
		t.Fatalf("personas did not drop out once the token has a slash: %+v", m.visibleMentions())
	}
	setMentionDraft(&m, "@ve")
	m.mentions.load = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "veronica", DisplayName: "Veronica"}}, nil
	}
	setMentionDraft(&m, "")
	setMentionDraft(&m, "@ve")
	m = press(m, tea.KeyTab)
	if m.input.Value() != "@veronica " {
		t.Fatalf("persona completion at message start = %q", m.input.Value())
	}
}

func TestPathMentionListOnlyWhereItBelongs(t *testing.T) {
	m, _ := pathConversation(t)
	for _, draft := range []string{"mail me at user@example.com", "see @zzz", "see @projects/missing/"} {
		setMentionDraft(&m, draft)
		if m.mentionsShowing() || m.commandPaletteRows() != 0 {
			t.Fatalf("%q opened a list: %+v", draft, m.visibleMentions())
		}
	}
	setMentionDraft(&m, "@nobody")
	if !strings.Contains(stripANSI(m.View()), "No matching personas") {
		t.Fatal("message-start miss lost its persona line")
	}
	setMentionDraft(&m, "@nothing/here")
	if m.mentionsShowing() {
		t.Fatal("a path token with no candidates opened a list")
	}
}

func TestPathMentionOutsideACampaignListsTheStartingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := mentionConversation(t)
	m.mentions.paths = mentionPathsFor(&config.Config{ConversationDirectories: &config.ConversationDirectories{Default: config.ConversationDirectory{Dir: dir}}})
	m = typeRunes(t, m, "see @")
	if got := paths(m); !slices.Equal(got, []string{"@./go.mod", "@src/"}) {
		t.Fatalf("candidates = %v", got)
	}
	m = typeRunes(t, m, "p/")
	if m.input.Value() != "see @p/" {
		t.Fatalf("shortcut expanded outside a campaign: %q", m.input.Value())
	}
}

func TestPathMentionsUseTheCampaignJumpsFile(t *testing.T) {
	root := pathFixture(t)
	jumps := "shortcuts:\n    cr:\n        path: workflow/code_reviews/\n    cfg:\n        concept: config\n"
	if err := os.WriteFile(filepath.Join(root, ".campaign", "settings", "jumps.yaml"), []byte(jumps), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := filepath.Join(root, "projects", "camp")
	m := mentionConversation(t)
	m.mentions.paths = mentionPathsFor(&config.Config{ConversationDirectories: &config.ConversationDirectories{Default: config.ConversationDirectory{Dir: launch}}})
	m = typeRunes(t, m, "see @cr/ and @p/")
	if m.input.Value() != "see @workflow/code_reviews/ and @p/" {
		t.Fatalf("jumps.yaml shortcuts not used as-is: %q", m.input.Value())
	}
}

func TestPathMentionEscDismissesUntilTheTokenChanges(t *testing.T) {
	m, _ := pathConversation(t)
	m.vim.enabled, m.vim.mode = true, vimInsert
	m = typeRunes(t, m, "see @p")
	if !m.pathMentionsShowing() {
		t.Fatal("no path list to dismiss")
	}
	m = press(m, tea.KeyEsc)
	if m.mentionsShowing() || m.vim.mode != vimInsert || m.input.Value() != "see @p" {
		t.Fatalf("Esc: showing=%v mode=%v value=%q", m.mentionsShowing(), m.vim.mode, m.input.Value())
	}
	m.syncComposer(m.input.Value())
	if m.mentionsShowing() {
		t.Fatal("list reopened without the token changing")
	}
	m = press(m, tea.KeyEsc)
	if m.vim.mode != vimNormal {
		t.Fatal("second Esc did not reach the existing chain")
	}
	m.enterVimInsert()
	m = typeRunes(t, m, "r")
	if !m.mentionsShowing() {
		t.Fatal("list did not reopen when the token changed")
	}
}

func TestPathMentionEscLeavesPersonaOnlyListToTheExistingChain(t *testing.T) {
	m := mentionConversation(t)
	m.vim.enabled, m.vim.mode = true, vimInsert
	m = typeRunes(t, m, "@j")
	m = press(m, tea.KeyEsc)
	if m.vim.mode != vimNormal {
		t.Fatal("Esc over a persona-only list changed behaviour")
	}
}

// With an @path under the cursor, NORMAL and VISUAL keys must do exactly what
// they do without path completion.
func TestPathMentionsLeaveVimNormalAndVisualAlone(t *testing.T) {
	for _, mode := range []vimInputMode{vimNormal, vimVisual} {
		for _, keys := range [][]tea.KeyType{{tea.KeyTab}, {tea.KeyUp}, {tea.KeyDown}, {tea.KeyEsc}, {tea.KeyShiftTab}} {
			run := func(withPaths bool) conversationModel {
				m, _ := pathConversation(t)
				if !withPaths {
					m.mentions.paths = nil
				}
				m.vim.enabled, m.vim.mode = true, vimInsert
				m = typeRunes(t, m, "first line\nsee @p")
				m.vim.mode = mode
				if mode == vimVisual {
					m.editor.clearSelection()
				}
				if withPaths && (m.mentionsShowing() || m.commandPaletteRows() != 0) {
					t.Fatalf("mode %v: path list visible", mode)
				}
				return press(m, keys...)
			}
			with, without := run(true), run(false)
			if with.input.Value() != without.input.Value() || with.textareaCursorOffset() != without.textareaCursorOffset() || with.vim.mode != without.vim.mode {
				t.Fatalf("mode %v keys %v: with paths %q@%d mode %v, without %q@%d mode %v", mode, keys,
					with.input.Value(), with.textareaCursorOffset(), with.vim.mode,
					without.input.Value(), without.textareaCursorOffset(), without.vim.mode)
			}
		}
	}
}

func TestPathMentionUpDownMoveSelectionOnlyWhileAListShows(t *testing.T) {
	m, _ := pathConversation(t)
	m = typeRunes(t, m, "first line")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = typeRunes(t, m, "see @p/")
	cursor := m.textareaCursorOffset()
	m = press(m, tea.KeyDown)
	if m.mentions.selected != 1 || m.textareaCursorOffset() != cursor {
		t.Fatalf("Down with a list: selected=%d cursor %d->%d", m.mentions.selected, cursor, m.textareaCursorOffset())
	}
	m = press(m, tea.KeyUp, tea.KeyUp)
	if m.mentions.selected != 1 || m.textareaCursorOffset() != cursor {
		t.Fatalf("Up wraps the selection: selected=%d", m.mentions.selected)
	}
	m = typeRunes(t, m, " ")
	m = press(m, tea.KeyUp)
	if m.input.Line() != 0 {
		t.Fatal("Up without a list did not move the cursor")
	}
}

func TestPathMentionEnterSendsTheDraftAsTyped(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	root := pathFixture(t)
	m.mentions.paths = func() (string, map[string]string) { return root, map[string]string{"p": "projects/"} }
	m.mentions.load = func() ([]*persona.Profile, error) { return nil, nil }
	m = typeRunes(t, m, "@p/ob")
	if !m.mentionsShowing() {
		t.Fatal("no list showing")
	}
	m = press(m, tea.KeyDown)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.input.Value() != "" {
		t.Fatalf("draft not sent: %q", m.input.Value())
	}
	last := m.plainTurns[len(m.plainTurns)-1]
	if last.role != "user" || last.text != "@projects/ob" {
		t.Fatalf("sent %+v, want the typed text", last)
	}
}

func TestPersonasReturnWhenAPathTokenLosesItsSlash(t *testing.T) {
	m, root := pathConversation(t)
	if err := os.MkdirAll(filepath.Join(root, "j", "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@j/"), Paste: true})
	if m.mentions.personas != 0 || !slices.Equal(paths(m), []string{"@j/notes/"}) {
		t.Fatalf("pasted path token = %+v", m.visibleMentions())
	}
	m = press(m, tea.KeyBackspace)
	if m.input.Value() != "@j" || m.mentions.personas != 2 {
		t.Fatalf("draft %q, personas did not come back: %+v", m.input.Value(), m.visibleMentions())
	}
}
