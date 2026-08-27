package transcript

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// TestMetricsRecordCarriesSegmentCost is what makes a real conversation
// measurable: without it, per-segment synthesis cost was computed every turn,
// shown once in the activity feed, and dropped. A field debug session can only
// read what the transcript kept.
func TestMetricsRecordCarriesSegmentCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	bus := events.NewBus()
	w.Attach(bus)

	bus.Emit(events.TurnMetrics{
		Outcome:                "completed",
		FirstAudioReadyElapsed: 7100 * time.Millisecond,
		PlaybackStartElapsed:   7100 * time.Millisecond,
		TTSProvider:            "qwen3-tts",
		TTSTier:                "1.7b",
		TTSFallbacks:           1,
		FirstAudioGrace:        47 * time.Second,
		StallWatchdogFired:     true,
		Segments: []events.SegmentMetric{
			{Index: 0, Chars: 40, EngineElapsed: 6100 * time.Millisecond, SynthElapsed: 6100 * time.Millisecond, AudioSeconds: 3.337, PlayElapsed: 3300 * time.Millisecond},
			// Segment 1 waited 3.3s behind segment 0's playback.
			{Index: 1, Chars: 50, EngineElapsed: 8900 * time.Millisecond, SynthElapsed: 12200 * time.Millisecond, AudioSeconds: 4.777, Fallback: true},
		},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var metrics *record
	for _, r := range readRecords(t, path) {
		if r.Type == "metrics" {
			m := r
			metrics = &m
		}
		if r.Type == "header" && r.V != Version {
			t.Errorf("header v = %d, want %d", r.V, Version)
		}
	}
	if metrics == nil {
		t.Fatal("no metrics record was written")
	}

	if metrics.TTSProvider != "qwen3-tts" || metrics.TTSTier != "1.7b" {
		t.Errorf("engine = %q/%q, want qwen3-tts/1.7b", metrics.TTSProvider, metrics.TTSTier)
	}
	if metrics.TTSFallbacks != 1 {
		t.Errorf("tts_fallbacks = %d, want 1", metrics.TTSFallbacks)
	}
	// The stall budget belongs beside the measurement: under a 47s grace, a
	// 30s wait is indistinguishable from a hang until the watchdog fires.
	if metrics.FirstAudioGraceS != 47 {
		t.Errorf("first_audio_grace_s = %v, want 47", metrics.FirstAudioGraceS)
	}
	if !metrics.StallWatchdogFired {
		t.Error("stall_watchdog_fired = false, want true")
	}

	if len(metrics.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(metrics.Segments))
	}
	first := metrics.Segments[0]
	if first.EngineS != 6.1 || math.Abs(first.AudioS-3.337) > 1e-9 {
		t.Errorf("segment 0 = %+v, want engine 6.1s over 3.337s of audio", first)
	}
	// The queue wait is written separately so a large synth_s can be
	// attributed rather than blamed on the engine.
	if got := metrics.Segments[1].QueueS; math.Abs(got-3.3) > 1e-9 {
		t.Errorf("segment 1 queue_s = %v, want 3.3", got)
	}
	// rtf is written rather than left to the reader so every consumer agrees
	// on how it was derived.
	if math.Abs(first.RTF-1.8280) > 1e-4 {
		t.Errorf("segment 0 rtf = %v, want 1.828", first.RTF)
	}
	if !metrics.Segments[1].Fallback {
		t.Error("segment 1 fallback = false, want true")
	}
}

// TestMetricsRecordOmitsSegmentsWhenThereAreNone keeps a text-only or silent
// turn from growing a null field on every line.
func TestMetricsRecordOmitsSegmentsWhenThereAreNone(t *testing.T) {
	if got := segmentRecords(nil); got != nil {
		t.Errorf("segmentRecords(nil) = %v, want nil", got)
	}
}
