package pipeline

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

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

// CaptureRetention is an optional extension implemented by local capture.
// Keeping it optional preserves the small CaptureMonitor contract for remote
// and test sources, which have no shared ring to trim.
type CaptureRetention interface {
	ResetKeeping(time.Duration)
}

// rawCaptureRetention is optional: local capture can replace unread
// processed PCM with the aligned pre-frontend tail after a startup barge-in.
type rawCaptureRetention interface {
	ReplaceUnreadWithRaw(time.Duration)
}

// startupAdmissionCapture snapshots and replaces unread capture for the
// DuringArm decoder window. Optional so remote/test sources stay unchanged.
type startupAdmissionCapture interface {
	SnapshotStartupWindows(time.Duration) (raw, residual []float32)
	Ingested() uint64
	Newest(int) []float32
	ReplaceUnread([]float32)
}

type startupAdmission struct {
	residual   []float32
	ingestedAt uint64
}

const (
	startupContinuationWait    = 400 * time.Millisecond
	startupContinuationSamples = 6400 // 400ms at 16kHz; APM starts passing the rest of a full question ~200ms after Stop
	startupContinuationPoll    = 10 * time.Millisecond
)

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
	Input      utterance.Provider
	AdmitAudio func(context.Context, *utterance.Finalized) (ResponseAdmission, error)
	// BeforeCapture refreshes the host binding before opening a reader and
	// snapshotting its policy. Hosts serialize this with clear/restore/selection.
	BeforeCapture func(context.Context) error
	Native        *NativePolicy
	journalMu     sync.RWMutex
	journalHealth events.WorkJournalHealth
	// identityMu protects the host-owned conversation identity used when the
	// optional work coordinator is disabled. Work-enabled conversations copy
	// the coordinator scope here so selection and snapshots use one identity
	// contract in both modes; this is not a second work ledger.
	identityMu      sync.RWMutex
	identityScope   work.WorkScope
	identityRetired bool
	traceOrigin     time.Time

	// AdmitInput runs after finalized input and speaker validation, before
	// compaction/model/speech. The host serializes it with selection controls.
	AdmitInput func(context.Context, string, bool) (ResponseAdmission, error)
	// Decisions is off by default. The host configures independent points; a
	// ready proposal still passes the ordinary worker admission policy.
	Decisions decision.Controller
	// ConfigureDecisions changes optional settings without replacing a brain or
	// interrupting admitted work. The owning runtime performs setup in the background.
	ConfigureDecisions func(*config.Config)
	DecisionStatus     func() decision.RuntimeStatus
	TestDecisions      func(context.Context) (decision.SmokeResult, error)
	// RouteHandoff classifies an intentional handoff without changing selection.
	// The pipeline commits the proposal after joining the speech interrupt watch.
	// It never invokes this callback for background or degraded work.
	RouteHandoff              func(context.Context, brain.Turn, []string) (HandoffProposal, bool, error)
	EndDiscussion             func(string)
	GroupParticipationEnabled bool
	CompleteDiscussion        func(context.Context, brain.Turn) error
	// The serialized host remembers omitted coverage until the next safe
	// boundary, where one deterministic brief replaces a model retry.
	completionFallback map[string]bool
	completionScope    work.WorkScope
	retiredBrains      sync.WaitGroup
	STT                stt.Provider
	Brain              brain.Provider
	TTS                tts.Provider
	// Utterance is nil when the audio source cannot be ended on demand.
	Utterance UtteranceEnder
	// TTSFallback is an optional, explicitly configured provider used for one
	// affected sentence when the selected provider fails before playback.
	// Kokoro is constructed here when voice_fallback_provider=kokoro.
	TTSFallback       tts.Provider
	ttsMu             sync.RWMutex
	ttsRetentions     map[*ttsRetention]struct{}
	Player            audio.Engine
	Capture           CaptureMonitor
	VAD               VoiceDetector
	BargeInVAD        VoiceDetector
	Events            *events.Bus
	VoiceToolsEnabled bool
	// RouterToolsPolicy is the live-brain tool surface (route_only vs legacy).
	RouterToolsPolicy brain.RouterToolsPolicy
	// VoiceFailedRouteRepair enables Layer 3 spawn repair for spoken tool promises.
	VoiceFailedRouteRepair bool
	// OnRoute is invoked when a turn is classified without speaking a chat reply.
	// Sequence 04 wires job/narration handlers here; tests observe spawn/narrate.
	OnRoute func(RouteDecision)
	// NarrationActive reports whether narration mode is active (Layer 1 guards).
	NarrationActive func() bool
	// JobActive reports whether a worker job is running or queued.
	JobActive func() bool
	// Agency handles spawn/abort/status routes and async worker jobs.
	Agency      *Agency
	activeTurns atomic.Int32
	// speechAdviceMu protects the bounded provider-configuration comparison
	// windows used by speech diagnostics. It is separate from turn metrics so
	// advice can survive turn completion without a lifetime-wide once flag.
	speechAdviceMu sync.Mutex
	speechAdvice   map[string]*speechAdviceWindow
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

	// idleFlush overrides how long a buffered sentence group waits for a
	// follower once brain tokens stop. Zero uses idleFlushAfter; tests set it
	// so grouping cases do not run at the product's 350 ms.
	idleFlush time.Duration

	// openingHold overrides the opening group's absolute deadline. Zero uses
	// openingHoldAfter; tests set it so a held beat does not wait 400 ms.
	openingHold time.Duration

	// TTSLookahead bounds how many admitted speech segments a turn may hold
	// ahead of the one playing (config tts_lookahead). Zero uses the compiled
	// default, same convention as PlaybackStallTimeout. See lookahead().
	TTSLookahead     int
	lookaheadClamped sync.Once

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

	// AddressGate, when set, may veto a voice turn after transcription because
	// the utterance was not addressed to the agent. Evaluated before
	// SpeakerGate. A non-empty reason drops the turn the same way SpeakerGate
	// does. Installed per session by the TUI, never by serve: a remote client
	// that sent voice_start addressed the agent by definition.
	AddressGate func(text string) (rejectReason string)
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
	// be before the interrupt controller treats it as stale and blocks interruption
	// (~2x speaker.live.window_ms). Zero uses
	// bargeInSpeakerVerdictMaxAgeDefault. See
	// config.BargeInSpeakerVerdictMaxAgeOf.
	BargeInSpeakerVerdictMaxAge time.Duration

	// keepCapture preserves the capture buffer into the next turn after a
	// barge-in — or a near-miss, speech heard during a playback pause that
	// never tripped — where the buffered audio is the user mid-utterance.
	keepCapture     atomic.Bool
	preparedCapture atomic.Bool
	startup         atomic.Pointer[startupAdmission]
	// CapturePreRoll bounds audio retained across a successful response
	// boundary. It is wired from config.vad_pre_roll_ms.
	CapturePreRoll            time.Duration
	captureMu                 sync.Mutex
	audioMedia                *chatmedia.AudioSession
	captureBoundary           *PreparedInputReplacement
	captureStarting           *captureStartup
	captureCancel             context.CancelFunc
	captureSession            stt.Session
	captureGeneration         uint64
	captureToken              stt.PauseToken
	captureYielded            bool
	captureDeferral           string
	captureRequests           atomic.Uint64
	externalCompletionCapture func(context.Context, *work.Coordinator, string) (work.CapturePermit, error)

	// outputMuted is toggled by interactive clients while a turn may be active.
	outputMuted atomic.Bool

	// activeMetrics points at the in-flight turn's metrics so out-of-band
	// interrupts (keystroke barge-in via StopPlayback) can stamp BargeIn.
	activeMu      sync.Mutex
	activeMetrics *turnMetrics
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
	if muted {
		if p.EndDiscussion != nil {
			p.EndDiscussion("muted")
		}
		if c := p.WorkCoordinator(); c != nil {
			_ = c.RevokeBinding(c.Scope())
		}
	}
	if muted && p.Player != nil {
		p.Player.Stop()
	}
	if !muted {
		p.RecoverCompletionOutput()
	}
}

