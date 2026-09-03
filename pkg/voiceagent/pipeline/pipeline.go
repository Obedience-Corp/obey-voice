package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

const voiceQueueDepth = 2

// How many speakable segments go into one synthesis call.
//
// laterBatchSegments is 1, and the 2 it was briefly set to is why this comment
// exists. Batching two sentences per call was supposed to let the synthesiser
// carry intonation across a sentence boundary. Measurement killed it:
//
//	Kokoro realtime factor         1.32x   (0.901 s to synthesize 1.185 s of audio)
//	synth(2 sentences)             1.989 s
//	playback(1 opening sentence)   1.185 s
//	=> 804 ms of silence after the opening sentence
//
// The synthesis worker is single and ordered, so segment 2 is synthesized while
// segment 1 plays. At 1.32x, a two-sentence segment 2 cannot finish in time and
// the listener hears a gap in the most audible position in the reply. Meanwhile
// the prosody benefit was unverifiable: merged audio measurably differs from
// concatenated audio, but total duration, inter-sentence pause and onset F0 were
// identical, so nothing showed the difference was an improvement.
//
// Batching becomes free above a realtime factor of about 2.3x — roughly 1.8x
// faster than Kokoro is today. `go test -tags integration ./pkg/voiceagent/tts/ -run
// TestKokoroRealtimeFactor` is the measurement; phase 009's TTS bake-off should
// re-run it, and a candidate that clears 2.3x makes this a one-constant change.
//
// See festival decision D009.
const (
	openingBatchSegments = 1
	laterBatchSegments   = 1
)

// speakableSegment reports whether a cleaned segment earns a place in the
// transcript and the synthesis queue. Call this. Neither half below is
// sufficient alone, and the two reject disjoint kinds of junk:
//
//	"Hmm."     speakable=false  pronounceable=true   (a bare hesitation)
//	"\u200b"  speakable=true   pronounceable=false  (a zero-width space)
//	"\u0301"  speakable=true   pronounceable=false  (a lone combining accent)
//
// So the pair is not redundant in either direction, which is easy to assume
// from a glance and wrong. TestSpeakableSegmentNeedsBothHalves fails if either
// half is dropped.
//
// Both speech paths decide here: streamResponse per segment as segments arrive,
// RunTurnTextMode through speakableFromResponse. Reaching for a half instead is
// how a filler-only reply got spoken while the recovery line went only to
// history (PR #223 review).
func speakableSegment(s string) bool {
	return brain.HasSpeakableContent(s) && hasPronounceableContent(s)
}

// speakableFromResponse reduces a whole gated reply to the text worth
// synthesizing, applying per segment what the progressive loop applies as
// segments arrive: the same splitter, the same cleaning, the same predicate.
//
// Text mode used to synthesize the gated reply verbatim, so it spoke markdown
// ("Here is **bold** text") and read a leading "Hmm." aloud before the recovery
// line that had replaced it — the PR #223 bug living on in the one path that
// fix did not reach.
//
// Segment granularity is the whole point. speakableSegment on the entire reply
// cannot work: "Hmm. <recovery>" satisfies both halves as soon as the recovery
// text is present, so the filler rides along. Only splitting first exposes it.
func speakableFromResponse(gated string) string {
	if gated == "" {
		return ""
	}
	input := make(chan string, 1)
	input <- gated
	close(input)

	var kept []string
	seen := make(map[string]struct{})
	for segment := range brain.ChunkSentencesRaw(input) {
		if cleaned := brain.CleanForVoice(segment); speakableSegment(cleaned) {
			if _, dup := seen[cleaned]; dup {
				continue
			}
			seen[cleaned] = struct{}{}
			kept = append(kept, cleaned)
		}
	}
	return strings.Join(kept, " ")
}

