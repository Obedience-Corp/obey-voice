package pipeline

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// describedTTS names itself the way the shipped providers do, so the turn's
// metrics can record which engine produced the audio.
type describedTTS struct {
	fakeTTS
	name string
	tier string
}

func (d *describedTTS) ProviderName() string { return d.name }
func (d *describedTTS) ModelTier() string    { return d.tier }

// TestTurnMetricsRecordEverySegment is the regression guard for the gap this
// instrumentation closes: FirstSegmentElapsed and FirstAudioReadyElapsed are
// stamped once, so a reply whose later sentences are the slow part used to
// read as fast. Every segment must appear, in playback order, with the
// synthesis cost and audio duration needed to derive a realtime factor.
func TestTurnMetricsRecordEverySegment(t *testing.T) {
	provider := &describedTTS{name: "qwen3-tts", tier: "1.7b"}
	player := newFakePlayer(5 * time.Millisecond)
	player.audioSeconds = 2.0
	defer player.Close()

	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	chunks := make(chan string, 3)
	done := make(chan brain.StreamResult, 1)
	chunks <- standaloneSentences[0] + " "
	chunks <- standaloneSentences[1] + " "
	chunks <- standaloneSentences[2]
	close(chunks)
	done <- brain.StreamResult{}
	close(done)

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, _, err := p.streamResponse(ctx, cancel, &brain.Stream{Chunks: chunks, Done: done}, false, metrics, turn, nil); err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	snap := metrics.snapshot()
	if len(snap.Segments) != 3 {
		t.Fatalf("Segments = %d, want one per sentence (3): %+v", len(snap.Segments), snap.Segments)
	}
	for i, seg := range snap.Segments {
		if seg.Index != i {
			t.Errorf("segment %d has Index %d; segments must arrive in playback order", i, seg.Index)
		}
		if seg.SynthElapsed <= 0 {
			t.Errorf("segment %d SynthElapsed = %v, want a positive duration", i, seg.SynthElapsed)
		}
		if seg.EngineElapsed <= 0 {
			t.Errorf("segment %d EngineElapsed = %v, want synthesis timed on its own", i, seg.EngineElapsed)
		}
		// Engine time is a subset of the total, never larger: the total adds
		// the wait behind whatever was already playing.
		if seg.EngineElapsed > seg.SynthElapsed {
			t.Errorf("segment %d engine %v exceeds total %v", i, seg.EngineElapsed, seg.SynthElapsed)
		}
		if seg.AudioSeconds != 2.0 {
			t.Errorf("segment %d AudioSeconds = %v, want 2.0 from the player", i, seg.AudioSeconds)
		}
		if seg.Chars == 0 {
			t.Errorf("segment %d Chars = 0, want the spoken text length", i)
		}
		if seg.Provider != "qwen3-tts" || seg.Tier != "1.7b" || seg.Place != tts.PlaceLocal {
			t.Errorf("segment %d identity = %q/%q/%q, want qwen3-tts/1.7b/local", i, seg.Provider, seg.Tier, seg.Place)
		}
		if seg.SynthQueuedElapsed <= 0 || seg.SynthStartedElapsed <= 0 || seg.SynthEndedElapsed <= 0 || seg.PlaybackStartedElapsed <= 0 || seg.PlaybackEndedElapsed <= 0 {
			t.Errorf("segment %d missing monotonic boundaries: %+v", i, seg)
		}
		if rtf := seg.RealtimeFactor(); rtf <= 0 {
			t.Errorf("segment %d RealtimeFactor() = %v, want it derivable", i, rtf)
		}
	}

	if snap.TTSProvider != "qwen3-tts" || snap.TTSTier != "1.7b" {
		t.Errorf("TTS identity = %q/%q, want qwen3-tts/1.7b", snap.TTSProvider, snap.TTSTier)
	}
	if snap.TTSFallbacks != 0 {
		t.Errorf("TTSFallbacks = %d, want 0 on a clean turn", snap.TTSFallbacks)
	}
	if snap.StallWatchdogFired {
		t.Error("StallWatchdogFired = true on a turn that played normally")
	}
	if snap.FirstAudioGrace <= 0 {
		t.Error("FirstAudioGrace = 0; the stall budget must be recorded beside the measurement")
	}
}

