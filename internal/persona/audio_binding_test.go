package persona

import (
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestNativeCandidateBindingIsIsolatedAndDoesNotEnableActivation(t *testing.T) {
	setConfigDir(t, t.TempDir())
	p := audioProfile()
	p.Audio = &Audio{Mode: AudioNative, Model: "selected-audio", Endpoint: "http://localhost:18144", Controls: AudioControlsConversation, Transcript: AudioTranscriptOff, Voice: "female"}
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	base := &config.Config{BrainProvider: "ollama", OllamaModel: "global-chat", OllamaHost: "http://localhost:11434", TTSProvider: "kokoro", STTProvider: "uninstalled-recognizer"}
	catalog := Catalog{Dir: Dir()}
	if _, err := catalog.ResolveBinding(base, p.ID); !errors.Is(err, ErrNativeAudioUnavailable) {
		t.Fatalf("ordinary resolution bypassed guard: %v", err)
	}
	candidate, _, err := catalog.ResolveCandidateBindingWithIssues(base, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	audio := candidate.AudioConfig()
	cfg := candidate.Config()
	if audio.Mode != AudioNative || cfg.OllamaModel != "selected-audio" || cfg.OllamaHost != p.Audio.Endpoint || cfg.OpenAISpeechModel != "selected-audio" || cfg.TTSFallbackProvider != "disabled" || cfg.VoiceWorkerModel != "standard-chat" {
		t.Fatalf("candidate changed routing: %+v %+v", audio, cfg)
	}
	if base.OllamaModel != "global-chat" || base.TTSProvider != "kokoro" {
		t.Fatal("candidate mutated global settings")
	}
	audio.Model = "changed-copy"
	cfg.OllamaModel = "changed-copy"
	p.Audio.Model = "later-profile-edit"
	if err := Write(p, true); err != nil {
		t.Fatal(err)
	}
	if candidate.AudioConfig().Model != "selected-audio" || candidate.Config().OllamaModel != "selected-audio" {
		t.Fatal("admitted candidate changed after edit")
	}
	if _, err := ResolveBinding(base, p.ID); !errors.Is(err, ErrNativeAudioUnavailable) {
		t.Fatalf("candidate preparation enabled startup: %v", err)
	}
}

func TestNativeCandidateBindingPreservesInactiveAndCompatibilityGuards(t *testing.T) {
	setConfigDir(t, t.TempDir())
	p := audioProfile()
	p.Audio = &Audio{Mode: AudioNative, Controls: AudioControlsConversation, Transcript: AudioTranscriptOff}
	p.Inactive = true
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	base := &config.Config{OllamaHost: "http://localhost:11434"}
	catalog := Catalog{Dir: Dir()}
	if _, _, err := catalog.ResolveCandidateBindingWithIssues(base, p.ID); !errors.Is(err, ErrInactive) {
		t.Fatalf("candidate activated inactive profile: %v", err)
	}
	p.Inactive = false
	p.Audio.Controls = AudioControlsStandard
	if err := Write(p, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.ResolveCandidateBindingWithIssues(base, p.ID); err == nil {
		t.Fatal("candidate accepted Standard controls with transcript off")
	}
}