// hasPronounceableContent reports whether a segment contains anything a
// synthesiser can say. Punctuation left behind by cleaning — a bare "." from
// "Umm." — is not speech, and must not reach TTS or be batched onto a real
// sentence. It also rejects a segment of nothing but invisible runes, which
// brain.HasSpeakableContent accepts: it trims spaces, punctuation and symbols,
// and a combining mark is none of those.
//
// Not called directly — speakableSegment is the entry point.
func hasPronounceableContent(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// defaultBrainTurnTimeout is the idle bound on model + tool work for one
// conversational turn. Tokens and tool activity reset it. Without a bound, a
// hung Ollama/Claude stream after tools leaves the TUI stuck on "thinking"
// forever while listening never resumes. A wall-clock cap on the whole turn
// is wrong: a 27b tool loop can be busy for longer than 3 minutes and still
// be healthy.
const defaultBrainTurnTimeout = 3 * time.Minute

// CaptureMonitor is an audio source the pipeline listens to.
//
// Exported because serve already proves the extension point: its Ingress
// substitutes a remote audio stream for the local microphone. An embedder
// wanting audio from a browser, a phone or a file needs exactly this and nothing
// more — three methods, which is why promoting it is cheap.
type CaptureMonitor interface {
	Subscribe(buffer int) (int, <-chan []float32)
	Unsubscribe(id int)
	Reset()
}

// VoiceDetector decides when the user is speaking.
//
// Four methods, and each has a caller: AcceptWaveform feeds it, IsSpeech gates
// the current frame, IsSpeechDetected reports the latched utterance state that
// endpointing depends on, and Clear resets between turns.
type VoiceDetector interface {
	AcceptWaveform(samples []float32)
	IsSpeech() bool
	IsSpeechDetected() bool
	Clear()
}

// UtteranceEnder ends the utterance the recogniser is currently reading, on
// demand. The local microphone is wrapped in one (audio.Finalizable) so a talk
// key can say "I have finished" instead of waiting out the silence timeout;
// serve's network ingress finalizes itself.
type UtteranceEnder interface {
	Finalize()
	Clear()
}

// Pipeline orchestrates the voice conversation loop.
type Pipeline struct {
	STT   stt.Provider
	Brain brain.Provider
	TTS   tts.Provider
	// Utterance is nil when the audio source cannot be ended on demand.
	Utterance UtteranceEnder
	// TTSFallback is an optional, explicitly configured provider used for one
	// affected sentence when the selected provider fails before playback.
	// Kokoro is constructed here when voice_fallback_provider=kokoro.
	TTSFallback       tts.Provider
	ttsMu             sync.RWMutex
	Player            audio.Engine
	Capture           CaptureMonitor
	VAD               VoiceDetector
	BargeInVAD        VoiceDetector
	Events            *events.Bus
	VoiceToolsEnabled bool
	// underrunOnce gates the "synthesis cannot keep up" notice to one per
	// session; the condition holds on every turn once it holds at all.
	underrunOnce sync.Once
	// backchannel is nil unless config.BackchannelEnabled; see backchannel.go.
	backchannel       *backchannel
	backchannelMu     sync.Mutex
	backchannelCancel context.CancelFunc
	// vocals plays [moan]/[gasp]/[laugh] as sounds instead of reading the tag.
	vocals *vocalBank
	OnTurn func() // called after each completed turn for session auto-save

	// CompactPrompt is the resolved kind=compact instruction for /compact's
	// summarize turn. Empty disables compaction with a clear error.
	CompactPrompt string
	// AutoCompactAfter is the history length that triggers CompactConversation
	// before a user turn, instead of silently dropping old messages. Zero
	// disables auto-compact. /compact still works at any length.
	AutoCompactAfter int
	// OnCompactBackup receives the pre-compact history so the host can persist
	// one recovery generation before LoadHistory rewrites the conversation.
	OnCompactBackup func(turns []brain.Turn)

	// PlaybackStallTimeout overrides the watchdog timeout and always wins when
	// positive. Zero uses defaultPlaybackStallTimeout, or the primary TTS
	// provider's FirstAudioGrace when that is larger.
	PlaybackStallTimeout time.Duration

	// BrainTurnTimeout bounds ThinkStream (model + tools) per turn. Zero uses
	// defaultBrainTurnTimeout; negative disables the bound. The bound is idle
	// time: tokens and tool activity reset it, so a long tool loop does not
	// die at wall-clock 3 minutes while work is still happening.
	BrainTurnTimeout  time.Duration
	brainDeadlineMu   sync.Mutex
	brainDeadlineBump func()

	// BargeInTailGuard overrides the interrupt controller's post-playback
	// echo tail guard (config barge_in_tail_guard_ms) and always wins when
	// positive. Zero uses bargeInPauseTailGuardDefault. See interrupt.go
	// bargeInTailGuard.
	BargeInTailGuard time.Duration

	// SpeakerGate, when set, may veto a voice turn after transcription based
	// on the live speaker state; a non-empty return is the user-facing reason
	// the utterance was dropped. Wired only for speaker.live.mode=owner_verify
	// — identity never gates turns by default, and text turns are never gated.
	SpeakerGate func(label string) (rejectReason string)
	// CurrentSpeaker returns the stable live speaker id (speaker-1) for the
	// in-flight user turn when live labels are active. Optional; nil/empty
	// means ordinary single-user attribution.
	CurrentSpeaker func() string

	// BargeInSpeakerVerdict, when set, reports the live speaker analyzer's
	// most recent window verdict to the interrupt controller (config
	// barge_in_speakers=enrolled_only, D7). Wired only for that policy —
	// anyone (the default) leaves this nil and it is never consulted. See
	// interrupt.go SpeakerVerdict / interruptController.allowsInterrupt.
	BargeInSpeakerVerdict func() SpeakerVerdict
	// BargeInSpeakerVerdictMaxAge bounds how old a BargeInSpeakerVerdict may
	// be before the interrupt controller treats it as stale and fails open
	// (~2x speaker.live.window_ms). Zero uses
	// bargeInSpeakerVerdictMaxAgeDefault. See
	// config.BargeInSpeakerVerdictMaxAgeOf.
	BargeInSpeakerVerdictMaxAge time.Duration

	// keepCapture preserves the capture buffer into the next turn after a
	// barge-in — or a near-miss, speech heard during a playback pause that
	// never tripped — where the buffered audio is the user mid-utterance.
	keepCapture bool

	// outputMuted is toggled by interactive clients while a turn may be active.
	outputMuted atomic.Bool

	// activeMetrics points at the in-flight turn's metrics so out-of-band
	// interrupts (keystroke barge-in via StopPlayback) can stamp BargeIn.
	activeMu      sync.Mutex
	activeMetrics *turnMetrics
}

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

// brainTimeout returns the per-turn model deadline and whether it is armed.
func (p *Pipeline) brainTimeout() (time.Duration, bool) {
	if p.BrainTurnTimeout < 0 {
		return 0, false
	}
	if p.BrainTurnTimeout > 0 {
		return p.BrainTurnTimeout, true
	}
	return defaultBrainTurnTimeout, true
}

// withBrainTimeout derives a child context that expires after idle brain work.
// Tokens and tool activity reset the timer; parent cancellation still wins
// immediately.
func (p *Pipeline) withBrainTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	d, ok := p.brainTimeout()
	if !ok {
		return context.WithCancel(ctx)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(d, func() { cancel(context.DeadlineExceeded) })
	alive := true
	bump := func() {
		p.brainDeadlineMu.Lock()
		defer p.brainDeadlineMu.Unlock()
		if !alive || timer == nil {
			return
		}
		if timer.Stop() {
			timer.Reset(d)
		}
	}
	p.brainDeadlineMu.Lock()
	p.brainDeadlineBump = bump
	p.brainDeadlineMu.Unlock()
	return ctx, func() {
		p.brainDeadlineMu.Lock()
		alive = false
		p.brainDeadlineBump = nil
		t := timer
		timer = nil
		p.brainDeadlineMu.Unlock()
		if t != nil {
			t.Stop()
		}
		cancel(nil)
	}
}

func (p *Pipeline) bumpBrainDeadline() {
	p.brainDeadlineMu.Lock()
	bump := p.brainDeadlineBump
	p.brainDeadlineMu.Unlock()
	if bump != nil {
		bump()
	}
}

// mapIdleDeadline turns an idle-timer cancel into DeadlineExceeded so callers
// that already distinguish a stall from a barge-in keep working.
func mapIdleDeadline(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}

// currentSpeakerID returns the live speaker label for this turn, if any.
func (p *Pipeline) currentSpeakerID() string {
	if p == nil || p.CurrentSpeaker == nil {
		return ""
	}
	return strings.TrimSpace(p.CurrentSpeaker())
}

// SetOutputMuted enables or disables spoken responses without rebuilding the
// pipeline. Muting immediately stops any audio already in flight.
func (p *Pipeline) SetOutputMuted(muted bool) {
	p.outputMuted.Store(muted)
	if muted && p.Player != nil {
		p.Player.Stop()
	}
}

// OutputMuted reports whether spoken responses are currently disabled.
func (p *Pipeline) OutputMuted() bool { return p.outputMuted.Load() }

// ReplaceTTS atomically changes the providers used by subsequent synthesis
// calls. Callers own provider cleanup and must keep retired providers alive
// until any already-started utterance has drained.
func (p *Pipeline) ReplaceTTS(primary, fallback tts.Provider) {
	p.ttsMu.Lock()
	p.TTS = primary
	p.TTSFallback = fallback
	p.ttsMu.Unlock()
}

// HasTTS reports whether a primary voice provider is configured.
func (p *Pipeline) HasTTS() bool {
	primary, _ := p.ttsProviders()
	return primary != nil
}

func (p *Pipeline) ttsProviders() (tts.Provider, tts.Provider) {
	p.ttsMu.RLock()
	defer p.ttsMu.RUnlock()
	return p.TTS, p.TTSFallback
}

func (p *Pipeline) ttsReady() bool {
	primary, _ := p.ttsProviders()
	return primary != nil && primary.Available()
}

type playbackEventType int

const (
	playbackStarted playbackEventType = iota
	playbackFinished
	// playbackNotEnqueued reports a handed-off sentence that produced no
	// playback (TTS/playback error, or synthesis canceled), so the loop can
	// release its pending slot.
	playbackNotEnqueued
)

type playbackEvent struct {
	kind     playbackEventType
	sentence string
	index    int
	fallback bool
	// engineElapsed is synthesis alone; synthElapsed also covers waiting
	// behind whatever was already playing.
	engineElapsed time.Duration
	synthElapsed  time.Duration
	playElapsed   time.Duration
	result        audio.PlaybackResult
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

// turnConductor bridges the turn state machine into the live pipeline: stages
// report state transitions and it guarantees exactly one terminal TurnMetrics
// emission per turn, regardless of which return path ends the turn.
type turnConductor struct {
	p        *Pipeline
	machine  *turnMachine
	metrics  *turnMetrics
	finished bool
}

func (p *Pipeline) newTurnConductor(metrics *turnMetrics) *turnConductor {
	return &turnConductor{p: p, machine: newTurnMachine(), metrics: metrics}
}

// to advances the lifecycle state, ignoring illegal or out-of-order transitions
// so a late stage signal cannot corrupt the turn outcome.
func (c *turnConductor) to(state TurnState) { c.machine.To(state) }

// finish moves the machine to its terminal state and emits the single terminal
// metrics event. Later calls are no-ops, so every return path can call it.
func (c *turnConductor) finish(terminal TurnState) {
	if c.finished {
		return
	}
	c.finished = true
	c.machine.To(terminal)
	// The machine is the outcome authority: the emitted metrics carry the
	// terminal state it settled on. A rejected transition (late or illegal
	// signal) falls back to the caller's intent so the outcome is never a
	// non-terminal state.
	outcome, ok := c.machine.Terminal()
	if !ok {
		outcome = terminal
	}
	m := c.metrics.snapshot()
	m.Outcome = outcome.String()
	c.p.emit(m)
	c.p.warnIfSynthesisCannotKeepUp(m)
}

// underrunDeadAirShare is the fraction of a turn's speaking time that may be
// silence before the synthesizer is judged unable to keep up. A little slack
// between sentences is normal; a third of the turn is a different engine.
const underrunDeadAirShare = 0.25

// warnIfSynthesisCannotKeepUp says so once per session when a turn spent a
// large share of its speaking time in silence.
//
// It is deliberately not a per-turn message: when an engine is too slow for
// conversation it is too slow on every turn, and a warning that fires every
// time is one people learn to ignore. It is also deliberately not an
// automatic downgrade — which voice to use is the operator's call, not a
// decision to make silently on their behalf mid-conversation.
//
// It fires on either of two symptoms of the same illness. Silence between
// sentences is what a whole-utterance engine does when it falls behind. Gaps
// inside a sentence are what an incrementally-played one does, and those are
// worse — the reply stutters mid-word — so a single one is enough to say so
// rather than a quarter of the turn.
func (p *Pipeline) warnIfSynthesisCannotKeepUp(m events.TurnMetrics) {
	if m.Interrupted {
		return
	}
	speaking := m.SpeakingWindow()
	deadAirShare := m.DeadAirElapsed > 0 && speaking > 0 &&
		float64(m.DeadAirElapsed)/float64(speaking) >= underrunDeadAirShare
	if !deadAirShare && m.Underruns == 0 {
		return
	}
	engine := m.TTSProvider
	if engine == "" {
		engine = "the speech engine"
	}
	if m.TTSTier != "" {
		engine += " " + m.TTSTier
	}
	symptom := fmt.Sprintf("%s of that reply's %s speaking time was silence between sentences",
		m.DeadAirElapsed.Round(time.Second), speaking.Round(time.Second))
	remedy := "A faster voice (Settings → TTS) removes the gaps."
	if m.Underruns > 0 {
		symptom = fmt.Sprintf("that reply broke up %s while it was still being generated",
			pluralize(m.Underruns, "time", "times"))
		remedy = "Set qwen_tts_stream to off to speak whole sentences instead, or use a faster voice (Settings → TTS)."
	}
	p.underrunOnce.Do(func() {
		p.emit(events.Info{Message: fmt.Sprintf(
			"%s is synthesizing slower than it plays: %s. %s", engine, symptom, remedy)})
	})
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// logDegradedTurn writes the underlying error to stderr with a timestamp and
// stage. Under the TUI fd 2 is redirected to native-diagnostics.log, so this
// is the durable record for degraded turns (bus Error flashes and can vanish).
func logDegradedTurn(stage string, err error) {
	fmt.Fprintf(os.Stderr, "%s obey-voice: degraded turn stage=%s: %v\n",
		time.Now().UTC().Format(time.RFC3339), stage, err)
}

// recoverTurn closes out a turn that died on a hard brain/tool failure with a
// user-visible (and spoken, when possible) recovery reply instead of silence.
// The error becomes activity detail, the transcript keeps any partial reply
// with the recovery line appended, and the turn completes degraded — so
// callers do not count a model failure as a voice-input failure.
func (p *Pipeline) recoverTurn(ctx context.Context, turn *turnConductor, metrics *turnMetrics, partial string, err error) {
	metrics.degraded = true
	stage := "brain"
	speak := true
	playbackStall := errors.Is(err, errPlaybackStalled)
	if playbackStall {
		// The player is wedged; queueing recovery speech into it would stall
		// again. The reply text still reaches the transcript.
		stage = "playback"
		speak = false
	}
	p.emit(events.Error{Stage: stage, Message: err.Error()})
	logDegradedTurn(stage, err)

	partial = strings.TrimSpace(partial)
	response := brain.RecoveryReply
	if playbackStall {
		// Whole-utterance TTS often delivers audio after the watchdog fires.
		// Appending RecoveryReply is how 2026-08-21 showed Veronica's line plus
		// "Want me to try a simpler approach?" on the same turn. The Error
		// event above is the activity detail; the transcript keeps what was
		// already spoken — speakable content only, never a think region.
		response = speakableStallTranscript(partial)
	} else if partial != "" {
		// Ollama's stream layer may already have recovered out loud (streaming
		// RecoveryReply itself) before the turn died; appending a second copy
		// is how the field capture showed the same apology twice.
		response = partial
		if !strings.HasSuffix(partial, brain.RecoveryReply) {
			response = partial + "\n\n" + brain.RecoveryReply
		}
	}
	p.emit(events.ResponseReady{Response: response, Degraded: true})

	if speak && ctx.Err() == nil {
		p.speakBestEffort(ctx, brain.RecoveryReply, metrics, turn)
	}
	turn.finish(TurnCompleted)
	if p.OnTurn != nil {
		p.OnTurn()
	}
}

func speakableStallTranscript(partial string) string {
	s := strings.TrimSpace(textclean.StripThinkBlocks(partial))
	if s == "" || textclean.ContainsThinkTag(s) {
		return ""
	}
	return s
}

// speakBestEffort synthesizes and plays one utterance for a degraded turn.
// Failures only produce activity detail — recovery speech must never fail the
// turn it is rescuing.
func (p *Pipeline) speakBestEffort(ctx context.Context, sentence string, metrics *turnMetrics, turn *turnConductor) {
	if p.OutputMuted() || p.Player == nil || !p.ttsReady() {
		return
	}
	turn.to(TurnSpeaking)
	if metrics.firstSegment.IsZero() {
		metrics.firstSegment = time.Now()
	}
	p.emit(events.SpeechSegmentReady{Text: sentence})
	p.emit(events.GeneratingVoice{Sentence: sentence})

	if metrics.ttsEngine.provider == "" {
		metrics.ttsEngine = p.ttsIdentity()
	}
	synthStarted := time.Now()
	stream, usedFallback, err := p.synthesizeWithFallback(ctx, sentence, nil)
	if err != nil {
		p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
		return
	}
	if p.OutputMuted() {
		discardPCMStream(stream)
		return
	}
	playback, err := p.Player.PlayStream(ctx, stream)
	if err != nil {
		p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
		return
	}
	p.handlePlaybackLifecycle(sentence, usedFallback, synthStarted, time.Since(synthStarted), playback, metrics)
}

// StopPlayback aborts audible TTS immediately. Used by the TUI when the user
// types to barge in so speech does not continue until the turn context drains.
// A stop that lands mid-playback is a barge-in and stamps the active turn's
// metrics; the turn itself keeps streaming.
func (p *Pipeline) StopPlayback() {
	if p == nil || p.Player == nil {
		return
	}
	if p.Player.IsPlaying() {
		p.activeMu.Lock()
		if m := p.activeMetrics; m != nil {
			m.markBargeIn(time.Now())
		}
		p.activeMu.Unlock()
	}
	p.Player.Stop()
}

// EndUtterance asks the recogniser to commit what it has heard so far, as a
// talk key released or a "send" tap would. It reports false when the audio
// source cannot be ended on demand, so a caller can fall back to waiting.
func (p *Pipeline) EndUtterance() bool {
	if p.Utterance == nil {
		return false
	}
	p.Utterance.Finalize()
	return true
}

// RunTurn executes one conversational turn with streaming TTS.
// Returns the user's input text, or empty string if no speech was detected.
func (p *Pipeline) RunTurn(ctx context.Context) (string, error) {
	p.bindBrainInfo()
	metrics := newTurnMetrics()
	defer p.trackMetrics(metrics)()
	turn := p.newTurnConductor(metrics)

	if p.Player != nil {
		p.Player.Stop()
	}

	turn.to(TurnListening)
	text, err := p.transcribeTurn(ctx, metrics, turn)
	// An EndUtterance that arrived after listening stopped was meant for this
	// turn; dropping it keeps it from cutting the next one short.
	if p.Utterance != nil {
		p.Utterance.Clear()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
		} else {
			turn.finish(TurnFailed)
		}
		return "", err
	}
	if text == "" {
		turn.finish(TurnTimedOut)
		return "", nil
	}

	turn.to(TurnThinking)
	// The wait a backchannel covers starts here: the user has stopped talking
	// and nothing will be heard until the brain and TTS finish. No-op unless
	// enabled and unless recent turns were actually slow.
	p.playBackchannel(ctx)
	speakerID := p.currentSpeakerID()
	if p.SpeakerGate != nil {
		if reason := p.SpeakerGate(speakerID); reason != "" {
			// Reuses TurnTimedOut's "no usable speech" terminal semantics;
			// the Info event carries the actual veto reason.
			p.emit(events.Info{Message: reason})
			turn.finish(TurnTimedOut)
			return "", nil
		}
	}
	p.emit(events.UserInput{Text: text, Speaker: speakerID})
	if err := p.autoCompactIfNeeded(ctx); err != nil {
		p.emit(events.Error{Stage: "compact", Message: err.Error()})
	}
	p.emit(events.ThinkingStarted{})

	// turnCtx scopes the whole turn so the playback watchdog can abort the brain
	// stream too — not just the derived playback context. Brain work is further
	// bounded so a hung model after tools cannot wedge the conversation loop.
	turnCtx, turnCancel := p.withBrainTimeout(ctx)
	defer turnCancel()

	brainStream, err := p.Brain.ThinkStream(turnCtx, text, brain.StreamOptions{
		VoiceMode:     true,
		Speaker:       speakerID,
		ToolsEnabled:  p.VoiceToolsEnabled,
		OnToolStart:   p.toolStartHook(),
		OnToolEnd:     p.toolEndHook(),
		OnUsage:       p.usageHook(),
		OnSessionWarn: p.sessionWarnHook(),
		OnPromptWarn:  p.promptWarnHook(),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return text, fmt.Errorf("brain: %w", err)
		}
		p.recoverTurn(ctx, turn, metrics, "", fmt.Errorf("brain: %w", err))
		return text, nil
	}

	fullResponse, interrupted, err := p.streamResponse(turnCtx, turnCancel, brainStream, true, metrics, turn)
	if err != nil {
		if interrupted || errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return text, err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			// Model/tool stall — distinct from STT timed_out (no speech).
			err = fmt.Errorf("model timed out before finishing a reply: %w", err)
		}
		// Speak on the parent ctx: turnCtx is dead after a deadline stall.
		p.recoverTurn(ctx, turn, metrics, fullResponse, err)
		return text, nil
	}

	// On a barge-in the user is already speaking their next turn into the mic;
	// keep that audio instead of draining it when the next listen begins. A
	// near-miss (F3.3) may already have set keepCapture inside streamResponse.
	if interrupted {
		p.keepCapture = true
	}

	p.emit(events.ResponseReady{
		Response:    fullResponse,
		Interrupted: interrupted,
		Degraded:    metrics.degraded,
	})
	if interrupted {
		turn.finish(TurnInterrupted)
	} else {
		turn.finish(TurnCompleted)
	}

	if p.OnTurn != nil {
		p.OnTurn()
	}

	return text, nil
}

// completeTextTurn finishes a successful text-mode turn exactly once:
// ResponseReady for the transcript, terminal metrics via finish, and OnTurn
// for session auto-save. Every degraded and mute short-circuit must use this
// so finish logic cannot drift across early returns.
func (p *Pipeline) completeTextTurn(turn *turnConductor, metrics *turnMetrics, response string) {
	p.emit(events.ResponseReady{Response: response, Degraded: metrics.degraded})
	turn.finish(TurnCompleted)
	if p.OnTurn != nil {
		p.OnTurn()
	}
}

// RunTurnTextMode runs a turn with text input instead of mic.
func (p *Pipeline) RunTurnTextMode(ctx context.Context, input string) error {
	p.bindBrainInfo()
	metrics := newTurnMetrics()
	defer p.trackMetrics(metrics)()
	turn := p.newTurnConductor(metrics)

	turn.to(TurnThinking)
	speakerID := p.currentSpeakerID()
	p.emit(events.UserInput{Text: input, Speaker: speakerID})
	if err := p.autoCompactIfNeeded(ctx); err != nil {
		p.emit(events.Error{Stage: "compact", Message: err.Error()})
	}
	p.emit(events.ThinkingStarted{})
	if metrics.sttFinal.IsZero() {
		metrics.sttFinal = time.Now()
	}
	thinkingStarted := metrics.sttFinal

	// Bound model + tools so a hung Ollama chat after tool results cannot leave
	// the TUI forever on "thinking" / last tool status.
	brainCtx, brainCancel := p.withBrainTimeout(ctx)
	defer brainCancel()

	stream, err := p.Brain.ThinkStream(brainCtx, input, brain.StreamOptions{
		Speaker:       speakerID,
		ToolsEnabled:  p.VoiceToolsEnabled,
		OnToolStart:   p.toolStartHook(),
		OnToolEnd:     p.toolEndHook(),
		OnUsage:       p.usageHook(),
		OnSessionWarn: p.sessionWarnHook(),
		OnPromptWarn:  p.promptWarnHook(),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return fmt.Errorf("brain: %w", err)
		}
		p.recoverTurn(ctx, turn, metrics, "", fmt.Errorf("brain: %w", err))
		return nil
	}

	// Stream the reply so the TUI can render it token-by-token, then fall back
	// to the existing whole-response synthesis path below. ResponseStreamingStarted
	// and per-chunk ResponseDelta events are emitted inside observeStream.
	response, err := p.collectTextStream(brainCtx, stream, metrics)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			turn.finish(TurnInterrupted)
			return fmt.Errorf("brain: %w", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("model timed out before finishing a reply: %w", err)
		}
		// Speak on the parent ctx: brainCtx is dead after a deadline stall.
		p.recoverTurn(ctx, turn, metrics, response, err)
		return nil
	}

	metrics.modelComplete = time.Now()
	p.emit(events.ThinkingComplete{
		Response: response,
		Elapsed:  time.Since(thinkingStarted),
	})

	// Voice gate (WI-dc9e33 B4): the full raw response stays in the chat
	// transcript; only voice-safe text may be synthesized. Gate only when the
	// turn would actually speak, so mute never counts phantom leaks.
	//
	// gateForSpeech removes structure the listener should not hear (fences,
	// tool blocks); speakableFromResponse then applies the per-segment cleaning
	// and predicate the progressive path uses, so a typed turn and a spoken one
	// say the same thing.
	speakable := ""
	if !p.OutputMuted() && p.Player != nil && p.ttsReady() {
		speakable = speakableFromResponse(p.gateForSpeech(response, metrics))
	}
	if speakable != "" {
		turn.to(TurnSpeaking)
		now := time.Now()
		if metrics.firstSegment.IsZero() {
			metrics.firstSegment = now
		}
		if metrics.firstAdmit.IsZero() {
			metrics.firstAdmit = now
		}
		metrics.ttsEngine = p.ttsIdentity()
		metrics.sinkAtArm = p.sinkKind()
		metrics.audibleSink = metrics.sinkAtArm
		p.warnNoSinkSynth(metrics.sinkAtArm)
		seg := events.SpeechSegmentReady{Text: speakable}
		if !metrics.firstSpeakable.IsZero() && !metrics.firstAdmit.Equal(metrics.firstSpeakable) {
			seg.Elapsed = metrics.elapsedFromEOU(metrics.firstAdmit)
		}
		p.emit(seg)
		p.emit(events.GeneratingVoice{Sentence: speakable})

		synthStarted := time.Now()
		stream, usedFallback, err := p.synthUtterance(ctx, speakable, nil)
		if err != nil {
			// Voice is best-effort in text mode: the text response still
			// completed, so the turn is completed (degraded), not failed.
			p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
			p.completeTextTurn(turn, metrics, response)
			return nil
		}
		if p.OutputMuted() {
			// Drop the stream so the synth producer is not left blocked on a
			// full frames channel after we skip PlayStream.
			discardPCMStream(stream)
			p.completeTextTurn(turn, metrics, response)
			return nil
		}

		playback, err := p.Player.PlayStream(ctx, stream)
		if err != nil {
			if !usedFallback {
				playback, usedFallback, err = p.playFallback(ctx, speakable, err, nil)
			}
			if err == nil {
				// The fallback playback is ready; continue through the normal
				// lifecycle below.
			} else {
				// Engine.PlayStream owns the stream once called (even on error) and
				// must drain it; the pipeline does not discard here.
				p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
				p.completeTextTurn(turn, metrics, response)
				return nil
			}
		}
		// PlayStream waits for the complete sentence buffer. Output may have
		// been muted while it was waiting, after SetOutputMuted's first Stop
		// saw an empty queue. Stop again so that late-enqueued audio cannot play.
		if p.OutputMuted() {
			p.Player.Stop()
			p.completeTextTurn(turn, metrics, response)
			return nil
		}

		// PlayStream returned, so the utterance is fully synthesized and
		// buffered: everything after this point is playback, not the engine.
		p.handlePlaybackLifecycle(speakable, usedFallback, synthStarted, time.Since(synthStarted), playback, metrics)
	}

	p.completeTextTurn(turn, metrics, response)
	return nil
}

