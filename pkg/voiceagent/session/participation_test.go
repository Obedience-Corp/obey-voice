package session

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"testing"
)

func TestParticipationSettingsPersistWithoutReplacingConversation(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "test")
	turns := []brain.Turn{{Role: "user", Content: "Original request"}}
	if err := store.Save(s, turns); err != nil {
		t.Fatal(err)
	}
	settings := discussion.Settings{Version: 1, Mode: "open", Members: []string{"jarvis", "samantha"}}
	if err := store.SaveParticipation(s, settings); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Participation == nil || saved.Participation.Mode != "open" || len(saved.Turns) != 1 || saved.Turns[0].Content != turns[0].Content {
		t.Fatalf("saved=%+v", saved)
	}
	settings.Members[0] = "changed"
	if saved.Participation.Members[0] != "jarvis" {
		t.Fatal("stored settings share caller memory")
	}
	invalid := settings
	invalid.Version = 99
	if err := store.SaveParticipation(s, invalid); err == nil {
		t.Fatal("future settings overwritten")
	}
	saved, err = store.Load(s.ID)
	if err != nil || saved.Participation.Version != 1 {
		t.Fatalf("prior settings lost: %v", err)
	}
}
