package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestSessionsCompactsForSmallTerminal(t *testing.T) {
	saved := []session.Session{{
		ID: "session-123", Summary: strings.Repeat("long summary ", 10), UpdatedAt: time.Now(),
		Turns: []brain.Turn{{Role: "user", Content: "hello"}},
	}}
	m := newSessions(saved)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 36, Height: 8})
	view := stripANSI(m.View())
	if got := len(strings.Split(view, "\n")); got > 8 {
		t.Fatalf("compact sessions rendered %d lines in 8-row terminal:\n%s", got, view)
	}
}

func TestSessionsFiltersEmptyConversations(t *testing.T) {
	saved := []session.Session{
		{ID: "empty"},
		{ID: "real", Turns: []brain.Turn{{Role: "user", Content: "hello"}}},
	}
	m := newSessions(saved)
	if len(m.sessions) != 1 || m.sessions[0].ID != "real" {
		t.Fatalf("resumable sessions = %+v", m.sessions)
	}
}

func TestSessionsVisibleRowsUsesTerminalHeight(t *testing.T) {
	saved := make([]session.Session, 20)
	for i := range saved {
		saved[i] = session.Session{
			ID: fmt.Sprintf("s%d", i), Summary: fmt.Sprintf("chat %d", i),
			Turns: []brain.Turn{{Role: "user", Content: "hi"}}, UpdatedAt: time.Now(),
		}
	}
	m := newSessions(saved)
	// Zero height used to hard-cap at 3 visible rows.
	if got := m.visibleRows(); got < 10 {
		t.Fatalf("visibleRows with unset height = %d, want a full default list", got)
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := m.visibleRows(); got != 18 {
		t.Fatalf("visibleRows at height 24 = %d, want 18", got)
	}
	view := stripANSI(m.View())
	// At least more than the old 3-row cap should appear.
	shown := 0
	for i := 0; i < 20; i++ {
		if strings.Contains(view, fmt.Sprintf("chat %d", i)) {
			shown++
		}
	}
	if shown < 10 {
		t.Fatalf("sessions view only shows %d entries at 24-row terminal:\n%s", shown, view)
	}
}

func sessionRows(n int) []session.Session {
	saved := make([]session.Session, n)
	for i := range saved {
		saved[i] = session.Session{
			ID: fmt.Sprintf("s%d", i), Summary: fmt.Sprintf("chat %d", i), UpdatedAt: time.Now(),
			Turns: []brain.Turn{{Role: "user", Content: "hi"}},
		}
	}
	return saved
}

// fakeSessionStore records what the sessions screen asks of the store, so no
// test reaches the user's real sessions directory.
type fakeSessionStore struct {
	saved     map[string]*session.Session
	loaded    []string
	deleted   []string
	deleteErr error
}

func (f *fakeSessionStore) load(id string) (*session.Session, error) {
	f.loaded = append(f.loaded, id)
	if s, ok := f.saved[id]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("read session %s: no such file", id)
}

func (f *fakeSessionStore) delete(id string) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

func sessionsWithFake(saved []session.Session) (sessionsModel, *fakeSessionStore) {
	store := &fakeSessionStore{saved: map[string]*session.Session{}}
	for i := range saved {
		s := saved[i]
		store.saved[s.ID] = &s
	}
	m := newSessions(saved)
	m.loadSession, m.deleteSession = store.load, store.delete
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m, store
}

func pressSessions(t *testing.T, m sessionsModel, keys ...tea.KeyMsg) (sessionsModel, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		m, cmd = m.Update(k)
	}
	return m, cmd
}

var (
	sessionsEnter = tea.KeyMsg{Type: tea.KeyEnter}
	sessionsEsc   = tea.KeyMsg{Type: tea.KeyEsc}
	sessionsDown  = tea.KeyMsg{Type: tea.KeyDown}
)

