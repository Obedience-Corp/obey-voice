//go:build integration

package stt

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
	f32, haveF32 := os.LookupEnv("OBEY_VOICE_DECODE_F32LE")
	if !haveModels && !haveWAV && !haveF32 {
		t.Skip("set OBEY_VOICE_DECODE_MODELS_DIR and exactly one of OBEY_VOICE_DECODE_WAV or OBEY_VOICE_DECODE_F32LE")
	}
	if strings.TrimSpace(models) == "" || haveWAV == haveF32 {
		t.Fatal("OBEY_VOICE_DECODE_MODELS_DIR and exactly one non-empty input variable are required")
	}
	inputPath := wav
	if haveF32 {
		inputPath = f32
	}
	if strings.TrimSpace(inputPath) == "" {
		t.Fatal("decoder input path must be non-empty")
	}
	inputPath, err := filepath.Abs(inputPath)
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
		inputPath,
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
	var samples []float32
	inputCount := 0
	inputRate := audio.SampleRate
	var sourceHash []byte
	if haveF32 {
		contents, readErr := os.ReadFile(inputPath)
		if readErr != nil {
			t.Fatalf("read diagnostic f32le: %v", readErr)
		}
		digest := sha256.Sum256(contents)
		sourceHash = digest[:]
		samples, err = decodeDiagnosticF32LE(contents)
		if err != nil {
			t.Fatalf("read diagnostic f32le: %v", err)
		}
	} else {
		file, openErr := os.Open(inputPath)
		if openErr != nil {
			t.Fatal(openErr)
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
		sourceHash = hash.Sum(nil)
		samples, inputCount, inputRate, err = readDiagnosticWAV(inputPath)
		if err != nil {
			t.Fatalf("load diagnostic WAV: %v", err)
		}
	}
	if len(samples) == 0 {
		t.Fatal("diagnostic input contains no decodable samples")
	}
	if haveF32 {
		inputCount = len(samples)
	}
	cfg := &config.Config{ModelsDir: models, ParakeetModel: model.Name}
	provider, err := NewParakeetSTT(cfg, nil, nil)
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
		WAVPath            string  `json:"wav_path,omitempty"`
		SourcePath         string  `json:"source_path"`
		InputFormat        string  `json:"input_format"`
		WAVSHA256          string  `json:"wav_sha256,omitempty"`
		SourceSHA256       string  `json:"source_sha256"`
		Model              string  `json:"model"`
		ModelDirectory     string  `json:"model_directory"`
		InputSampleCount   int     `json:"input_sample_count"`
		InputSampleRate    int     `json:"input_sample_rate"`
		DecodeSampleCount  int     `json:"decode_sample_count"`
		DecodeSampleRate   int     `json:"decode_sample_rate"`
		DurationSeconds    float64 `json:"duration_seconds"`
		Transcript         string  `json:"transcript"`
	}{
		Diagnostic:        map[bool]string{true: "exact_f32le_parakeet_decode", false: "whole_wav_parakeet_decode"}[haveF32],
		WAVPath:           map[bool]string{true: "", false: inputPath}[haveF32],
		SourcePath:        inputPath,
		InputFormat:       map[bool]string{true: "f32le", false: "wav"}[haveF32],
		WAVSHA256:         map[bool]string{true: "", false: fmt.Sprintf("%x", sourceHash)}[haveF32],
		SourceSHA256:      fmt.Sprintf("%x", sourceHash),
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

// readDiagnosticWAV preserves original input metadata while converting the
// decoder's input to the production 16kHz fixture-source representation.
func readDiagnosticWAV(path string) ([]float32, int, int, error) {
	input, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		return nil, 0, 0, err
	}
	source, err := audio.NewFixtureSourceFromWAV(path, audio.ChunkSize, false)
	if err != nil {
		return nil, 0, 0, err
	}
	var samples []float32
	for chunk := source.Read(); len(chunk) > 0; chunk = source.Read() {
		samples = append(samples, chunk...)
	}
	if err := source.Close(); err != nil {
		return nil, 0, 0, err
	}
	return samples, len(input), rate, nil
}

func decodeDiagnosticF32LE(b []byte) ([]float32, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("input is empty")
	}
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("byte count %d is not divisible by 4", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		v := math.Float32frombits(bits)
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, fmt.Errorf("sample %d is non-finite", i)
		}
		out[i] = v
	}
	return out, nil
}
