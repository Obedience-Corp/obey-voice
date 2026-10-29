package transcription

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// TimingUtterance is the only timing granularity this runner produces: one
// timed segment per detected speech span. Word timing needs a decoder that
// reports it, which no shipped provider does. The value is "utterance" and
// must match stt.TimingUtterance.
const TimingUtterance = "utterance"

// Stable failure codes. They are the subset of the wire's code list that the
// runner itself can produce; the command layer reuses these strings.
const (
	CodeInputUnreadable       = "input_unreadable"
	CodeUnsupportedCapability = "unsupported_capability"
	CodeDecodeFailed          = "decode_failed"
	CodeInternal              = "internal"
)

// FrameSourceWithPosition is the finite frame source the runner drives.
// Position and TotalSamples are what let the runner keep its own count of the
// timeline and check it against the source and the detector, so a silent drift
// between the three becomes an error instead of captions that slide.
type FrameSourceWithPosition interface {
	audio.FrameSource
	// Position is the number of samples handed over so far.
	Position() int64
	// TotalSamples is the length of the source in samples.
	TotalSamples() int64
}

// Request is one transcription job. It carries no paths: opening the input is
// the caller's job, so the runner is equally usable over a file, a fixture or a
// pipe the caller has already framed.
type Request struct {
	// JobID identifies the job in logs and in the caller's wire events.
	JobID string
	// Language is the caller's preference. Run does not pass it through:
	// Accepted.Languages is a snapshot of the decoder's resolved Info.Languages.
	Language string
	// Timing is the requested granularity. Empty means TimingUtterance.
	Timing string
}

// Sink receives runner events in order. Emit is called synchronously from the
// read loop, so a slow sink backpressures reading and no audio is dropped.
type Sink interface {
	Emit(ctx context.Context, ev Event) error
}

// Deps are the runner's injected collaborators. None of them needs cgo or a
// model file to be faked, so the loop is testable end to end.
type Deps struct {
	Source   FrameSourceWithPosition
	Detector SpanDetector
	Decoder  stt.SegmentDecoder
	Info     stt.DecoderInfo
	Sink     Sink
	// Logf receives diagnostics. Nil discards them: a library that writes to a
	// process stderr its caller does not control is badly behaved.
	Logf func(string, ...any)
	// MinSpeechSamples is the shortest span worth decoding. Zero uses
	// DefaultMinSpeechSamples.
	MinSpeechSamples int
	// SpeechFloor is the RMS below which a span is silence. Zero uses
	// DefaultSpeechFloor.
	SpeechFloor float64
}

// Event is one runner output. The set is closed: accepted, progress, segment
// and exactly one summary, in that order. Kind is the public name; isEvent is
// unexported so a type outside this package cannot satisfy Event.
type Event interface {
	// Kind names the event for logs and for a caller mapping events to a wire.
	Kind() string
	isEvent()
}

// Accepted reports the resolved decoder before any audio is read. It names what
// actually ran, never the caller's preference string.
type Accepted struct {
	Provider      string
	Mode          string
	ModelID       string
	ModelRevision string
	Languages     []string
	Timing        string
	DurationUS    int64
}

// Progress reports consumed audio against the known duration. It is advisory
// and throttled; it never reports a percentage for work that has not happened.
type Progress struct {
	ConsumedUS int64
	DurationUS int64
}

// Segment is one committed, timed utterance. There are no partials and no
// revisions: a segment is emitted once, already final.
type Segment struct {
	ID      string
	StartUS int64
	EndUS   int64
	Text    string
	Words   []stt.TimedWord
}

// Outcome is how a finished run ended.
type Outcome string

const (
	OutcomeSpeech   Outcome = "speech"
	OutcomeNoSpeech Outcome = "no_speech"
)

// Summary is the terminal event and the value Run returns. It is emitted once,
// as the last event, and returned with the same contents. A caller mapping
// events to a wire protocol writes its terminal line exactly once, from either
// the emitted event or the returned value.
type Summary struct {
	DurationUS   int64
	SegmentCount int
	Outcome      Outcome
}

func (Accepted) Kind() string { return "accepted" }
func (Progress) Kind() string { return "progress" }
func (Segment) Kind() string  { return "segment" }
func (Summary) Kind() string  { return "summary" }

func (Accepted) isEvent() {}
func (Progress) isEvent() {}
func (Segment) isEvent()  {}
func (Summary) isEvent()  {}

// Error is a runner failure with a stable code the caller can map to a wire
// failure and an exit status without parsing a message.
type Error struct {
	Code      string
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }
