package events

import "time"

// Event is the base interface all events implement.
type Event interface {
	eventType() string
}

// EventType returns the event's stable wire name (e.g. "response_ready"),
// for consumers outside this package that need a type discriminator.
func EventType(e Event) string { return e.eventType() }

// STTPhase reports a speech-to-text phase transition.
type STTPhase struct {
	Phase   string        // "listening", "hearing", "transcribing"
	Elapsed time.Duration // time spent in previous phase (0 if starting)
}

func (e STTPhase) eventType() string { return "stt_phase" }

// UserInput carries the transcribed user text.
type UserInput struct {
	Text string
	// Speaker is the stable live label for this turn (speaker-1) when live
	// analysis attributed the utterance. Empty for text-only or unlabeled.
	Speaker string
}

func (e UserInput) eventType() string { return "user_input" }

// TranscriptPartial carries an incremental transcript update.
type TranscriptPartial struct {
	Text string
}

func (e TranscriptPartial) eventType() string { return "transcript_partial" }

// AudioLevel is a throttled, droppable energy sample for TUI meters.
// Source is "input" (mic) or "output" (playback). Level is in [0, 1].
type AudioLevel struct {
	Source string
	Level  float64
}

func (e AudioLevel) eventType() string { return "audio_level" }

// ThinkingStarted signals Claude has begun processing.
type ThinkingStarted struct{}

func (e ThinkingStarted) eventType() string { return "thinking_started" }

// ThinkingComplete signals Claude has finished.
type ThinkingComplete struct {
	Response     string
	FullResponse string
	Elapsed      time.Duration
}

func (e ThinkingComplete) eventType() string { return "thinking_complete" }

// TurnMetrics captures per-turn latency milestones for benchmarking.
type TurnMetrics struct {
	// Outcome is the turn's terminal state as decided by the turn state
	// machine: completed, interrupted, failed, or timed_out.
	Outcome string
	// Degraded marks a completed turn whose reply is a recovery line after a
	// hard brain/tool failure rather than a normal model response.
	Degraded               bool
	Interrupted            bool
	STTFinalElapsed        time.Duration
	FirstModelChunkElapsed time.Duration
	ModelCompleteElapsed   time.Duration
	FirstSegmentElapsed    time.Duration
	FirstAudioReadyElapsed time.Duration
	PlaybackStartElapsed   time.Duration
	// PlaybackCompleteElapsed is local-speaker Done from T_eou. Zero when
	// AudibleSink is stream_client or none — fanout drain is not playback.
	// Do not subtract PlaybackStartElapsed (start-relative) from this field.
	PlaybackCompleteElapsed time.Duration
	BargeInElapsed          time.Duration
	// FirstTokenElapsed is T_eou to the first byte on Chunks or Thinking.
	FirstTokenElapsed time.Duration
	// FirstThinkElapsed is T_eou to the first ThinkingDelta. Zero if none.
	FirstThinkElapsed time.Duration
	// FirstSpeakableElapsed is T_eou to the first non-whitespace Chunks byte.
	// Not SpeakTTFTMS — that is FirstAdmitElapsed.
	FirstSpeakableElapsed time.Duration
	// FirstAdmitElapsed is T_eou to the first speakable segment admitted to
	// synth. Aligns with SpeakTTFTMS.
	FirstAdmitElapsed time.Duration
	// SynthFirstFrameElapsed is T_eou to the first PCM the synthesizer produced.
	SynthFirstFrameElapsed time.Duration
	// TTFAElapsed is T_eou to the first sample an audible sink accepted.
	// Zero when AudibleSink is none, or a stream client dropped every chunk.
	TTFAElapsed time.Duration
	// AudibleSink is local_speaker, stream_client, or none.
	AudibleSink string
	// ThinkLeak counts think tags that reached filterThink after the brain split.
	ThinkLeak int
	// ToolLeakLines counts lines the voice gate stripped from speech this
	// turn — tool-shaped text the model narrated into its reply (WI-dc9e33
	// B4). The chat transcript keeps the raw text; only TTS is filtered.
	ToolLeakLines int
	// TTSProvider and TTSTier identify which synthesizer produced this turn's
	// audio. A latency artifact that does not say which one ran cannot be
	// compared against another, and the tier matters as much as the provider:
	// qwen3-tts 1.7b measured 1.40x slower than 0.6b on the same machine.
	TTSProvider string
	TTSTier     string
	// TTSPlace is which computer synthesized: "local" or "lan". Provider and
	// tier alone do not identify an engine once speech can run off-box — the
	// same qwen3-tts:0.6b is different silicon plus a socket over LAN — so a
	// turn that does not record its place cannot be compared with one that
	// ran elsewhere.
	TTSPlace string
	// TTSWorkerStreaming is the speech worker's ready.streaming frame
	// (spec/30 §2). False means the worker generates the whole utterance
	// before any PCM exists, so first audio cannot arrive before synthesis
	// finishes no matter what the rest of the pipeline does. It is recorded
	// beside the timings because it is what makes them explicable.
	TTSWorkerStreaming bool
	// TTSWarm reports that every recorded segment supplied explicit warm-session
	// evidence. False means at least one segment was cold/unknown; callers must
	// not infer warmness from elapsed time or from a successful synthesis.
	TTSWarm bool
	// TTSFallbacks counts segments the configured provider failed to
	// synthesize, which the Kokoro fallback spoke instead. A turn that fell
	// back is not a measurement of the configured provider.
	TTSFallbacks int
	// FirstAudioGrace is the stall watchdog's budget for this turn. It is
	// recorded because a slow turn and a hung one are indistinguishable until
	// the grace expires: a 30s wait under a 47s grace never fires the
	// watchdog, so the budget belongs beside the measurement.
	FirstAudioGrace time.Duration
	// StallWatchdogFired marks a turn cut short by that watchdog rather than
	// by synthesis completing.
	StallWatchdogFired bool
	// DeadAirElapsed is the silence between segments: time the player spent
	// idle after one sentence finished and before the next was ready.
	//
	// It is the cost of a synthesizer that cannot keep up, and until it was
	// measured nothing in the system could see it — the stall watchdog only
	// ever checks the FIRST audible frame, so a turn that starts speaking and
	// then pauses for seconds between every sentence is, to the watchdog, a
	// completely healthy turn.
	DeadAirElapsed time.Duration
	// Underruns is the turn's total of the per-segment counts below: gaps
	// inside a sentence, which only incremental playback can produce.
	//
	// DeadAirElapsed cannot see these. It measures the player going idle
	// between segments; a segment that starts speaking, runs dry mid-word and
	// resumes never goes idle by that definition, so before this counter a
	// stuttering streamed reply and a clean one produced identical metrics.
	Underruns int
	// Segments carries per-segment synthesis cost in playback order. The turn
	// milestones above only ever describe the first segment; everything after
	// it — where a long reply spends its time — is here.
	Segments []SegmentMetric
}

