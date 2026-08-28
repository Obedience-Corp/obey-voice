package tts

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/onnxrt"
)

// writeAudio8Manifests lays out the two manifests an export needs for
// registration. Nothing here is a graph: the checks under test all happen
// before a session is opened.
func writeAudio8Manifests(t *testing.T, dir string, runtime, registration map[string]any) {
	t.Helper()
	write := func(path string, body map[string]any) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if runtime != nil {
		write(filepath.Join(dir, "runtime_manifest.json"), runtime)
	}
	if registration != nil {
		write(filepath.Join(audio8RegistrationDir(dir), audio8RegistrationManifestFile), registration)
	}
}

func goodRuntimeManifest() map[string]any {
	return map[string]any{
		"model_fingerprint":  testFingerprint,
		"slow_logits_layout": "relative_semantic_then_eos",
		"sample_rate":        44100,
		"num_codebooks":      10,
		"codec_frame_size":   2048,
	}
}

func goodRegistrationManifest() map[string]any {
	return map[string]any{
		"model_fingerprint": testFingerprint,
		"sample_rate":       44100,
		"num_codebooks":     10,
		"encoder":           "codec_encoder_fp16.onnx",
		"max_audio_bytes":   52428800,
	}
}

func TestNewAudio8EncoderRefusesABadExport(t *testing.T) {
	tests := []struct {
		name         string
		runtime      map[string]any
		registration map[string]any
		want         string
	}{
		{"no runtime manifest", nil, goodRegistrationManifest(), "runtime_manifest.json"},
		{"no registration package", goodRuntimeManifest(), nil, "registration_manifest.json"},
		{
			"a registration manifest missing its encoder",
			goodRuntimeManifest(),
			map[string]any{"model_fingerprint": testFingerprint, "sample_rate": 44100, "num_codebooks": 10},
			"missing encoder",
		},
		{
			"codes from another model revision",
			goodRuntimeManifest(),
			map[string]any{
				"model_fingerprint": "audio8-tts-preview-0.2b-int8-v1", "sample_rate": 44100,
				"num_codebooks": 10, "encoder": "codec_encoder_fp16.onnx",
			},
			"installed export is",
		},
		{
			"an unsigned registration package",
			goodRuntimeManifest(),
			map[string]any{"sample_rate": 44100, "num_codebooks": 10, "encoder": "codec_encoder_fp16.onnx"},
			"no model_fingerprint",
		},
		{
			"a codebook count that disagrees with the model",
			goodRuntimeManifest(),
			map[string]any{
				"model_fingerprint": testFingerprint, "sample_rate": 44100,
				"num_codebooks": 8, "encoder": "codec_encoder_fp16.onnx",
			},
			"8 codebooks",
		},
		{
			"a sample rate that disagrees with the model",
			goodRuntimeManifest(),
			map[string]any{
				"model_fingerprint": testFingerprint, "sample_rate": 24000,
				"num_codebooks": 10, "encoder": "codec_encoder_fp16.onnx",
			},
			"24000 Hz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAudio8Manifests(t, dir, tt.runtime, tt.registration)
			_, err := NewAudio8Encoder(dir)
			if err == nil {
				t.Fatalf("NewAudio8Encoder() error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("NewAudio8Encoder() error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestNewAudio8EncoderNeedsAModelDir(t *testing.T) {
	if _, err := NewAudio8Encoder(""); err == nil {
		t.Fatal("NewAudio8Encoder(\"\") error = nil, want a missing model dir")
	}
}

func TestAudio8RegistrationInstalled(t *testing.T) {
	dir := t.TempDir()
	if Audio8RegistrationInstalled(dir) {
		t.Error("Audio8RegistrationInstalled() = true for an empty dir")
	}
	writeAudio8Manifests(t, dir, goodRuntimeManifest(), goodRegistrationManifest())
	if Audio8RegistrationInstalled(dir) {
		t.Error("Audio8RegistrationInstalled() = true with no encoder graph on disk")
	}
	if err := os.WriteFile(filepath.Join(audio8RegistrationDir(dir), "codec_encoder_fp16.onnx"), []byte("graph"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Audio8RegistrationInstalled(dir) {
		t.Error("Audio8RegistrationInstalled() = false with manifests and the encoder present")
	}
}

func TestAudio8CodesFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		out    onnxrt.Output
		want   string
		frames int
	}{
		{
			name: "a float output is not codes",
			out:  onnxrt.Output{Data: make([]float32, 20), Shape: []int64{1, 10, 2}},
			want: "want int64 codes",
		},
		{
			name: "the wrong codebook count",
			out:  onnxrt.Output{I64: make([]int64, 16), Shape: []int64{1, 8, 2}},
			want: "want [10, frames]",
		},
		{
			name: "no frames at all",
			out:  onnxrt.Output{I64: nil, Shape: []int64{1, 10, 0}},
			want: "want int64 codes",
		},
		{
			name: "fewer values than the shape claims",
			out:  onnxrt.Output{I64: make([]int64, 5), Shape: []int64{1, 10, 2}},
			want: "returned 5 codes",
		},
		{name: "a batched result", out: onnxrt.Output{I64: make([]int64, 30), Shape: []int64{1, 10, 3}}, frames: 3},
		{name: "an unbatched result", out: onnxrt.Output{I64: make([]int64, 30), Shape: []int64{10, 3}}, frames: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codes, err := audio8CodesFromOutput(tt.out, 10)
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("audio8CodesFromOutput() error = %v, want %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("audio8CodesFromOutput: %v", err)
			}
			if len(codes) != 10 || len(codes[0]) != tt.frames {
				t.Errorf("codes = %dx%d, want 10x%d", len(codes), len(codes[0]), tt.frames)
			}
		})
	}
}

func TestResampleBandlimitedRejectsBadRates(t *testing.T) {
	for _, tt := range []struct{ src, dst int }{{0, 44100}, {44100, 0}, {-1, 44100}} {
		if _, err := resampleBandlimited([]float32{1, 2, 3}, tt.src, tt.dst); err == nil {
			t.Errorf("resampleBandlimited(_, %d, %d) error = nil, want a rate error", tt.src, tt.dst)
		}
	}
}

// TestResampleBandlimitedPreservesATone is the quality check the registration
// path depends on: a tone well inside both bands must come out at the same
// frequency and roughly the same amplitude, at the new length.
func TestResampleBandlimitedPreservesATone(t *testing.T) {
	tests := []struct {
		name     string
		src, dst int
	}{
		{"kokoro to the codec rate", 24000, 44100},
		{"a common capture rate down to the codec rate", 48000, 44100},
		{"no conversion", 44100, 44100},
	}
	const freq = 440.0
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := make([]float32, tt.src) // one second
			for i := range in {
				in[i] = float32(0.5 * math.Sin(2*math.Pi*freq*float64(i)/float64(tt.src)))
			}
			out, err := resampleBandlimited(in, tt.src, tt.dst)
			if err != nil {
				t.Fatalf("resampleBandlimited: %v", err)
			}
			if len(out) != tt.dst {
				t.Fatalf("length = %d, want %d", len(out), tt.dst)
			}
			// Compare against the ideal tone at the new rate, ignoring the
			// first and last kernel width where the edge clamp applies.
			edge := tt.dst / 50
			var worst float64
			for i := edge; i < len(out)-edge; i++ {
				want := 0.5 * math.Sin(2*math.Pi*freq*float64(i)/float64(tt.dst))
				if d := math.Abs(float64(out[i]) - want); d > worst {
					worst = d
				}
			}
			if worst > 0.01 {
				t.Errorf("worst sample error %.4f, want below 0.01 (a band-limited resampler should be near-exact here)", worst)
			}
		})
	}
}

// TestResampleBandlimitedRejectsAliasing checks the anti-alias filter is real:
// a tone above the output Nyquist must be attenuated, not folded back as a
// loud lower tone the way linear interpolation would leave it.
func TestResampleBandlimitedRejectsAliasing(t *testing.T) {
	const src, dst = 44100, 16000
	in := make([]float32, src)
	for i := range in {
		in[i] = float32(math.Sin(2 * math.Pi * 15000 * float64(i) / float64(src)))
	}
	out, err := resampleBandlimited(in, src, dst)
	if err != nil {
		t.Fatalf("resampleBandlimited: %v", err)
	}
	var peak float64
	for _, v := range out[dst/10 : len(out)-dst/10] {
		if a := math.Abs(float64(v)); a > peak {
			peak = a
		}
	}
	if peak > 0.05 {
		t.Errorf("a 15 kHz tone resampled to %d Hz peaks at %.3f; it should be filtered out, not aliased", dst, peak)
	}
}
