package stt

import (
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

// nativeCaptureBuffer follows the existing loop's one reader. Its offsets share
// the segmenter's Reset basis; only the latest persona-sized window is retained.
type nativeCaptureBuffer struct {
	frames     []nativeCaptureFrame
	fed        int
	base       int
	incomplete bool
}
type nativeCaptureFrame struct {
	start int
	frame audio.Frame
}

func (b *nativeCaptureBuffer) reset() { b.frames = nil; b.fed = 0; b.base = 0; b.incomplete = false }
func (b *nativeCaptureBuffer) append(frame audio.Frame) {
	if frame.CaptureGap != (audio.CaptureGap{}) {
		b.incomplete = true
	}
	if len(frame.Samples) == 0 {
		return
	}
	start := b.fed
	b.fed += len(frame.Samples)
	if len(frame.Samples) > utterance.MaxSamples {
		drop := len(frame.Samples) - utterance.MaxSamples
		frame.CaptureSpans = sliceCaptureSpans(frame.CaptureSpans, drop, len(frame.Samples))
		frame.Samples = frame.Samples[drop:]
		frame.StartedAt = advanceCaptureTime(frame.StartedAt, drop)
		start += drop
	}
	frame.Samples = append([]float32(nil), frame.Samples...)
	frame.CaptureSpans = append([]audio.CaptureSpan(nil), frame.CaptureSpans...)
	b.frames = append(b.frames, nativeCaptureFrame{start: start, frame: frame})
	b.base = max(0, b.fed-utterance.MaxSamples)
	for len(b.frames) > 0 && b.frames[0].start+len(b.frames[0].frame.Samples) <= b.base {
		b.frames = b.frames[1:]
	}
	// A single malicious oversized frame must not pin unbounded PCM.
	if len(b.frames) > 0 && b.frames[0].start < b.base {
		first := &b.frames[0]
		drop := b.base - first.start
		first.frame.Samples = append([]float32(nil), first.frame.Samples[drop:]...)
		first.frame.CaptureSpans = sliceCaptureSpans(first.frame.CaptureSpans, drop, drop+len(first.frame.Samples))
		first.frame.StartedAt = advanceCaptureTime(first.frame.StartedAt, drop)
		first.start = b.base
	}
}
func advanceCaptureTime(t time.Time, samples int) time.Time {
	if t.IsZero() {
		return t
	}
	return t.Add(audio.SamplesDuration(samples))
}
func sliceCaptureSpans(spans []audio.CaptureSpan, lo, hi int) []audio.CaptureSpan {
	var out []audio.CaptureSpan
	offset := 0
	for _, span := range spans {
		end := offset + span.SampleCount
		l, r := max(lo, offset), min(hi, end)
		if l < r {
			span.SampleOffset += uint64(l - offset)
			span.SampleCount = r - l
			span.EstimatedStart = advanceCaptureTime(span.EstimatedStart, l-offset)
			out = append(out, span)
		}
		offset = end
	}
	return out
}
func (b *nativeCaptureBuffer) slice(lo, hi int) ([]float32, []utterance.Span, time.Time, time.Time, error) {
	if b.incomplete || lo < b.base || hi > b.fed || lo < 0 || hi <= lo || hi-lo > utterance.MaxSamples {
		return nil, nil, time.Time{}, time.Time{}, utterance.ErrIncomplete
	}
	var samples []float32
	var spans []utterance.Span
	var started, ended time.Time
	for _, part := range b.frames {
		frame := part.frame
		left, right := max(lo, part.start), min(hi, part.start+len(frame.Samples))
		if left >= right {
			continue
		}
		if frame.SampleRate != utterance.SampleRate || frame.Channels != 1 || frame.CaptureGap != (audio.CaptureGap{}) {
			return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("capture format or gap: %w", utterance.ErrIncomplete)
		}
		l, r := left-part.start, right-part.start
		if len(samples) == 0 {
			started = advanceCaptureTime(frame.StartedAt, l)
		}
		ended = advanceCaptureTime(frame.StartedAt, r)
		samples = append(samples, frame.Samples[l:r]...)
		sliced := sliceCaptureSpans(frame.CaptureSpans, l, r)
		for _, span := range sliced {
			spans = append(spans, utterance.Span{SampleOffset: span.SampleOffset, SampleCount: span.SampleCount, DeviceGeneration: span.DeviceGeneration, CallbackAt: span.CallbackAt, EstimatedStart: span.EstimatedStart, DeviceDiscontinuity: span.DeviceDiscontinuity})
		}
	}
	if len(samples) != hi-lo {
		return nil, nil, time.Time{}, time.Time{}, utterance.ErrIncomplete
	}
	return samples, spans, started, ended, nil
}
