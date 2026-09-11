package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestStaleTurnSavePreservesSelectionWithoutWork(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "fixture")
	turns := []brain.Turn{{Role: "user", Content: "remember cobalt"}}
	if err := store.Save(s, turns); err != nil {
		t.Fatal(err)
	}
	stale := *s
	if err := store.SaveSelection(s, turns, "santa", "Santa", "p2"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&stale, append(turns, brain.Turn{Role: "assistant", Content: "cobalt", PersonaID: "santa"})); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedPersonaID != "santa" || stale.SelectedPersonaID != "santa" || len(loaded.Turns) != 2 {
		t.Fatalf("stale save regressed selection: %+v", loaded)
	}
}

func TestFailedSelectionAndTurnSaveKeepRecoverableState(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	s := New("ollama", "fixture")
	old := []brain.Turn{{Role: "user", Content: "first"}}
	if err := store.SaveSelection(s, old, "samantha", "Samantha", "p1"); err != nil {
		t.Fatal(err)
	}
	before := *s
	disk, err := os.ReadFile(filepath.Join(dir, s.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	unsaved := append(append([]brain.Turn(nil), old...), brain.Turn{Role: "assistant", Content: "recover me", PersonaID: "santa", ResponseID: "r2"})
	// A normal readable but unwritable session directory makes temp creation
	// fail before replacement, preserving the previous actual disk snapshot.
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Errorf("restore directory permissions: %v", err)
		}
	})
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	if err := store.SaveSelection(s, unsaved, "santa", "Santa", "p2"); err == nil {
		t.Fatal("selection save unexpectedly succeeded")
	}
	if err := store.Save(s, unsaved); err == nil {
		t.Fatal("turn save unexpectedly succeeded")
	}
	got, err := os.ReadFile(filepath.Join(dir, s.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, disk) || !reflect.DeepEqual(*s, before) || unsaved[1].Content != "recover me" {
		t.Fatal("failed write mutated committed state or consumed unsaved turns")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSelection(s, unsaved, "santa", "Santa", "p2"); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedPersonaID != "santa" || len(loaded.Turns) != 2 || loaded.Turns[1].ResponseID != "r2" {
		t.Fatalf("retry failed: %+v", loaded)
	}
}

func TestLegacyUpgradeAndFutureWriteRejection(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := os.WriteFile(filepath.Join(dir, "old.json"), []byte(`{"id":"old","turns":[{"role":"samantha","content":"legacy"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Load("old")
	if err != nil {
		t.Fatal(err)
	}
	if s.Turns[0].PersonaID != "" || s.Turns[0].PersonaDisplayName != "" {
		t.Fatal("legacy persona fabricated")
	}
	if err := store.Save(s, s.Turns); err != nil {
		t.Fatal(err)
	}
	if s.FormatVersion != CurrentFormatVersion || s.Turns[0].Role != "assistant" || s.Turns[0].PersonaID != "" {
		t.Fatalf("bad migration: %+v", s)
	}
	future := New("ollama", "fixture")
	future.FormatVersion = CurrentFormatVersion + 1
	if err := store.Save(future, nil); !errors.Is(err, ErrUnsupportedSessionVersion) {
		t.Fatalf("future save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, future.ID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("future file written: %v", err)
	}
}

func TestSaveDetachesNestedToolArguments(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "fixture")
	turns := []brain.Turn{{Role: "assistant", ToolCalls: []brain.TurnToolCall{{ID: "call", Name: "lookup", Arguments: `{"key":"original"}`}}}}
	if err := store.Save(s, turns); err != nil {
		t.Fatal(err)
	}
	turns[0].ToolCalls[0].Arguments = `{"key":"mutated"}`
	if s.Turns[0].ToolCalls[0].Arguments != `{"key":"original"}` {
		t.Fatal("session retained caller alias")
	}
}
