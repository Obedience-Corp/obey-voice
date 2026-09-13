package tts

import (
	"context"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// Provider is the interface all TTS backends implement.
type Provider interface {
	// Synthesize streams synthesized PCM frames for the given text.
	Synthesize(ctx context.Context, text string) (*audio.PCMStream, error)

	// Available returns true if this provider is ready.
	Available() bool

	// ListVoices returns available voices.
	ListVoices(locale, gender string) []Voice
}

// Describer is optionally implemented by providers that can name the engine
// and model size behind them. A latency artifact that does not record both
// cannot be compared against another: the same code on the same machine
// measured 1.33x realtime at qwen3-tts 0.6b and 1.87x at 1.7b.
type Describer interface {
	// ProviderName is the configured provider id, e.g. "kokoro" or
	// "qwen3-tts".
	ProviderName() string
	// ModelTier is the model size within that provider, e.g. "0.6b". Empty
	// when the provider ships exactly one model.
	ModelTier() string
}

// TopologyDescriber is optionally implemented by providers whose synthesis can
// happen on another computer.
//
// Provider and tier are only two thirds of an engine's identity. The same
// qwen3-tts:0.6b measured from a local Metal worker and from a speech-serve
// host over the LAN are different measurements — different silicon, plus a
// socket in the path — and whether PCM actually arrives during decode decides
// whether time-to-first-audio can be anything but the whole synthesis. A
// latency number without that topology cannot be read.
type TopologyDescriber interface {
	// SpeechPlace is PlaceLocal or PlaceLAN.
	SpeechPlace() string
	// WorkerStreaming reports whether this session's PCM arrives *during*
	// synthesis: the worker advertised the capability and this host asked it
	// to use it. False for an engine that synthesizes in this process, for a
	// Stage A worker, and for a capable worker whose backend was measured to
	// underrun — in all three, first audio is the whole synthesis.
	//
	// It is deliberately the effective answer rather than the worker's raw
	// ready.streaming frame. A capable worker told not to stream produces
	// whole-utterance audio, and an artifact recording it as streaming would
	// attribute a whole-utterance latency to a streaming engine. The value
	// travels: TurnMetrics.TTSWorkerStreaming, transcript TTSStreaming, the
	// netapi tts_streaming field and the bench's "worker streams PCM" line all
	// read it, and all of them want what happened, not what was possible.
	WorkerStreaming() bool
}

// WarmDescriber is implemented by providers that can verify whether the
// session used for a measurement was already warm. Absence is deliberately
// treated as unknown; callers must not manufacture warmness from a successful
// request or from a short elapsed time.
type WarmDescriber interface {
	SpeechWarm() bool
}

// WarmStateDescriber reports the pre-request session state. Providers that
// cannot prove a prior successful request must return "unknown" or "cold";
// callers must not infer warmness from the request that is about to run.
type WarmStateDescriber interface {
	SpeechWarmState() string
}

// AdviceCapability describes remedies the provider has actually verified.
// WholeSentencePlayback means the provider can honor a whole-sentence mode
// when streaming is causing interruptions; it does not promise that mode is
// currently selected or that it will be faster.
type AdviceCapability interface {
	WholeSentencePlayback() bool
}

// Where synthesis ran, as recorded on a turn's metrics. These mirror the
// tts_place config values; they are repeated as provider-facing constants so
// nothing on the metrics path has to depend on the config package.
const (
	PlaceLocal = "local"
	PlaceLAN   = "lan"
)

// SpeechPlaceOf reports which computer a provider speaks from. Only the Qwen
// speech path has a LAN transport, so a provider that does not describe itself
// synthesized in this process and PlaceLocal is the truthful answer rather
// than a guess.
func SpeechPlaceOf(p Provider) string {
	if d, ok := p.(TopologyDescriber); ok {
		if place := d.SpeechPlace(); place != "" {
			return place
		}
	}
	return PlaceLocal
}

// WorkerStreamingOf reports whether a provider's PCM arrives during synthesis
// (see TopologyDescriber.WorkerStreaming), and false when there is no worker.
func WorkerStreamingOf(p Provider) bool {
	d, ok := p.(TopologyDescriber)
	return ok && d.WorkerStreaming()
}

// FirstAudioGracer is optionally implemented by providers whose time-to-first
// audible frame can exceed the pipeline's default playback-stall budget.
// When present, the stall watchdog waits at least this long before aborting a
// turn as "playback stalled" — otherwise a healthy but slow synthesis
// (whole-utterance workers like Qwen3-TTS) is cut off mid-sentence and the
// conversation injects the recovery reply.
type FirstAudioGracer interface {
	FirstAudioGrace() time.Duration
}

// SpeechCharsPerSecond converts a segment's length into expected audio
// duration.
//
// Measured 11.9–18.2 chars/s over real replies on both engines. 10 sits
// below the observed minimum on purpose: the estimate must over-predict
// audio, because under-predicting shortens the budget on exactly the
// segments that need it most. A slow-speaking segment at the observed
// floor would otherwise have left only 12% margin.
//
// TypicalSegmentChars sizes the budget when the caller does not say how long
// the segment is. It is the measured median segment length, so the
// length-blind path lands on an ordinary sentence rather than a worst-case
// one.
//
// Both are exported because every provider that implements SegmentGracer
// needs them, and providers no longer all live in this package.
const (
	SpeechCharsPerSecond = 10.0
	TypicalSegmentChars  = 120
)

// SegmentGracer refines FirstAudioGracer with the length of the segment about
// to be synthesized.
//
// A fixed budget has to be sized for the longest reply the engine might be
// asked for, so every short one inherits that ceiling: a 17-character
// acknowledgement waited out the same grace as a paragraph. Synthesis cost
// scales with how much audio there is, so the budget should too.
type SegmentGracer interface {
	FirstAudioGraceForChars(chars int) time.Duration
}

// SoftCanceler is optionally implemented by warm-worker TTS providers.
// Pipeline barge-in and turn cancel should SoftCancel the in-flight request
// without killing the worker process (reload cost).
type SoftCanceler interface {
	SoftCancel(requestID string) error
}

// SynthesisRequest describes a single synthesis call. Empty Voice and zero
// Speed fall back to the provider's configured defaults.
type SynthesisRequest struct {
	Text                string
	Voice               string
	Mode                VoiceMode
	Language            string
	Instruction         string
	ReferenceAudio      string
	ReferenceTranscript string
	Speed               float64
	SampleRate          int
	Metadata            map[string]string
}

// SynthesisResult carries a synthesized stream and its metadata.
type SynthesisResult struct {
	Stream     *audio.PCMStream
	SampleRate int
	Provider   string
	Model      string
	Voice      string
	Mode       VoiceMode
	Metadata   map[string]string
}

// RequestProvider is implemented by providers that accept typed requests.
type RequestProvider interface {
	SynthesizeRequest(ctx context.Context, req SynthesisRequest) (SynthesisResult, error)
}

// Voice describes an available TTS voice.
type Voice struct {
	Name         string
	FriendlyName string
	Gender       string
	Locale       string
	// Kind says where a voice came from, for providers where that is not a
	// fixed property of the engine: audio8 reports "recording" or "synthetic"
	// for a registered voice. Empty for the built-in catalogs, whose voices
	// all came from the same place.
	//
	// It is its own field because provenance is not gender: putting it in
	// Gender made `voices --gender male` drop every registered voice and
	// `voices --json` emit "gender":"recording".
	Kind string
}

// VoiceNames returns the ordered list of all kokoro voice names.
func VoiceNames() []string {
	// Copy to avoid mutation.
	out := make([]string, len(voiceNames))
	copy(out, voiceNames)
	return out
}

// VoiceInfo returns voice metadata for a given name.
func VoiceInfo(name string) Voice {
	if len(name) < 3 {
		return Voice{Name: name}
	}
	locale := langMap[name[0]]
	gender := genderMap[name[1]]
	return Voice{
		Name:   name,
		Gender: gender,
		Locale: locale,
	}
}
