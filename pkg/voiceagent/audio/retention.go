package audio

import (
	"errors"
	"fmt"
	"time"
)

// ErrRetentionUnsupported means a source cannot atomically retain a previously
// read frame together with its unread capture tail.
var ErrRetentionUnsupported = errors.New("audio: source does not support next-listen retention")

type nextListenRetainer interface {
	RetainForNextListen(Frame, time.Duration) (Frame, error)
}

// CanRetainForNextListen reports capability without consuming or discarding PCM.
func (c *Capture) CanRetainForNextListen() bool { return c != nil && c.buf != nil }

// RetainForNextListen keeps the newest bounded pre-roll across [parked, unread
// ring]. parked must be this capture's most recent read, with its original
// metadata; there must be no intervening reader. Device callbacks may continue.
// Durations are clamped to [0,2s], matching the listen pre-roll limit.
//
// The returned frame contains only the retained parked suffix. Its PCM aliases
// parked (read-only); its span metadata is copied. The ring retains its suffix
// in place, without copying PCM. All parked and pending ring loss diagnostics,
// including intentional discard and physical discontinuity, are transferred
// once to the returned CaptureGap, even when Samples is empty. The caller must
// preserve that gap when it resumes reading. Errors leave parked and ring intact.
func (c *Capture) RetainForNextListen(parked Frame, keep time.Duration) (Frame, error) {
	if !c.CanRetainForNextListen() {
		return Frame{}, ErrRetentionUnsupported
	}
	keep = min(max(keep, 0), 2*time.Second)
	return c.buf.retainForNextListen(parked, int(keep*SampleRate/time.Second))
}

func (rb *RingBuffer) retainForNextListen(parked Frame, keep int) (Frame, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if parked.Final {
		return Frame{}, errors.New("audio retention: final frame is not live capture")
	}
	if len(parked.Samples) > 0 {
		if parked.SampleRate != SampleRate || parked.Channels != Channels || parked.SourceKind != SourceLive {
			return Frame{}, errors.New("audio retention: parked frame is not capture-format live PCM")
		}
		end, err := validateParkedSpans(parked)
		if err != nil {
			return Frame{}, err
		}
		ringStart := rb.written - uint64(rb.available())
		gap := rb.gap.OverflowSamples + rb.gap.DiscardedSamples
		if end > ringStart || ringStart-end != gap {
			return Frame{}, fmt.Errorf("audio retention: parked frame is not the most recent read (end %d, unread %d, known gap %d)", end, ringStart, gap)
		}
	}
	available := rb.available()
	discard := max(0, len(parked.Samples)+available-keep)
	parkedDrop := min(len(parked.Samples), discard)
	ringDrop := discard - parkedDrop
	result := parked
	result.Samples = parked.Samples[parkedDrop:]
	result.Duration = SamplesDuration(len(result.Samples))
	result.CaptureSpans = nil
	result.CaptureGap = mergeRetentionGap(parked.CaptureGap, rb.gap)
	result.CaptureGap.DiscardedSamples += uint64(discard)
	if discard > 0 || keep == 0 {
		result.CaptureGap.Reset = true
	}
	// A generation transition must remain visible even if its entire callback
	// lies in the discarded prefix, or an overflow removed its marker span.
	var generation uint64
	for _, spans := range [][]CaptureSpan{parked.CaptureSpans, rb.spans} {
		for _, span := range spans {
			if span.DeviceDiscontinuity || (generation != 0 && span.DeviceGeneration != 0 && generation != span.DeviceGeneration) {
				result.CaptureGap.DeviceDiscontinuity = true
			}
			if span.DeviceGeneration != 0 {
				generation = span.DeviceGeneration
			}
		}
	}
	if generation != 0 && rb.generation != 0 && generation != rb.generation {
		result.CaptureGap.DeviceDiscontinuity = true
	}
	if len(result.Samples) > 0 {
		start := parked.CaptureSpans[0].SampleOffset + uint64(parkedDrop)
		end := start + uint64(len(result.Samples))
		for _, span := range parked.CaptureSpans {
			if span.SampleOffset+uint64(span.SampleCount) > start {
				result.CaptureSpans = append(result.CaptureSpans, sliceCaptureSpan(span, start, end))
			}
		}
		result.StartedAt = result.CaptureSpans[0].EstimatedStart
	} else {
		result.StartedAt = time.Time{}
	}
	if ringDrop > 0 {
		rb.tail = (rb.tail + ringDrop) % rb.size
		rb.full = false
		rb.pruneSpansLocked()
	}
	rb.gap = CaptureGap{}
	return result, nil
}

func validateParkedSpans(frame Frame) (uint64, error) {
	if len(frame.CaptureSpans) == 0 {
		return 0, errors.New("audio retention: parked capture metadata missing")
	}
	end := frame.CaptureSpans[0].SampleOffset
	count := 0
	for _, span := range frame.CaptureSpans {
		if span.SampleCount <= 0 || span.SampleOffset != end || uint64(span.SampleCount) > ^uint64(0)-end {
			return 0, errors.New("audio retention: invalid or noncontiguous parked spans")
		}
		count += span.SampleCount
		end += uint64(span.SampleCount)
	}
	if count != len(frame.Samples) {
		return 0, errors.New("audio retention: parked spans do not cover PCM")
	}
	return end, nil
}

func mergeRetentionGap(a, b CaptureGap) CaptureGap {
	return CaptureGap{
		OverflowSamples:                 a.OverflowSamples + b.OverflowSamples,
		DiscardedSamples:                a.DiscardedSamples + b.DiscardedSamples,
		Reset:                           a.Reset || b.Reset,
		DeviceDiscontinuity:             a.DeviceDiscontinuity || b.DeviceDiscontinuity,
		SubscriptionDroppedSamples:      a.SubscriptionDroppedSamples + b.SubscriptionDroppedSamples,
		SubscriptionDroppedCallbacks:    a.SubscriptionDroppedCallbacks + b.SubscriptionDroppedCallbacks,
		SubscriptionDeviceDiscontinuity: a.SubscriptionDeviceDiscontinuity || b.SubscriptionDeviceDiscontinuity,
	}
}
