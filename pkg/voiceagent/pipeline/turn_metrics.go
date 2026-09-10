package pipeline

import (
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// trackMetrics registers the turn's metrics for out-of-band stamping; the
// returned func clears the registration before terminal snapshotting.
func (p *Pipeline) trackMetrics(m *turnMetrics) func() {
	p.activeMu.Lock()
	p.activeMetrics = m
	p.activeMu.Unlock()
	return func() {
		p.activeMu.Lock()
		p.activeMetrics = nil
		p.activeMu.Unlock()
	}
}

type turnMetrics struct {
	start            time.Time
	sttFinal         time.Time
	firstModelChunk  time.Time
	modelComplete    time.Time
	firstSegment     time.Time
	firstAudioReady  time.Time
	playbackStart    time.Time
	playbackComplete time.Time
	firstToken       time.Time
	firstThink       time.Time
	firstSpeakable   time.Time
	firstAdmit       time.Time
	synthFirstFrame  time.Time
	ttfa             time.Time
	audibleSink      string
	sinkAtArm        string
	// bargeInMu guards bargeIn: the voice watcher stamps it from the turn
	// goroutine, keystroke barge-in from the TUI goroutine.
	bargeInMu   sync.Mutex
	bargeIn     time.Time
	interrupted bool
	degraded    bool
	// toolLeakLines counts lines the voice gate stripped from speech this
	// turn (WI-dc9e33 B4). Written only from the turn's own goroutine.
	toolLeakLines int
	// segments accumulates per-segment synthesis cost in playback order.
	// Written only from the turn loop goroutine, in applyPlaybackEvent.
	segments []events.SegmentMetric
	// ttsFallbacks counts segments the Kokoro fallback spoke after the
	// configured provider failed.
	ttsFallbacks int
	// ttsEngine and firstAudioGrace are stamped once per turn when the first
	// segment is synthesized, so a metrics record identifies the engine that
	// produced it, where it ran, and the stall budget it ran under.
	ttsEngine       ttsEngineIdentity
	firstAudioGrace time.Duration
	// stallWatchdogFired marks a turn the playback-stall watchdog cut short.
	stallWatchdogFired bool
	// lastPlaybackEnd is when the player last went idle mid-turn, and deadAir
	// accumulates the gaps until the next segment became audible. Both are
	// written only from the turn loop goroutine.
	lastPlaybackEnd time.Time
	deadAir         time.Duration
	// underruns totals the gaps *inside* segments, which deadAir cannot see.
	underruns int
	// thinkLeak counts think tags that still reached filterThink after the
	// brain split. Must stay 0 on the product path.
	thinkLeak int
}

func newTurnMetrics() *turnMetrics {
	return &turnMetrics{start: time.Now()}
}

func (m *turnMetrics) snapshot() events.TurnMetrics {
	playbackComplete := time.Duration(0)
	if m.audibleSink == audio.SinkLocalSpeaker {
		playbackComplete = m.elapsedFromEOU(m.playbackComplete)
	}
	audible := m.audibleSink
	if audible == "" {
		audible = audio.SinkNone
	}
	return events.TurnMetrics{
		Degraded:                m.degraded,
		Interrupted:             m.interrupted,
		STTFinalElapsed:         m.elapsed(m.sttFinal),
		FirstModelChunkElapsed:  m.elapsed(m.firstModelChunk),
		ModelCompleteElapsed:    m.elapsed(m.modelComplete),
		FirstSegmentElapsed:     m.elapsed(m.firstSegment),
		FirstAudioReadyElapsed:  m.elapsed(m.firstAudioReady),
		PlaybackStartElapsed:    m.elapsed(m.playbackStart),
		PlaybackCompleteElapsed: playbackComplete,
		BargeInElapsed:          m.elapsed(m.bargeInAt()),
		FirstTokenElapsed:       m.elapsedFromEOU(m.firstToken),
		FirstThinkElapsed:       m.elapsedFromEOU(m.firstThink),
		FirstSpeakableElapsed:   m.elapsedFromEOU(m.firstSpeakable),
		FirstAdmitElapsed:       m.elapsedFromEOU(m.firstAdmit),
		SynthFirstFrameElapsed:  m.elapsedFromEOU(m.synthFirstFrame),
		TTFAElapsed:             m.elapsedFromEOU(m.ttfa),
		AudibleSink:             audible,
		ThinkLeak:               m.thinkLeak,
		ToolLeakLines:           m.toolLeakLines,
		TTSProvider:             m.ttsEngine.provider,
		TTSTier:                 m.ttsEngine.tier,
		TTSPlace:                m.ttsEngine.place,
		TTSWorkerStreaming:      m.ttsEngine.workerStreaming,
		TTSFallbacks:            m.ttsFallbacks,
		FirstAudioGrace:         m.firstAudioGrace,
		StallWatchdogFired:      m.stallWatchdogFired,
		DeadAirElapsed:          m.deadAir,
		Underruns:               m.underruns,
		Segments:                append([]events.SegmentMetric(nil), m.segments...),
	}
}

// recordSegment appends one finished segment's synthesis cost. Called only
// from the turn loop goroutine, which is the sole writer of metrics.segments.
func (m *turnMetrics) recordSegment(event playbackEvent) {
	m.segments = append(m.segments, events.SegmentMetric{
		Index:         event.index,
		Chars:         len(event.sentence),
		EngineElapsed: event.engineElapsed,
		SynthElapsed:  event.synthElapsed,
		AudioSeconds:  event.result.AudioSeconds,
		PlayElapsed:   event.playElapsed,
		Fallback:      event.fallback,
		Interrupted:   event.result.Interrupted,
		Underruns:     event.result.Underruns,
	})
	m.underruns += event.result.Underruns
	if event.fallback {
		m.ttsFallbacks++
	}
}

// ttsEngineIdentity is everything a latency artifact needs to say which engine
// produced a turn: the provider, its model tier, the computer it ran on, and
// whether that computer's worker streams PCM during decode.
type ttsEngineIdentity struct {
	provider        string
	tier            string
	place           string
	workerStreaming bool
}

// ttsIdentity names the primary provider, its model tier and its topology for
// the turn's metrics. A provider that does not describe itself leaves provider
// and tier empty rather than guessing, so an artifact never claims an engine it
// did not confirm.
func (p *Pipeline) ttsIdentity() ttsEngineIdentity {
	primary, _ := p.ttsProviders()
	id := ttsEngineIdentity{
		place:           tts.SpeechPlaceOf(primary),
		workerStreaming: tts.WorkerStreamingOf(primary),
	}
	if d, ok := primary.(tts.Describer); ok {
		id.provider, id.tier = d.ProviderName(), d.ModelTier()
	}
	return id
}

// markBargeIn stamps the first barge-in moment; later marks are ignored.
func (m *turnMetrics) markBargeIn(at time.Time) {
	m.bargeInMu.Lock()
	defer m.bargeInMu.Unlock()
	if m.bargeIn.IsZero() {
		m.bargeIn = at
	}
}

func (m *turnMetrics) bargeInAt() time.Time {
	m.bargeInMu.Lock()
	defer m.bargeInMu.Unlock()
	return m.bargeIn
}

func (m *turnMetrics) elapsed(ts time.Time) time.Duration {
	if ts.IsZero() {
		return 0
	}
	return ts.Sub(m.start)
}

// sincePlaybackStart reports how long TTS had been audible at ts, measured
// from the turn's first playbackStarted event (metrics.playbackStart, stamped
// once in applyPlaybackEvent). Zero when playback never started or ts
// precedes it — the mic barge-in path can only fire once armed, which itself
// only happens after the first playbackStarted event, so this is non-zero on
// every real barge-in.
func (m *turnMetrics) sincePlaybackStart(ts time.Time) time.Duration {
	if m.playbackStart.IsZero() || ts.Before(m.playbackStart) {
		return 0
	}
	return ts.Sub(m.playbackStart)
}

func (m *turnMetrics) tEOU() time.Time {
	if m != nil && !m.sttFinal.IsZero() {
		return m.sttFinal
	}
	if m == nil {
		return time.Time{}
	}
	return m.start
}

func (m *turnMetrics) elapsedFromEOU(ts time.Time) time.Duration {
	if ts.IsZero() {
		return 0
	}
	origin := m.tEOU()
	if ts.Before(origin) {
		return 0
	}
	return ts.Sub(origin)
}

func (p *Pipeline) sinkKind() string {
	if p == nil || p.Player == nil || p.OutputMuted() {
		return audio.SinkNone
	}
	if r, ok := p.Player.(audio.SinkReporter); ok {
		if kind := r.SinkKind(); kind != "" {
			return kind
		}
		return audio.SinkNone
	}
	return audio.SinkLocalSpeaker
}

type streamAccepter interface {
	AcceptedCount() int
}

func (p *Pipeline) acceptedCount() int {
	if p == nil || p.Player == nil {
		return 0
	}
	if a, ok := p.Player.(streamAccepter); ok {
		return a.AcceptedCount()
	}
	return 0
}

var noSinkSynthOnce sync.Once

func (p *Pipeline) warnNoSinkSynth(kind string) {
	if kind != audio.SinkNone {
		return
	}
	noSinkSynthOnce.Do(func() {
		p.emit(events.Info{Message: "synthesizing with no audible sink; Stage is not listening"})
	})
}

func (p *Pipeline) stampTTFA(metrics *turnMetrics, at time.Time) {
	if metrics == nil || !metrics.ttfa.IsZero() || at.IsZero() {
		return
	}
	switch metrics.audibleSink {
	case audio.SinkNone, "":
		return
	case audio.SinkStreamClient:
		if p.acceptedCount() <= 0 {
			return
		}
	}
	metrics.ttfa = at
}

// maybeStampLateTTFA covers a stream_client whose first broadcast dropped
// every chunk and a later one accepted. TTFA is pcmReady, not drain, and
// Started() is not delayed to accept.
func (p *Pipeline) maybeStampLateTTFA(metrics *turnMetrics) {
	if metrics == nil || !metrics.ttfa.IsZero() {
		return
	}
	at := metrics.synthFirstFrame
	if at.IsZero() {
		at = metrics.firstAudioReady
	}
	if at.IsZero() {
		at = metrics.playbackStart
	}
	p.stampTTFA(metrics, at)
}