func TestSessionsViewShowsTranscriptWithoutStartingAnything(t *testing.T) {
	saved := sessionRows(3)
	saved[1].Provider, saved[1].Model = "ollama", "qwen3"
	saved[1].Turns = []brain.Turn{
		{Role: "user", Content: "hello there"},
		{Role: "tool", Content: "tool output must not show"},
		{Role: "assistant", Content: "general kenobi", PersonaDisplayName: "Samantha"},
	}
	saved[1].Turns = append(saved[1].Turns, brain.Turn{Role: "user", Content: "from the desk", Speaker: "speaker-1"})
	m, store := sessionsWithFake(saved)
	m.userName = "Ada"

	m, cmd := pressSessions(t, m, sessionsDown, runes("v"))
	if cmd != nil {
		t.Fatalf("v returned a command (%T); viewing must not start anything", cmd())
	}
	if len(store.loaded) != 1 || store.loaded[0] != "s1" {
		t.Fatalf("loaded %v, want [s1]", store.loaded)
	}
	view := stripANSI(m.View())
	for _, want := range []string{"chat 1", "4 turns", "ollama / qwen3", "hello there", "Samantha", "general kenobi", "› Ada", "from the desk", "enter open", "esc back"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "tool output") {
		t.Fatalf("view shows a tool turn the conversation would drop:\n%s", view)
	}

	m, cmd = pressSessions(t, m, sessionsEsc)
	if cmd != nil || m.viewing != nil {
		t.Fatalf("esc in view: cmd=%v viewing=%v, want back on the list", cmd != nil, m.viewing != nil)
	}
	if m.cursor != 1 || !strings.Contains(stripANSI(m.View()), "v view") {
		t.Fatalf("esc did not return to the list at the same row: cursor=%d\n%s", m.cursor, stripANSI(m.View()))
	}

	m, _ = pressSessions(t, m, runes("v"))
	_, cmd = pressSessions(t, m, sessionsEnter)
	if cmd == nil {
		t.Fatal("enter in view returned no command")
	}
	if got, ok := cmd().(startPipelineMsg); !ok || got.sessionID != "s1" {
		t.Fatalf("enter in view = %#v, want startPipelineMsg for s1", got)
	}
}

func TestSessionsViewReadsTheStoreNotTheListSnapshot(t *testing.T) {
	saved := sessionRows(1)
	m, store := sessionsWithFake(saved)
	fresh := saved[0]
	fresh.Turns = append(fresh.Turns, brain.Turn{Role: "assistant", Content: "written after the list was read"})
	store.saved["s0"] = &fresh
	m, _ = pressSessions(t, m, runes("v"))
	if view := stripANSI(m.View()); !strings.Contains(view, "written after the list was read") {
		t.Fatalf("view shows the stale snapshot:\n%s", view)
	}
}

func TestSessionsViewLoadFailureStaysOnList(t *testing.T) {
	m, store := sessionsWithFake(sessionRows(2))
	delete(store.saved, "s0")
	m, _ = pressSessions(t, m, runes("v"))
	if m.viewing != nil {
		t.Fatal("failed load opened the view")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Could not view") || !strings.Contains(view, "chat 0") {
		t.Fatalf("load failure not shown on the list:\n%s", view)
	}
	m, _ = pressSessions(t, m, sessionsDown)
	if strings.Contains(stripANSI(m.View()), "Could not view") {
		t.Fatal("error survived the next key")
	}
}

func TestSessionsDeleteNeedsConfirmation(t *testing.T) {
	for name, cancel := range map[string]tea.KeyMsg{
		"n": runes("n"), "esc": sessionsEsc, "enter": sessionsEnter, "d": runes("d"),
	} {
		t.Run(name, func(t *testing.T) {
			m, store := sessionsWithFake(sessionRows(2))
			m, cmd := pressSessions(t, m, runes("d"))
			if cmd != nil {
				t.Fatal("d alone returned a command")
			}
			view := stripANSI(m.View())
			if !strings.Contains(view, `Delete "chat 0"? y delete`) || !strings.Contains(view, "chat 1") {
				t.Fatalf("confirm prompt missing or list hidden:\n%s", view)
			}
			m, cmd = pressSessions(t, m, cancel)
			if cmd != nil {
				t.Fatalf("cancel key returned a command (%T)", cmd())
			}
			if len(store.deleted) != 0 || len(m.sessions) != 2 || m.confirmID != "" {
				t.Fatalf("cancel deleted=%v rows=%d armed=%q", store.deleted, len(m.sessions), m.confirmID)
			}
		})
	}
}

