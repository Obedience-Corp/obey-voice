package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

func readDecodeEvidence(t *testing.T, dir, base string) decodeEvidence {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, base+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence decodeEvidence
	if err := json.Unmarshal(b, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestDecodeRecorderPreservesExactBitsAndSeparateQuantizedWAV(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "turn", "stt-decodes")
	recorder, err := newDecodeRecorder(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Signed zero, a subnormal, and non-PCM16 fractions distinguish exact model
	// input from a WAV reconstructed after quantization.
	bits := []uint32{0x80000000, 0x00000001, 0x3dcccccd, 0xbe4ccccd, 0x3f7fffff}
	samples := make([]float32, len(bits))
	for i, b := range bits {
		samples[i] = math.Float32frombits(b)
	}
	start := time.Date(2026, 9, 16, 1, 2, 3, 123456789, time.FixedZone("fixture", -6*3600))
	end := start.Add(1234567 * time.Nanosecond)
	decodeErr := errors.New("decoder failed: \"known\"\nretained input")
	observation := stt.DecodeObservation{Samples: samples, Text: "Partial \"Wait.\"", Err: decodeErr, StartedAt: start, CompletedAt: end}
	if err := recorder.observe(t.Context(), observation); err != nil {
		t.Fatal(err)
	}
	if err := recorder.verify(); err != nil {
		t.Fatal(err)
	} // evidence completion, not recognition success
	for i, b := range bits {
		if math.Float32bits(samples[i]) != b {
			t.Fatal("borrowed decoder input mutated")
		}
	}
	for i := range samples {
		samples[i] = 0
	} // later decoder-buffer reuse must not change disk evidence
	evidence := readDecodeEvidence(t, dir, "decode-001")
	if evidence.Format != "float32-little-endian-mono" || evidence.SampleRate != audio.SampleRate || evidence.Samples != len(bits) || evidence.PCM != "decode-001.f32le" || evidence.WAV != "decode-001.wav" {
		t.Fatalf("wrong input provenance: %+v", evidence)
	}
	if evidence.Transcript != observation.Text || evidence.DecodeError != decodeErr.Error() || !evidence.StartedAt.Equal(start) || !evidence.CompletedAt.Equal(end) {
		t.Fatalf("decode outcome or timestamps lost: %+v", evidence)
	}
	pcm, err := os.ReadFile(filepath.Join(dir, evidence.PCM))
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 4*len(bits) {
		t.Fatalf("PCM byte count=%d", len(pcm))
	}
	for i, want := range bits {
		if got := binary.LittleEndian.Uint32(pcm[i*4:]); got != want {
			t.Fatalf("sample%d bits=%08x want=%08x", i, got, want)
		}
	}
	if evidence.SHA256 != fmt.Sprintf("%x", sha256.Sum256(pcm)) {
		t.Fatal("exact PCM fingerprint mismatch")
	}
	wavBytes, err := os.ReadFile(filepath.Join(dir, evidence.WAV))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.WAVSHA256 != fmt.Sprintf("%x", sha256.Sum256(wavBytes)) {
		t.Fatal("WAV fingerprint mismatch")
	}
	wav, rate, err := audio.ReadWAVFloat32(filepath.Join(dir, evidence.WAV))
	if err != nil {
		t.Fatal(err)
	}
	if rate != audio.SampleRate || len(wav) != len(bits) {
		t.Fatalf("WAV shape=%d/%d", rate, len(wav))
	}
	different := false
	for i, want := range bits {
		original := math.Float32frombits(want)
		if math.Abs(float64(wav[i]-original)) > 2.0/32768 {
			t.Fatalf("WAV sample%d is not a quantized view: %g/%g", i, wav[i], original)
		}
		different = different || math.Float32bits(wav[i]) != want
	}
	if !different {
		t.Fatal("test did not distinguish PCM16 listening view from exact float input")
	}
}

func TestDecodeRecorderMissingOrFailedEvidenceCannotVerify(t *testing.T) {
	empty, err := newDecodeRecorder(filepath.Join(t.TempDir(), "empty"))
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.verify(); err == nil || !strings.Contains(err.Error(), "no offline decoder evidence") {
		t.Fatalf("empty evidence accepted: %v", err)
	}
	for _, extension := range []string{".f32le", ".wav", ".json"} {
		t.Run(extension, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "stt-decodes")
			r, err := newDecodeRecorder(dir)
			if err != nil {
				t.Fatal(err)
			}
			observation := stt.DecodeObservation{Samples: []float32{.12345}, Text: "first", StartedAt: time.Now(), CompletedAt: time.Now()}
			if err := r.observe(t.Context(), observation); err != nil {
				t.Fatal(err)
			}
			obstruction := filepath.Join(dir, "decode-002"+extension)
			if err := os.Mkdir(obstruction, 0o700); err != nil {
				t.Fatal(err)
			}
			writeErr := r.observe(t.Context(), observation)
			if writeErr == nil {
				t.Fatal("write failure did not reach observer caller")
			}
			if err := r.verify(); err == nil {
				t.Fatal("earlier success hid failed evidence write")
			}
			if err := os.Remove(obstruction); err != nil {
				t.Fatal(err)
			}
			if err := r.observe(t.Context(), observation); err == nil || err.Error() != writeErr.Error() {
				t.Fatalf("failed recorder resumed silently: %v", err)
			}
			if err := r.verify(); err == nil {
				t.Fatal("removing obstruction cleared latched failure")
			}
			if _, err := os.Stat(filepath.Join(dir, "decode-003.json")); !os.IsNotExist(err) {
				t.Fatalf("failed write advanced evidence sequence: %v", err)
			}
			if got := readDecodeEvidence(t, dir, "decode-001").Transcript; got != "first" {
				t.Fatalf("prior retained evidence changed: %q", got)
			}
		})
	}
}

func TestDecodeRecorderRefusesExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dir, "decode-001.json")
	if err := os.WriteFile(sentinel, []byte("original evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := newDecodeRecorder(dir); err == nil || r != nil {
		t.Fatal("existing evidence directory accepted")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "original evidence" {
		t.Fatalf("existing evidence altered: %q %v", data, err)
	}
}

func TestDecodeRecorderConcurrentObservationsHaveDistinctArtifacts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stt-decodes")
	r, err := newDecodeRecorder(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			err := r.observe(context.Background(), stt.DecodeObservation{Samples: []float32{float32(i) / 16}, Text: fmt.Sprint(i), StartedAt: time.Now(), CompletedAt: time.Now()})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := r.verify(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 1; i <= 8; i++ {
		e := readDecodeEvidence(t, dir, fmt.Sprintf("decode-%03d", i))
		if seen[e.Transcript] {
			t.Fatal("concurrent decode overwrote an artifact")
		}
		seen[e.Transcript] = true
	}
}
