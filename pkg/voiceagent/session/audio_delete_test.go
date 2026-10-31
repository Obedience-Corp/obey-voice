package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestDeleteRemovesOnlyOwnedRetainedAudioAndMetadata(t *testing.T) {
	store := NewStore(t.TempDir())
	memory := chatmedia.NewAudioSession()
	defer memory.Close()
	ref, err := memory.ImportSamples(t.Context(), []float32{.1, .2, .3})
	if err != nil {
		t.Fatal(err)
	}
	data, err := memory.ResolveAudio(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var saved []*Session
	var media []chatmedia.Store
	for i := 0; i < 2; i++ {
		s := New("ollama", "audio")
		owned, err := store.AudioMedia(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := owned.PersistAudio(t.Context(), ref, data); err != nil {
			t.Fatal(err)
		}
		turns := []brain.Turn{{Role: "user", Utterance: &utterance.Finalized{ID: "recording", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}, AudioRetained: true}}
		if err := store.Save(s, turns); err != nil {
			t.Fatal(err)
		}
		if err := s.saveBackupTo(store.dir, turns); err != nil {
			t.Fatal(err)
		}
		saved = append(saved, s)
		media = append(media, owned)
	}
	if err := store.Delete(saved[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{media[0].Dir, filepath.Join(store.dir, saved[0].ID+".json"), filepath.Join(store.dir, saved[0].ID+".pre-compact.json")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("deleted session data remains", path, err)
		}
	}
	if got, err := media[1].ReadAudio(t.Context(), ref); err != nil || string(got) != string(data) {
		t.Fatal("other session audio changed", err)
	}
	if _, err := store.Load(saved[1].ID); err != nil {
		t.Fatal("other session metadata changed", err)
	}
}

func TestDeleteRejectsMediaSymlinkBeforeChangingSessionMetadata(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "audio")
	if err := store.Save(s, nil); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "private.wav")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	mediaRoot := filepath.Join(store.dir, "media")
	if err := os.Mkdir(mediaRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mediaRoot, s.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(s.ID); err == nil {
		t.Fatal("symlinked media deletion accepted")
	}
	if _, err := store.Load(s.ID); err != nil {
		t.Fatal("failed deletion lost metadata", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatal("outside file affected", err)
	}
}