func TestSessionsDeleteLastRowClampsCursor(t *testing.T) {
	m, store := sessionsWithFake(sessionRows(3))
	m, _ = pressSessions(t, m, runes("G"), runes("d"))
	m, cmd := pressSessions(t, m, runes("y"))
	if cmd == nil {
		t.Fatal("y returned no delete command")
	}
	if len(store.deleted) != 0 {
		t.Fatal("delete ran on the update loop instead of in the command")
	}
	// Keys wait for the result: resuming or leaving mid-delete would act on a
	// session that is being removed.
	m, blocked := pressSessions(t, m, sessionsEnter)
	if blocked != nil {
		t.Fatal("enter during a delete returned a command")
	}
	if !strings.Contains(stripANSI(m.View()), "Deleting") {
		t.Fatalf("pending delete not shown:\n%s", stripANSI(m.View()))
	}
	m, _ = m.Update(cmd())
	if len(store.deleted) != 1 || store.deleted[0] != "s2" {
		t.Fatalf("deleted %v, want [s2]", store.deleted)
	}
	if len(m.sessions) != 2 || m.cursor != 1 || m.offset != 0 {
		t.Fatalf("after delete rows=%d cursor=%d offset=%d, want 2/1/0", len(m.sessions), m.cursor, m.offset)
	}
	view := stripANSI(m.View())
	if strings.Contains(view, "▸ chat 2") || !strings.Contains(view, "▸ chat 1") || !strings.Contains(view, `Deleted "chat 2"`) {
		t.Fatalf("list after delete:\n%s", view)
	}
}

func TestSessionsDeleteScrolledListClampsOffset(t *testing.T) {
	m, store := sessionsWithFake(sessionRows(30))
	m, _ = pressSessions(t, m, runes("G"))
	if m.offset == 0 {
		t.Fatal("test needs a scrolled list")
	}
	m, cmd := pressSessions(t, m, runes("d"), runes("y"))
	m, _ = m.Update(cmd())
	if store.deleted[0] != "s29" {
		t.Fatalf("deleted %v", store.deleted)
	}
	if want := len(m.sessions) - m.visibleRows(); m.cursor != 28 || m.offset != want {
		t.Fatalf("cursor=%d offset=%d, want 28/%d", m.cursor, m.offset, want)
	}
}

func TestSessionsDeleteOnlyRowShowsEmptyState(t *testing.T) {
	m, _ := sessionsWithFake(sessionRows(1))
	m, cmd := pressSessions(t, m, runes("d"), runes("y"))
	m, _ = m.Update(cmd())
	if len(m.sessions) != 0 || m.cursor != 0 || m.offset != 0 {
		t.Fatalf("rows=%d cursor=%d offset=%d", len(m.sessions), m.cursor, m.offset)
	}
	if !strings.Contains(stripANSI(m.View()), "No saved sessions.") {
		t.Fatalf("empty state missing:\n%s", stripANSI(m.View()))
	}
	// Nothing left to act on.
	for _, k := range []tea.KeyMsg{runes("d"), runes("v"), sessionsEnter} {
		if m, cmd = m.Update(k); cmd != nil || m.confirmID != "" || m.viewing != nil {
			t.Fatalf("%q on an empty list acted", k.String())
		}
	}
}