func (p *Pipeline) transcribeTurn(ctx context.Context, metrics *turnMetrics, turn *turnConductor) (string, error) {
	if p.STT == nil {
		return "", errors.New("STT provider is not configured")
	}

	// Drain echo and silence captured while Samantha spoke — including the tail
	// that keeps hitting the mic after playback stops — so this turn hears live
	// speech, not stale audio. Skip right after a barge-in, where the buffered
	// audio is the user already mid-utterance.
	if p.Capture != nil && !p.keepCapture {
		p.Capture.Reset()
	}
	p.keepCapture = false

	session, err := p.STT.Start(ctx)
	if err != nil {
		return "", fmt.Errorf("STT: %w", err)
	}
	defer session.Close()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				return "", nil
			}

			// InputLevel is droppable UI meter data; handle before the typed
			// switch so high-rate samples never participate in turn control.
			if lvl, ok := event.(stt.InputLevel); ok {
				p.emit(events.AudioLevel{Source: "input", Level: lvl.Level})
				continue
			}

			te := stt.ToTyped(event)
			switch te.Kind {
			case stt.KindPhase:
				if te.Phase == "transcribing" {
					turn.to(TurnTranscribing)
				}
				p.emit(events.STTPhase{
					Phase:   te.Phase,
					Elapsed: te.Elapsed,
				})
			case stt.KindPartialTranscript:
				if te.Text != "" {
					p.emit(events.TranscriptPartial{Text: te.Text})
				}
			case stt.KindFinalTranscript:
				metrics.sttFinal = time.Now()
				return te.Text, nil
			case stt.KindTimeout:
				return "", nil
			case stt.KindFailure:
				// Failure needs the error value (errors.Is + %w), not ErrText.
				fail, _ := event.(stt.Failure)
				if errors.Is(fail.Err, context.Canceled) && ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", fmt.Errorf("STT: %w", fail.Err)
			case stt.KindInputLevel:
				p.emit(events.AudioLevel{Source: "input", Level: te.Level})
			}
		}
	}
}