func (e TurnMetrics) eventType() string { return "turn_metrics" }

// SpeechDiagnostic is an attributed speech observation. Measurements are
// emitted for Activity/log consumers; ActionCode is populated only when the
// evidence supports a user-facing remedy. Message is intentionally concise
// and does not imply that a setting was changed.
type SpeechDiagnostic struct {
	Symptom      string
	Cause        string
	Confidence   string
	Provider     string
	Scope        string
	ActionCode   string
	DedupeKey    string
	Message      string
	EngineRTF    float64
	Gap          time.Duration
	SampleCount  int
	AudioSeconds float64
	Exclusions   []string
}

func (e SpeechDiagnostic) eventType() string { return "speech_diagnostic" }

// SegmentMetric is one synthesized speech segment's cost inside a turn.
//
// RealtimeFactor (EngineElapsed / AudioSeconds) is a supporting performance
// measurement. It does not prove the cause of a playback gap; stage attribution
// requires the observed, non-overlapping Gaps intervals below.
type SegmentMetric struct {
	Index int
	Chars int
	// Provider, Tier, Place and WorkerStreaming identify the provider that
	// actually produced this segment. They may differ from turn-level TTS
	// identity when synthesis or playback falls back.
	Provider         string
	Tier             string
	Place            string
	WorkerStreaming  bool
	TextReadyElapsed time.Duration
	// WarmState is evidence captured before this segment's provider request.
	// Unknown/cold segments exclude a turn from warm-session comparisons.
	WarmState             string
	Error                 string
	Exclusions            []string
	WholeSentencePlayback bool
	Gaps                  []SpeechGap
	// Boundary elapsed values are measured on one monotonic clock relative to
	// the turn start. Zero means that stage was not observed.
	SynthQueuedElapsed     time.Duration
	SynthStartedElapsed    time.Duration
	SynthFirstPCMElapsed   time.Duration
	SynthEndedElapsed      time.Duration
	PlaybackStartedElapsed time.Duration
	PlaybackEndedElapsed   time.Duration
	// EngineElapsed is synthesis alone: the segment handed to the provider
	// through the complete utterance being buffered for playback. This is the
	// numerator of the realtime factor.
	EngineElapsed time.Duration
	// SynthElapsed spans the segment being handed to the synthesizer through
	// its first audible frame, so it also includes waiting behind whatever is
	// already playing.
	//
	// Both are kept because they answer different questions, and conflating
	// them makes every segment after the first look slow: segment 1 is
	// synthesized while segment 0 plays, so its wait is the previous
	// sentence's audio, not the engine. Only the first segment's SynthElapsed
	// is a delay a listener actually experiences.
	SynthElapsed time.Duration
	// AudioSeconds is the synthesized duration, measured at the stream's own
	// sample rate.
	AudioSeconds float64
	// PlayElapsed spans the first audible frame through playback finishing.
	PlayElapsed time.Duration
	// Fallback marks a segment the Kokoro fallback spoke after the configured
	// provider failed.
	Fallback bool
	// Interrupted marks a segment cut short by barge-in. Its AudioSeconds
	// still reports what was synthesized.
	Interrupted bool
	// Underruns counts audible gaps *inside* this segment: the speaker ran out
	// of audio while the synthesizer was still producing it. Only a segment
	// played incrementally can have any — a whole-utterance one is complete
	// before it is audible — and one is enough to say streaming is running
	// dry on this machine.
	Underruns int
	// GapElapsed is the raw observed interval immediately before this segment's
	// audible start (when one exists). GapCause is populated only when the
	// boundary evidence identifies one non-overlapping stage. Gaps retains the
	// partition when a raw interval spans multiple stages or unknown time.
	GapElapsed time.Duration
	GapCause   string
}

