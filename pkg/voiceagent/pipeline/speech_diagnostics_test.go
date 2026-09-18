package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

type describedErrorTTS struct {
	name string
	tier string
}

func (d *describedErrorTTS) Synthesize(context.Context, string) (*audio.PCMStream, error) {
	return nil, &tts.ProviderError{Provider: d.name, Kind: tts.ProviderErrorUnavailable, Err: errors.New("worker unavailable")}
}
func (d *describedErrorTTS) Available() bool                       { return true }
func (d *describedErrorTTS) ListVoices(string, string) []tts.Voice { return nil }
func (d *describedErrorTTS) ProviderName() string                  { return d.name }
func (d *describedErrorTTS) ModelTier() string                     { return d.tier }

type adviceTTS struct {
	describedTTS
	warm bool
}

func (a *adviceTTS) SpeechWarm() bool            { return a.warm }
func (a *adviceTTS) WholeSentencePlayback() bool { return true }

func TestSpeechDiagnosticsKeepsAmbiguousUnderrunUnknown(t *testing.T) {
	bus := events.NewBus()
	var got []events.SpeechDiagnostic
	events.Subscribe(bus, func(e events.SpeechDiagnostic) { got = append(got, e) })
	p := &Pipeline{Events: bus}
	p.warnIfSynthesisCannotKeepUp(events.TurnMetrics{
		Outcome: "completed",
		Segments: []events.SegmentMetric{{
			Provider: "qwen3-tts", AudioSeconds: 2, EngineElapsed: 3 * time.Second,
			Underruns: 1,
		}},
	})
	var observed events.SpeechDiagnostic
	for _, diagnostic := range got {
		if diagnostic.Symptom == "playback_gap" {
			observed = diagnostic
		}
	}
	if observed.Cause != speechGapUnknown || observed.Confidence != "unknown" || observed.ActionCode != "" {
		t.Fatalf("diagnostic = %+v, want unknown non-actionable observation", got)
	}
}

func TestFallbackSegmentRecordsActualProviderIdentity(t *testing.T) {
	bus := events.NewBus()
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		TTS:         &describedErrorTTS{name: "qwen3-tts", tier: "1.7b"},
		TTSFallback: &describedTTS{name: "kokoro"}, Player: player, Events: bus,
	}
	m := newTurnMetrics()
	stop := p.trackMetrics(m)
	defer stop()
	out := make(chan playbackEvent, 2)
	loopDone := make(chan struct{})
	if !p.synthesizeSegment(context.Background(), loopDone, "fallback sentence", 0, new(atomic.Bool), out, new(fallbackLatch)) {
		t.Fatal("synthesizeSegment returned false")
	}
	first := <-out
	p.applyPlaybackEvent(first, m, new(atomic.Int64))
	second := <-out
	p.applyPlaybackEvent(second, m, new(atomic.Int64))
	close(loopDone)
	segments := m.snapshot().Segments
	if len(segments) != 1 || !segments[0].Fallback || segments[0].Provider != "kokoro" {
		t.Fatalf("fallback segment = %+v, want actual kokoro identity with fallback=true", segments)
	}
}

func TestSpeechDiagnosticsRequiresWarmComparableTurnsAndDeduplicates(t *testing.T) {
	bus := events.NewBus()
	var got []events.SpeechDiagnostic
	events.Subscribe(bus, func(e events.SpeechDiagnostic) { got = append(got, e) })
	provider := &adviceTTS{describedTTS: describedTTS{name: "qwen3-tts", tier: "0.6b"}, warm: true}
	p := &Pipeline{TTS: provider, Events: bus}
	turn := events.TurnMetrics{
		Outcome: "completed", TTSWarm: true,
		Segments: []events.SegmentMetric{{
			Provider: "qwen3-tts", Tier: "0.6b", Place: tts.PlaceLocal,
			WorkerStreaming: true, WarmState: events.WarmStateWarm, WholeSentencePlayback: true,
			SynthStartedElapsed: time.Second, SynthEndedElapsed: 5 * time.Second,
			EngineElapsed: 4 * time.Second, AudioSeconds: 2,
			GapElapsed: time.Second, GapCause: speechGapSynthesis,
			Gaps: []events.SpeechGap{{StartElapsed: 5 * time.Second, EndElapsed: 6 * time.Second, Cause: speechGapSynthesis}},
		}},
	}
	for range 2 {
		p.warnIfSynthesisCannotKeepUp(turn)
	}
	for _, diagnostic := range got {
		if diagnostic.ActionCode != "" {
			t.Fatalf("advice fired before three turns: %+v", diagnostic)
		}
	}
	p.warnIfSynthesisCannotKeepUp(turn)
	p.warnIfSynthesisCannotKeepUp(turn)
	var advice []events.SpeechDiagnostic
	for _, diagnostic := range got {
		if diagnostic.ActionCode != "" {
			advice = append(advice, diagnostic)
		}
	}
	if len(advice) != 1 {
		t.Fatalf("got %d advice records, want one: %+v", len(advice), got)
	}
	if !strings.Contains(advice[0].Message, "Whole-sentence playback") {
		t.Errorf("advice = %q, want capability-based whole-sentence wording", advice[0].Message)
	}
}