func (p *Pipeline) streamResponse(ctx context.Context, cancelTurn context.CancelFunc, stream *brain.Stream, allowBargeIn bool, metrics *turnMetrics, turn *turnConductor) (string, bool, error) {
	streamCtx, cancel := context.WithCancel(ctx)

	// loopDone closes when this function returns — the moment the loop stops
	// draining playbackEvents. Playback watchers select on it so their sends
	// cannot block (and leak) after an early return (ctx cancel, stall, or brain
	// error). It is distinct from streamCtx on purpose: a barge-in cancels
	// streamCtx but the loop keeps reading playbackEvents to drain pending
	// playbacks, so watchers must still deliver their finished events.
	loopDone := make(chan struct{})
	defer close(loopDone)

	// Two narrow upstream stages feed the turn loop: the model observer forwards
	// brain chunks (stamping first-chunk metrics), then the segmenter turns that
	// chunk stream into voice-ready sentences. Both are testable in isolation.
	streamedChunks, observeDone := p.observeStream(streamCtx, stream, metrics)
	// Join the observer before returning: it stamps firstModelChunk, which the
	// caller reads via snapshot() in finish(). Cancel first so the goroutine
	// unblocks, then wait for it to exit. cancel is idempotent — the barge-in and
	// normal-completion paths also call it. This runs after close(loopDone).
	defer func() {
		cancel()
		<-observeDone
	}()
	// Raw segments: the voice gate needs the markdown fences CleanForVoice
	// strips, so gating happens first and each destination cleans after.
	sentences := brain.ChunkSentencesRaw(streamedChunks)
	// One projection per turn holds the think filter, the voice gate and the
	// spoken-line memory, so the rules a segment is judged by live in one place
	// and a measurement harness can ask for them instead of re-deriving them.
	proj := NewSpeechProjection()

	var fullResponse strings.Builder
	// heard drops a sentence this turn already put on the transcript; the
	// projection drops one already in the synth queue. Local models often emit
	// the spoken line, fire a phantom tool, then emit the same line again
	// (Veronica, 2026-08-28: web_search for the World Cup mid-conversation,
	// then the identical sentence a second time). Without this she says it
	// twice.
	heard := make(map[string]struct{})
	var interrupted bool
	var pending int
	var bargeIn <-chan interruptRequest
	var bargeDone <-chan struct{}
	// armAt zero = unarmed: barge-in stays off until the turn's first playback
	// arms it, once, in applyPlaybackEvent. Speech during the thinking phase is
	// a separate product decision (F3.4) and stays disarmed here.
	var armAt atomic.Int64
	var ctrl *interruptController

	if allowBargeIn {
		ctrl = p.newInterruptController()
		watch := ctrl.watchWithDone(streamCtx, &armAt)
		bargeIn = watch.requests
		bargeDone = watch.done
		// Join the watcher on every exit path — including early returns via
		// outer cancellation, playback stall, and brain error — so its capture
		// subscription and VAD Clear never overlap the next turn. Cancel first
		// so it unblocks; joining twice is fine (done is closed).
		defer func() {
			if bargeDone == nil {
				return
			}
			cancel()
			<-bargeDone
		}()
	}

	playbackEvents := make(chan playbackEvent, voiceQueueDepth*2)
	modelDone := stream.Done
	modelFinished := false

	var audioStarted atomic.Bool
	var realAudioQueued atomic.Bool
	stalled := make(chan struct{})
	watchdogArmed := false

	// Progressive sentence TTS: brain.ChunkSentences emits each complete
	// sentence as stream tokens arrive; we hand each segment to the warm TTS
	// worker (native qwen3-tts-worker or Kokoro) without waiting for the full
	// brain reply. That is the product progressive path for long replies.
	//
	// Synthesis runs on a single ordered worker so this loop keeps servicing
	// barge-in and playback events while a sentence is being generated —
	// PlayStream cannot return until the TTS engine has produced audio, and
	// Kokoro generates a whole BATCH per uncancellable cgo call, holding its
	// provider mutex for the duration (see openingBatchSegments). One worker
	// (not one goroutine per sentence) preserves playback order. Backpressure is
	// unchanged: the pending gate keeps at most voiceQueueDepth sentences
	// outstanding, so the buffered handoff below never blocks.
	//
	// stickFallback is turn-scoped: once any sentence falls back to Kokoro,
	// remaining sentences stay on Kokoro so a flaky Qwen worker cannot flip
	// mid-reply between persona voice and the fallback voice.
	var stickFallback bool
	synthQueue := make(chan string, voiceQueueDepth)
	var synthQueueOnce sync.Once
	closeSynthQueue := func() { synthQueueOnce.Do(func() { close(synthQueue) }) }
	defer closeSynthQueue()

	// Adaptive batching: the first segment that will actually be heard goes out
	// alone, and later segments are synthesized two at a time.
	//
	// The opening stays short because time-to-first-audio waits on it finishing
	// synthesis. After that the listener is no longer waiting, and a chunk
	// boundary is not free: each batch is an independent synthesis call, so
	// prosody resets at every boundary and the player concatenates raw buffers.
	// Batching lets the synthesiser carry intonation across a sentence boundary.
	//
	// This lives here rather than in brain.ChunkSentencesRaw because only this
	// loop knows which segments become audio — the voice gate below suppresses
	// tool output and code fences, and cleaning can empty a segment. Counting
	// emitted chunks instead of audible ones spent the opening's budget on
	// silence whenever a reply began with a suppressed tool line.
	var voiceBatch []string
	openingSpoken := false
	flushVoiceBatch := func() {
		if len(voiceBatch) == 0 {
			return
		}
		pending++
		synthQueue <- strings.Join(voiceBatch, " ")
		voiceBatch = voiceBatch[:0]
		openingSpoken = true
	}
	// routeSegment sends one think-filtered segment to the transcript and, if it
	// survives gating and cleaning, to the voice batch. The end-of-stream think
	// flush enters here too, so both entry points share one copy of the
	// transcript and speakability rules instead of drifting apart.
	routeSegment := func(text string) {
		if text == "" {
			return
		}

		// Feed the gate every segment, in order, before deciding whether
		// this turn speaks at all: its regions span segments, so skipping
		// one loses the boundary that closes a tool block or a fence.
		voiceText, stripped := proj.gateSegment(text)

		// Filler-only chunks are provisional: providers replace a turn that
		// ends there with the recovery reply. Keep them out of both transcript
		// and TTS so the finalizer's replacement is the only visible/audible
		// response. A filler followed by content in the same sentence survives.
		if display := stripVocalTags(brain.CleanForVoice(text)); speakableSegment(display) {
			if _, dup := heard[display]; !dup {
				heard[display] = struct{}{}
				if fullResponse.Len() > 0 {
					fullResponse.WriteByte(' ')
				}
				fullResponse.WriteString(display)
			}
		}

		if interrupted || p.OutputMuted() || p.Player == nil || !p.ttsReady() {
			return
		}

		p.recordStrips(stripped, metrics)
		// Cleaning, the speakability predicate and this turn's repeat
		// suppression, all of which the bench harness replays through the
		// same code (SpeechProjection).
		speakable, ok := proj.admit(voiceText)
		if !ok {
			return
		}

		turn.to(TurnSpeaking)
		now := time.Now()
		if metrics.firstSegment.IsZero() {
			metrics.firstSegment = now
		}
		if metrics.firstAdmit.IsZero() {
			metrics.firstAdmit = now
		}
		if !watchdogArmed {
			watchdogArmed = true
			// The budget is sized to the segment the listener is actually
			// waiting on. openingBatchSegments is 1, so this speakable is
			// that segment. Arm before PlayStream so hung WaitReady is visible.
			stallTimeout := p.stallTimeout(len(speakable))
			metrics.firstAudioGrace = stallTimeout
			metrics.ttsEngine = p.ttsIdentity()
			metrics.sinkAtArm = p.sinkKind()
			metrics.audibleSink = metrics.sinkAtArm
			p.warnNoSinkSynth(metrics.sinkAtArm)
			go p.watchPlaybackStall(streamCtx, &audioStarted, cancel, stalled, stallTimeout, len(speakable), metrics.sinkAtArm == audio.SinkNone)
		}

		voiceBatch = append(voiceBatch, speakable)
		target := laterBatchSegments
		if !openingSpoken {
			target = openingBatchSegments
		}
		if len(voiceBatch) >= target {
			flushVoiceBatch()
		}
	}
	go func() {
		// Segments are numbered in playback order by the single ordered synth
		// worker, so the index is the position a listener hears.
		index := 0
		for sentence := range synthQueue {
			if !p.synthesizeSegment(streamCtx, loopDone, sentence, index, &audioStarted, playbackEvents, &stickFallback, &realAudioQueued) {
				// No playback was enqueued: release the pending slot so the
				// loop's accounting and the intake gate stay correct.
				sendPlaybackEvent(loopDone, playbackEvents, playbackEvent{kind: playbackNotEnqueued, sentence: sentence, index: index})
			}
			index++
		}
	}()

	for sentences != nil || pending > 0 || modelDone != nil {
		// Pause sentence intake while the playback queue is full so the loop
		// always returns to select and can drain playbackEvents. Blocking here
		// to acquire a slot would deadlock the turn once the queue fills.
		var sentenceCh <-chan string
		if pending < voiceQueueDepth {
			sentenceCh = sentences
		}

		select {
		case <-ctx.Done():
			if interrupted {
				return strings.TrimSpace(fullResponse.String()), interrupted, nil
			}
			return strings.TrimSpace(fullResponse.String()), interrupted, ctx.Err()

		case <-stalled:
			if metrics.sinkAtArm == audio.SinkNone {
				return strings.TrimSpace(fullResponse.String()), interrupted, nil
			}
			metrics.stallWatchdogFired = true
			return strings.TrimSpace(fullResponse.String()), interrupted, errPlaybackStalled

		case req := <-bargeIn:
			if interrupted {
				continue
			}
			interrupted = true
			metrics.interrupted = true
			metrics.markBargeIn(req.At)
			turn.to(TurnInterrupted)
			// Send the session's actual active request ID without waiting on the
			// synthesis lock. Stage B workers can stop mid-synthesis; Stage A
			// exits through the canceled context and is restarted by the provider.
			p.softCancelTTS("barge-in")
			cancel()
			if cancelTurn != nil {
				cancelTurn()
			}
			if p.Player != nil {
				p.Player.Stop()
			}
			sincePlayback := metrics.sincePlaybackStart(req.At)
			p.emit(events.SpeakingInterrupted{Reason: req.Reason, SincePlaybackStart: sincePlayback})
			p.emit(events.TurnInterrupted{Reason: req.Reason, SincePlaybackStart: sincePlayback})

		case result, ok := <-modelDone:
			if !ok {
				modelDone = nil
				continue
			}
			modelDone = nil
			modelFinished = true
			metrics.modelComplete = time.Now()
			p.emit(events.ThinkingComplete{Elapsed: metrics.elapsed(metrics.modelComplete)})
			if result.Err != nil && !interrupted {
				if result.Recovered {
					// The brain already streamed a spoken recovery reply
					// through Chunks; let the turn drain and complete degraded
					// while the failure lands in the activity feed.
					metrics.degraded = true
					p.emit(events.Error{Stage: "brain", Message: result.Err.Error()})
					logDegradedTurn("brain", result.Err)
					continue
				}
				if p.Player != nil {
					p.Player.Stop()
				}
				return strings.TrimSpace(fullResponse.String()), interrupted, fmt.Errorf("brain: %w", mapIdleDeadline(ctx, result.Err))
			}

		case sentence, ok := <-sentenceCh:
			if !ok {
				sentences = nil
				// Drain the think filter: Feed holds a trailing "<" in case it
				// opens a think tag, and at end of stream nothing more can
				// arrive to disambiguate it — the same end-of-stream Flush the
				// label stripper does in brain.go.
				//
				// This cannot change what is spoken today: the hold is only ever
				// a prefix of a tag, so the one value Flush releases is a lone
				// "<", which speakableSegment rejects anyway. It routes rather
				// than drops so that if the hold ever carries content, the loop
				// judges it by the same predicate as every other segment.
				routeSegment(proj.flushThink())
				// A half-full batch is still a real segment: flush it before
				// closing, or the last sentence of every odd-length reply is
				// silently never spoken.
				if !interrupted {
					flushVoiceBatch()
				}
				closeSynthQueue() // no more handoffs; let the worker exit
				continue
			}

			sentence = strings.TrimSpace(sentence)
			if sentence == "" {
				continue
			}

			// Think blocks come off before anything else reads the segment.
			// They look like speech (letters, periods) so the gate would read
			// them aloud, and the same filtered text feeds the transcript — so
			// the model never reads its own reasoning back out of conversation
			// history on the next turn, paying its tokens every turn after.
			// A tag still on Chunks after the brain split is a leak: strip it
			// and tell the operator, but do not stall the turn.
			if textclean.ContainsThinkTag(sentence) {
				metrics.thinkLeak++
				p.emit(events.Error{Stage: "think", Message: "think markup leaked onto the speech bus"})
			}
			routeSegment(proj.filterThink(sentence))

		case event := <-playbackEvents:
			if interrupted && event.kind == playbackStarted && p.Player != nil {
				// A synthesis that raced the barge-in finished after Stop();
				// stop the late playback immediately.
				p.Player.Stop()
			}
			if p.applyPlaybackEvent(event, metrics, &armAt) {
				pending--
			}
		}
	}

	if !modelFinished {
		metrics.modelComplete = time.Now()
		p.emit(events.ThinkingComplete{Elapsed: metrics.elapsed(metrics.modelComplete)})
	}

	if !interrupted {
		cancel()
		if bargeDone != nil {
			<-bargeDone
		}
		// Near-miss retention (F3.3): the watcher heard pause speech that never
		// reached the trip threshold. The utterance is in the capture ring
		// buffer — keep it so the next listen transcribes it instead of
		// Reset()ing it away.
		if ctrl != nil && ctrl.sawPauseSpeechWithin(bargeInNearMissWindow) {
			p.keepCapture = true
		}
		p.resetEchoState()
	}

	return strings.TrimSpace(fullResponse.String()), interrupted, nil
}