func TestSessionsDeleteFailureKeepsRowForRetry(t *testing.T) {
	m, store := sessionsWithFake(sessionRows(2))
	store.deleteErr = errors.New("remove audio: permission denied")
	m, cmd := pressSessions(t, m, runes("d"), runes("y"))
	m, _ = m.Update(cmd())
	if len(m.sessions) != 2 {
		t.Fatalf("failed delete dropped the row: %d rows", len(m.sessions))
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Could not delete: remove audio: permission denied") {
		t.Fatalf("error not shown:\n%s", view)
	}
	store.deleteErr = nil
	m, cmd = pressSessions(t, m, runes("d"), runes("y"))
	m, _ = m.Update(cmd())
	if len(store.deleted) != 2 || len(m.sessions) != 1 || strings.Contains(stripANSI(m.View()), "Could not delete") {
		t.Fatalf("retry: deleted=%v rows=%d\n%s", store.deleted, len(m.sessions), stripANSI(m.View()))
	}
}

func TestSessionsDeleteThroughStore(t *testing.T) {
	store := session.NewStore(t.TempDir())
	s := session.New("ollama", "qwen3")
	turns := []brain.Turn{{Role: "user", Content: "keep or delete"}}
	if err := store.Save(s, turns); err != nil {
		t.Fatal(err)
	}
	saved := store.List()
	m := newSessions(append(saved, sessionRows(1)...))
	m.loadSession, m.deleteSession = store.Load, store.Delete

	m, _ = pressSessions(t, m, runes("v"))
	if !strings.Contains(stripANSI(m.View()), "keep or delete") {
		t.Fatalf("view from store:\n%s", stripANSI(m.View()))
	}
	m, cmd := pressSessions(t, m, runes("d"), runes("y"))
	m, _ = m.Update(cmd())
	if m.viewing != nil || len(m.sessions) != 1 || len(store.List()) != 0 {
		t.Fatalf("view delete: viewing=%v rows=%d on disk=%d", m.viewing != nil, len(m.sessions), len(store.List()))
	}

	// s0 was never on disk: already gone counts as deleted.
	m, cmd = pressSessions(t, m, runes("d"), runes("y"))
	msg := cmd().(sessionDeletedMsg)
	if !errors.Is(msg.err, session.ErrSessionNotFound) {
		t.Fatalf("delete of a missing session = %v, want ErrSessionNotFound", msg.err)
	}
	m, _ = m.Update(msg)
	if len(m.sessions) != 0 || strings.Contains(stripANSI(m.View()), "Could not delete") {
		t.Fatalf("not-found kept the row:\n%s", stripANSI(m.View()))
	}
}

func TestSessionsFitNarrowAndShortTerminals(t *testing.T) {
	saved := sessionRows(4)
	saved[0].Summary = strings.Repeat("a long summary that will not fit ", 4)
	saved[0].Provider, saved[0].Model = "ollama", "a-model-with-a-long-name"
	saved[0].Turns = []brain.Turn{
		{Role: "user", Content: strings.Repeat("unbroken", 20)},
		{Role: "assistant", Content: strings.Repeat("a reply that wraps ", 10)},
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 24, Height: 8}, {Width: 30, Height: 24}, {Width: 12, Height: 5}} {
		m, _ := sessionsWithFake(saved)
		m, _ = m.Update(size)
		states := map[string]sessionsModel{"list": m}
		confirm, _ := pressSessions(t, m, runes("d"))
		states["confirm"] = confirm
		viewing, _ := pressSessions(t, m, runes("v"))
		states["view"] = viewing
		viewConfirm, _ := pressSessions(t, viewing, runes("d"))
		states["view confirm"] = viewConfirm
		failed := m
		failed.message, failed.messageErr = strings.Repeat("an error ", 10), true
		states["message"] = failed
		for name, state := range states {
			lines := strings.Split(state.View(), "\n")
			if len(lines) > size.Height {
				t.Fatalf("%dx%d %s: %d lines:\n%s", size.Width, size.Height, name, len(lines), stripANSI(state.View()))
			}
			for _, line := range lines {
				if w := ansi.StringWidth(line); w > size.Width {
					t.Fatalf("%dx%d %s: line is %d wide: %q", size.Width, size.Height, name, w, stripANSI(line))
				}
			}
		}
	}
}

