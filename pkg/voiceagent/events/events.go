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
	Degraded                bool
	Interrupted             bool
	STTFinalElapsed         time.Duration
	FirstModelChunkElapsed  time.Duration
	ModelCompleteElapsed    time.Duration
	FirstSegmentElapsed     time.Duration
	FirstAudioReadyElapsed  time.Duration
	PlaybackStartElapsed    time.Duration
	PlaybackCompleteElapsed time.Duration
	BargeInElapsed          time.Duration
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

// SegmentMetric is one synthesized speech segment's cost inside a turn.
//
// RealtimeFactor (SynthElapsed / AudioSeconds) is the quantity that decides
// whether synthesis keeps up with playback: above 1.0 the synthesizer produces
// audio more slowly than the speaker consumes it, so a streaming path would
// underrun and a progressive path opens a gap between sentences.
type SegmentMetric struct {
	Index int
	Chars int
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
}

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

// ResponseStreamingStarted signals the first token on either channel.
type ResponseStreamingStarted struct {
	Elapsed time.Duration
}

func (e ResponseStreamingStarted) eventType() string { return "response_streaming_started" }

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

// ResponseSpeakableStarted is the first non-whitespace Chunks byte. Distinct
// from ResponseStreamingStarted, which fires on the first byte of either
// channel (including thinking).
type ResponseSpeakableStarted struct {
	Elapsed time.Duration
}

func (e ResponseSpeakableStarted) eventType() string { return "response_speakable_started" }

// SpeechSegmentReady signals a sentence is ready for voice synthesis.
type SpeechSegmentReady struct {
	Text string
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
	Text string
}

func (e SpeakingStarted) eventType() string { return "speaking_started" }

// SpeakingComplete signals audio playback is done.
type SpeakingComplete struct {
	Elapsed     time.Duration
	Interrupted bool
}

func (e SpeakingComplete) eventType() string { return "speaking_complete" }

// SpeakingInterrupted signals playback was cut short.
type SpeakingInterrupted struct {
	Reason string
}

func (e SpeakingInterrupted) eventType() string { return "speaking_interrupted" }

// TurnInterrupted signals the active assistant turn was canceled.
type TurnInterrupted struct {
	Reason string
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
type ToolCallStarted struct {
	Name    string
	Summary string
}

func (e ToolCallStarted) eventType() string { return "tool_call_started" }

// ToolCallFinished signals a tool finished. Preview is a short result snippet
// for status/transcript (truncated; never secrets by design of tool handlers).
type ToolCallFinished struct {
	Name    string
	Preview string
	Err     string // empty on success
}

func (e ToolCallFinished) eventType() string { return "tool_call_finished" }

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
