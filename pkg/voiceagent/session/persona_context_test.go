package session

import (
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"reflect"
	"testing"
)

func TestPersonaContextPersistenceIsAtomicAndSurvivesLegacySaves(t *testing.T) {
	store := NewStore(t.TempDir())
	sess := New("ollama", "fixture")
	turns := []brain.Turn{{Role: "user", Content: "shared request"}}
	contexts := brain.PersonaContexts{"alpha": brain.CapturePersonaContext(brain.ContinuitySnapshot{Canonical: turns, Working: []brain.Turn{{Role: "assistant", Content: "alpha private summary"}}, Archive: []continuity.Entry{{ID: "alpha-note"}}, ContextHighWater: 8192})}
	selected := events.PersonaSelection{PersonaID: "alpha", DisplayName: "Alpha", AudioMode: "standard"}
	if err := store.SaveSelectionState(sess, turns, selected, contexts); err != nil {
		t.Fatal(err)
	}
	disk, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.FormatVersion != 4 || !reflect.DeepEqual(disk.PersonaContexts, contexts) {
		t.Fatalf("context roundtrip failed: %+v", disk.PersonaContexts)
	}
	stale := *sess
	stale.PersonaContexts = nil
	if err := store.Save(&stale, turns); err != nil {
		t.Fatal(err)
	}
	disk, err = store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk.PersonaContexts, contexts) {
		t.Fatal("legacy turn save discarded durable private context")
	}
	store.failBeforeRename = errors.New("injected disk failure")
	changed := contexts.Clone()
	state := changed["alpha"]
	state.ContextHighWater = 16384
	changed["alpha"] = state
	if err := store.SaveSelectionState(sess, turns, selected, changed); err == nil {
		t.Fatal("expected failed context commit")
	}
	if !reflect.DeepEqual(sess.PersonaContexts, contexts) {
		t.Fatal("failed commit changed in-memory contexts")
	}
	disk, err = store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk.PersonaContexts, contexts) {
		t.Fatal("failed commit changed durable contexts")
	}
}
