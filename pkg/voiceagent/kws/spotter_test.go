package kws

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"

	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestMissingModelIsActionable(t *testing.T) {
	if _, err := NewObey(t.TempDir()); err == nil {
		t.Fatal("accepted missing model")
	}
}

// Explicit path: no downloads, microphones, playback, or user configuration.
func TestObeyNativeReplay(t *testing.T) {
	dir := os.Getenv("OBEY_KWS_MODEL_DIR")
	if dir == "" {
		t.Skip("set OBEY_KWS_MODEL_DIR to the extracted keyword model")
	}
	detector, err := NewObey(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer detector.Close()
	for _, tc := range []struct {
		file string
		hit  bool
	}{
		{"bare", true}, {"positive", true}, {"british", true}, {"kokoro", true},
		{"verb", true}, {"negative", false}, {"other-keyword", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			samples, sr, err := audio.ReadWAVFloat32(filepath.Join("../../../testdata/kws", tc.file+".wav"))
			if err != nil {
				t.Fatal(err)
			}
			if sr != sampleRate {
				t.Fatalf("sample rate %d", sr)
			}
			original := append([]float32(nil), samples...)
			start := time.Now()
			got, err := detector.Detect(t.Context(), samples)
			t.Logf("hit=%q elapsed=%s audio=%.2fs", got, time.Since(start), float64(len(samples))/sampleRate)
			if err != nil || (got != "") != tc.hit {
				t.Fatalf("hit=%q want=%v err=%v", got, tc.hit, err)
			}
			if !reflect.DeepEqual(original, samples) {
				t.Fatal("modified input")
			}
			if name, err := detector.Detect(t.Context(), make([]float32, sampleRate)); err != nil || name != "" {
				t.Fatalf("evidence leaked to silence: %q %v", name, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := detector.Detect(ctx, make([]float32, sampleRate)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	noise := make([]float32, sampleRate*10)
	for i := range noise {
		noise[i] = float32(rng.NormFloat64() * 0.05)
	}
	if got, err := detector.Detect(t.Context(), noise); err != nil || got != "" {
		t.Fatalf("noise hit=%q err=%v", got, err)
	}
	detector.Close()
	detector.Close()
	if _, err := detector.Detect(t.Context(), noise); err == nil {
		t.Fatal("closed detector accepted audio")
	}
}

// More names in one spotter must not change what it reports for each, and the
// hit time must tell the opening of an utterance from its end.
func TestSpotterListensForSeveralNames(t *testing.T) {
	dir := os.Getenv("OBEY_KWS_MODEL_DIR")
	if dir == "" {
		t.Skip("set OBEY_KWS_MODEL_DIR to the extracted keyword model")
	}
	spotter, err := New(dir, []string{"Juno", "the computer", "Obey", " obey ", "Mary-Jane"})
	if err != nil {
		t.Fatal(err)
	}
	defer spotter.Close()
	if got := spotter.Names(); !reflect.DeepEqual(got, []string{"Juno", "the computer", "Obey", "Mary-Jane"}) {
		t.Fatalf("names = %q", got)
	}
	observe := func(file string) Hit {
		t.Helper()
		samples, _, err := audio.ReadWAVFloat32(filepath.Join("../../../testdata/kws", file+".wav"))
		if err != nil {
			t.Fatal(err)
		}
		hit, err := spotter.Observe(t.Context(), samples)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %+v", file, hit)
		return hit
	}
	opening := observe("british") // "Obey, can you help me?"
	closing := observe("mention") // "Tell me about Obey."
	if opening.Name != "Obey" || closing.Name != "Obey" {
		t.Fatalf("opening=%+v closing=%+v", opening, closing)
	}
	if opening.By <= 0 || opening.By >= closing.By {
		t.Fatalf("a name that opens an utterance was not heard before one that ends it: %s vs %s", opening.By, closing.By)
	}
	for _, file := range []string{"negative", "other-keyword"} {
		if hit := observe(file); hit != (Hit{}) {
			t.Fatalf("%s: %+v", file, hit)
		}
	}
}

func TestNewRejectsNamesItCannotSpell(t *testing.T) {
	dir := os.Getenv("OBEY_KWS_MODEL_DIR")
	if dir == "" {
		t.Skip("set OBEY_KWS_MODEL_DIR to the extracted keyword model")
	}
	if _, err := New(dir, []string{"Juno", "R2D2"}); !errors.Is(err, ErrUnspellable) {
		t.Fatalf("New = %v", err)
	}
}
