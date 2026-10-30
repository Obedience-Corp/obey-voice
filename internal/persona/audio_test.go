package persona

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"gopkg.in/yaml.v3"
)

func audioProfile() *Profile {
	return &Profile{Schema: Schema, ID: "audio-test", DisplayName: "Audio Test", Prompts: PromptRefs{Persona: "obey"},
		Brain: Brain{Provider: "ollama", Model: "standard-chat"}, TTS: TTS{Provider: "kokoro", Voice: "af_heart"}}
}

func TestAudioProfileRoundTripAndStandardDefaults(t *testing.T) {
	setConfigDir(t, t.TempDir())
	p := audioProfile()
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(old), "audio:") {
		t.Fatal("old profile acquired audio settings")
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["audio"]; ok {
		t.Fatal("absent audio should remain absent in JSON")
	}
	base := &config.Config{OllamaHost: "http://localhost:11434", STTProvider: "whisper", TTSFallbackProvider: "kokoro"}
	want := base.Clone()
	Apply(want, p)
	got, mode, err := ResolveAudioConfig(base, p)
	if err != nil || mode.Mode != AudioStandard || !reflect.DeepEqual(got, want) {
		t.Fatalf("Standard changed: mode=%+v err=%v", mode, err)
	}
	p.Audio = &Audio{Mode: AudioStandard, Model: "custom", Voice: "male", Controls: AudioControlsStandard, Transcript: AudioTranscriptOff, RetainAudio: true}
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Audio, p.Audio) || loaded.Brain != p.Brain || loaded.TTS != p.TTS {
		t.Fatalf("lost saved settings: %+v", loaded)
	}
	if err := RequireEnabled(loaded); err != nil {
		t.Fatal(err)
	}
	got, _, err = ResolveAudioConfig(base, loaded)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("inactive native settings changed Standard: %v", err)
	}
}