// softCancelTTS asks the primary provider to abandon in-flight synthesis without
// process kill when it implements tts.SoftCanceler (native qwen3-tts-worker).
func (p *Pipeline) softCancelTTS(reason string) {
	primary, _ := p.ttsProviders()
	if sc, ok := primary.(tts.SoftCanceler); ok {
		_ = sc.SoftCancel(reason)
	}
}

// discardPCMStream drains a synthesized stream that will not be played so the
// producer goroutine is not left blocked on a full frames channel. Drain runs
// asynchronously: the caller must not wait on synth completion after mute.
func discardPCMStream(stream *audio.PCMStream) {
	if stream == nil {
		return
	}
	go func() {
		for range stream.Frames() {
		}
	}()
}

// synthesizeSegment is the synth scheduler stage, run on the ordered synth
// worker: it announces the segment, synthesizes it, starts playback, and hands
// the playback to the watcher goroutine, returning true when a playback was
// enqueued so the loop can track it as pending. TTS/playback failures surface as
// Error events; failures after the turn context is canceled (cancel or barge-in)
// are swallowed so teardown stays quiet. It never blocks on a full queue —
// backpressure is the loop's job via the pending count.
//
// stickFallback, when non-nil, is turn-scoped sticky Kokoro fallback: after the
// first successful primary→fallback switch, later sentences skip the primary so
// one reply cannot alternate persona voice and Kokoro.
func (p *Pipeline) synthesizeSegment(ctx context.Context, loopDone <-chan struct{}, sentence string, index int, audioStarted *atomic.Bool, out chan<- playbackEvent, stickFallback *bool, realAudioQueued ...*atomic.Bool) bool {
	if ctx.Err() != nil || p.OutputMuted() {
		return false // canceled while queued: drain without synthesizing
	}
	seg := events.SpeechSegmentReady{Text: sentence}
	if index == 0 {
		p.activeMu.Lock()
		m := p.activeMetrics
		p.activeMu.Unlock()
		if m != nil && !m.firstAdmit.IsZero() && !m.firstAdmit.Equal(m.firstSpeakable) {
			seg.Elapsed = m.elapsedFromEOU(m.firstAdmit)
		}
	}
	p.emit(seg)
	p.emit(events.GeneratingVoice{Sentence: sentence})

	synthStarted := time.Now()
	stream, usedFallback, err := p.synthUtterance(ctx, sentence, stickFallback)
	if err != nil {
		if ctx.Err() == nil {
			p.emit(events.Error{Stage: "tts", Message: fmt.Sprintf("TTS: %v", err)})
		}
		return false
	}
	if p.OutputMuted() {
		// Synthesize already started a producer goroutine. Drain so it is not
		// left blocked on the buffered frames channel until turn cancel.
		discardPCMStream(stream)
		return false
	}

	firstRealAudio := true
	if len(realAudioQueued) > 0 && realAudioQueued[0] != nil {
		firstRealAudio = realAudioQueued[0].CompareAndSwap(false, true)
	}
	if firstRealAudio {
		p.stopBackchannel()
	}
	playback, err := p.Player.PlayStream(ctx, stream)
	if err != nil {
		if !usedFallback {
			playback, usedFallback, err = p.playFallback(ctx, sentence, err, stickFallback)
		}
		if err == nil {
			// The fallback playback is ready; continue to enqueue its watcher.
		} else {
			// PlayStream owns stream once invoked (real Player pumps even when
			// waitReady fails); do not discard here or two consumers race.
			if ctx.Err() == nil {
				p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", err)})
			}
			return false
		}
	}
	// For a whole-utterance engine PlayStream returns once the audio is
	// buffered, so this is where synthesis ended. A streamed segment becomes
	// audible long before that; watchPlayback replaces this with the player's
	// own stamp of when the last frame arrived.
	engineElapsed := time.Since(synthStarted)

	// PlayStream buffers a complete sentence before enqueueing it. Muting can
	// race that wait, so the initial Stop may see nothing; close the race by
	// checking once more after enqueue and stopping the late segment.
	if p.OutputMuted() {
		p.Player.Stop()
		return false
	}

	go watchPlayback(loopDone, sentence, index, usedFallback, synthStarted, engineElapsed, playback, audioStarted, out)
	return true
}

