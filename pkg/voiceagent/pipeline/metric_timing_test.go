package pipeline

import (
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func TestTimingKeepsRawOffsetsAndNormalizesDelayedInput(t *testing.T) {
	start := time.Now()
	m := &turnMetrics{start: start, sttFinal: start.Add(3 * time.Second),
		modelComplete: start.Add(4 * time.Second), firstAudioReady: start.Add(5 * time.Second),
		playbackStart: start.Add(6 * time.Second), playbackComplete: start.Add(9 * time.Second),
		audibleSink: audio.SinkLocalSpeaker}
	m.markBargeIn(start.Add(2 * time.Second))
	got := m.snapshot()
	timing := got.Timing
	if timing.Origin != "finalized_input" || timing.OriginOffsetMS != 3000 {
		t.Fatalf("origin = %+v", timing)
	}
	for _, check := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"raw final", timing.Raw.FinalizedInputMS, 3000},
		{"raw model", timing.Raw.ModelCompleteMS, 4000},
		{"model", timing.Normalized.ModelCompleteMS, 1000},
		{"audio", timing.Normalized.FirstAudioReadyMS, 2000},
		{"start", timing.Normalized.PlaybackStartMS, 3000},
		{"done", timing.Normalized.PlaybackCompleteMS, 6000},
		{"early barge-in", timing.Normalized.BargeInMS, -1000},
		{"finalized origin", timing.Normalized.FinalizedInputMS, 0},
	} {
		if check.got == nil || *check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
	if timing.Normalized.FirstModelChunkMS != nil || timing.Normalized.StreamDrainMS != nil {
		t.Fatal("unobserved boundary was invented")
	}
	if got.ModelCompleteElapsed != 4*time.Second || got.PlaybackCompleteElapsed != 6*time.Second {
		t.Fatal("legacy timing fields were reinterpreted")
	}
}

func TestTimingDeclaresFallbackAndSeparatesTransportDrain(t *testing.T) {
	start := time.Now()
	m := &turnMetrics{start: start, modelComplete: start, playbackStart: start.Add(time.Second), playbackComplete: start.Add(2 * time.Second), audibleSink: audio.SinkStreamClient}
	got := m.snapshot()
	if got.Timing.Origin != "turn_start" || got.Timing.Raw.FinalizedInputMS != nil {
		t.Fatalf("fallback = %+v", got.Timing)
	}
	if got.Timing.Normalized.ModelCompleteMS == nil || *got.Timing.Normalized.ModelCompleteMS != 0 {
		t.Fatal("observed zero was lost")
	}
	if got.Timing.Normalized.PlaybackStartMS != nil || got.Timing.Normalized.PlaybackCompleteMS != nil || got.PlaybackCompleteElapsed != 0 {
		t.Fatal("transport drain claimed local playback")
	}
	if got.Timing.Normalized.StreamDrainMS == nil || *got.Timing.Normalized.StreamDrainMS != 2000 {
		t.Fatal("transport drain boundary missing")
	}
	m.audibleSink = audio.SinkNone
	if got := m.snapshot().Timing; got.Raw.StreamDrainMS != nil || got.Raw.PlaybackCompleteMS != nil {
		t.Fatal("no sink claimed playback")
	}
}
