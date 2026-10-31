package nativeaudio

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/openaispeech"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

// Explicit opt-in only; fixture/software drain, never a microphone or device.
func TestLiveNativeProductionAdapters(t *testing.T) {
	source := os.Getenv("OBEY_NATIVE_AUDIO_FIXTURE")
	if source == "" {
		t.Skip("set explicit synthetic WAV fixture to run local adapter proof")
	}
	config.SetConfigDirForTest(t, t.TempDir())
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.Import(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient("http://localhost:11434", "lfm3-audio:2b")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	scheduler := NewScheduler()
	if err := client.BindScheduler(scheduler); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{BrainProvider: "ollama", OllamaHost: "http://localhost:11434", OllamaModel: "lfm3-audio:2b", OllamaNumCtx: 8192, MaxHistory: 8, Persona: "samantha", AgentName: "Samantha", WorkDir: t.TempDir(), SkillsEnabled: false, OpenAISpeechRuntime: "external", OpenAISpeechExternal: true, OpenAISpeechURL: "http://localhost:11434/v1", OpenAISpeechModel: "lfm3-audio:2b", OpenAISpeechVoice: "female", OpenAISpeechTimeout: 15}
	responder, err := brain.NewNativeOllama(ctx, cfg, client.HTTPClient(), media)
	if err != nil {
		t.Fatal(err)
	}
	speech, closeSpeech, err := openaispeech.NewWithContext(ctx, cfg, client.HTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	defer closeSpeech()
	var transcript utterance.Transcript
	var answer string
	var pcmSamples int
	started := time.Now()
	var asrFinal, chatFinal, speechStart time.Duration
	err = scheduler.Run(ctx, func(ctx context.Context) error {
		var err error
		transcript, err = client.Transcribe(ctx, ref, media)
		asrFinal = time.Since(started)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	input := &utterance.Finalized{ID: "quiet-production-fixture", CaptureEpoch: 1, Audio: &ref, Transcript: transcript}
	err = scheduler.Run(ctx, func(ctx context.Context) error {
		var err error
		answer, err = responder.ThinkFull(ctx, "", brain.StreamOptions{Utterance: input})
		chatFinal = time.Since(started)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = scheduler.Run(ctx, func(ctx context.Context) error {
		speechStart = time.Since(started)
		stream, err := speech.Synthesize(ctx, answer)
		if err != nil {
			return err
		}
		for samples := range stream.Frames() {
			pcmSamples += len(samples)
		}
		return stream.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer == "" || pcmSamples == 0 {
		t.Fatal("empty adapter result")
	}
	promptHash, err := brain.PersonaPromptHash(cfg)
	if err != nil {
		t.Fatal(err)
	}
	receipt := map[string]any{"transport": "chat_then_speech", "scope": "production adapters and scheduler, not full pipeline activation", "model": cfg.OllamaModel, "endpoint": cfg.OllamaHost, "voice": "female", "source_sha256": fmt.Sprintf("%x", sha256.Sum256(data)), "audio_ref": ref, "transcript": transcript, "answer": answer, "persona_prompt_hash": promptHash, "asr_final_ms": asrFinal.Milliseconds(), "chat_final_ms": chatFinal.Milliseconds(), "speech_start_ms": speechStart.Milliseconds(), "speech_samples": pcmSamples, "speech_rate": 24000, "sink": "software_drain", "qualification": false}
	payload, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("OBEY_NATIVE_AUDIO_RECEIPT"); path != "" {
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(payload))
}
