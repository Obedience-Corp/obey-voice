package audio

import (
	"context"
	"errors"
	"time"
)

// SourceKind identifies where an audio Frame originated so consumers can apply
// the right lifecycle policy: a live microphone streams until canceled, while a
// finite fixture or file ends with an explicit final frame.
type SourceKind string

const (
	SourceLive    SourceKind = "live"
	SourceFixture SourceKind = "fixture"
	SourceFile    SourceKind = "file"
)

// Frame is a typed unit of captured audio plus the lifecycle metadata needed to
// tell "no audio yet" apart from "end of input". A Frame with Final set marks
// the end of a finite source, after any Samples it still carries.
type Frame struct {
	Samples    []float32
	SampleRate int
	Channels   int
	// StartedAt is the source's start-time estimate, if available. Live capture
	// derives it from callback arrival minus sample duration, not an ADC clock.
	// It retains that estimate through buffering; zero means unknown.
	StartedAt  time.Time
	Duration   time.Duration
	Sequence   int64
	SourceKind SourceKind
	Final      bool
	// CaptureSpans preserves callback boundaries within this frame. Sources
	// without capture ordering metadata leave it nil.
	CaptureSpans []CaptureSpan
	CaptureGap   CaptureGap
}

// CaptureSpan describes a contiguous part of one capture callback. Offsets count
// samples in one Capture instance for its lifetime, including across resets and
// device reopen. They are not comparable between Capture instances or with a
// playback device's independent sample counter.
type CaptureSpan struct {
	SampleOffset     uint64
	SampleCount      int
	DeviceGeneration uint64 // device-open attempt; 0 means unknown
	// CallbackAt is observed at callback entry, before conversion/frontend work.
	// EstimatedStart subtracts callback duration and advances when a span is
	// sliced. Neither is a hardware timestamp or proof of acoustic alignment.
	// Both remain zero for PCM written without callback timing metadata.
	CallbackAt     time.Time
	EstimatedStart time.Time
	// DeviceDiscontinuity marks the first callback of a changed device
	// generation. The intervening physical loss has unknown sample count.
	DeviceDiscontinuity bool
}

// CaptureGap reports known loss on the frame delivery path. ReadFrame reports
// unread ring loss; SubscribeFrames reports its own subscription loss. These
// counters are independent: a ring reset does not discard subscription frames.
// Intentional retention/reset is separate from capacity overflow. It describes
// known buffered samples only, not an estimate of missing hardware callbacks.
type CaptureGap struct {
	OverflowSamples  uint64
	DiscardedSamples uint64
	Reset            bool
	// DeviceDiscontinuity preserves unknown physical loss when retention removes
	// the callback span that originally carried a device-generation transition.
	DeviceDiscontinuity bool
	// Subscription fields count only callbacks dropped by this subscriber's
	// full channel since its previous delivered frame. Unknown device downtime
	// is not assigned a sample count. A lost generation transition is latched
	// here without rewriting the delivered callback's original CaptureSpan.
	SubscriptionDroppedSamples      uint64
	SubscriptionDroppedCallbacks    uint64
	SubscriptionDeviceDiscontinuity bool
}

// Empty reports whether the frame carries no samples.
func (f Frame) Empty() bool { return len(f.Samples) == 0 }

var (
	// ErrNoFrameReady signals a live source has no audio buffered yet. It is
	// non-fatal: callers should poll again. Finite sources never return it; they
	// report end-of-input with a Final frame instead.
	ErrNoFrameReady = errors.New("audio: no frame ready")
	// ErrSourceClosed is returned by ReadFrame once the source has been closed.
	ErrSourceClosed = errors.New("audio: source closed")
)

// FrameSource is the typed audio input contract. ReadFrame returns the next
// Frame or an error, following these semantics:
//
//   - nil error with non-empty Samples: usable audio.
//   - nil error with Final == true: end of input, after any Samples included.
//   - context.Canceled / context.DeadlineExceeded: the caller canceled; return promptly.
//   - ErrNoFrameReady: a live source has no audio buffered yet; poll again.
//   - ErrSourceClosed: ReadFrame was called after Close.
//   - any other error: a source failure that names the failing input.
//
// Live sources (SourceLive) stream until canceled or closed and never set Final.
// Finite sources (SourceFixture, SourceFile) must signal end of input with a
// Final frame rather than letting silence imply finality.
type FrameSource interface {
	ReadFrame(ctx context.Context) (Frame, error)
	Close() error
}

// The live capture, the finite fixture and the finite file sources satisfy the
// contract.
var (
	_ FrameSource = (*Capture)(nil)
	_ FrameSource = (*FixtureSource)(nil)
	_ FrameSource = (*WAVFileSource)(nil)
)

// SamplesDuration returns the wall-time a chunk of n mono samples represents at
// the capture sample rate. It is derived from the sample count, so it stays
// deterministic and independent of the wall clock.
func SamplesDuration(n int) time.Duration {
	if SampleRate <= 0 || n <= 0 {
		return 0
	}
	return time.Duration(float64(n) / float64(SampleRate) * float64(time.Second))
}
