package transcription

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

const (
	// DefaultMinSpeechSamples is the shortest span worth decoding, 0.3 s at
	// 16 kHz. It matches minSpeechSamples in the live offline loop, which is
	// unexported.
	DefaultMinSpeechSamples = 4800

	// DefaultSpeechFloor is the RMS below which a span is room tone rather than
	// a talker. It matches minSpeechRMS. A file source applies no gain, so the
	// pre-gain number is used directly.
	DefaultSpeechFloor = 0.002

	// audibleWindow is the 100 ms stretch judged for a talker, matching
	// audibleWindow in the live loop.
	audibleWindow = 1600

	// progressInterval is how much consumed audio separates two progress
	// events. Progress is advisory, so it is throttled by audio position rather
	// than by the wall clock.
	progressInterval = 5 * audio.SampleRate
)

// Run transcribes a finite source into timed segments. It emits accepted, then
// progress and segment events as audio is consumed, then exactly one summary,
// and returns that same summary.
//
// Every blocking point checks the context, and Sink.Emit is called on this
// goroutine, so a slow sink stops the reader instead of dropping audio.
func Run(ctx context.Context, req Request, deps Deps) (Summary, error) {
	r, err := newRunner(req, deps)
	if err != nil {
		return Summary{}, err
	}
	return r.run(ctx)
}

// runner holds one run's counters. A run is single-goroutine, so nothing here
// is guarded.
type runner struct {
	req  Request
	deps Deps

	total      int64 // samples the source says it holds
	durationUS int64
	consumed   int64 // samples this runner read, counted independently
	progressAt int64 // consumed position of the last progress event
	spanEnd    int64 // end sample of the last span accepted
	segments   int
}

func newRunner(req Request, deps Deps) (*runner, error) {
	switch {
	case deps.Source == nil:
		return nil, &Error{Code: CodeInternal, Err: errors.New("transcription: run without a source")}
	case deps.Detector == nil:
		return nil, &Error{Code: CodeInternal, Err: errors.New("transcription: run without a span detector")}
	case deps.Decoder == nil:
		return nil, &Error{Code: CodeInternal, Err: errors.New("transcription: run without a decoder")}
	case deps.Sink == nil:
		return nil, &Error{Code: CodeInternal, Err: errors.New("transcription: run without a sink")}
	}
	if req.Timing == "" {
		req.Timing = TimingUtterance
	}
	if req.Timing != TimingUtterance {
		return nil, &Error{Code: CodeUnsupportedCapability, Err: fmt.Errorf("transcription: timing %q, only %q is produced", req.Timing, TimingUtterance)}
	}
	if deps.MinSpeechSamples <= 0 {
		deps.MinSpeechSamples = DefaultMinSpeechSamples
	}
	if deps.SpeechFloor <= 0 {
		deps.SpeechFloor = DefaultSpeechFloor
	}
	total := deps.Source.TotalSamples()
	return &runner{
		req:   req,
		deps:  deps,
		total: total,
		// File duration rounds up so a last cue whose EndUS comes from
		// SpanMicros cannot sit past Accepted/Summary.DurationUS.
		// Progress.ConsumedUS stays on SamplesToMicros: it is a position.
		durationUS: DurationMicros(total, audio.SampleRate),
	}, nil
}

func (r *runner) run(ctx context.Context) (Summary, error) {
	accepted := Accepted{
		Provider:      r.deps.Info.Provider,
		Mode:          r.deps.Info.Mode,
		ModelID:       r.deps.Info.ModelID,
		ModelRevision: r.deps.Info.ModelRevision,
		Languages:     slices.Clone(r.deps.Info.Languages),
		Timing:        r.req.Timing,
		DurationUS:    r.durationUS,
	}
	if err := r.emit(ctx, accepted); err != nil {
		return Summary{}, err
	}
	if err := r.readAll(ctx); err != nil {
		return Summary{}, err
	}
	if err := r.finish(ctx); err != nil {
		return Summary{}, err
	}
	summary := Summary{DurationUS: r.durationUS, SegmentCount: r.segments, Outcome: OutcomeNoSpeech}
	if r.segments > 0 {
		summary.Outcome = OutcomeSpeech
	}
	if err := r.emit(ctx, summary); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

// readAll consumes the source until its final frame, feeding the detector and
// draining finished spans as it goes.
func (r *runner) readAll(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame, err := r.deps.Source.ReadFrame(ctx)
		switch {
		case err == nil:
		case errors.Is(err, audio.ErrSourceClosed):
			// The source is already closed, so there is no more audio. The live
			// loops treat this as end of input too (readLoopFrame).
			return nil
		case errors.Is(err, audio.ErrNoFrameReady):
			return &Error{Code: CodeInternal, Err: errors.New("transcription: finite source reported no frame ready; it must end with a final frame")}
		default:
			if cancelled := cancellation(ctx, err); cancelled != nil {
				return cancelled
			}
			return &Error{Code: CodeInputUnreadable, Err: fmt.Errorf("transcription: read audio: %w", err)}
		}
		if n := len(frame.Samples); n > 0 {
			r.consumed += int64(n)
			r.deps.Detector.Accept(frame.Samples)
		}
		if err := r.drain(ctx); err != nil {
			return err
		}
		if frame.Final {
			return nil
		}
		if err := r.progress(ctx); err != nil {
			return err
		}
	}
}