// SpeechGap is one non-overlapping observed interval in a segment's raw
// playback gap. Unknown intervals are retained instead of being assigned to a
// provider stage by inference.
type SpeechGap struct {
	StartElapsed time.Duration
	EndElapsed   time.Duration
	Cause        string
}

const (
	WarmStateWarm    = "warm"
	WarmStateCold    = "cold"
	WarmStateUnknown = "unknown"
)

// RealtimeFactor reports engine wall time divided by audio duration, or 0
// when no audio was produced.
//
// EngineElapsed, not SynthElapsed: a segment queued behind another sentence's
// playback did not take longer to synthesize, and charging it that wait would
// report an engine as slower than it is.
func (s SegmentMetric) RealtimeFactor() float64 {
	if s.AudioSeconds <= 0 {
		return 0
	}
	engine := s.EngineElapsed
	if engine <= 0 {
		// Pre-split records, and the recovery path before it stamps: fall
		// back to the only figure available rather than reporting nothing.
		engine = s.SynthElapsed
	}
	return engine.Seconds() / s.AudioSeconds
}

// QueueElapsed is the part of SynthElapsed spent waiting for the player, not
// synthesizing. Zero when the split is not available.
func (s SegmentMetric) QueueElapsed() time.Duration {
	if s.EngineElapsed <= 0 || s.SynthElapsed <= s.EngineElapsed {
		return 0
	}
	return s.SynthElapsed - s.EngineElapsed
}

// SpeakingWindow is local-speaker play plus inter-segment dead air — the
// denominator for dead-air share. Zero for stream_client and none: fanout
// PlayElapsed is queue-drain, not acoustic play. Empty AudibleSink is treated
// as local so unit fixtures that omit the field still exercise the math.
func (e TurnMetrics) SpeakingWindow() time.Duration {
	switch e.AudibleSink {
	case "stream_client", "none":
		return 0
	}
	var play time.Duration
	for _, seg := range e.Segments {
		play += seg.PlayElapsed
	}
	if play > 0 {
		return play + e.DeadAirElapsed
	}
	return 0
}

// TokenUsage reports one model request's prompt/generation token counts.
// With a warm prefix cache, Prefill should track the size of the new turn —
// a prefill near the whole transcript means the cache was invalidated.
type TokenUsage struct {
	Prefill int
	Gen     int
}

func (e TokenUsage) eventType() string { return "token_usage" }

// SessionWarning reports that the prompt a harness session replays has crossed
// the configured warn threshold. Emitted at most once per underlying session;
// nothing is dropped — this is visibility, not enforcement.
type SessionWarning struct {
	PromptTokens int
	Threshold    int
}

func (e SessionWarning) eventType() string { return "session_warning" }

// ConversationCompacted reports that /compact replaced the model's working
// context with a summary. The TUI transcript is annotated, not cleared: the
// scrollback stays, but the model now continues from the summary.
type ConversationCompacted struct {
	TurnsBefore int
	Summary     string
}