func TestSpeechDiagnosticsResetOnActualConfigurationChange(t *testing.T) {
	bus := events.NewBus()
	var advice int
	events.Subscribe(bus, func(e events.SpeechDiagnostic) {
		if e.ActionCode != "" {
			advice++
		}
	})
	p := &Pipeline{Events: bus}
	turn := func(tier string) events.TurnMetrics {
		return events.TurnMetrics{Outcome: "completed", TTSWarm: true, Segments: []events.SegmentMetric{{
			Provider: "qwen3-tts", Tier: tier, Place: "local", WorkerStreaming: true, WarmState: events.WarmStateWarm, WholeSentencePlayback: true, SynthStartedElapsed: time.Second, SynthEndedElapsed: 5 * time.Second, EngineElapsed: 4 * time.Second,
			AudioSeconds: 2, GapElapsed: time.Second, GapCause: speechGapSynthesis,
			Gaps: []events.SpeechGap{{StartElapsed: 5 * time.Second, EndElapsed: 6 * time.Second, Cause: speechGapSynthesis}},
		}}}
	}
	for range 3 {
		p.warnIfSynthesisCannotKeepUp(turn("0.6b"))
	}
	for range 2 {
		p.warnIfSynthesisCannotKeepUp(turn("1.7b"))
	}
	if advice != 1 {
		t.Fatalf("configuration change consumed or duplicated advice: got %d", advice)
	}
}

func classifierTurn(tier string, streaming bool, gap time.Duration) events.TurnMetrics {
	turn := events.TurnMetrics{Outcome: "completed", Segments: []events.SegmentMetric{{
		Provider: "qwen3-tts", Tier: tier, Place: tts.PlaceLocal,
		WorkerStreaming: streaming, WarmState: events.WarmStateWarm,
		WholeSentencePlayback: true, EngineElapsed: 4 * time.Second,
		SynthStartedElapsed: time.Second, SynthEndedElapsed: 5 * time.Second,
		AudioSeconds: 2, GapElapsed: gap, GapCause: speechGapSynthesis,
	}}}
	if gap > 0 {
		turn.Segments[0].Gaps = []events.SpeechGap{{StartElapsed: time.Second, EndElapsed: time.Second + gap, Cause: speechGapSynthesis}}
	}
	return turn
}

func TestSpeechDiagnosticsHealthyZeroGapTurnsEnterWindow(t *testing.T) {
	bus := events.NewBus()
	var advice int
	events.Subscribe(bus, func(e events.SpeechDiagnostic) {
		if e.ActionCode != "" {
			advice++
		}
	})
	p := &Pipeline{Events: bus}
	p.warnIfSynthesisCannotKeepUp(classifierTurn("0.6b", true, 0))
	p.warnIfSynthesisCannotKeepUp(classifierTurn("0.6b", true, 0))
	p.warnIfSynthesisCannotKeepUp(classifierTurn("0.6b", true, 2*time.Second))
	if advice != 1 {
		t.Fatalf("advice=%d, want one after three comparable turns with aggregate gap", advice)
	}
}

func TestSpeechDiagnosticsSwitchBackDoesNotReuseSamples(t *testing.T) {
	bus := events.NewBus()
	var advice int
	events.Subscribe(bus, func(e events.SpeechDiagnostic) {
		if e.ActionCode != "" {
			advice++
		}
	})
	p := &Pipeline{Events: bus}
	for range 2 {
		p.warnIfSynthesisCannotKeepUp(classifierTurn("0.6b", true, time.Second))
	}
	for range 2 {
		p.warnIfSynthesisCannotKeepUp(classifierTurn("1.7b", true, time.Second))
	}
	p.warnIfSynthesisCannotKeepUp(classifierTurn("0.6b", true, time.Second))
	if advice != 0 {
		t.Fatalf("switch-back reused old samples and advised: %d", advice)
	}
}