// OutputMuted reports whether spoken responses are currently disabled.
func (p *Pipeline) OutputMuted() bool { return p.outputMuted.Load() }

// ReplaceTTS atomically changes the providers used by subsequent synthesis
// calls. Callers own provider cleanup and must keep retired providers alive
// until any already-started utterance has drained and TTSRetentions have released.
func (p *Pipeline) ReplaceTTS(primary, fallback tts.Provider) {
	p.ttsMu.Lock()
	p.TTS = primary
	p.TTSFallback = fallback
	p.ttsMu.Unlock()
	p.RecoverCompletionOutput()
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

func (p *Pipeline) resetEchoState() {
	// Reset VAD state after joining the interrupt watcher. Capture retention
	// for the next listen is managed separately at the response boundary.
	// A yielded completion session still consumes capture and owns the STT
	// detector. Resetting it here would race that owner and change native sample
	// offsets without resetting the segmenter's retained onset window.
	p.captureMu.Lock()
	captureActive := p.captureSession != nil
	p.captureMu.Unlock()
	if p.VAD != nil && !captureActive {
		p.VAD.Clear()
	}
	if p.BargeInVAD != nil {
		p.BargeInVAD.Clear()
	}
}

func (p *Pipeline) capturePreRoll() time.Duration {
	return max(0, min(p.CapturePreRoll, 2*time.Second))
}

func (p *Pipeline) retainInterruptedCapture(req interruptRequest, sincePlayback time.Duration) {
	p.keepCapture.Store(true)
	if !req.Source.DuringArm {
		return
	}
	keep := sincePlayback + 150*time.Millisecond
	if keep < 400*time.Millisecond {
		keep = 400 * time.Millisecond
	}
	if keep > 3*time.Second {
		keep = 3 * time.Second
	}
	cap, ok := p.Capture.(startupAdmissionCapture)
	if !ok {
		if raw, ok := p.Capture.(rawCaptureRetention); ok {
			raw.ReplaceUnreadWithRaw(keep)
		}
		return
	}
	_, residual := cap.SnapshotStartupWindows(keep)
	p.startup.Store(&startupAdmission{residual: residual, ingestedAt: cap.Ingested()})
	if raw, ok := p.Capture.(rawCaptureRetention); ok {
		raw.ReplaceUnreadWithRaw(keep)
	}
}

func (p *Pipeline) applyStartupAdmission(ctx context.Context) {
	adm := p.startup.Swap(nil)
	if adm == nil || len(adm.residual) == 0 {
		return
	}
	cap, ok := p.Capture.(startupAdmissionCapture)
	if !ok {
		return
	}
	waitStartupContinuation(ctx, cap, adm.ingestedAt)
	post := newestSince(cap, adm.ingestedAt)
	if len(post) == 0 || tailHasSpeech(continuationTail(post)) {
		return
	}
	cap.ReplaceUnread(audio.PadStartupListen(audio.NormalizeSpeechRMS(adm.residual)))
}

func continuationTail(samples []float32) []float32 {
	if len(samples) <= startupContinuationSamples {
		return samples
	}
	return samples[len(samples)-startupContinuationSamples:]
}

func newestSince(cap startupAdmissionCapture, ingestedAt uint64) []float32 {
	n := cap.Ingested() - ingestedAt
	if cap.Ingested() < ingestedAt || n == 0 {
		return nil
	}
	if n > uint64(^uint(0)>>1) {
		n = uint64(^uint(0) >> 1)
	}
	return cap.Newest(int(n))
}

func waitStartupContinuation(ctx context.Context, cap startupAdmissionCapture, ingestedAt uint64) {
	need := ingestedAt + uint64(startupContinuationSamples)
	if cap.Ingested() >= need {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, startupContinuationWait)
	defer cancel()
	ticker := time.NewTicker(startupContinuationPoll)
	defer ticker.Stop()
	for cap.Ingested() < need {
		select {
		case <-waitCtx.Done():
			return
		case <-ticker.C:
		}
	}
}

func tailHasSpeech(samples []float32) bool {
	if len(samples) == 0 {
		return false
	}
	for i := 0; i < len(samples); i += nearEndHopSamples {
		end := min(i+nearEndHopSamples, len(samples))
		if audio.FrameRMS(samples[i:end]) > audio.StartupPreserveMinRMS {
			return true
		}
	}
	return false
}

const nearEndHopSamples = 960

// prepareNextListen trims already buffered audio to the configured onset
// window and marks it as a prepared listen scope. This is distinct from
// keepCapture: the latter is evidence that the user actually interrupted and
// must not make HasCapturedInput true for every ordinary continuation.
func (p *Pipeline) prepareNextListen() {
	if p.Capture == nil {
		return
	}
	// Near-miss detection may have already marked a real in-progress
	// utterance. Do not trim that utterance down to the ordinary onset window.
	if p.keepCapture.Load() {
		p.clearPreparedCapture()
		return
	}
	if retained, ok := p.Capture.(CaptureRetention); ok {
		retained.ResetKeeping(p.capturePreRoll())
		p.preparedCapture.Store(true)
		return
	}
	// Non-ring sources cannot provide bounded retention. Their existing Reset
	// semantics remain authoritative.
	p.preparedCapture.Store(false)
}

func (p *Pipeline) clearPreparedCapture() {
	p.preparedCapture.Store(false)
}

// CurrentSpeakerID is the finalized-input speaker snapshot used by admission owners.
func (p *Pipeline) CurrentSpeakerID() string { return p.currentSpeakerID() }

// HasCaptureProvider includes finalized native input as well as Standard STT.
// It does not open a reader or imply that a hardware device is available.
func (p *Pipeline) HasCaptureProvider() bool { return p != nil && (p.STT != nil || p.Input != nil) }