func (e ConversationCompacted) eventType() string { return "conversation_compacted" }

// ResponseStreamingStarted signals the first token on either brain channel.
type ResponseStreamingStarted struct {
	Elapsed time.Duration
}

func (e ResponseStreamingStarted) eventType() string { return "response_streaming_started" }

// ResponseSpeakableStarted is T_eou to the first non-whitespace Chunks byte.
// Emitted only when that instant differs from the first token.
type ResponseSpeakableStarted struct {
	Elapsed time.Duration
}

func (e ResponseSpeakableStarted) eventType() string { return "response_speakable_started" }

// ResponseDelta carries one incremental piece of assistant text as it streams
// from the brain, for token-by-token display in the TUI. Deltas are advisory
// and droppable under UI back-pressure; ResponseReady always follows with the
// complete, canonical text.
type ResponseDelta struct {
	Text string
}

func (e ResponseDelta) eventType() string { return "response_delta" }

// ThinkingDelta carries one incremental piece of operator-facing reasoning.
// It is never spoken. Advisory and droppable like ResponseDelta.
type ThinkingDelta struct {
	Text string
}

func (e ThinkingDelta) eventType() string { return "thinking_delta" }

// SpeechSegmentReady signals a sentence is ready for voice synthesis.
type SpeechSegmentReady struct {
	Text string
	// Elapsed is FirstAdmitElapsed on the opening segment when it differs
	// from first speakable; zero otherwise.
	Elapsed time.Duration
}

func (e SpeechSegmentReady) eventType() string { return "speech_segment_ready" }

// GeneratingVoice signals TTS generation has started.
type GeneratingVoice struct {
	Sentence string
}

func (e GeneratingVoice) eventType() string { return "generating_voice" }

// VoiceGenerated signals TTS generation is complete.
type VoiceGenerated struct {
	Sentence string
	Elapsed  time.Duration
	// Index is the segment's position in the turn, from 0.
	Index int
	// Fallback marks audio the Kokoro fallback produced after the configured
	// provider failed.
	Fallback bool
}

func (e VoiceGenerated) eventType() string { return "voice_generated" }

// SpeakingStarted signals audio playback has begun.
type SpeakingStarted struct {
	Text        string
	AudibleSink string
	// Elapsed is TTFA from T_eou when an audible sink accepted; zero otherwise.
	Elapsed time.Duration
}

func (e SpeakingStarted) eventType() string { return "speaking_started" }

// SpeakingComplete signals audio playback is done.
type SpeakingComplete struct {
	Elapsed     time.Duration
	Interrupted bool
	AudibleSink string
}

func (e SpeakingComplete) eventType() string { return "speaking_complete" }

// SpeakingInterrupted signals playback was cut short.
type SpeakingInterrupted struct {
	Reason string
	// SincePlaybackStart is how long TTS had been audible — from the turn's
	// first playbackStarted event to this interrupt — when a mic barge-in cut
	// it off. Zero when not applicable (e.g. a keystroke, remote, or
	// client-silent interrupt, which this field does not cover yet).
	SincePlaybackStart time.Duration
}

func (e SpeakingInterrupted) eventType() string { return "speaking_interrupted" }

// TurnInterrupted signals the active assistant turn was canceled.
type TurnInterrupted struct {
	Reason string
	// SincePlaybackStart mirrors SpeakingInterrupted's field of the same
	// name — see there.
	SincePlaybackStart time.Duration
}

func (e TurnInterrupted) eventType() string { return "turn_interrupted" }

// ResponseReady carries the final display text.
type ResponseReady struct {
	Response    string
	Interrupted bool
	// Degraded marks a recovery reply emitted after a hard brain/tool failure;
	// the error detail is delivered separately as an Error event.
	Degraded bool
}

func (e ResponseReady) eventType() string { return "response_ready" }

// ConversationCleared signals history was wiped.
type ConversationCleared struct{}

func (e ConversationCleared) eventType() string { return "conversation_cleared" }

// Error carries an error message for display.
type Error struct {
	Stage   string
	Message string
}

func (e Error) eventType() string { return "error" }

// StageTTSFallback is the one Error stage that reports a recovery rather than a
// failure: the primary engine could not speak a sentence, so Kokoro spoke it.
// The turn still produced audio in a different voice.
//
// It stays an Error because a voice changing mid-reply is precisely the kind of
// thing a listener needs told, and the TUI renders it as such. Anything
// deciding an *outcome* from these events has to tell the two apart, though —
// `benchmark` counted a rescued turn as a failed run and exited non-zero on a
// successful recovery. The name exists so that decision is not a string
// literal repeated in a package that does not own the emit site.
const StageTTSFallback = "tts-fallback"