func TestSessionsViewRewrapsOnResize(t *testing.T) {
	saved := sessionRows(1)
	saved[0].Turns = []brain.Turn{{Role: "assistant", Content: strings.Repeat("word ", 60)}}
	m, _ := sessionsWithFake(saved)
	m, _ = pressSessions(t, m, runes("v"))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	for _, line := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Fatalf("line %d wide after resize: %q", w, stripANSI(line))
		}
	}
	if m.viewport.Height != m.visibleRows() || m.viewport.Width != 40 {
		t.Fatalf("viewport %dx%d after resize", m.viewport.Width, m.viewport.Height)
	}
}

func TestSessionsDeleteRefreshesLauncher(t *testing.T) {
	saved := sessionRows(2)
	app := newApp(&config.Config{}, nil, saved)
	if id := launcherContinueID(app.launcher); id != "s0" {
		t.Fatalf("launcher Continue = %q before delete", id)
	}
	model, _ := app.Update(switchScreenMsg(screenSessions))
	app = model.(App)
	store := &fakeSessionStore{}
	app.sessions.deleteSession = store.delete

	for _, k := range []tea.KeyMsg{runes("d"), runes("y")} {
		var cmd tea.Cmd
		model, cmd = app.Update(k)
		app = model.(App)
		if k.String() == "y" {
			model, _ = app.Update(cmd())
			app = model.(App)
		}
	}
	if len(store.deleted) != 1 || store.deleted[0] != "s0" {
		t.Fatalf("deleted %v", store.deleted)
	}
	if id := launcherContinueID(app.launcher); id != "s1" {
		t.Fatalf("launcher Continue = %q after deleting s0, want s1", id)
	}

	// A result that lands after the user left the screen still reaches the
	// launcher.
	model, _ = app.Update(switchScreenMsg(screenLauncher))
	app = model.(App)
	model, _ = app.Update(sessionDeletedMsg{id: "s1"})
	app = model.(App)
	if len(app.sessions.sessions) != 0 {
		t.Fatalf("sessions after late result: %d", len(app.sessions.sessions))
	}
	for _, item := range app.launcher.items {
		if item.action == actionContinue || item.action == actionSessions {
			t.Fatalf("launcher still offers %q with no sessions left", item.label)
		}
	}
	if app.launcher.cursor < 0 || app.launcher.cursor >= len(app.launcher.items) {
		t.Fatalf("launcher cursor %d out of range", app.launcher.cursor)
	}
}

func TestSessionsScreenEntryStartsOnList(t *testing.T) {
	app := newApp(&config.Config{}, nil, sessionRows(2))
	model, _ := app.Update(switchScreenMsg(screenSessions))
	app = model.(App)
	store := &fakeSessionStore{saved: map[string]*session.Session{"s0": &app.sessions.sessions[0]}}
	app.sessions.loadSession = store.load
	model, _ = app.Update(runes("v"))
	model, _ = model.(App).Update(runes("d"))
	app = model.(App)
	if app.sessions.viewing == nil || app.sessions.confirmID == "" {
		t.Fatal("test setup: view with an armed delete expected")
	}
	app.screen = screenLauncher
	model, _ = app.Update(switchScreenMsg(screenSessions))
	app = model.(App)
	if app.sessions.viewing != nil || app.sessions.confirmID != "" {
		t.Fatalf("re-entry kept view=%v confirm=%q", app.sessions.viewing != nil, app.sessions.confirmID)
	}
}

func launcherContinueID(m launcherModel) string {
	for _, item := range m.items {
		if item.action == actionContinue {
			return item.sessionID
		}
	}
	return ""
}