func TestAudioValidation(t *testing.T) {
	for _, a := range []*Audio{
		{Mode: "typo"}, {Provider: "remote"}, {Controls: "typo"}, {Transcript: "typo"}, {Model: " "}, {Model: "model\x00"}, {Voice: " male"}, {Voice: "ma\tle"},
	} {
		if err := a.Validate(); err == nil {
			t.Fatalf("accepted %+v", a)
		}
	}
	for _, endpoint := range []string{
		"localhost:11434", "ftp://example.com", "http://", "http://user:secret@example.com", "http://example.com?token=secret",
		"http://example.com#secret", "http://example.com/v1", "http://example.com/api/", "http://example.com/proxy/v1/",
		"http://example.com/a/../b", "http://example.com/a//b", "http://example.com/a%2fb", "http://example.com:",
	} {
		t.Run(endpoint, func(t *testing.T) {
			err := (&Audio{Endpoint: endpoint}).Validate()
			if err == nil {
				t.Fatal("invalid endpoint accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("endpoint error leaked credentials")
			}
		})
	}
	for _, endpoint := range []string{"http://localhost:11434", "https://example.com/proxy/", "http://[::1]:11434"} {
		if err := (&Audio{Endpoint: endpoint}).Validate(); err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
	}
	var parsed Profile
	if err := yaml.Unmarshal([]byte("audio:\n  mode: native\n  retain_audio: false\n"), &parsed); err != nil || parsed.Audio == nil || parsed.Audio.Mode != AudioNative {
		t.Fatalf("parse: %+v %v", parsed, err)
	}
}

func TestNativeCandidatePreservesStandardAndWorkers(t *testing.T) {
	setConfigDir(t, t.TempDir())
	base := &config.Config{BrainProvider: "ollama", OllamaModel: "global-chat", OllamaHost: "http://localhost:11434", STTProvider: "whisper",
		TTSProvider: "qwen3-tts", TTSVoice: "af_bella", OpenAISpeechVoice: "old", OpenAISpeechModel: "phonon", OpenAISpeechRuntime: "native",
		OpenAISpeechAPIKey: "unrelated-key", TTSPlace: config.TTSPlaceLAN, TTSWorkerURL: "https://speech.example", TTSWorkerToken: "unrelated-token", TTSFallbackProvider: "kokoro",
		STTCustomWords: []string{"example"}, VoiceWorkers: config.DefaultVoiceWorkers()}
	// Exercise legacy inheritance separately from the tiered policy below.
	base.VoiceWorkers = nil
	before := base.Clone()
	p := audioProfile()
	p.Audio = &Audio{Mode: AudioNative, Endpoint: "https://native.example/proxy/"}
	candidate, effective, err := ResolveAudioConfig(base, p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base, before) || p.Audio.Model != "" {
		t.Fatal("resolution mutated saved configuration")
	}
	if effective.Model != DefaultAudioModel || effective.Provider != "ollama" || effective.Controls != AudioControlsStandard || effective.Transcript != AudioTranscriptAuto || effective.Voice != "" || effective.RetainAudio {
		t.Fatalf("defaults: %+v", effective)
	}
	if candidate.OllamaModel != DefaultAudioModel || candidate.OllamaHost != "https://native.example/proxy" || candidate.BrainProvider != "ollama" {
		t.Fatalf("live model: %+v", effective)
	}
	worker := config.WorkerBrainConfig(candidate)
	if worker.BrainProvider != "ollama" || worker.OllamaModel != "standard-chat" || worker.OllamaHost != base.OllamaHost {
		t.Fatalf("worker inherited native routing: %s %s %s", worker.BrainProvider, worker.OllamaModel, worker.OllamaHost)
	}
	if candidate.TTSProvider != config.OpenAISpeechProviderName || candidate.OpenAISpeechRuntime != "external" || !candidate.OpenAISpeechExternal || candidate.OpenAISpeechURL != "https://native.example/proxy/v1" || candidate.OpenAISpeechModel != DefaultAudioModel || candidate.OpenAISpeechVoice != "" {
		t.Fatal("speech did not use native model/origin/default voice")
	}
	if candidate.OpenAISpeechAPIKey != "" || candidate.TTSWorkerURL != "" || candidate.TTSWorkerToken != "" || candidate.TTSPlace != config.TTSPlaceLocal || candidate.OpenAISpeechBackend != "" || candidate.TTSFallbackProvider != "disabled" {
		t.Fatal("native candidate inherited speech credentials, placement or fallback")
	}
	if candidate.STTProvider != base.STTProvider || candidate.TTSVoice != p.TTS.Voice {
		t.Fatal("Standard provider settings were lost")
	}
	candidate.STTCustomWords[0] = "changed"
	if base.STTCustomWords[0] != "example" {
		t.Fatal("candidate shares mutable settings")
	}
	// Edits after resolution cannot mutate the value snapshot.
	p.Audio.Model = "edited"
	if effective.Model != DefaultAudioModel || candidate.OllamaModel != DefaultAudioModel {
		t.Fatal("candidate changed after profile edit")
	}
}

func TestNativeCandidateExplicitWorkersAndTieredPolicy(t *testing.T) {
	setConfigDir(t, t.TempDir())
	for _, provider := range []string{"ollama", "grok", "codex"} {
		t.Run(provider, func(t *testing.T) {
			p := audioProfile()
			p.Brain.Worker = Worker{Provider: provider, Model: "chosen-worker", OllamaHost: "http://worker:11434"}
			p.Audio = &Audio{Mode: AudioNative, Voice: "male"}
			candidate, effective, err := ResolveAudioConfig(&config.Config{OllamaHost: "http://localhost:11434"}, p)
			if err != nil {
				t.Fatal(err)
			}
			worker := config.WorkerBrainConfig(candidate)
			if worker.BrainProvider != provider || ModelForProvider(worker, provider) != "chosen-worker" {
				t.Fatalf("worker changed: %s %s", worker.BrainProvider, ModelForProvider(worker, provider))
			}
			if provider == "ollama" && worker.OllamaHost != "http://worker:11434" {
				t.Fatal("lost worker endpoint")
			}
			if effective.Endpoint != "http://localhost:11434" || candidate.OpenAISpeechVoice != "male" {
				t.Fatal("endpoint inheritance/voice override failed")
			}
		})
	}
	p := audioProfile()
	p.Audio = &Audio{Mode: AudioNative}
	base := &config.Config{OllamaHost: "http://localhost:11434", VoiceWorkers: config.DefaultVoiceWorkers()}
	base.VoiceWorkers.Mode = config.WorkerModeTiered
	candidate, _, err := ResolveAudioConfig(base, p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(candidate.VoiceWorkers, base.VoiceWorkers) || candidate.VoiceWorkers == base.VoiceWorkers {
		t.Fatal("tiered policy changed or shares ownership")
	}
}

func TestNativeControlPolicyAndActivationFence(t *testing.T) {
	setConfigDir(t, t.TempDir())
	p := audioProfile()
	for _, controls := range []string{AudioControlsStandard, AudioControlsConversation} {
		for _, transcript := range []string{AudioTranscriptAuto, AudioTranscriptRequired, AudioTranscriptOff} {
			p.Audio = &Audio{Mode: AudioNative, Controls: controls, Transcript: transcript}
			_, _, err := ResolveAudioConfig(&config.Config{OllamaHost: "http://localhost:11434"}, p)
			invalid := controls == AudioControlsStandard && transcript == AudioTranscriptOff
			if (err != nil) != invalid {
				t.Fatalf("%s/%s: %v", controls, transcript, err)
			}
		}
	}
	p.Audio = &Audio{Mode: AudioNative, Controls: AudioControlsConversation, Transcript: AudioTranscriptOff}
	for _, base := range []*config.Config{
		{OllamaHost: "http://localhost:11434", AcousticNameEnabled: true},
		{OllamaHost: "http://localhost:11434", GroupParticipationEnabled: true},
	} {
		if _, _, err := ResolveAudioConfig(base, p); err == nil {
			t.Fatal("incompatible controls accepted")
		}
	}
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p.ID); err != nil {
		t.Fatalf("native draft cannot be loaded: %v", err)
	}
	base := &config.Config{ActivePersona: p.ID, OllamaHost: "http://localhost:11434"}
	if _, err := ResolveBinding(base, p.ID); !errors.Is(err, ErrNativeAudioUnavailable) {
		t.Fatalf("binding fence: %v", err)
	}
	if _, err := (Catalog{Dir: Dir()}).ResolveBinding(base, p.ID); !errors.Is(err, ErrNativeAudioUnavailable) {
		t.Fatalf("catalog fence: %v", err)
	}
	if err := Use(base, p.ID); !errors.Is(err, ErrNativeAudioUnavailable) {
		t.Fatalf("use fence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.ConfigDir(), "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("failed activation wrote config: %v", err)
	}
}
