package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func nativeSelectionStateFixture() events.PersonaSelection {
	return events.PersonaSelection{PersonaID: "native", DisplayName: "Native", PromptHash: "prompt", BindingHash: strings.Repeat("a", 64), Revision: 7, AudioMode: "native", AudioControls: "conversation", AudioTranscript: "off", AudioRetained: true, BrainProvider: "ollama", BrainModel: "selected-audio", VoiceProvider: config.OpenAISpeechProviderName, Voice: "female"}
}
func TestSelectionStatePersistsNativeModeWithoutRecordingAndFencesOldReaders(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "standard-model")
	selected := nativeSelectionStateFixture()
	if err := store.SaveSelectionState(s, nil, selected); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FormatVersion != 3 || loaded.Selection == nil || loaded.Selection.AudioMode != "native" || loaded.Selection.Revision != 7 || loaded.Selection.BrainModel != "selected-audio" || loaded.Selection.BindingHash != selected.BindingHash {
		t.Fatalf("native selection lost: %+v", loaded)
	}
	loaded.FormatVersion = 2
	data, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, s.ID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(s.ID); err == nil {
		t.Fatal("native-mode evidence accepted in old session format")
	}
}
func TestOrdinaryStandardSelectionStaysVersionTwoAndStaleSavePreservesNativeSelection(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "standard-model")
	standard := events.PersonaSelection{PersonaID: "standard", AudioMode: "standard", BindingHash: strings.Repeat("b", 64)}
	if err := store.SaveSelectionState(s, nil, standard); err != nil {
		t.Fatal(err)
	}
	if s.FormatVersion != 2 {
		t.Fatal("ordinary Standard session bumped format")
	}
	stale := *s
	selected := nativeSelectionStateFixture()
	if err := store.SaveSelectionState(s, nil, selected); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&stale, []brain.Turn{{Role: "user", Content: "late autosave"}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Selection.AudioMode != "native" || loaded.Selection.Revision != selected.Revision || loaded.Selection.BindingHash != selected.BindingHash {
		t.Fatal("stale turn save overwrote mode/revision")
	}
}
func TestSelectionStateHonorsRenameCommitBoundary(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before rename", true: "after rename"}[committed], func(t *testing.T) {
			store := NewStore(t.TempDir())
			s := New("ollama", "standard-model")
			if err := store.SaveSelectionState(s, nil, events.PersonaSelection{PersonaID: "standard", AudioMode: "standard"}); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("fixture durability failure")
			if committed {
				store.injectWriteFailures(nil, failure)
			} else {
				store.injectWriteFailures(failure, nil)
			}
			err := store.SaveSelectionState(s, nil, nativeSelectionStateFixture())
			if err == nil || WriteCommitted(err) != committed {
				t.Fatal(err)
			}
			loaded, err := store.Load(s.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "standard"
			if committed {
				want = "native"
			}
			if loaded.Selection.AudioMode != want || s.Selection.AudioMode != want {
				t.Fatal("mode publication disagrees with durable state")
			}
		})
	}
}
