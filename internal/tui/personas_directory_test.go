package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/persona/campdirs"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// directoryFormModel opens the edit form on one persona with a stubbed camp
// scan that reports camps, and lands the scan result so the row has choices.
func directoryFormModel(t *testing.T, p *persona.Profile, camps []meeting.Campaign, notice string) personasModel {
	t.Helper()
	m := newPickerPersonas(t, &config.Config{ActivePersona: p.ID, TTSProvider: "kokoro"}, p)
	m.directory.discover = func(context.Context) campdirs.Choices {
		return campdirs.Choices{Campaigns: camps, Notice: notice}
	}
	m.cursor = 0
	cmd := m.beginEditAt(formRowName)
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) < 3 {
		t.Fatalf("opening the form did not batch the camp scan (%T)", cmd())
	}
	msg, ok := batch[1]().(personaDirectoriesMsg)
	if !ok {
		t.Fatalf("camp scan returned %T", batch[1]())
	}
	m, _ = m.Update(msg)
	m.setFormRow(formRowDirectory)
	return m
}

func campDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPersonaFormShowsDirectoryRowWithCamps(t *testing.T) {
	root := campDir(t)
	m := directoryFormModel(t, &persona.Profile{ID: "ada", DisplayName: "Ada"}, []meeting.Campaign{{Name: "My_Tools", Path: root}}, "")
	view := stripANSI(m.View())
	for _, want := range []string{"Starting directory", "Directory", "(default)", "app default"} {
		if !strings.Contains(view, want) {
			t.Fatalf("edit form missing %q:\n%s", want, view)
		}
	}
	// (default) → camp → (custom) → wraps to (default).
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.directoryValue() != root || m.directory.custom {
		t.Fatalf("→ did not pick the camp: value=%q custom=%v", m.directoryValue(), m.directory.custom)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "My_Tools") || !strings.Contains(view, filepath.Base(root)) {
		t.Fatalf("camp choice must show its name and path:\n%s", view)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if !m.directory.custom || !m.directoryInput.Focused() {
		t.Fatal("→ past the camps must land on (custom) with the field focused")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.directory.custom || m.directoryValue() != "" {
		t.Fatalf("wrap to (default) left value=%q custom=%v", m.directoryValue(), m.directory.custom)
	}
}

func TestPersonaFormDirectoryCustomPathSavesNormalized(t *testing.T) {
	root := campDir(t)
	m := directoryFormModel(t, &persona.Profile{ID: "ada", DisplayName: "Ada"}, nil, "")
	var saved []string
	m.saveDirectory = func(id, dir string) (*persona.Profile, error) {
		saved = append(saved, id+"="+dir)
		return &persona.Profile{ID: id, StartingDirectory: dir}, nil
	}
	m.saveName = func(id, name string) (*persona.Profile, error) { return &persona.Profile{ID: id}, nil }
	m.savePrompt = func(id, p string) (*persona.Profile, error) { return &persona.Profile{ID: id}, nil }
	m.saveStack = func(id string, b persona.Brain, tt persona.TTS) (*persona.Profile, error) {
		return &persona.Profile{ID: id}, nil
	}

	// No camps: ← from (default) lands on (custom) and typing fills the path.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if !m.directory.custom {
		t.Fatal("← from (default) with no camps must reach (custom)")
	}
	// A relative-looking path with a trailing slash still normalizes to the
	// canonical absolute folder.
	for _, r := range root + string(filepath.Separator) {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.formMode != "" {
		t.Fatalf("save did not close the form: %s", m.message)
	}
	if len(saved) != 1 || saved[0] != "ada="+root {
		t.Fatalf("saved = %v, want ada=%s", saved, root)
	}
}

func TestPersonaFormDirectoryInvalidPathBlocksEveryWrite(t *testing.T) {
	m := directoryFormModel(t, &persona.Profile{ID: "ada", DisplayName: "Ada"}, nil, "")
	writes := 0
	m.saveDirectory = func(id, dir string) (*persona.Profile, error) { writes++; return nil, nil }
	m.saveName = func(id, name string) (*persona.Profile, error) { writes++; return nil, nil }
	m.savePrompt = func(id, p string) (*persona.Profile, error) { writes++; return nil, nil }
	m.saveStack = func(id string, b persona.Brain, tt persona.TTS) (*persona.Profile, error) { writes++; return nil, nil }

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	for _, r := range filepath.Join(t.TempDir(), "missing") {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.formMode != "edit" || m.formRow != formRowDirectory {
		t.Fatalf("a bad folder must keep the form open on the directory row (mode=%q row=%d)", m.formMode, m.formRow)
	}
	if writes != 0 {
		t.Fatalf("%d writes happened before the folder was accepted", writes)
	}
	if !strings.Contains(m.message, "Ada") || !strings.Contains(m.message, "missing") {
		t.Fatalf("error must name the persona and the path: %q", m.message)
	}
}

func TestPersonaFormDirectoryMissingSavedFolderStillEdits(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	gone, _ = filepath.EvalSymlinks(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	m := directoryFormModel(t, &persona.Profile{ID: "ada", DisplayName: "Ada", StartingDirectory: gone}, nil, "")
	if m.directoryValue() != gone {
		t.Fatalf("saved folder not loaded as-is: %q", m.directoryValue())
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "folder missing") {
		t.Fatalf("row must say the saved folder is gone:\n%s", view)
	}
	dirSaves := 0
	m.saveDirectory = func(id, dir string) (*persona.Profile, error) { dirSaves++; return nil, nil }
	nameSaved := ""
	m.saveName = func(id, name string) (*persona.Profile, error) {
		nameSaved = name
		return &persona.Profile{ID: id}, nil
	}
	m.savePrompt = func(id, p string) (*persona.Profile, error) { return &persona.Profile{ID: id}, nil }
	m.saveStack = func(id string, b persona.Brain, tt persona.TTS) (*persona.Profile, error) {
		return &persona.Profile{ID: id}, nil
	}

	m.setFormRow(formRowName)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.formMode != "" || nameSaved != "Ada!" || dirSaves != 0 {
		t.Fatalf("renaming must not be blocked by an untouched missing folder (mode=%q name=%q dirSaves=%d msg=%s)", m.formMode, nameSaved, dirSaves, m.message)
	}
}

func TestPersonaFormDirectoryClearsToDefault(t *testing.T) {
	root := campDir(t)
	m := directoryFormModel(t, &persona.Profile{ID: "ada", DisplayName: "Ada", StartingDirectory: root}, []meeting.Campaign{{Name: "My_Tools", Path: root}}, "")
	if m.directoryIndex() != 1 {
		t.Fatalf("saved camp path must select its camp, got index %d", m.directoryIndex())
	}
	var saved *string
	m.saveDirectory = func(id, dir string) (*persona.Profile, error) { saved = &dir; return &persona.Profile{ID: id}, nil }
	m.saveName = func(id, name string) (*persona.Profile, error) { return &persona.Profile{ID: id}, nil }
	m.savePrompt = func(id, p string) (*persona.Profile, error) { return &persona.Profile{ID: id}, nil }
	m.saveStack = func(id string, b persona.Brain, tt persona.TTS) (*persona.Profile, error) {
		return &persona.Profile{ID: id}, nil
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if saved == nil || *saved != "" {
		t.Fatalf("picking (default) must save an empty directory, got %v", saved)
	}
}

func TestPersonaFormDirectoryNoticeKeepsCustomUsable(t *testing.T) {
	m := directoryFormModel(t, &persona.Profile{ID: "ada", DisplayName: "Ada"}, nil, "Cannot load registered camps: timeout")
	view := stripANSI(m.View())
	if !strings.Contains(view, "camp list unavailable") {
		t.Fatalf("discovery failure must be visible on the row:\n%s", view)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if !m.directory.custom {
		t.Fatal("(custom) must stay reachable when discovery fails")
	}
}

func TestPersonaFormCreatePassesDirectory(t *testing.T) {
	root := campDir(t)
	m := newPickerPersonas(t, &config.Config{TTSProvider: "kokoro"}, nil)
	m.directory.discover = func(context.Context) campdirs.Choices {
		return campdirs.Choices{Campaigns: []meeting.Campaign{{Name: "My_Tools", Path: root}}}
	}
	var got persona.CreateOpts
	m.createPersona = func(_ *config.Config, opts persona.CreateOpts) (*persona.Profile, error) {
		got = opts
		return &persona.Profile{ID: "buddy", DisplayName: opts.DisplayName}, nil
	}
	cmd := m.beginCreate()
	batch := cmd().(tea.BatchMsg)
	m, _ = m.Update(batch[1]())
	for _, r := range "Buddy" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.setFormRow(formRowDirectory)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if got.StartingDirectory != root || got.DisplayName != "Buddy" {
		t.Fatalf("create opts = %+v", got)
	}
}

func TestPersonaListDetailShowsDirectory(t *testing.T) {
	root := campDir(t)
	m := newPickerPersonas(t, &config.Config{ActivePersona: "ada"}, &persona.Profile{ID: "ada", DisplayName: "Ada", StartingDirectory: root})
	if view := stripANSI(m.View()); !strings.Contains(view, "Directory     "+root[:20]) {
		t.Fatalf("detail pane missing the configured directory:\n%s", view)
	}
	m = newPickerPersonas(t, &config.Config{ActivePersona: "ada"}, &persona.Profile{ID: "ada", DisplayName: "Ada"})
	if view := stripANSI(m.View()); !strings.Contains(view, "(app default)") {
		t.Fatalf("detail pane must say a persona inherits the app default:\n%s", view)
	}
}
