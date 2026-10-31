package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestPersonaAudioDraftEditingPreservesStandardAndGlobalChoices(t *testing.T) {
	dir := personaEnv(t)
	global, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runPersona(t, "edit", "samantha", "--audio-mode", "native", "--audio-controls", "conversation", "--audio-transcript", "off", "--brain-model", "saved-standard", "--json"); err != nil {
		t.Fatal(err)
	}
	p, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if p.Audio == nil || p.Audio.Mode != persona.AudioNative || p.Audio.Transcript != persona.AudioTranscriptOff || p.Brain.Model != "saved-standard" || p.TTS.Provider != "kokoro" || p.TTS.Voice != "af_heart" {
		t.Fatalf("lost draft or standard choices: %+v", p)
	}
	if _, err := runPersona(t, "edit", "samantha", "--audio-mode", "standard", "--json"); err != nil {
		t.Fatal(err)
	}
	p, err = persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if p.Audio.Controls != persona.AudioControlsConversation || p.Audio.Transcript != persona.AudioTranscriptOff {
		t.Fatal("switch discarded inactive native choices")
	}
	after, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(global, after) {
		t.Fatal("draft edits changed global settings")
	}
}

func TestPersonaAudioInvalidDraftWritesNothing(t *testing.T) {
	dir := personaEnv(t)
	before, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runPersona(t, "edit", "samantha", "--display-name", "Changed", "--audio-endpoint", "http://user:secret@localhost:11434", "--json"); err == nil {
		t.Fatal("accepted credentialed endpoint")
	}
	after, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if after.DisplayName != before.DisplayName || after.Audio != nil {
		t.Fatal("invalid draft changed profile")
	}
	if _, err := runPersona(t, "create", "Native draft", "--audio-mode", "native", "--json"); err == nil {
		t.Fatal("implicitly activated native draft")
	}
	if _, err := os.Stat(filepath.Join(dir, "personas", "native-draft")); !os.IsNotExist(err) {
		t.Fatal("failed activation left a profile", err)
	}
	if _, err := runPersona(t, "create", "Native draft", "--audio-mode", "native", "--no-activate", "--json"); err != nil {
		t.Fatal(err)
	}
	p, err := persona.Load("native-draft")
	if err != nil || p.Audio == nil || p.Audio.Mode != persona.AudioNative {
		t.Fatal("missing native draft", err)
	}
}

func TestPersonaOrdinaryEditKeepsAudioOmitted(t *testing.T) {
	personaEnv(t)
	if _, err := runPersona(t, "edit", "samantha", "--display-name", "Renamed", "--json"); err != nil {
		t.Fatal(err)
	}
	p, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if p.Audio != nil {
		t.Fatal("ordinary profile acquired audio settings")
	}
}

func TestNativeDefaultDraftKeepsRealCommandPreRunRepairAvailable(t *testing.T) {
	personaEnv(t)
	if err := config.SetAndSave("active_persona", "samantha"); err != nil {
		t.Fatal(err)
	}
	if _, err := runPersona(t, "edit", "samantha", "--audio-mode", "native", "--json"); err != nil {
		t.Fatal(err)
	}
	tree := newPersonaCmd()
	edit, _, err := tree.Find([]string{"edit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rootCmd.PersistentPreRunE(edit, nil); err != nil {
		t.Fatal("saved native draft blocked repair pre-run", err)
	}
	if _, err := runPersona(t, "edit", "samantha", "--audio-mode", "standard", "--json"); err != nil {
		t.Fatal(err)
	}
	p, err := persona.Load("samantha")
	if err != nil || p.Audio.Mode != persona.AudioStandard {
		t.Fatal("could not repair default", err)
	}
}