// synthesizeWithFallback retries a synchronous provider failure once with the
// configured fallback. A Qwen worker failure is surfaced by PlayStream instead
// and is handled by playFallback below because Player owns that stream.
//
// stickFallback, when non-nil, latches true after the first successful switch so
// later sentences in the same turn stay on Kokoro (one voice switch max per
// reply, not uncle-fu / af_heart / uncle-fu thrashing).
func (p *Pipeline) synthesizeWithFallback(ctx context.Context, text string, stickFallback *bool) (*audio.PCMStream, bool, error) {
	primary, fallback := p.ttsProviders()
	if primary == nil {
		return nil, false, errors.New("TTS provider is not configured")
	}
	if stickFallback != nil && *stickFallback && p.canUseFallback(ctx, primary, fallback) {
		fallbackStream, fallbackErr := fallback.Synthesize(ctx, text)
		if fallbackErr != nil {
			return nil, true, fmt.Errorf("kokoro fallback (sticky): %w", fallbackErr)
		}
		return fallbackStream, true, nil
	}
	stream, err := primary.Synthesize(ctx, text)
	if err == nil {
		p.boundPrimarySilence(stream, len(text))
		return stream, false, nil
	}
	if !p.canUseFallback(ctx, primary, fallback) || !shouldFallback(err) {
		return nil, false, err
	}
	p.emit(events.Error{Stage: events.StageTTSFallback, Message: ttsFallbackMessage("primary TTS failed", err, stickFallback != nil)})
	fallbackStream, fallbackErr := fallback.Synthesize(ctx, text)
	if fallbackErr != nil {
		return nil, true, fmt.Errorf("primary TTS: %v; Kokoro fallback: %w", err, fallbackErr)
	}
	if stickFallback != nil {
		*stickFallback = true
	}
	return fallbackStream, true, nil
}

// ttsFallbackMessage describes a Kokoro fallback switch. When sticky is true
// the turn will stay on fallback for later sentences; otherwise only this
// utterance is affected (speakBestEffort / text-mode paths).
func ttsFallbackMessage(prefix string, err error, sticky bool) string {
	if sticky {
		return fmt.Sprintf("%s; speaking with Kokoro (different voice) for the rest of the reply: %v", prefix, err)
	}
	return fmt.Sprintf("%s; retrying this sentence with Kokoro (different voice): %v", prefix, err)
}

// playFallback retries an error returned after Player takes ownership of the
// primary stream. This covers file-oriented native workers whose failure is
// reported when the stream becomes ready.
func (p *Pipeline) playFallback(ctx context.Context, text string, primaryErr error, stickFallback *bool) (*audio.Playback, bool, error) {
	primary, fallback := p.ttsProviders()
	if !p.canUseFallback(ctx, primary, fallback) || !shouldFallback(primaryErr) {
		return nil, false, primaryErr
	}
	p.emit(events.Error{Stage: events.StageTTSFallback, Message: ttsFallbackMessage("primary playback TTS failed", primaryErr, stickFallback != nil)})
	stream, err := fallback.Synthesize(ctx, text)
	if err != nil {
		return nil, true, fmt.Errorf("primary playback TTS: %v; Kokoro fallback: %w", primaryErr, err)
	}
	if stickFallback != nil {
		*stickFallback = true
	}
	playback, err := p.Player.PlayStream(ctx, stream)
	return playback, true, err
}

// shouldFallback limits Kokoro recovery to provider failures that may be
// transient or caused by an unusable worker output. Permanent request/config
// errors must reach the user instead of being silently converted into a
// different voice policy.
func shouldFallback(err error) bool {
	if err == nil {
		return false
	}
	// A stalled engine is precisely what the fallback voice exists for.
	if errors.Is(err, audio.ErrStreamStalled) {
		return true
	}
	// An explicit provider classification outranks the raw cause. A worker that
	// times out reports ProviderErrorWorker while still unwrapping to
	// context.DeadlineExceeded, so sniffing the cause first refused the
	// fallback for the exact failure it should have covered. Turn-level
	// cancellation is still refused below, and canUseFallback re-checks the
	// turn context before anything is synthesized.
	if tts.IsProviderErrorKind(err, tts.ProviderErrorCanceled) {
		return false
	}
	if tts.IsProviderErrorKind(err, tts.ProviderErrorUnavailable) ||
		tts.IsProviderErrorKind(err, tts.ProviderErrorWorker) ||
		tts.IsProviderErrorKind(err, tts.ProviderErrorMalformed) {
		return true
	}
	return false
}

// boundPrimarySilence arms the primary engine's stall budget on a synthesis
// stream so a worker that produces no audio fails the segment over to the
// fallback voice instead of holding the turn until the watchdog abandons it.
//
// Only the primary is bounded. The fallback is the last voice available, so
// failing it would produce silence rather than another retry. Providers that
// publish no grace (Kokoro) leave the stream unbounded, exactly as before.
func (p *Pipeline) boundPrimarySilence(stream *audio.PCMStream, chars int) {
	if stream == nil {
		return
	}
	if budget := p.ttsFirstAudioGrace(chars); budget > 0 {
		stream.SetStallBudget(budget)
	}
}

func (p *Pipeline) canUseFallback(ctx context.Context, primary, fallback tts.Provider) bool {
	return ctx.Err() == nil && !p.OutputMuted() && fallback != nil && fallback != primary && fallback.Available()
}

