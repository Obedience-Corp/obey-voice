//go:build integration

package stt

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func TestReadDiagnosticF32LEPreservesBits(t *testing.T) {
	values := []uint32{0x00000000, 0x80000000, 0x3f800001, 0x007fffff}
	b := make([]byte, len(values)*4)
	for i, bits := range values {
		binary.LittleEndian.PutUint32(b[i*4:], bits)
	}
	got, err := decodeDiagnosticF32LE(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(values) {
		t.Fatalf("sample count = %d, want %d", len(got), len(values))
	}
	for i, want := range values {
		if math.Float32bits(got[i]) != want {
			t.Errorf("sample %d bits = %#x, want %#x", i, math.Float32bits(got[i]), want)
		}
	}
}

func TestDiagnosticWAVSourceRetainsInputAndResamplesForDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.wav")
	input := []float32{0, .25, -.5, .75, -.25, 0}
	if err := audio.WriteWAVFloat32(path, 24000, input); err != nil {
		t.Fatal(err)
	}
	decoded, count, rate, err := readDiagnosticWAV(path)
	if err != nil {
		t.Fatal(err)
	}
	if count != len(input) || rate != 24000 {
		t.Fatalf("raw WAV = (%d samples, %d Hz), want (%d, 24000)", count, rate, len(input))
	}
	want := 4 // six samples at24kHz are four samples at16kHz
	if len(decoded) != want {
		t.Fatalf("resampled source length = %d, want %d", len(decoded), want)
	}
}

func TestReadDiagnosticF32LERejectsMalformedInputs(t *testing.T) {
	cases := map[string][]byte{
		"empty":             nil,
		"partial":           {1, 2, 3},
		"nan":               func() []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, 0x7fc00001); return b }(),
		"positive infinity": func() []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, 0x7f800000); return b }(),
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDiagnosticF32LE(contents); err == nil {
				t.Fatal("expected malformed input error")
			}
		})
	}
}