// Info carries an informational message.
type Info struct {
	Message string
}

func (e Info) eventType() string { return "info" }

// ToolCallStarted signals the brain is invoking a tool (Ollama function call).
// Name is the tool id (list_files, read_file, write_file, run_command, read_skill).
// Summary is a short, non-sensitive arg preview for status UI.
//
// ID pairs a start with its finish. JobID attributes the call to a background
// worker job; empty means the live brain, whose tools drive the orb and status
// line. Kind is the brain's ToolKind carried as a plain string — this package
// is the shared vocabulary and must not depend on brain.
type ToolCallStarted struct {
	ID      string
	JobID   string
	Name    string
	Kind    string
	Summary string
}

func (e ToolCallStarted) eventType() string { return "tool_call_started" }

// ToolCallFinished signals a tool finished. Preview is a short result snippet
// for status/transcript (truncated; never secrets by design of tool handlers).
type ToolCallFinished struct {
	ID      string
	JobID   string
	Name    string
	Kind    string
	Preview string
	Err     string // legacy error detail; absence does not establish success
	Elapsed time.Duration
	Outcome *ToolOutcome
}

func (e ToolCallFinished) eventType() string { return "tool_call_finished" }

// JobQueued signals the table accepted a worker job. It waits behind whatever
// is running; JobStarted follows once it actually begins.
type JobQueued struct {
	ID   string
	Task string
}

func (e JobQueued) eventType() string { return "job_queued" }

// JobStarted signals a worker job's run began. It follows the job's own
// JobQueued and the previous job's terminal event, and precedes every tool
// event the job produces.
type JobStarted struct {
	ID   string
	Task string
}

func (e JobStarted) eventType() string { return "job_started" }

// JobFinished signals a worker job completed successfully.
type JobFinished struct {
	ID      string
	Task    string
	Summary string
	Elapsed time.Duration
}

func (e JobFinished) eventType() string { return "job_finished" }

// JobFailed signals a worker job ended with an error.
type JobFailed struct {
	ID      string
	Task    string
	Err     string
	Elapsed time.Duration
}

func (e JobFailed) eventType() string { return "job_failed" }

// JobAborted signals a worker job was stopped before it finished. Reason is
// "cancelled" when the user stopped it (speech stays silent; this event is
// how the abort becomes visible) or "budget" when it ran past the configured
// job budget (spoken once, since nobody asked for that stop).
type JobAborted struct {
	ID      string
	Task    string
	Elapsed time.Duration
	Reason  string
}

// Abort reasons carried by JobAborted.Reason.
const (
	AbortCancelled = "cancelled"
	AbortBudget    = "budget"
)

func (e JobAborted) eventType() string { return "job_aborted" }

// VoiceGateStripped reports that the pipeline's voice gate removed tool-shaped
// lines from a speech segment before TTS (WI-dc9e33 B4). The stripped text
// still reaches the chat transcript — only the spoken channel is filtered.
type VoiceGateStripped struct {
	Lines int
}

func (e VoiceGateStripped) eventType() string { return "voice_gate_stripped" }

// BackchannelStarted reports that a short filler ("Mm-hm", "Let me think") began
// playing while the real reply was still being produced. Emitted so the TUI and
// serve clients can render it as a filler rather than as speech the model chose
// to say — the distinction matters in a transcript.
type BackchannelStarted struct {
	Phrase string
}

func (e BackchannelStarted) eventType() string { return "backchannel_started" }

// ContinuityBudget reports the estimated prompt size against soft/hard limits
// before an Ollama turn (WI-b94dad observability).
type ContinuityBudget struct {
	EstTokens int
	SoftLimit int
	HardLimit int
	NumCtx    int
	Action    string
}

func (e ContinuityBudget) eventType() string { return "continuity.budget" }

// ContinuityTruncated reports a completion that may have ended before the model
// finished — done_reason=length, prompt saturation, or mid-sentence heuristic.
type ContinuityTruncated struct {
	DoneReason   string
	PromptTokens int
	GenTokens    int
	MidSentence  bool
}

func (e ContinuityTruncated) eventType() string { return "continuity.truncated" }

// ContinuityRecovery reports a truncation recovery attempt. Emitted by S5;
// defined in S1 so the event table compiles.
type ContinuityRecovery struct {
	Attempt  int
	Strategy string
}

func (e ContinuityRecovery) eventType() string { return "continuity.recovery" }