// applyPlaybackEvent applies one playback lifecycle event (produced by the
// watchPlayback watcher) to the turn's metrics and events. It returns true when
// the event marks a finished playback so the caller can decrement its pending
// count.
func (p *Pipeline) applyPlaybackEvent(event playbackEvent, metrics *turnMetrics, armAt *atomic.Int64) (finished bool) {
	switch event.kind {
	case playbackNotEnqueued:
		return true

	case playbackStarted:
		// Arm once per turn, on the first playback: re-arming per sentence left
		// short sentences uninterruptible and re-imposed the delay across every
		// queue-drain gap (WI-dc9e33 B3 / F3.2).
		now := time.Now()
		armAt.CompareAndSwap(0, now.Add(bargeInArmDelay).UnixNano())
		// Re-check sink at pcmReady (PlayStream already returned) so a Stage
		// that opted in during generate still names AudibleSink / TTFA.
		metrics.audibleSink = p.sinkKind()
		if metrics.firstAudioReady.IsZero() {
			metrics.firstAudioReady = now
			if metrics.synthFirstFrame.IsZero() {
				metrics.synthFirstFrame = now
			}
			// No sttFinal, no sample — deliberately. The only fallback stamp,
			// metrics.start, includes the user's own speaking time, so it would
			// inflate the rolling p50 and open the filler gate on turns that are
			// actually fast. Text turns never play filler, so their missing
			// sample costs nothing.
			if p.backchannel != nil && !metrics.sttFinal.IsZero() {
				p.backchannel.observe(metrics.firstAudioReady.Sub(metrics.sttFinal))
			}
		}
		if metrics.playbackStart.IsZero() {
			metrics.playbackStart = now
		}
		p.stampTTFA(metrics, now)
		// The player went idle after the previous sentence and has only now
		// found the next one ready. That gap is audible silence.
		if !metrics.lastPlaybackEnd.IsZero() {
			metrics.deadAir += time.Since(metrics.lastPlaybackEnd)
			metrics.lastPlaybackEnd = time.Time{}
		}
		p.emit(events.VoiceGenerated{
			Sentence: event.sentence,
			Elapsed:  event.synthElapsed,
			Index:    event.index,
			Fallback: event.fallback,
		})
		started := events.SpeakingStarted{Text: event.sentence, AudibleSink: metrics.audibleSink}
		if !metrics.ttfa.IsZero() {
			started.Elapsed = metrics.elapsedFromEOU(metrics.ttfa)
		}
		p.emit(started)
		return false

	case playbackFinished:
		metrics.playbackComplete = time.Now()
		// Stamped unconditionally: if this was the turn's last segment the
		// stamp is simply never consumed, so trailing silence after the reply
		// is not counted as a gap inside it.
		metrics.lastPlaybackEnd = metrics.playbackComplete
		p.maybeStampLateTTFA(metrics)
		if metrics.audibleSink == audio.SinkNone && len(metrics.segments) == 0 {
			p.emit(events.Info{Message: "synth drain (no sink)"})
		}
		metrics.recordSegment(event)
		complete := events.SpeakingComplete{
			Elapsed:     event.playElapsed,
			Interrupted: event.result.Interrupted,
			AudibleSink: metrics.audibleSink,
		}
		if event.result.Interrupted {
			p.emit(complete)
			return true
		}
		if event.result.Err != nil && !errors.Is(event.result.Err, context.Canceled) {
			p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", event.result.Err)})
		}
		p.emit(complete)
		return true
	}
	return false
}

// handlePlaybackLifecycle drives one utterance queued outside the turn loop —
// today only the degraded turn's recovery line. Its segment is recorded like
// any other: a degraded turn that spoke still paid for the synthesis, and a
// metrics record showing zero segments beside audible speech is wrong.
func (p *Pipeline) handlePlaybackLifecycle(sentence string, fallback bool, synthStarted time.Time, engineElapsed time.Duration, playback *audio.Playback, metrics *turnMetrics) {
	var startedAt time.Time
	synthElapsed := time.Duration(0)
	// Index continues the turn's own numbering: recovery speech follows
	// whatever the reply managed to say.
	index := len(metrics.segments)

	select {
	case <-playback.Started():
		startedAt = time.Now()
		synthElapsed = time.Since(synthStarted)
		metrics.audibleSink = p.sinkKind()
		if metrics.firstAudioReady.IsZero() {
			metrics.firstAudioReady = startedAt
		}
		if metrics.synthFirstFrame.IsZero() {
			metrics.synthFirstFrame = startedAt
		}
		if metrics.playbackStart.IsZero() {
			metrics.playbackStart = startedAt
		}
		p.stampTTFA(metrics, startedAt)
		p.emit(events.VoiceGenerated{
			Sentence: sentence,
			Elapsed:  synthElapsed,
			Index:    index,
			Fallback: fallback,
		})
		started := events.SpeakingStarted{Text: sentence, AudibleSink: metrics.audibleSink}
		if !metrics.ttfa.IsZero() {
			started.Elapsed = metrics.elapsedFromEOU(metrics.ttfa)
		}
		p.emit(started)
	case result := <-playback.Done():
		if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
			p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", result.Err)})
		}
		metrics.playbackComplete = time.Now()
		p.maybeStampLateTTFA(metrics)
		if metrics.audibleSink == audio.SinkNone {
			p.emit(events.Info{Message: "synth drain (no sink)"})
		}
		metrics.recordSegment(playbackEvent{
			sentence:      sentence,
			index:         index,
			fallback:      fallback,
			engineElapsed: engineElapsed,
			synthElapsed:  time.Since(synthStarted),
			result:        result,
		})
		p.emit(events.SpeakingComplete{Interrupted: result.Interrupted, AudibleSink: metrics.audibleSink})
		return
	}

	result := <-playback.Done()
	elapsed := time.Duration(0)
	if !startedAt.IsZero() {
		elapsed = time.Since(startedAt)
	}
	metrics.recordSegment(playbackEvent{
		sentence:      sentence,
		index:         index,
		fallback:      fallback,
		engineElapsed: engineElapsed,
		synthElapsed:  synthElapsed,
		playElapsed:   elapsed,
		result:        result,
	})

	if result.Err != nil && !result.Interrupted && !errors.Is(result.Err, context.Canceled) {
		p.emit(events.Error{Stage: "playback", Message: fmt.Sprintf("playback: %v", result.Err)})
	}
	if result.Interrupted {
		p.emit(events.SpeakingInterrupted{Reason: "stopped"})
	}
	metrics.playbackComplete = time.Now()
	p.maybeStampLateTTFA(metrics)
	if metrics.audibleSink == audio.SinkNone {
		p.emit(events.Info{Message: "synth drain (no sink)"})
	}
	p.emit(events.SpeakingComplete{
		Elapsed:     elapsed,
		Interrupted: result.Interrupted,
		AudibleSink: metrics.audibleSink,
	})
}

// observeStream is the model-observer stage. It is the sole consumer of
// stream.Chunks and stream.Thinking. Chunks are forwarded for sentence
// segmentation; Thinking is drained so a thinking producer cannot block, and
// emitted as ThinkingDelta for the operator. The terminal result path
// (stream.Done) is owned separately by streamResponse.
//
// A closed Thinking channel must be niled: a closed case delivers "" forever
// and can starve Chunks.
//
// The forwarding send is cancellation-safe: on ctx cancellation the stage
// returns instead of blocking on a downstream that has stopped reading.
// The returned done channel closes once the goroutine has fully exited, so a
// caller can join it before reading metrics (it stamps firstModelChunk) and
// avoid racing snapshot() on an early return.
func (p *Pipeline) observeStream(ctx context.Context, stream *brain.Stream, metrics *turnMetrics) (<-chan string, <-chan struct{}) {
	out := make(chan string, 8)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer close(out)
		chunks, thinking := stream.Chunks, stream.Thinking
		stampToken := func(now time.Time) {
			if metrics.firstModelChunk.IsZero() {
				metrics.firstModelChunk = now
			}
			if metrics.firstToken.IsZero() {
				metrics.firstToken = now
				p.emit(events.ResponseStreamingStarted{Elapsed: metrics.elapsedFromEOU(now)})
			}
		}
		for chunks != nil || thinking != nil {
			select {
			case <-ctx.Done():
				return
			case t, ok := <-thinking:
				if !ok {
					thinking = nil
					continue
				}
				if t == "" {
					continue
				}
				p.bumpBrainDeadline()
				now := time.Now()
				stampToken(now)
				if metrics.firstThink.IsZero() {
					metrics.firstThink = now
				}
				p.emit(events.ThinkingDelta{Text: t})
			case c, ok := <-chunks:
				if !ok {
					chunks = nil
					continue
				}
				if c == "" {
					continue
				}
				p.bumpBrainDeadline()
				now := time.Now()
				stampToken(now)
				if metrics.firstSpeakable.IsZero() && strings.TrimSpace(c) != "" {
					metrics.firstSpeakable = now
					if !metrics.firstSpeakable.Equal(metrics.firstToken) {
						p.emit(events.ResponseSpeakableStarted{Elapsed: metrics.elapsedFromEOU(now)})
					}
				}
				p.emit(events.ResponseDelta{Text: c})
				select {
				case <-ctx.Done():
					return
				case out <- c:
				}
			}
		}
	}()

	return out, done
}

// collectTextStream drains a brain stream to completion for the text-input
// path, letting observeStream emit ResponseStreamingStarted and per-chunk
// ResponseDelta events along the way, and returns the full accumulated text.
// It waits on stream.Done for the terminal error so a failed stream surfaces
// even after partial chunks were displayed.
func (p *Pipeline) collectTextStream(ctx context.Context, stream *brain.Stream, metrics *turnMetrics) (string, error) {
	out, done := p.observeStream(ctx, stream, metrics)

	var b strings.Builder
	for chunk := range out {
		b.WriteString(chunk)
	}
	<-done

	select {
	case res := <-stream.Done:
		if res.Err != nil {
			if res.Recovered {
				// The streamed text already ends with the brain's recovery
				// reply; keep it and surface the failure as activity detail.
				metrics.degraded = true
				p.emit(events.Error{Stage: "brain", Message: res.Err.Error()})
				logDegradedTurn("brain", res.Err)
				break
			}
			// Return any partial text so the caller's recovery path can keep
			// it in the transcript alongside the recovery reply.
			return strings.TrimSpace(b.String()), mapIdleDeadline(ctx, res.Err)
		}
	case <-ctx.Done():
		return strings.TrimSpace(b.String()), mapIdleDeadline(ctx, ctx.Err())
	}
	return strings.TrimSpace(b.String()), nil
}

