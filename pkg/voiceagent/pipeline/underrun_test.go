package pipeline

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Incremental playback makes a new failure visible: the reply breaks up
// *inside* a sentence. Dead-air accounting cannot see it — the player never
// goes idle between segments — so the count has to ride the segment metric to
// the turn.
func TestUnderrunsReachTheTurnMetrics(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	m := newTurnMetrics()

	for i, count := range []int{2, 0, 1} {
		p.applyPlaybackEvent(playbackEvent{
			kind:     playbackFinished,
			index:    i,
			sentence: "a sentence",
			result:   audio.PlaybackResult{AudioSeconds: 1, Underruns: count},
		}, m, new(atomic.Int64))
	}

	snapshot := m.snapshot()
	if snapshot.Underruns != 3 {
		t.Fatalf("TurnMetrics.Underruns = %d, want 3", snapshot.Underruns)
	}
	if len(snapshot.Segments) != 3 {
		t.Fatalf("got %d segments", len(snapshot.Segments))
	}
	if snapshot.Segments[0].Underruns != 2 || snapshot.Segments[2].Underruns != 1 {
		t.Fatalf("per-segment underruns = %d/%d/%d, want 2/0/1",
			snapshot.Segments[0].Underruns, snapshot.Segments[1].Underruns, snapshot.Segments[2].Underruns)
	}
}

// One break-up inside a sentence is worse than a quarter of the turn spent in
// silence between them, so it says so on its own rather than waiting for a
// share threshold that measures a different symptom.
func TestWarnIfSynthesisCannotKeepUpSpeaksForASingleUnderrun(t *testing.T) {
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Events: bus}

	p.warnIfSynthesisCannotKeepUp(events.TurnMetrics{
		TTSProvider:             "qwen3-tts",
		TTSTier:                 "0.6b",
		Underruns:               1,
		PlaybackStartElapsed:    time.Second,
		PlaybackCompleteElapsed: 9 * time.Second,
	})

	if len(infos) != 1 {
		t.Fatalf("got %d notices, want 1: %v", len(infos), infos)
	}
	if !strings.Contains(infos[0], "broke up 1 time") {
		t.Errorf("notice = %q, want it to describe the break-up", infos[0])
	}
	// It has to name the way out, and for streaming that is the config key.
	if !strings.Contains(infos[0], "qwen_tts_stream") {
		t.Errorf("notice = %q, want it to name the setting that stops it", infos[0])
	}
}

func TestWarnIfSynthesisCannotKeepUpIgnoresUnderrunsOnAnInterruptedTurn(t *testing.T) {
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Events: bus}

	p.warnIfSynthesisCannotKeepUp(events.TurnMetrics{
		Interrupted:             true,
		Underruns:               4,
		PlaybackStartElapsed:    time.Second,
		PlaybackCompleteElapsed: 3 * time.Second,
	})
	if len(infos) != 0 {
		t.Fatalf("blamed the engine for a barge-in: %v", infos)
	}
}

// A streamed segment is audible long before synthesis ends, so PlayStream's
// return is no longer where the engine finished. The realtime factor's
// numerator has to come from the player's own stamp, or every streamed segment
// reports a fraction of its true cost and an engine that cannot keep up looks
// like one that can.
func TestMeasuredEngineElapsedPrefersThePlayersStamp(t *testing.T) {
	synthStarted := time.Now()
	caller := 40 * time.Millisecond

	streamed := audio.PlaybackResult{SynthesisEndedAt: synthStarted.Add(3 * time.Second)}
	if got := measuredEngineElapsed(synthStarted, caller, streamed); got < 2900*time.Millisecond {
		t.Fatalf("engineElapsed = %v, want the player's ~3s stamp, not the caller's %v", got, caller)
	}

	// No stamp (an interrupted segment, or a player that reports none) keeps
	// the caller's measurement rather than inventing one.
	if got := measuredEngineElapsed(synthStarted, caller, audio.PlaybackResult{}); got != caller {
		t.Fatalf("engineElapsed = %v, want the caller's %v when the player stamped nothing", got, caller)
	}
	// A stamp that predates the request is nonsense; fall back rather than
	// report a negative cost.
	stale := audio.PlaybackResult{SynthesisEndedAt: synthStarted.Add(-time.Second)}
	if got := measuredEngineElapsed(synthStarted, caller, stale); got != caller {
		t.Fatalf("engineElapsed = %v, want the caller's %v for a stamp before the request", got, caller)
	}
}

// The stall budget is set on the stream the provider returned, and that is the
// object the player pumps. Marking a stream incremental must not wrap or
// replace it, or the budget would be armed on something nothing reads.
func TestStallBudgetSurvivesAnIncrementalStream(t *testing.T) {
	p := &Pipeline{TTS: &graceTTS{grace: 9 * time.Second}}
	stream := audio.NewPCMStream(context.Background())
	stream.SetIncremental(true)

	p.boundPrimarySilence(stream, 40)

	if got := stream.StallBudget(); got != 9*time.Second {
		t.Fatalf("StallBudget() = %v on an incremental stream, want the provider grace 9s", got)
	}
	if !stream.Incremental() {
		t.Fatal("arming the budget cleared the incremental flag")
	}
}
