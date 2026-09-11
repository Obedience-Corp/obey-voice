package pipeline

import (
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

func TestWarnIfSynthesisCannotKeepUpRequiresSustainedComparableEvidence(t *testing.T) {
	bus := events.NewBus()
	var diagnostics []events.SpeechDiagnostic
	events.Subscribe(bus, func(e events.SpeechDiagnostic) { diagnostics = append(diagnostics, e) })
	p := &Pipeline{Events: bus}

	// Three warm, completed, comparable Qwen turns cross the policy gate. The
	// gap is explicitly attributed to synthesis; dead-air share alone is not.
	bad := events.TurnMetrics{
		Outcome: "completed", TTSWarm: true,
		Segments: []events.SegmentMetric{{Provider: "qwen3-tts", Tier: "1.7b", Place: "local", WorkerStreaming: true, WarmState: events.WarmStateWarm, WholeSentencePlayback: true, SynthStartedElapsed: time.Second, SynthEndedElapsed: 5 * time.Second, EngineElapsed: 4 * time.Second, AudioSeconds: 2, GapElapsed: time.Second, GapCause: speechGapSynthesis, Gaps: []events.SpeechGap{{StartElapsed: 5 * time.Second, EndElapsed: 6 * time.Second, Cause: speechGapSynthesis}}}},
	}
	for range 3 {
		p.warnIfSynthesisCannotKeepUp(bad)
	}

	var advice []events.SpeechDiagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.ActionCode != "" {
			advice = append(advice, diagnostic)
		}
	}
	if len(advice) != 1 {
		t.Fatalf("got %d applicable notices, want exactly 1: %v", len(advice), diagnostics)
	}
	// The message has to name the engine, or it is not actionable.
	if want := "qwen3-tts"; advice[0].Provider != want {
		t.Errorf("diagnostic = %+v, want provider %q", advice[0], want)
	}
}

func TestKokoroKeepUpAdviceIsSuppressedWithoutConsumingQwenNotice(t *testing.T) {
	for _, provider := range []string{"kokoro", "qwen3-tts"} {
		for _, symptom := range []string{"dead air", "underrun"} {
			t.Run(provider+"/"+symptom, func(t *testing.T) {
				bus := events.NewBus()
				var diagnostics []events.SpeechDiagnostic
				events.Subscribe(bus, func(e events.SpeechDiagnostic) { diagnostics = append(diagnostics, e) })
				p := &Pipeline{Events: bus}
				m := events.TurnMetrics{Outcome: "completed", TTSProvider: provider, TTSWarm: true}
				if provider == "qwen3-tts" {
					// The turn identity describes the primary, even when Kokoro
					// supplied some or all of its audio.
					m.TTSFallbacks = 1
				}
				if symptom == "dead air" {
					m.DeadAirElapsed = 6 * time.Second
					m.Segments = []events.SegmentMetric{{Provider: provider, WorkerStreaming: true, WarmState: events.WarmStateWarm, WholeSentencePlayback: true, SynthStartedElapsed: time.Second, SynthEndedElapsed: 5 * time.Second, EngineElapsed: 4 * time.Second, AudioSeconds: 2, GapElapsed: 6 * time.Second, GapCause: speechGapSynthesis, Gaps: []events.SpeechGap{{StartElapsed: 5 * time.Second, EndElapsed: 11 * time.Second, Cause: speechGapSynthesis}}}}
				} else {
					m.Underruns = 1
					m.Segments = []events.SegmentMetric{{Provider: provider, EngineElapsed: 4 * time.Second, AudioSeconds: 2, GapCause: speechGapUnknown, Underruns: 1}}
				}
				p.warnIfSynthesisCannotKeepUp(m)
				for _, d := range diagnostics {
					if d.ActionCode != "" {
						t.Fatalf("%s turn received applicable advice: %+v", provider, d)
					}
				}

				// Three later, entirely Qwen turns still get one applicable notice.
				m.TTSProvider, m.TTSFallbacks = "qwen3-tts", 0
				m.Segments = []events.SegmentMetric{{Provider: "qwen3-tts", Tier: "0.6b", Place: "local", WorkerStreaming: true, WarmState: events.WarmStateWarm, WholeSentencePlayback: true, SynthStartedElapsed: time.Second, SynthEndedElapsed: 5 * time.Second, EngineElapsed: 4 * time.Second, AudioSeconds: 2, GapElapsed: time.Second, GapCause: speechGapSynthesis, Gaps: []events.SpeechGap{{StartElapsed: 5 * time.Second, EndElapsed: 6 * time.Second, Cause: speechGapSynthesis}}}}
				for range 3 {
					p.warnIfSynthesisCannotKeepUp(m)
				}
				count := 0
				for _, d := range diagnostics {
					if d.ActionCode != "" {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("Qwen notice after Kokoro = %v, want one applicable notice", diagnostics)
				}
			})
		}
	}
}

func TestKokoroTurnStillPublishesGapMetrics(t *testing.T) {
	bus := events.NewBus()
	var metrics []events.TurnMetrics
	var infos []string
	events.Subscribe(bus, func(e events.TurnMetrics) { metrics = append(metrics, e) })
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Events: bus}
	m := newTurnMetrics()
	m.ttsEngine.provider = "kokoro"
	m.deadAir = 6 * time.Second
	m.underruns = 1
	m.segments = []events.SegmentMetric{{PlayElapsed: 6 * time.Second, Underruns: 1}}
	p.newTurnConductor(m).finish(TurnCompleted)

	if len(metrics) != 1 || metrics[0].DeadAirElapsed != m.deadAir || metrics[0].Underruns != 1 || len(metrics[0].Segments) != 1 {
		t.Fatalf("Kokoro gap metrics not retained: %+v", metrics)
	}
	if len(infos) != 0 {
		t.Fatalf("Kokoro turn received keep-up advice: %v", infos)
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
				Segments: []events.SegmentMetric{{PlayElapsed: 19 * time.Second}},
			},
		},
		{
			// A little slack between sentences is normal and must not nag.
			name: "dead air below the share threshold",
			m: events.TurnMetrics{
				DeadAirElapsed: time.Second,
				Segments:       []events.SegmentMetric{{PlayElapsed: 19 * time.Second}},
			},
		},
		{
			// Barge-in truncates playback, which inflates the apparent share.
			// Blaming the engine for the user interrupting would be wrong.
			name: "interrupted turn",
			m: events.TurnMetrics{
				Interrupted:    true,
				DeadAirElapsed: 9 * time.Second,
				Segments:       []events.SegmentMetric{{PlayElapsed: 2 * time.Second}},
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

func TestWarnIfSynthesisCannotKeepUpIgnoresFanoutDrainDeadAir(t *testing.T) {
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Events: bus}

	// Stage still playing while the host is idle: drain+dead-air must not
	// count as a keep-up failure. Underruns still warn on their own.
	p.warnIfSynthesisCannotKeepUp(events.TurnMetrics{
		AudibleSink:    "stream_client",
		DeadAirElapsed: 6 * time.Second,
		Segments:       []events.SegmentMetric{{PlayElapsed: time.Millisecond}},
	})
	if len(infos) != 0 {
		t.Fatalf("fanout drain dead-air warned: %v", infos)
	}

	p.warnIfSynthesisCannotKeepUp(events.TurnMetrics{
		AudibleSink: "stream_client",
		Underruns:   1,
		Segments:    []events.SegmentMetric{{PlayElapsed: time.Millisecond, Underruns: 1}},
	})
	if len(infos) != 0 {
		t.Fatalf("underrun on stream_client produced chat info: %v", infos)
	}
}