func (p *Pipeline) resetEchoState() {
	// The capture buffer is drained at the start of the next listen (see
	// transcribeTurn), which also catches echo arriving after playback stops.
	if p.VAD != nil {
		p.VAD.Clear()
	}
	if p.BargeInVAD != nil {
		p.BargeInVAD.Clear()
	}
}

func (p *Pipeline) emit(event events.Event) {
	if p.Events != nil {
		p.Events.Emit(event)
	}
}

func (p *Pipeline) bindBrainInfo() {
	if is, ok := p.Brain.(interface{ SetInfoSink(func(events.Info)) }); ok {
		is.SetInfoSink(func(info events.Info) { p.emit(info) })
	}
	if cs, ok := p.Brain.(interface{ SetContinuitySink(func(events.Event)) }); ok {
		cs.SetContinuitySink(func(e events.Event) { p.emit(e) })
	}
}

// toolStartHook emits ToolCallStarted for UI (stdout + TUI).
func (p *Pipeline) toolStartHook() func(name, summary string) {
	return func(name, summary string) {
		p.bumpBrainDeadline()
		p.emit(events.ToolCallStarted{Name: name, Summary: summary})
	}
}

// toolEndHook emits ToolCallFinished for UI.
func (p *Pipeline) toolEndHook() func(name, preview string) {
	return func(name, preview string) {
		p.bumpBrainDeadline()
		p.emit(events.ToolCallFinished{Name: name, Preview: preview})
	}
}

// usageHook emits per-request token accounting so prefix-cache regressions
// are visible in the activity feed, not vibes.
func (p *Pipeline) usageHook() func(prefill, gen int) {
	return func(prefill, gen int) {
		p.emit(events.TokenUsage{Prefill: prefill, Gen: gen})
	}
}

// sessionWarnHook emits SessionWarning so a growing harness session is visible
// in the activity feed before it becomes a latency or cost problem.
// promptWarnHook surfaces a non-fatal prompt-resolution failure. The turn keeps
// speaking on its last good prompt, so this is reported as an error event for
// visibility rather than failing the turn — a half-saved persona file must not
// end a live conversation, but it must not be silent either, or the user is left
// wondering why their edit did nothing.
func (p *Pipeline) promptWarnHook() func(message string) {
	return func(message string) {
		p.emit(events.Error{Stage: "prompt", Message: message})
	}
}

func (p *Pipeline) sessionWarnHook() func(promptTokens, threshold int) {
	return func(promptTokens, threshold int) {
		p.emit(events.SessionWarning{PromptTokens: promptTokens, Threshold: threshold})
	}
}

// CompactConversation shrinks the model's working context while keeping the
// gist, identically for every provider. One summarize turn runs first — the
// provider sees its own full context (harness CLI transcript or messages[]),
// so the summary is grounded in what the model actually knows, not a local
// mirror. LoadHistory then performs the kind-appropriate reset+seed: harness
// providers drop the resume id and carry the seed in via the first-turn
// flatten; ollama rebuilds messages[]. The seed keeps the last exchange
// verbatim after the summary so the next turn continues naturally.
//
// Callers must own the pipeline (no turn in flight) — the TUI gates this the
// same way it gates text turns.
// maxCompactTail bounds the turns kept after the summary. With the summary
// itself that is 5, so the next turn still fits the providers' ≤6-turn flatten
// window.
const maxCompactTail = 4

// compactTail picks the turns that follow the summary in the reseeded history:
// the exchange the conversation is in the middle of, found by walking back to
// the last user turn rather than blindly taking the last two. An ollama tool
// loop ends on tool/assistant turns, so a fixed pair can miss the user turn
// that started the exchange entirely.
//
// Tool turns never survive the reseed: brain.Turn carries no tool-call id, so a
// tool result cannot be seeded together with the assistant message that
// requested it — a stranded one is a malformed messages[] for the next request.
// Empty turns (a tool-call-only assistant message) go with them.
func compactTail(before []brain.Turn) []brain.Turn {
	start := len(before)
	for i := len(before) - 1; i >= 0 && len(before)-i <= maxCompactTail; i-- {
		start = i
		if before[i].Role == "user" {
			break
		}
	}

	tail := make([]brain.Turn, 0, len(before)-start)
	for _, t := range before[start:] {
		if t.Role == "tool" || strings.TrimSpace(t.Content) == "" {
			continue
		}
		tail = append(tail, t)
	}
	return tail
}

func (p *Pipeline) autoCompactIfNeeded(ctx context.Context) error {
	if p.Brain == nil || p.CompactPrompt == "" || p.AutoCompactAfter <= 0 {
		return nil
	}
	if len(p.Brain.History()) <= p.AutoCompactAfter {
		return nil
	}
	p.emit(events.Info{Message: "Compacting conversation."})
	return p.CompactConversation(ctx)
}

func (p *Pipeline) CompactConversation(ctx context.Context) error {
	if p.CompactPrompt == "" {
		return errors.New("compact prompt not configured")
	}
	history := p.Brain.History()
	turnsBefore := len(history)
	if turnsBefore < 2 {
		return errors.New("nothing to compact yet")
	}
	// Snapshot before the summarize turn appends to history (History may
	// alias the provider's live slice).
	before := append([]brain.Turn(nil), history...)
	tail := compactTail(before)
	if p.OnCompactBackup != nil {
		p.OnCompactBackup(before)
	}

	ctx, cancel := p.withBrainTimeout(ctx)
	defer cancel()
	// OmitTurnInstruction: the compact briefing asks for a dense document, and
	// the per-turn voice instruction ("2–3 sentences max") would otherwise land
	// after it as the model's last word. Tools stay off — this turn only reads
	// the conversation it already has.
	summary, err := p.Brain.ThinkFull(ctx, p.CompactPrompt, brain.StreamOptions{OmitTurnInstruction: true})
	if err != nil {
		// Providers roll their own appended input back on failure, so history is
		// already the pre-compact transcript here — no LoadHistory (it would
		// drop a still-valid harness session and floor it to the seed).
		return fmt.Errorf("compact summarize turn: %w", err)
	}

	seed := append([]brain.Turn{{Role: "assistant", Content: summary}}, tail...)
	p.Brain.LoadHistory(seed)
	p.emit(events.ConversationCompacted{TurnsBefore: turnsBefore, Summary: summary})
	if p.OnTurn != nil {
		p.OnTurn()
	}
	return nil
}

// watchPlayback is the playback watcher: it turns one playback's lifecycle into
// playbackStarted/playbackFinished events on out. Every send selects on loopDone
// (closed when streamResponse stops draining) so a watcher can never block — and
// leak — after the loop has returned on cancel, stall, or model error. loopDone
// is deliberately not the turn context: a barge-in cancels the turn context but
// the loop keeps consuming, so finished events must still be delivered to drain
// the pending count.
func watchPlayback(loopDone <-chan struct{}, sentence string, index int, fallback bool, synthStarted time.Time, engineElapsed time.Duration, playback *audio.Playback, started *atomic.Bool, out chan<- playbackEvent) {
	var startedAt time.Time
	// synthElapsed is stamped on both terminal paths, not just the audible
	// one: a segment whose playback finished without ever starting still cost
	// its synthesis time, and dropping it would silently bias the aggregate
	// toward the segments that went well.
	synthElapsed := time.Duration(0)

	select {
	case <-loopDone:
		return
	case <-playback.Started():
		started.Store(true)
		startedAt = time.Now()
		synthElapsed = time.Since(synthStarted)
		if !sendPlaybackEvent(loopDone, out, playbackEvent{
			kind:          playbackStarted,
			sentence:      sentence,
			index:         index,
			fallback:      fallback,
			engineElapsed: engineElapsed,
			synthElapsed:  synthElapsed,
		}) {
			return
		}
	case result := <-playback.Done():
		sendPlaybackEvent(loopDone, out, playbackEvent{
			kind:          playbackFinished,
			sentence:      sentence,
			index:         index,
			fallback:      fallback,
			engineElapsed: measuredEngineElapsed(synthStarted, engineElapsed, result),
			synthElapsed:  time.Since(synthStarted),
			result:        result,
		})
		return
	}

	var result audio.PlaybackResult
	select {
	case <-loopDone:
		return
	case result = <-playback.Done():
	}
	elapsed := time.Duration(0)
	if !startedAt.IsZero() {
		elapsed = time.Since(startedAt)
	}

	sendPlaybackEvent(loopDone, out, playbackEvent{
		kind:          playbackFinished,
		sentence:      sentence,
		index:         index,
		fallback:      fallback,
		engineElapsed: measuredEngineElapsed(synthStarted, engineElapsed, result),
		synthElapsed:  synthElapsed,
		playElapsed:   elapsed,
		result:        result,
	})
}

// measuredEngineElapsed prefers the player's stamp of when the synthesizer
// stopped producing over the caller's own timing.
//
// They agree for a whole-utterance engine — PlayStream returns immediately
// after the stream closes — and they disagree by the length of the reply for a
// streamed one, which becomes audible on its first chunk. Without this the
// realtime factor of every streamed segment would read as a fraction of its
// true value, and RealtimeFactor is the number that decides whether an engine
// can keep up. A missing stamp (an interrupted segment, or a player that does
// not report one) keeps the caller's measurement.
func measuredEngineElapsed(synthStarted time.Time, fallback time.Duration, result audio.PlaybackResult) time.Duration {
	if result.SynthesisEndedAt.IsZero() {
		return fallback
	}
	measured := result.SynthesisEndedAt.Sub(synthStarted)
	if measured <= 0 {
		return fallback
	}
	return measured
}

// sendPlaybackEvent delivers ev unless the loop has stopped consuming, returning
// false if it abandoned the send.
func sendPlaybackEvent(loopDone <-chan struct{}, out chan<- playbackEvent, ev playbackEvent) bool {
	select {
	case <-loopDone:
		return false
	case out <- ev:
		return true
	}
}
