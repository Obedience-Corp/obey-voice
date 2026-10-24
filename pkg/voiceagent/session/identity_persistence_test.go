package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestStaleTurnSavePreservesSelectionWithoutWork(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "fixture")
	turns := []brain.Turn{{Role: "user", Content: "remember cobalt"}}
	if err := store.Save(s, turns); err != nil {
		t.Fatal(err)
	}
	stale := *s
	directories := &config.ConversationDirectories{Default: config.ConversationDirectory{Dir: t.TempDir(), Source: "launch-dir"}, Personas: map[string]config.ConversationDirectory{"santa": {Dir: t.TempDir(), Source: "persona"}}}
	if err := store.SaveSelection(s, turns, "santa", "Santa", "p2", directories); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&stale, append(turns, brain.Turn{Role: "assistant", Content: "cobalt", PersonaID: "santa"})); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Directories, directories) || loaded.WorkDir != directories.Personas["santa"].Dir {
		t.Fatalf("stale save regressed directories: %+v", loaded)
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

func TestSelectionWriteReportsPostRenameCommit(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "fixture")
	old := []brain.Turn{{Role: "user", Content: "old"}}
	if err := store.SaveSelection(s, old, "sam", "Samantha", "p1"); err != nil {
		t.Fatal(err)
	}
	store.injectWriteFailures(nil, errors.New("directory fsync failed"))
	err := store.SaveSelection(s, []brain.Turn{{Role: "user", Content: "new"}}, "santa", "Santa", "p2")
	if !WriteCommitted(err) {
		t.Fatalf("error = %v, want committed durability error", err)
	}
	if s.SelectedPersonaID != "santa" || len(s.Turns) != 1 || s.Turns[0].Content != "new" {
		t.Fatalf("session not advanced to committed state: %+v", s)
	}
	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedPersonaID != "santa" || loaded.Turns[0].Content != "new" {
		t.Fatalf("disk did not contain committed state: %+v", loaded)
	}
	memoryJSON, _ := json.Marshal(s)
	diskJSON, _ := json.Marshal(loaded)
	if !bytes.Equal(memoryJSON, diskJSON) {
		t.Fatalf("committed memory differs from disk: memory=%+v disk=%+v", *s, *loaded)
	}
}

func TestSelectionWritePreRenameFailurePreservesState(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "fixture")
	old := []brain.Turn{{Role: "user", Content: "old"}}
	if err := store.SaveSelection(s, old, "sam", "Samantha", "p1"); err != nil {
		t.Fatal(err)
	}
	before := *s
	store.injectWriteFailures(errors.New("temp write failed"), nil)
	if err := store.SaveSelection(s, []brain.Turn{{Role: "user", Content: "new"}}, "santa", "Santa", "p2"); err == nil || WriteCommitted(err) {
		t.Fatalf("error = %v, want pre-rename failure", err)
	}
	if !reflect.DeepEqual(*s, before) {
		t.Fatalf("in-memory state changed: before=%+v after=%+v", before, *s)
	}
	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedPersonaID != "sam" || loaded.Turns[0].Content != "old" {
		t.Fatalf("disk state changed: %+v", loaded)
	}
}

func TestOrdinaryCheckpointDoesNotAcknowledgeUncertainWrite(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "fixture")
	if err := store.Save(s, []brain.Turn{{Role: "user", Content: "old"}}); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(s)
	store.injectWriteFailures(nil, errors.New("directory sync failed"))
	if err := store.Save(s, []brain.Turn{{Role: "user", Content: "new"}}); !WriteCommitted(err) {
		t.Fatalf("save = %v", err)
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("uncertain ordinary save advanced caller state")
	}
}

func TestWorkCheckpointRetryConfirmsDirectoryDurability(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "fixture")
	scope := work.WorkScope{SessionID: s.ID, RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	snapshot := work.Snapshot{Scope: scope, Revision: 1}
	store.injectWriteFailures(nil, errors.New("directory sync failed"))
	if err := store.BeginWork(s, snapshot, nil); !WriteCommitted(err) {
		t.Fatalf("begin = %v", err)
	}
	if s.Work != nil {
		t.Fatal("uncertain begin acknowledged work")
	}
	store.injectWriteFailures(nil, nil)
	if err := store.SaveWork(s, snapshot); err != nil {
		t.Fatal(err)
	}
	if s.Work == nil || s.Work.PersistedRevision != 1 {
		t.Fatal("confirmed initial checkpoint missing")
	}
	snapshot.Revision = 2
	store.injectWriteFailures(nil, errors.New("directory sync failed"))
	for range 2 {
		if err := store.SaveWork(s, snapshot); !WriteCommitted(err) {
			t.Fatalf("uncertain save/retry = %v", err)
		}
		if s.Work.PersistedRevision != 1 {
			t.Fatal("uncertain work checkpoint advanced watermark")
		}
	}
	store.injectWriteFailures(nil, nil)
	if err := store.SaveWork(s, snapshot); err != nil {
		t.Fatal(err)
	}
	if s.Work.PersistedRevision != 2 {
		t.Fatal("confirmed retry failed to acknowledge work")
	}
}

func TestSuccessfulSessionWritesPublishSerializedCandidate(t *testing.T) {
	for _, selection := range []bool{false, true} {
		store := NewStore(t.TempDir())
		s := New("ollama", "fixture")
		turns := []brain.Turn{{Role: "user", Content: "new summary"}}
		var err error
		if selection {
			err = store.SaveSelection(s, turns, "santa", "Santa", "hash")
		} else {
			err = store.Save(s, turns)
		}
		if err != nil {
			t.Fatal(err)
		}
		disk, err := store.Load(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		memoryJSON, _ := json.Marshal(s)
		diskJSON, _ := json.Marshal(disk)
		if !bytes.Equal(memoryJSON, diskJSON) || s.Summary != "new summary" || s.FormatVersion != CurrentFormatVersion {
			t.Fatalf("selection=%t memory=%s disk=%s", selection, memoryJSON, diskJSON)
		}
	}
}

func TestSessionLoadRejectsPathsOutsideStore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "outside.json"), []byte(`{"id":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(root, "sessions"))
	if _, err := store.Load("../outside"); err == nil {
		t.Fatal("load escaped the session store")
	}
}
