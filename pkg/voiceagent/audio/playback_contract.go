package audio

import (
	"context"
	"time"
)

// PlaybackResult reports the terminal state of a queued playback segment.
type PlaybackResult struct {
	AttemptID    string
	CaptureEpoch uint64
	Err          error
	Interrupted  bool
	// AudioSeconds is the duration of the audio the synthesizer produced for
	// this segment, measured at the stream's own sample rate before any
	// resampling. It is the denominator of the realtime factor, so it reports
	// what was synthesized even when playback was interrupted before the
	// samples were heard.
	AudioSeconds float64
	// Underruns counts how many times this segment ran dry: the device asked
	// for audio, the segment had none buffered, and synthesis had not finished
	// producing it. Each one is an audible gap *inside* a sentence.
	//
	// A whole-utterance segment cannot report one — its audio is complete
	// before it becomes audible. Only an incremental stream can, which is
	// exactly why the count exists: it is the failure mode streaming
	// introduces, and dead-air accounting cannot see it because that only
	// measures the silence *between* segments.
	Underruns int
	// SynthesisEndedAt is when the last frame of this segment arrived from the
	// synthesizer, or the zero time when the player did not observe an end
	// (a barge-in that discarded the segment first).
	//
	// A whole-utterance segment becomes audible only after synthesis ends, so
	// the caller can time that from its own side. A streamed one is audible
	// long before, so the only honest numerator for its realtime factor is
	// this stamp.
	SynthesisEndedAt time.Time
}

// Playback exposes playback lifecycle signals for a single queued segment.
type Playback struct {
	started <-chan struct{}
	done    <-chan PlaybackResult
}

// NewPlayback constructs a playback handle from lifecycle channels.
func NewPlayback(started <-chan struct{}, done <-chan PlaybackResult) *Playback {
	return &Playback{started: started, done: done}
}

// Started closes when audible playback begins for the segment.
func (p *Playback) Started() <-chan struct{} {
	return p.started
}

// Done delivers the terminal playback result.
func (p *Playback) Done() <-chan PlaybackResult {
	return p.done
}

// Engine is the playback interface used by the pipeline.
// Engine plays synthesized PCM. PlayStream takes ownership of stream even when
// it returns an error: implementations must drain (or cancel) the stream so
// synth producers are not left blocked on a full frames channel.
type Engine interface {
	PlayStream(ctx context.Context, stream *PCMStream) (*Playback, error)
	Stop()
	IsPlaying() bool
	Close() error
}
