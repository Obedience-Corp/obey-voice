package audio

import "sync"

// RingBuffer is a thread-safe circular buffer for audio samples.
type RingBuffer struct {
	mu   sync.Mutex
	data []float32
	size int
	head int // write position
	tail int // read position
	full bool
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
	rb.mu.Lock()
	defer rb.mu.Unlock()

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
}

// Read returns up to n samples from the buffer.
// Returns nil if fewer than n samples are available.
func (rb *RingBuffer) Read(n int) []float32 {
	rb.mu.Lock()
	defer rb.mu.Unlock()

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
	return out
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
	rb.head = 0
	rb.tail = 0
	rb.full = false
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
		rb.head = 0
		rb.tail = 0
		rb.full = false
		return
	}
	available := rb.available()
	if available <= n {
		return
	}

	drop := available - n
	rb.tail = (rb.tail + drop) % rb.size
	rb.full = false
}