func TestSpeechDiagnosticsMixedFallbackResetsActualConfig(t *testing.T) {
	p := &Pipeline{}
	turn := classifierTurn("0.6b", true, time.Second)
	for range 2 {
		p.warnIfSynthesisCannotKeepUp(turn)
	}
	mixed := turn
	mixed.Segments = []events.SegmentMetric{
		turn.Segments[0],
		{Provider: "kokoro", Place: tts.PlaceLocal, Fallback: true, WarmState: events.WarmStateWarm},
		turn.Segments[0],
	}
	p.warnIfSynthesisCannotKeepUp(mixed)
	p.warnIfSynthesisCannotKeepUp(turn)
	p.speechAdviceMu.Lock()
	w := p.speechAdvice[diagnosticKey("qwen3-tts", "0.6b", tts.PlaceLocal, true, "")]
	p.speechAdviceMu.Unlock()
	if w == nil {
		t.Fatal("missing Qwen window")
	}
	w.mu.Lock()
	got := len(w.samples)
	w.mu.Unlock()
	if got != 1 {
		t.Fatalf("samples after Qwen→Kokoro→Qwen switch = %d, want 1", got)
	}
}

func TestSpeechDiagnosticsNonStreamingHasNoWholeSentenceRemedy(t *testing.T) {
	bus := events.NewBus()
	var got events.SpeechDiagnostic
	events.Subscribe(bus, func(e events.SpeechDiagnostic) {
		if e.ActionCode != "" || e.Symptom == "synthesis_starvation" {
			got = e
		}
	})
	p := &Pipeline{Events: bus}
	for range 3 {
		p.warnIfSynthesisCannotKeepUp(classifierTurn("0.6b", false, time.Second))
	}
	if got.Symptom != "synthesis_starvation" || got.ActionCode != "" {
		t.Fatalf("diagnostic=%+v, want observation without remedy", got)
	}
}

func TestSpeechDiagnosticsReentrantEmissionDoesNotHoldStateLock(t *testing.T) {
	bus := events.NewBus()
	p := &Pipeline{Events: bus}
	done := make(chan struct{}, 1)
	var called bool
	events.Subscribe(bus, func(e events.SpeechDiagnostic) {
		if e.ActionCode != "" && !called {
			called = true
			p.warnIfSynthesisCannotKeepUp(classifierTurn("0.6b", true, time.Second))
			done <- struct{}{}
		}
	})
	for range 3 {
		p.warnIfSynthesisCannotKeepUp(classifierTurn("0.6b", true, time.Second))
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant callback blocked")
	}
}

func TestSpeechDiagnosticsSaturatedConfigStateDoesNotReadviseEvictedConfigs(t *testing.T) {
	bus := events.NewBus()
	var advice int
	events.Subscribe(bus, func(e events.SpeechDiagnostic) {
		if e.ActionCode != "" {
			advice++
		}
	})
	p := &Pipeline{Events: bus}
	for i := 0; i < speechConfigLimit; i++ {
		for range 3 {
			p.warnIfSynthesisCannotKeepUp(classifierTurn(fmt.Sprintf("tier-%d", i), true, time.Second))
		}
	}
	if advice != speechConfigLimit {
		t.Fatalf("initial advice=%d, want %d", advice, speechConfigLimit)
	}
	for range 3 {
		p.warnIfSynthesisCannotKeepUp(classifierTurn("tier-new", true, time.Second))
	}
	if advice != speechConfigLimit {
		t.Fatalf("saturated state re-advised after suppression: %d", advice)
	}
}

func TestSpeechDiagnosticsHealthyWindowDropsOldBadEvidence(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	bad := classifierTurn("0.6b", true, 0)
	bad.Segments[0].EngineElapsed = 10 * time.Second
	for range 2 {
		p.warnIfSynthesisCannotKeepUp(bad)
	}
	for range speechWindowLimit {
		healthy := classifierTurn("0.6b", true, 0)
		healthy.Segments[0].EngineElapsed = 2 * time.Second
		p.warnIfSynthesisCannotKeepUp(healthy)
	}
	p.speechAdviceMu.Lock()
	w := p.speechAdvice[diagnosticKey("qwen3-tts", "0.6b", tts.PlaceLocal, true, "")]
	p.speechAdviceMu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.samples) != speechWindowLimit {
		t.Fatalf("window size=%d, want %d", len(w.samples), speechWindowLimit)
	}
	for _, sample := range w.samples {
		if sample.rtf > qwenAdviceMinRTF {
			t.Fatal("old slow evidence remained in rolling window")
		}
	}
}

// Compile-time guard: diagnostics use the ordinary provider interface and do
// not require live workers or a model download.
var _ tts.Provider = (*adviceTTS)(nil)