// finish flushes trailing speech and checks that the runner, the source and the
// detector agree on how much audio passed through.
func (r *runner) finish(ctx context.Context) error {
	r.deps.Detector.Flush()
	if err := r.drain(ctx); err != nil {
		return err
	}
	fed, position := r.deps.Detector.Fed(), r.deps.Source.Position()
	if fed != r.consumed || position != r.consumed {
		return &Error{Code: CodeInternal, Err: fmt.Errorf(
			"transcription: timeline drift: read %d samples, source reports %d, detector was fed %d",
			r.consumed, position, fed)}
	}
	r.logf("transcription: job %s read %d samples and emitted %d segments", r.req.JobID, r.consumed, r.segments)
	return nil
}

// drain empties the detector's finished spans.
func (r *runner) drain(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		span, ok := r.deps.Detector.Next()
		if !ok {
			return nil
		}
		if err := r.handleSpan(ctx, span); err != nil {
			return err
		}
	}
}

// handleSpan gates one span, decodes it and emits its segment. A span that is
// too short, too quiet or decodes to nothing is dropped without an event: the
// audio was consumed, there is just nothing to say about it.
func (r *runner) handleSpan(ctx context.Context, span Span) error {
	if span.StartSample < r.spanEnd {
		return &Error{Code: CodeInternal, Err: fmt.Errorf(
			"transcription: span starts at sample %d, inside the previous span which ended at %d",
			span.StartSample, r.spanEnd)}
	}
	r.spanEnd = span.EndSample

	switch {
	case len(span.Samples) < r.deps.MinSpeechSamples:
		r.logf("transcription: span at sample %d is %d samples, under the %d-sample minimum",
			span.StartSample, len(span.Samples), r.deps.MinSpeechSamples)
		return nil
	case !audibleAbove(span.Samples, r.deps.SpeechFloor):
		r.logf("transcription: span at sample %d is below the %.4f speech floor", span.StartSample, r.deps.SpeechFloor)
		return nil
	}

	text, err := r.deps.Decoder.DecodeSegment(ctx, span.Samples)
	if err != nil {
		if cancelled := cancellation(ctx, err); cancelled != nil {
			return cancelled
		}
		return &Error{Code: CodeDecodeFailed, Err: fmt.Errorf("transcription: decode span at sample %d: %w", span.StartSample, err)}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		r.logf("transcription: span at sample %d decoded to no text", span.StartSample)
		return nil
	}

	startUS, endUS, err := SpanMicros(span, audio.SampleRate, r.total)
	if err != nil {
		return &Error{Code: CodeInternal, Err: fmt.Errorf("transcription: time span at sample %d: %w", span.StartSample, err)}
	}
	r.segments++
	return r.emit(ctx, Segment{ID: fmt.Sprintf("s%d", r.segments), StartUS: startUS, EndUS: endUS, Text: text})
}

// progress emits a progress event once per progressInterval of consumed audio.
func (r *runner) progress(ctx context.Context) error {
	if r.consumed-r.progressAt < progressInterval {
		return nil
	}
	r.progressAt = r.consumed
	return r.emit(ctx, Progress{
		ConsumedUS: SamplesToMicros(r.consumed, audio.SampleRate),
		DurationUS: r.durationUS,
	})
}

// emit hands one event to the sink. The sink is called on this goroutine, so a
// sink that blocks stops the reader: backpressure, not dropped audio.
func (r *runner) emit(ctx context.Context, ev Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.deps.Sink.Emit(ctx, ev); err != nil {
		if cancelled := cancellation(ctx, err); cancelled != nil {
			return cancelled
		}
		return &Error{Code: CodeInternal, Err: fmt.Errorf("transcription: emit %s event: %w", ev.Kind(), err)}
	}
	return nil
}

func (r *runner) logf(format string, args ...any) {
	if r.deps.Logf != nil {
		r.deps.Logf(format, args...)
	}
}

// cancellation reports the cancellation that ended an operation, so a run the
// caller stopped returns context.Canceled rather than a decode or sink failure.
func cancellation(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// audibleAbove reports whether any 100 ms window of a span is loud enough to be
// a talker. It is the finite-buffer equivalent of audibleSpeechAbove, which is
// unexported and streams; a span is already in memory, so its windows are
// simply walked.
func audibleAbove(samples []float32, floor float64) bool {
	for start := 0; start < len(samples); start += audibleWindow {
		end := min(start+audibleWindow, len(samples))
		if rms(samples[start:end]) >= floor {
			return true
		}
	}
	return false
}

func rms(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		v := float64(s)
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(samples)))
}
