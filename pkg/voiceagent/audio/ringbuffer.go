package audio

import (
	"sync"
	"time"
)

// RingBuffer is a thread-safe circular buffer for audio samples.
type RingBuffer struct {
	mu   sync.Mutex
	data []float32
	size int
	head int // write position
	tail int // read position
	full bool
	// The sample timeline never rewinds. Metadata is retained only for unread
	// PCM; each span describes at least one buffered sample.
	written    uint64
	generation uint64
	spans      []CaptureSpan
	gap        CaptureGap
}

// NewRingBuffer creates a ring buffer with the given capacity in samples.
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		data: make([]float32, size),
		size: size,
	}
}

// Write adds samples to the buffer. Overwrites oldest data if full.
func (rb *RingBuffer) Write(samples []float32) {
	rb.writeCapture(samples, time.Time{}, 0)
}

// writeCapture is the callback ingestion seam. Legacy Write retains exact
// sample positions but leaves callback timing and device identity unknown.
// The returned span describes the entire callback, even if ring capacity prunes
// its retained copy. Returning under the lock keeps subscription offsets exact.
func (rb *RingBuffer) writeCapture(samples []float32, callbackAt time.Time, generation uint64) CaptureSpan {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if len(samples) == 0 {
		return CaptureSpan{}
	}
	span := CaptureSpan{SampleOffset: rb.written, SampleCount: len(samples), DeviceGeneration: generation, CallbackAt: callbackAt}
	if !callbackAt.IsZero() {
		span.EstimatedStart = callbackAt.Add(-SamplesDuration(len(samples)))
	}
	if generation != 0 {
		span.DeviceDiscontinuity = rb.generation != 0 && rb.generation != generation
		rb.generation = generation
	}
	rb.written += uint64(len(samples))
	overflow := max(0, rb.available()+len(samples)-rb.size)
	rb.gap.OverflowSamples += uint64(overflow)
	if rb.size == 0 {
		return span
	}

	for _, s := range samples {
		rb.data[rb.head] = s
		rb.head = (rb.head + 1) % rb.size

		if rb.full {
			rb.tail = (rb.tail + 1) % rb.size
		}
		if rb.head == rb.tail {
			rb.full = true
		}
	}
	rb.spans = append(rb.spans, span)
	rb.pruneSpansLocked()
	return span
}

// Read returns up to n samples from the buffer.
// Returns nil if fewer than n samples are available.
func (rb *RingBuffer) Read(n int) []float32 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.readLocked(n)
}

// readCapture atomically reads PCM and its retained callback spans. A short
// read consumes neither samples nor pending loss diagnostics.
func (rb *RingBuffer) readCapture(n int) ([]float32, []CaptureSpan, CaptureGap) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if n <= 0 || rb.available() < n {
		return nil, nil, CaptureGap{}
	}
	start := rb.written - uint64(rb.available())
	end := start + uint64(n)
	var spans []CaptureSpan
	for _, span := range rb.spans {
		if span.SampleOffset >= end {
			break
		}
		if span.SampleOffset+uint64(span.SampleCount) > start {
			spans = append(spans, sliceCaptureSpan(span, start, end))
		}
	}
	gap := rb.gap
	rb.gap = CaptureGap{}
	return rb.readLocked(n), spans, gap
}

func (rb *RingBuffer) readLocked(n int) []float32 {

	avail := rb.available()
	if avail < n {
		return nil
	}

	out := make([]float32, n)
	for i := range n {
		out[i] = rb.data[rb.tail]
		rb.tail = (rb.tail + 1) % rb.size
	}
	rb.full = false
	rb.pruneSpansLocked()
	return out
}

func sliceCaptureSpan(span CaptureSpan, start, end uint64) CaptureSpan {
	lo := max(start, span.SampleOffset)
	hi := min(end, span.SampleOffset+uint64(span.SampleCount))
	if !span.EstimatedStart.IsZero() {
		span.EstimatedStart = span.EstimatedStart.Add(SamplesDuration(int(lo - span.SampleOffset)))
	}
	span.SampleOffset = lo
	span.SampleCount = int(hi - lo)
	return span
}

func (rb *RingBuffer) pruneSpansLocked() {
	if rb.available() == 0 {
		rb.spans = nil
		return
	}
	start := rb.written - uint64(rb.available())
	for len(rb.spans) > 0 && rb.spans[0].SampleOffset+uint64(rb.spans[0].SampleCount) <= start {
		rb.spans[0] = CaptureSpan{}
		rb.spans = rb.spans[1:]
	}
	if len(rb.spans) > 0 {
		rb.spans[0] = sliceCaptureSpan(rb.spans[0], start, rb.written)
	}
}

// Available returns the number of samples ready to read.
func (rb *RingBuffer) Available() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.available()
}

func (rb *RingBuffer) available() int {
	if rb.full {
		return rb.size
	}
	if rb.head >= rb.tail {
		return rb.head - rb.tail
	}
	return rb.size - rb.tail + rb.head
}

// Clear resets the buffer.
func (rb *RingBuffer) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.gap.DiscardedSamples += uint64(rb.available())
	rb.gap.Reset = true
	rb.head = 0
	rb.tail = 0
	rb.full = false
	rb.spans = nil
}

// KeepLast discards all but the newest n samples currently buffered.
//
// The operation is atomic with respect to Write and Read. It is intentionally
// bounded to the unread portion of the ring: samples already handed to a
// consumer cannot be recovered or replayed as a new utterance.
func (rb *RingBuffer) KeepLast(n int) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if n <= 0 {
		rb.gap.DiscardedSamples += uint64(rb.available())
		rb.gap.Reset = true
		rb.head = 0
		rb.tail = 0
		rb.full = false
		rb.spans = nil
		return
	}
	available := rb.available()
	if available <= n {
		return
	}

	drop := available - n
	rb.gap.DiscardedSamples += uint64(drop)
	rb.gap.Reset = true
	rb.tail = (rb.tail + drop) % rb.size
	rb.full = false
	rb.pruneSpansLocked()
}
