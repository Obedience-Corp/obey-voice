//go:build integration

package stt

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

// TestReplaySavedSTTLoop diagnoses a saved processed 16kHz WAV through the real
// offline loop, Silero segmenter and Parakeet decoder, without audio devices.
// Required: OBEY_VOICE_STT_REPLAY_WAV, _MODELS_DIR and _OUTPUT_DIR (must not exist).
// Optional suffixes: _GAIN (1), _CHUNK_SAMPLES (1600), _START_SAMPLE (0),
// _SILENCE_MS (1200), _PRE_ROLL_MS (300), _VAD_THRESHOLD (0.6),
// _MIN_SPEECH_MS (250), _MODEL (the configured Parakeet default).
//
// Gain is reported metadata; the already-processed samples are NOT amplified.
// It is a declared hypothesis when the recording lacks frame gain metadata.
// Read sizes are also declared, not reconstructed capture callback timing.
// Pacing is accelerated: this reproduces sample-based silence/segmentation, not
// wall-clock caps, live backlog or device ordering. Gate and policy records are
// a shadow evaluation of the package's actual functions, not instrumentation
// inside runOfflineLoop. Segment operations and decoder input are observed.
// A pass means the diagnostic ran, never that physical acceptance passed.
func TestReplaySavedSTTLoop(t *testing.T) {
	const prefix = "OBEY_VOICE_STT_REPLAY_"
	wav, models, output := os.Getenv(prefix+"WAV"), os.Getenv(prefix+"MODELS_DIR"), os.Getenv(prefix+"OUTPUT_DIR")
	if wav == "" && models == "" && output == "" {
		t.Skip("set OBEY_VOICE_STT_REPLAY_WAV, _MODELS_DIR and _OUTPUT_DIR")
	}
	if wav == "" || models == "" || output == "" {
		t.Fatal("WAV, MODELS_DIR and OUTPUT_DIR must all be non-empty")
	}
	readNumber := func(suffix string, fallback float64) float64 {
		t.Helper()
		value := os.Getenv(prefix + suffix)
		if value == "" {
			return fallback
		}
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			t.Fatalf("%s%s=%q must be a finite non-negative number", prefix, suffix, value)
		}
		return n
	}
	readInt := func(suffix string, fallback int) int {
		t.Helper()
		n := readNumber(suffix, float64(fallback))
		if n != math.Trunc(n) || n > float64(math.MaxInt32) {
			t.Fatalf("%s%s must be a non-negative integer", prefix, suffix)
		}
		return int(n)
	}
	gain := readNumber("GAIN", 1)
	chunk, start := readInt("CHUNK_SAMPLES", audio.ChunkSize), readInt("START_SAMPLE", 0)
	silence, preRoll := readInt("SILENCE_MS", 1200), readInt("PRE_ROLL_MS", 300)
	threshold, minSpeech := readNumber("VAD_THRESHOLD", .6), readInt("MIN_SPEECH_MS", 250)
	if gain <= 0 || chunk <= 0 || chunk > audio.ChunkSize || silence <= 0 || threshold <= 0 || threshold > 1 || minSpeech <= 0 {
		t.Fatal("gain, silence and minimum speech must be positive; chunk must be 1..audio.ChunkSize; threshold must be (0,1]")
	}
	bytes, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	pcm, rate, err := audio.ReadWAVFloat32(wav)
	if err != nil || rate != audio.SampleRate || start >= len(pcm) {
		t.Fatalf("require non-empty 16kHz WAV and start before EOF: rate=%d samples=%d start=%d err=%v", rate, len(pcm), start, err)
	}
	model, err := config.ParakeetModel(os.Getenv(prefix + "MODEL"))
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range []string{filepath.Join(models, "silero_vad.onnx"), filepath.Join(model.ModelDir(models), model.Encoder()), filepath.Join(model.ModelDir(models), model.Decoder()), filepath.Join(model.ModelDir(models), model.Joiner()), filepath.Join(model.ModelDir(models), model.Tokens())} {
		info, err := os.Stat(asset)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("require non-empty regular model asset %s: %v", asset, err)
		}
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatalf("create new diagnostic output directory: %v", err)
	}
	trace, err := os.Create(filepath.Join(output, "trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := trace.Close(); err != nil {
			t.Error(err)
		}
	}()
	encoder := json.NewEncoder(trace)
	record := func(value any) {
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{ModelsDir: models, ParakeetModel: model.Name, VADThreshold: threshold, VADMinSpeechDuration: float64(minSpeech) / 1000, VADSilenceDuration: float64(silence) / 1000, VADPreRollMS: preRoll}
	policy := endpoint.FromConfig(cfg, false)
	// Keep the saved recording's exhausted boundary explicit, but never claim
	// that EOF finalization was the live microphone's endpoint.
	policy.MaxUtterance, policy.StartTimeout = 0, 0
	manifest := map[string]any{
		"diagnostic": "saved_production_stt_loop", "physical_acceptance": false,
		"wav": wav, "wav_sha256": fmt.Sprintf("%x", sha256.Sum256(bytes)), "input_samples": len(pcm), "sample_rate": rate,
		"start_sample": start, "read_chunk_samples": chunk, "reported_gain": gain,
		"gain_provenance": "declared constant; original live gain not reconstructed", "read_provenance": "declared fixed maximum; original live reads not reconstructed",
		"pacing": "accelerated; wall-clock caps disabled; explicit saved EOF", "model": model.Name, "models_dir": models,
		"vad_threshold": threshold, "vad_min_speech_ms": minSpeech, "silence_ms": silence, "pre_roll_ms": preRoll,
		"gate_policy_trace": "shadow evaluation using production functions; actual segment operations and decoder input observed",
	}
	writeJSON := func(name string, value any) {
		t.Helper()
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, name), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON("manifest.json", manifest)
	frames := &savedLoopFrames{pcm: pcm, pos: start, chunk: chunk, gain: gain}
	vad, err := audio.NewVAD(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer vad.Delete()
	provider, err := NewParakeetSTT(cfg, audio.NewFinalizable(frames), vad)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Delete()
	seg := &savedLoopSegmenter{segmenter: newVADSegmenter(vad, preRollSamplesFromMS(preRoll)), frames: frames, track: newSpeechTracker(), policy: policy, record: record}
	decodes := 0
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	// Synchronous execution keeps all diagnostic writes on this test goroutine.
	// Advisory levels are droppable; the small finite set of phases fits here.
	events := make(chan Event, 64)
	runOfflineLoop(ctx, offlineLoopDeps{frames: frames, seg: seg, policy: policy, transcribe: func(samples []float32) (string, error) {
		decodes++
		name := fmt.Sprintf("decode-%02d", decodes)
		// f32le retains the decoder's exact float bits; WAV is listening/export
		// convenience and may quantize. Neither is substituted into production.
		data := make([]byte, len(samples)*4)
		for i, sample := range samples {
			binary.LittleEndian.PutUint32(data[4*i:], math.Float32bits(sample))
		}
		if err := os.WriteFile(filepath.Join(output, name+".f32le"), data, 0o600); err != nil {
			return "", err
		}
		if err := audio.WriteWAVFloat32(filepath.Join(output, name+".wav"), audio.SampleRate, samples); err != nil {
			return "", err
		}
		text, err := provider.transcribe(samples)
		record(map[string]any{"op": "decode", "file": name + ".f32le", "sha256": fmt.Sprintf("%x", sha256.Sum256(data)), "samples": len(samples), "source_read_end": frames.pos, "source_eof": frames.eof, "last_segment_operation": seg.lastOp, "shadow_policy_reason": seg.policy.Decide(seg.track.observation(false, false)).Reason, "transcript": text, "error": fmt.Sprint(err)})
		return text, err
	}}, events)
	close(events)
	var final Event
	for event := range events {
		record(map[string]any{"op": "event", "type": fmt.Sprintf("%T", event), "event": event})
		switch e := event.(type) {
		case FinalTranscript, Timeout:
			final = event
		case Failure:
			t.Fatal(e.Err)
		}
	}
	if ctx.Err() != nil || final == nil {
		t.Fatalf("diagnostic did not finish: terminal=%v err=%v", final, ctx.Err())
	}
	writeJSON("result.json", map[string]any{"terminal": final, "terminal_type": fmt.Sprintf("%T", final), "decode_calls": decodes, "consumed_through_sample": frames.pos, "remaining_samples": len(pcm) - frames.pos, "saved_eof_reached": frames.eof, "physical_acceptance": false})
	t.Logf("saved-loop diagnostic artifacts: %s; terminal=%+v consumed=%d/%d gain=%g chunk=%d", output, final, frames.pos, len(pcm), gain, chunk)
}

type savedLoopFrames struct {
	pcm        []float32
	pos, chunk int
	gain       float64
	eof        bool
}

func (f *savedLoopFrames) ReadFrame(ctx context.Context) (audio.Frame, error) {
	if err := ctx.Err(); err != nil {
		return audio.Frame{}, err
	}
	if f.pos == len(f.pcm) {
		f.eof = true
		return audio.Frame{SourceKind: audio.SourceLive, Final: true}, nil
	}
	end := min(len(f.pcm), f.pos+f.chunk)
	samples := f.pcm[f.pos:end]
	f.pos = end
	return audio.Frame{Samples: samples, SampleRate: audio.SampleRate, Channels: 1, Duration: audio.SamplesDuration(len(samples)), SourceKind: audio.SourceLive}, nil
}
func (f *savedLoopFrames) Close() error       { return nil }
func (f *savedLoopFrames) InputGain() float64 { return f.gain }

type savedLoopSegmenter struct {
	segmenter
	frames *savedLoopFrames
	track  *speechTracker
	gate   noiseGate
	policy endpoint.Policy
	record func(any)
	lastOp string
	popped bool
}

func (s *savedLoopSegmenter) AcceptWaveform(samples []float32) {
	// If capture continues after segments were drained, finalize(false) did
	// not produce a terminal transcript and reset the production tracker.
	if s.popped {
		s.track.reset()
		s.popped = false
		s.lastOp = ""
	}
	s.segmenter.AcceptWaveform(samples)
	neural := s.segmenter.IsSpeech()
	s.gate.minRMS = speechFloor(s.frames.gain)
	d := s.gate.decide(samples, neural)
	if !d.drop || s.track.detected {
		s.track.observe(d.speech(s.track.detected), audio.SamplesDuration(len(samples)))
	}
	observation := s.track.observation(false, false)
	decision := s.policy.Decide(observation)
	s.record(map[string]any{"op": "frame", "start_sample": s.frames.pos - len(samples), "end_sample": s.frames.pos, "rms": frameRMS(samples), "gain": s.frames.gain, "floor": s.gate.minRMS, "neural": neural, "queued_segment": s.segmenter.HasSegments(), "shadow_voice": d.voice, "shadow_continuation": d.continuation, "shadow_drop": d.drop, "shadow_has_speech": s.track.detected, "shadow_speech_ns": observation.SpeechSeen, "shadow_silence_ns": observation.TrailingSilence, "shadow_policy": decision.Kind.String(), "shadow_reason": decision.Reason})
}

func (s *savedLoopSegmenter) Reset() {
	s.record(map[string]any{"op": "segment_reset", "source_read_end": s.frames.pos})
	// Production also resets its speech tracker when policy rejects a short
	// utterance. A pure-noise reset deliberately leaves gate learning intact.
	if s.policy.Decide(s.track.observation(false, false)).Kind == endpoint.TooShort {
		s.track.reset()
	}
	s.segmenter.Reset()
}

func (s *savedLoopSegmenter) Flush() {
	s.lastOp = "flush"
	s.record(map[string]any{"op": "segment_flush", "source_read_end": s.frames.pos, "source_eof": s.frames.eof})
	s.segmenter.Flush()
}

func (s *savedLoopSegmenter) NextSegment() ([]float32, bool) {
	samples, ok := s.segmenter.NextSegment()
	if ok {
		s.popped = true
		s.record(map[string]any{"op": "next_segment", "source_read_end": s.frames.pos, "samples": len(samples), "preceded_by_flush": s.lastOp == "flush"})
		if s.lastOp != "flush" {
			s.lastOp = "queued_segment"
		}
	}
	return samples, ok
}
