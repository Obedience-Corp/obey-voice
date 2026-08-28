//go:build integration

package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
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

// newTestAudio8WithStore points the provider at the real export (read-only) and
// at a throwaway voice store. The separation is the point of the layout: the
// export is a symlink tree into a HuggingFace cache that must never be written
// to, here or on a user's machine.
func newTestAudio8WithStore(t *testing.T, modelsDir, voice string) *Audio8TTS {
	t.Helper()
	dir := audio8ModelDirForTest(t)
	// The real tokenizer, not the captured fixture: a registered voice brings
	// its own transcript, which no fixture could have captured in advance.
	tok, err := NewAudio8Tokenizer(dir)
	if err != nil {
		t.Fatalf("NewAudio8Tokenizer: %v", err)
	}
	a, err := NewAudio8TTS(Audio8Options{
		ModelDir:  dir,
		ModelsDir: modelsDir,
		Voice:     voice,
		Tokenizer: tok,
	})
	if err != nil {
		t.Fatalf("NewAudio8TTS: %v", err)
	}
	t.Cleanup(a.Delete)
	return a
}

// registerLiveVoice runs the real codec encoder over a synthetic reference and
// stores the result in modelsDir. The audio is a decaying harmonic buzz, not a
// person: this test proves the encoder binding works, and needs no recording of
// anyone to do it.
func registerLiveVoice(t *testing.T, modelsDir, name string, seconds float64) Audio8VoiceMeta {
	t.Helper()
	const rate = 22050 // deliberately not the codec rate, so resampling runs too
	n := int(seconds * rate)
	samples := make([]float32, n)
	for i := range samples {
		tSec := float64(i) / rate
		env := math.Exp(-2 * math.Mod(tSec, 0.5))
		var v float64
		for h := 1; h <= 6; h++ {
			v += math.Sin(2*math.Pi*140*float64(h)*tSec) / float64(h)
		}
		samples[i] = float32(0.3 * env * v)
	}
	meta, err := RegisterAudio8Voice(context.Background(), Audio8RegisterRequest{
		ModelDir:   audio8ModelDirForTest(t),
		ModelsDir:  modelsDir,
		Name:       name,
		Transcript: "This is a synthetic reference tone, not a recording of anyone.",
		Locale:     "en-us",
		Samples:    samples,
		SampleRate: rate,
		Provenance: Audio8VoiceProvenance{Kind: Audio8VoiceSynthetic, BootstrapProvider: "test tone"},
	})
	if err != nil {
		t.Fatalf("RegisterAudio8Voice: %v", err)
	}
	return meta
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

	if len(a.prefixes) != 1 {
		t.Fatalf("prefix cache holds %d entries, want the one voice that spoke", len(a.prefixes))
	}
	prefix := a.prefixes[config.Audio8PackagedVoice]
	if prefix == nil {
		t.Fatal("prefix state was not cached under the packaged voice")
	}
	if prefix.length == 0 {
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

// TestAudio8EncoderProducesPlausibleCodes is the encoder binding against the
// real graph: a known duration in, [10][T] int64 out, with T where the codec's
// frame rate says it should be.
func TestAudio8EncoderProducesPlausibleCodes(t *testing.T) {
	dir := audio8ModelDirForTest(t)
	if !Audio8RegistrationInstalled(dir) {
		t.Skipf("no registration package under %s", dir)
	}
	encoder, err := NewAudio8Encoder(dir)
	if err != nil {
		t.Fatalf("NewAudio8Encoder: %v", err)
	}
	defer encoder.Close()

	if encoder.SampleRate() != 44100 {
		t.Errorf("encoder sample rate = %d, want 44100", encoder.SampleRate())
	}
	const seconds, rate = 3.0, 16000
	samples := make([]float32, int(seconds*rate))
	for i := range samples {
		samples[i] = float32(0.25 * math.Sin(2*math.Pi*220*float64(i)/rate))
	}
	start := time.Now()
	codes, err := encoder.Encode(context.Background(), samples, rate)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("codes = %d codebooks, want 10", len(codes))
	}
	frames := len(codes[0])
	// codec_frame_rate_hz is 21.53 (A1); the tail is padded up to a whole hop,
	// so allow the frame either side.
	want := seconds * audio8CodecFrameRateHz
	if float64(frames) < want-2 || float64(frames) > want+2 {
		t.Errorf("%.1fs encoded to %d frames, want about %.0f", seconds, frames, want)
	}
	var distinct int
	seen := map[int64]bool{}
	for _, c := range codes[0] {
		if c < 0 || c >= 4096 {
			t.Fatalf("code %d is outside the codebook", c)
		}
		if !seen[c] {
			seen[c] = true
			distinct++
		}
	}
	if distinct < 2 {
		t.Errorf("the first codebook holds %d distinct values; the encoder returned a constant", distinct)
	}
	t.Logf("encoded %.1fs at %d Hz into %d frames in %v", seconds, rate, frames, time.Since(start).Round(time.Millisecond))
}

func TestAudio8EncoderRejectsBadReferenceAudio(t *testing.T) {
	dir := audio8ModelDirForTest(t)
	if !Audio8RegistrationInstalled(dir) {
		t.Skipf("no registration package under %s", dir)
	}
	encoder, err := NewAudio8Encoder(dir)
	if err != nil {
		t.Fatalf("NewAudio8Encoder: %v", err)
	}
	defer encoder.Close()

	tests := []struct {
		name    string
		samples []float32
		rate    int
		want    string
	}{
		{"no audio", nil, 44100, "is empty"},
		{"a bad rate", make([]float32, 1000), 0, "sample rate"},
		{"too short", make([]float32, 4410), 44100, "want between"},
		{"too long", make([]float32, 44100*31), 44100, "want between"},
		{"not finite", nonFinite(44100 * 2), 44100, "not finite"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := encoder.Encode(context.Background(), tt.samples, tt.rate)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Encode() error = %v, want %q", err, tt.want)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := encoder.Encode(ctx, make([]float32, 44100*2), 44100); err == nil {
		t.Fatal("Encode() with a cancelled context error = nil")
	}
}

func nonFinite(n int) []float32 {
	s := make([]float32, n)
	s[n/2] = float32(math.NaN())
	return s
}

// TestAudio8SpeaksInARegisteredVoice is the end-to-end claim of this change:
// audio encoded here becomes a voice the model actually speaks in, selected by
// name through the request, with the prefix cache keyed per voice.
func TestAudio8SpeaksInARegisteredVoice(t *testing.T) {
	dir := audio8ModelDirForTest(t)
	if !Audio8RegistrationInstalled(dir) {
		t.Skipf("no registration package under %s", dir)
	}
	// The store is a temp dir; the export stays read-only.
	modelsDir := t.TempDir()
	meta := registerLiveVoice(t, modelsDir, "tone", 4)
	if meta.NumCodebooks != 10 || meta.Frames < 60 {
		t.Fatalf("registered %d codebooks x %d frames", meta.NumCodebooks, meta.Frames)
	}
	if meta.ModelFingerprint == "" {
		t.Fatal("the registered voice carries no fingerprint")
	}

	a := newTestAudio8WithStore(t, modelsDir, "")
	if !a.Capabilities().SupportsReferenceAudio {
		t.Error("SupportsReferenceAudio = false with the registration package installed")
	}
	names := []string{}
	for _, v := range a.ListVoices("", "") {
		names = append(names, v.Name)
	}
	if strings.Join(names, ",") != config.Audio8PackagedVoice+",tone" {
		t.Fatalf("ListVoices = %v, want the packaged reference then tone", names)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	speak := func(voice string) int {
		t.Helper()
		res, err := a.SynthesizeRequest(ctx, SynthesisRequest{Text: "It continues here.", Voice: voice})
		if err != nil {
			t.Fatalf("SynthesizeRequest(%q): %v", voice, err)
		}
		if res.Voice != voice && !(voice == "" && res.Voice == config.Audio8PackagedVoice) {
			t.Errorf("result voice = %q, want %q", res.Voice, voice)
		}
		if _, err := res.Stream.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
		var samples int
		var peak float32
		for frame := range res.Stream.Frames() {
			samples += len(frame)
			for _, v := range frame {
				if v > peak {
					peak = v
				}
			}
		}
		if err := res.Stream.Err(); err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if samples == 0 || peak == 0 {
			t.Fatalf("voice %q produced %d samples peaking at %v", voice, samples, peak)
		}
		return samples
	}

	registered := speak("tone")
	packaged := speak(config.Audio8PackagedVoice)
	t.Logf("registered voice %d samples, packaged voice %d samples", registered, packaged)

	// Each voice conditions on its own reference, so each has its own primed
	// head; one shared entry would mean the second voice spoke with the first
	// one's throat.
	if len(a.prefixes) != 2 {
		t.Fatalf("prefix cache holds %d entries after two voices, want 2", len(a.prefixes))
	}
	if a.prefixes["tone"].length == a.prefixes[config.Audio8PackagedVoice].length {
		t.Error("both voices primed the same number of tokens; the reference frames did not reach the prompt")
	}

	if _, err := a.SynthesizeRequest(ctx, SynthesisRequest{Text: "hi", Voice: "nobody"}); err == nil {
		t.Error("SynthesizeRequest with an unknown voice error = nil")
	}
}

// TestAudio8RefusesCodesFromAnotherModel is the load-time half of the
// fingerprint rule: an export swap under an existing store must be refused, not
// decoded as noise.
func TestAudio8RefusesCodesFromAnotherModel(t *testing.T) {
	dir := audio8ModelDirForTest(t)
	if !Audio8RegistrationInstalled(dir) {
		t.Skipf("no registration package under %s", dir)
	}
	modelsDir := t.TempDir()
	registerLiveVoice(t, modelsDir, "tone", 2)

	// Rewrite the stored fingerprint as if the export had been replaced.
	path := filepath.Join(config.Audio8VoiceDir(modelsDir, "tone"), config.Audio8VoiceMetaFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta Audio8VoiceMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	meta.ModelFingerprint = "audio8-tts-preview-9.9b-int8-v9"
	blob, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	a := newTestAudio8WithStore(t, modelsDir, "")
	_, err = a.SynthesizeRequest(context.Background(), SynthesisRequest{Text: "hi", Voice: "tone"})
	if err == nil {
		t.Fatal("SynthesizeRequest with foreign codes error = nil")
	}
	if !strings.Contains(err.Error(), "9.9b") {
		t.Errorf("error = %q, want it to name the stale fingerprint", err)
	}
}