func TestSegmentMetricRealtimeFactor(t *testing.T) {
	tests := []struct {
		name string
		seg  events.SegmentMetric
		want float64
	}{
		{
			// F7's measured 1.7b figure: 6.1s of synthesis for 3.337s of audio.
			name: "slower than realtime",
			seg:  events.SegmentMetric{EngineElapsed: 6100 * time.Millisecond, SynthElapsed: 6100 * time.Millisecond, AudioSeconds: 3.337},
			want: 1.8280,
		},
		{
			// Kokoro's measured 1.32x realtime is an RTF below 1.
			name: "faster than realtime",
			seg:  events.SegmentMetric{EngineElapsed: 901 * time.Millisecond, SynthElapsed: 901 * time.Millisecond, AudioSeconds: 1.185},
			want: 0.7603,
		},
		{
			// The reason the split exists: a segment synthesized in 2s but
			// queued behind 8s of playback is a 2x engine, not a 10x one.
			// Charging it the wait is how a healthy engine reads as broken.
			name: "queue wait is not charged to the engine",
			seg:  events.SegmentMetric{EngineElapsed: 2 * time.Second, SynthElapsed: 10 * time.Second, AudioSeconds: 1.0},
			want: 2.0,
		},
		{
			// Records written before the split carry only the total; using it
			// beats reporting nothing, and the field's absence is visible.
			name: "pre-split record falls back to the total",
			seg:  events.SegmentMetric{SynthElapsed: 2 * time.Second, AudioSeconds: 1.0},
			want: 2.0,
		},
		{
			// A segment that produced no audio has no factor. Returning 0
			// rather than dividing keeps a silent turn out of the aggregate
			// instead of poisoning it with an infinity.
			name: "no audio",
			seg:  events.SegmentMetric{SynthElapsed: time.Second},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.seg.RealtimeFactor(); math.Abs(got-tt.want) > 1e-4 {
				t.Errorf("RealtimeFactor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTTSIdentityIsEmptyForAnUndescribedProvider(t *testing.T) {
	// Guessing an engine name would put a claim in a latency artifact that
	// nothing confirmed. Place is the exception: an engine with no LAN
	// transport synthesized in this process, which is a fact, not a guess.
	p := &Pipeline{TTS: &fakeTTS{}, Events: events.NewBus()}
	id := p.ttsIdentity()
	if id.provider != "" || id.tier != "" {
		t.Errorf("ttsIdentity() = %q/%q, want empty for a provider that does not describe itself", id.provider, id.tier)
	}
	if id.place != tts.PlaceLocal || id.workerStreaming {
		t.Errorf("ttsIdentity() place = %q streaming = %v, want %q/false", id.place, id.workerStreaming, tts.PlaceLocal)
	}
}

// TestTTSIdentityRecordsTopology is the E9 provenance guard: an artifact that
// records provider and tier but not where they ran cannot be compared across
// the two topologies the fleet design exists to choose between.
func TestTTSIdentityRecordsTopology(t *testing.T) {
	provider := &remoteDescribedTTS{
		describedTTS: describedTTS{name: "qwen3-tts", tier: "1.7b"},
		place:        tts.PlaceLAN,
		streaming:    true,
	}
	p := &Pipeline{TTS: provider, Events: events.NewBus()}
	id := p.ttsIdentity()
	if id.place != tts.PlaceLAN || !id.workerStreaming {
		t.Fatalf("ttsIdentity() place = %q streaming = %v, want %q/true", id.place, id.workerStreaming, tts.PlaceLAN)
	}

	m := newTurnMetrics()
	m.ttsEngine = id
	snap := m.snapshot()
	if snap.TTSPlace != tts.PlaceLAN || !snap.TTSWorkerStreaming {
		t.Errorf("snapshot place = %q streaming = %v, want %q/true", snap.TTSPlace, snap.TTSWorkerStreaming, tts.PlaceLAN)
	}
	if snap.TTSProvider != "qwen3-tts" || snap.TTSTier != "1.7b" {
		t.Errorf("snapshot engine = %q/%q, want qwen3-tts/1.7b", snap.TTSProvider, snap.TTSTier)
	}
}

// remoteDescribedTTS speaks from another computer, the way a Qwen provider
// dialing speech-serve does.
type remoteDescribedTTS struct {
	describedTTS
	place     string
	streaming bool
}

func (r *remoteDescribedTTS) SpeechPlace() string   { return r.place }
func (r *remoteDescribedTTS) WorkerStreaming() bool { return r.streaming }

var (
	_ tts.Describer         = (*describedTTS)(nil)
	_ tts.TopologyDescriber = (*remoteDescribedTTS)(nil)
)
