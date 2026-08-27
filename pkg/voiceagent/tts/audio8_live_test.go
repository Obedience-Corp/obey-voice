//go:build integration

package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixtureTokenizer replays token ids captured from the model's own HuggingFace
// tokenizer. It exists so the DualAR loop, prefix cache and codec pipeline can
// be tested without a Go BPE implementation; it is not a general tokenizer.
type fixtureTokenizer struct{ enc map[string][]int64 }

func loadFixtureTokenizer(t *testing.T) *fixtureTokenizer {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "audio8_tokenizer_fixture.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc struct {
		Encodings map[string][]int64 `json:"encodings"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return &fixtureTokenizer{enc: doc.Encodings}
}

func (f *fixtureTokenizer) Encode(text string) ([]int64, error) {
	ids, ok := f.enc[text]
	if !ok {
		return nil, fmt.Errorf("fixture tokenizer has no encoding for %q", text)
	}
	return ids, nil
}

func audio8ModelDirForTest(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("AUDIO8_MODEL_DIR")
	if dir == "" {
		t.Skip("set AUDIO8_MODEL_DIR to run")
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime_manifest.json")); err != nil {
		t.Skipf("no Audio8 export in %s: %v", dir, err)
	}
	return dir
}

func newTestAudio8(t *testing.T) *Audio8TTS {
	t.Helper()
	a, err := NewAudio8TTS(Audio8Options{ModelDir: audio8ModelDirForTest(t), Tokenizer: loadFixtureTokenizer(t)})
	if err != nil {
		t.Fatalf("NewAudio8TTS: %v", err)
	}
	t.Cleanup(a.Delete)
	return a
}

// TestAudio8SynthesizeProducesAudio drives the whole pipeline: prompt assembly,
// prefix priming, the slow/fast AR loop and pipelined codec decode.
func TestAudio8SynthesizeProducesAudio(t *testing.T) {
	a := newTestAudio8(t)
	if !a.Available() {
		t.Fatal("provider reports unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	start := time.Now()
	stream, err := a.Synthesize(ctx, "It continues here.")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	rate, err := stream.WaitReady(ctx)
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if rate != a.man.SampleRate {
		t.Fatalf("stream rate = %d, want %d", rate, a.man.SampleRate)
	}

	var samples int
	var firstAudio time.Duration
	for frame := range stream.Frames() {
		if samples == 0 {
			firstAudio = time.Since(start)
		}
		samples += len(frame)
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if samples == 0 {
		t.Fatal("no audio produced")
	}

	dur := float64(samples) / float64(rate)
	elapsed := time.Since(start)
	t.Logf("audio %.3fs from %d samples in %v (RTF %.2fx), first audio at %v",
		dur, samples, elapsed.Round(time.Millisecond), dur/elapsed.Seconds(),
		firstAudio.Round(time.Millisecond))

	// A short sentence should land in a plausible band; this catches a decode
	// loop that terminates immediately or never stops.
	if dur < 0.3 || dur > 12 {
		t.Fatalf("implausible audio duration %.3fs", dur)
	}
}

// TestAudio8PrefixCacheIsReused checks the second utterance skips the constant
// head. Without this the provider pays ~1s of prefill on every turn.
func TestAudio8PrefixCacheIsReused(t *testing.T) {
	a := newTestAudio8(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	drain := func() time.Duration {
		start := time.Now()
		stream, err := a.Synthesize(ctx, "It continues here.")
		if err != nil {
			t.Fatalf("Synthesize: %v", err)
		}
		if _, err := stream.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
		for range stream.Frames() {
		}
		if err := stream.Err(); err != nil {
			t.Fatalf("stream error: %v", err)
		}
		return time.Since(start)
	}

	first := drain()
	second := drain()
	t.Logf("first %v, second %v", first.Round(time.Millisecond), second.Round(time.Millisecond))

	if a.prefix == nil {
		t.Fatal("prefix state was not cached")
	}
	if a.prefixLen == 0 {
		t.Fatal("prefix length is zero")
	}
}

// TestAudio8RejectsForeignSampleRate keeps the no-resampling contract explicit.
func TestAudio8RejectsForeignSampleRate(t *testing.T) {
	a := newTestAudio8(t)
	_, err := a.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "hi", SampleRate: 24000})
	if err == nil {
		t.Fatal("want an error for a non-native sample rate")
	}
}
