package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestAudioSessionVersionAndRetentionPublication(t *testing.T) {
	store := NewStore(t.TempDir())
	saved := New("ollama", "lfm3-audio:2b")
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ctx := context.Background()
	ref, err := media.ImportSamples(ctx, []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	input := &utterance.Finalized{ID: "u1", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}
	turns := []brain.Turn{{Role: "user", Utterance: input, AudioRetained: true}}
	if err := store.Save(saved, turns); err == nil {
		t.Fatal("published missing retained asset")
	}
	disk, err := store.AudioMedia(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := media.PersistAudio(ctx, disk, ref); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(saved, turns); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.FormatVersion != 3 || reopened.Turns[0].Utterance.Audio.ID != ref.ID || !reopened.Turns[0].AudioRetained {
		t.Fatal(reopened)
	}
	// Older-format records must not silently hide the new audio-only turn.
	reopened.FormatVersion = 2
	payload, err := json.Marshal(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, saved.ID+".json"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(saved.ID); err == nil {
		t.Fatal("accepted audio under legacy format")
	}
}
func TestStandardSessionKeepsVersionTwoAndEphemeralAudioWritesNoMedia(t *testing.T) {
	store := NewStore(t.TempDir())
	standard := New("ollama", "ordinary")
	if err := store.Save(standard, []brain.Turn{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	if standard.FormatVersion != StandardFormatVersion {
		t.Fatal("Standard format changed", standard.FormatVersion)
	}
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	ephemeral := New("ollama", "lfm3-audio:2b")
	input := &utterance.Finalized{ID: "u2", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Ready, Text: "real words"}}
	if err := store.Save(ephemeral, []brain.Turn{{Role: "user", Content: "real words", Utterance: input}}); err != nil {
		t.Fatal(err)
	}
	if ephemeral.FormatVersion != 3 {
		t.Fatal("ready transcript silently downgraded canonical audio evidence")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "media", ephemeral.ID)); !os.IsNotExist(err) {
		t.Fatal("ephemeral audio wrote media", err)
	}
}
