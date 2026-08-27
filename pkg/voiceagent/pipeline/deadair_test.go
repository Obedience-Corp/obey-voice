package pipeline

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// applyStarted and applyFinished drive the two playback events the turn loop
// serializes, without standing up a whole pipeline.
func applyStarted(t *testing.T, p *Pipeline, m *turnMetrics, index int) {
	t.Helper()
	p.applyPlaybackEvent(playbackEvent{kind: playbackStarted, index: index, sentence: "s"}, m, new(atomic.Int64))
}

func applyFinished(t *testing.T, p *Pipeline, m *turnMetrics, index int) {
	t.Helper()
	p.applyPlaybackEvent(playbackEvent{kind: playbackFinished, index: index, sentence: "s"}, m, new(atomic.Int64))
}

// TestDeadAirCountsSilenceBetweenSegments covers what no watchdog could see:
// the stall watchdog returns as soon as the FIRST frame is audible, so a turn
// that starts speaking and then pauses between every sentence registered as
// completely healthy.
func TestDeadAirCountsSilenceBetweenSegments(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	m := newTurnMetrics()

	applyStarted(t, p, m, 0)
	applyFinished(t, p, m, 0)
	// The synthesizer is still working on segment 1; the player is idle.
	gap := 40 * time.Millisecond
	time.Sleep(gap)
	applyStarted(t, p, m, 1)

	got := m.snapshot().DeadAirElapsed
	if got < gap {
		t.Fatalf("DeadAirElapsed = %v, want at least the %v the player sat idle", got, gap)
	}
	if got > 10*gap {
		t.Fatalf("DeadAirElapsed = %v, implausibly larger than the %v gap", got, gap)
	}
}

// TestDeadAirExcludesSilenceAfterTheReply keeps the metric honest: the pause
// after the last sentence is the turn ending, not a gap inside it.
func TestDeadAirExcludesSilenceAfterTheReply(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	m := newTurnMetrics()

	applyStarted(t, p, m, 0)
	applyFinished(t, p, m, 0)
	time.Sleep(30 * time.Millisecond) // turn is over; nothing follows

	if got := m.snapshot().DeadAirElapsed; got != 0 {
		t.Fatalf("DeadAirElapsed = %v, want 0 when no segment followed", got)
	}
}

// TestDeadAirIsZeroWhenPlaybackIsContinuous is the healthy shape: the next
// segment is already synthesized when the previous finishes, which is what an
// engine faster than realtime produces.
func TestDeadAirIsZeroWhenPlaybackIsContinuous(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	m := newTurnMetrics()

	for i := range 4 {
		applyStarted(t, p, m, i)
		applyFinished(t, p, m, i)
	}

	// Back-to-back events leave only scheduling noise, well under a
	// millisecond; anything larger would mean the accounting is running.
	if got := m.snapshot().DeadAirElapsed; got > time.Millisecond {
		t.Fatalf("DeadAirElapsed = %v, want ~0 for continuous playback", got)
	}
}

func TestWarnIfSynthesisCannotKeepUpFiresOncePerSession(t *testing.T) {
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Events: bus}

	// 6s of silence in a 12s reply: half the speaking time.
	bad := events.TurnMetrics{
		TTSProvider:             "qwen3-tts",
		TTSTier:                 "1.7b",
		DeadAirElapsed:          6 * time.Second,
		PlaybackStartElapsed:    10 * time.Second,
		PlaybackCompleteElapsed: 22 * time.Second,
	}
	p.warnIfSynthesisCannotKeepUp(bad)
	p.warnIfSynthesisCannotKeepUp(bad)

	if len(infos) != 1 {
		t.Fatalf("got %d notices, want exactly 1 per session: %v", len(infos), infos)
	}
	// The message has to name the engine, or it is not actionable.
	if want := "qwen3-tts 1.7b"; !strings.Contains(infos[0], want) {
		t.Errorf("notice = %q, want it to name %q", infos[0], want)
	}
}

func TestWarnIfSynthesisCannotKeepUpStaysQuietWhenItKeepsUp(t *testing.T) {
	tests := []struct {
		name string
		m    events.TurnMetrics
	}{
		{
			name: "no dead air",
			m: events.TurnMetrics{
				PlaybackStartElapsed:    time.Second,
				PlaybackCompleteElapsed: 20 * time.Second,
			},
		},
		{
			// A little slack between sentences is normal and must not nag.
			name: "dead air below the share threshold",
			m: events.TurnMetrics{
				DeadAirElapsed:          time.Second,
				PlaybackStartElapsed:    time.Second,
				PlaybackCompleteElapsed: 21 * time.Second,
			},
		},
		{
			// Barge-in truncates playback, which inflates the apparent share.
			// Blaming the engine for the user interrupting would be wrong.
			name: "interrupted turn",
			m: events.TurnMetrics{
				Interrupted:             true,
				DeadAirElapsed:          9 * time.Second,
				PlaybackStartElapsed:    time.Second,
				PlaybackCompleteElapsed: 11 * time.Second,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := events.NewBus()
			var infos []string
			events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
			p := &Pipeline{Events: bus}
			p.warnIfSynthesisCannotKeepUp(tt.m)
			if len(infos) != 0 {
				t.Fatalf("got %v, want no notice", infos)
			}
		})
	}
}
