//go:build integration

package stt

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// TestDecodeSavedParakeetWAV is an opt-in whole-recording diagnostic. It calls
// the production decoder directly, without VAD, endpointing, or audio devices.
// A passing test means decoding completed, not that physical acceptance passed.
func TestDecodeSavedParakeetWAV(t *testing.T) {
	models, haveModels := os.LookupEnv("OBEY_VOICE_DECODE_MODELS_DIR")
	wav, haveWAV := os.LookupEnv("OBEY_VOICE_DECODE_WAV")
	if !haveModels && !haveWAV {
		t.Skip("set OBEY_VOICE_DECODE_MODELS_DIR and OBEY_VOICE_DECODE_WAV for saved-WAV decoding")
	}
	if strings.TrimSpace(models) == "" || strings.TrimSpace(wav) == "" {
		t.Fatal("OBEY_VOICE_DECODE_MODELS_DIR and OBEY_VOICE_DECODE_WAV must both be non-empty")
	}
	wav, err := filepath.Abs(wav)
	if err != nil {
		t.Fatal(err)
	}
	models, err = filepath.Abs(models)
	if err != nil {
		t.Fatal(err)
	}
	model, err := config.ParakeetModel(os.Getenv("OBEY_VOICE_DECODE_MODEL"))
	if err != nil {
		t.Fatal(err)
	}

	// Fail on missing, empty, or unreadable assets before entering native code.
	for _, path := range []string{
		wav,
		filepath.Join(model.ModelDir(models), model.Encoder()),
		filepath.Join(model.ModelDir(models), model.Decoder()),
		filepath.Join(model.ModelDir(models), model.Joiner()),
		filepath.Join(model.ModelDir(models), model.Tokens()),
	} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open diagnostic input %s: %v", path, err)
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil {
			t.Fatalf("stat diagnostic input %s: %v", path, statErr)
		}
		if closeErr != nil {
			t.Fatalf("close diagnostic input %s: %v", path, closeErr)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("diagnostic input %s must be a non-empty regular file", path)
		}
	}
	file, err := os.Open(wav)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	closeErr := file.Close()
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	input, inputRate, err := audio.ReadWAVFloat32(wav)
	if err != nil {
		t.Fatalf("read diagnostic WAV: %v", err)
	}
	inputCount := len(input)
	source, err := audio.NewFixtureSourceFromWAV(wav, audio.ChunkSize, false)
	if err != nil {
		t.Fatalf("load diagnostic fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Errorf("close diagnostic fixture: %v", err)
		}
	})
	var samples []float32
	for chunk := source.Read(); len(chunk) > 0; chunk = source.Read() {
		samples = append(samples, chunk...)
	}
	if len(samples) == 0 {
		t.Fatal("diagnostic WAV contains no decodable samples")
	}
	cfg := &config.Config{ModelsDir: models, ParakeetModel: model.Name}
	provider, err := NewParakeetSTT(cfg, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Delete)
	transcript, err := provider.transcribe(samples)
	if err != nil {
		t.Fatal(err)
	}
	record := struct {
		Diagnostic         string  `json:"diagnostic"`
		PhysicalAcceptance bool    `json:"physical_acceptance"`
		WAVPath            string  `json:"wav_path"`
		WAVSHA256          string  `json:"wav_sha256"`
		Model              string  `json:"model"`
		ModelDirectory     string  `json:"model_directory"`
		InputSampleCount   int     `json:"input_sample_count"`
		InputSampleRate    int     `json:"input_sample_rate"`
		DecodeSampleCount  int     `json:"decode_sample_count"`
		DecodeSampleRate   int     `json:"decode_sample_rate"`
		DurationSeconds    float64 `json:"duration_seconds"`
		Transcript         string  `json:"transcript"`
	}{
		Diagnostic:        "whole_wav_parakeet_decode",
		WAVPath:           wav,
		WAVSHA256:         fmt.Sprintf("%x", hash.Sum(nil)),
		Model:             model.Name,
		ModelDirectory:    model.ModelDir(models),
		InputSampleCount:  inputCount,
		InputSampleRate:   inputRate,
		DecodeSampleCount: len(samples),
		DecodeSampleRate:  audio.SampleRate,
		DurationSeconds:   float64(len(samples)) / audio.SampleRate,
		Transcript:        transcript,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}
