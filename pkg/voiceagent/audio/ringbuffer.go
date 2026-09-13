package audio

import "sync"

// RingBuffer is a thread-safe circular buffer for audio samples.
type RingBuffer struct {
	mu           sync.Mutex
	data         []float32
	size         int
	head         int // write position
	tail         int // read position
	full         bool
	totalWritten uint64
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
	rb.WriteWithPosition(samples)
}

// WriteWithPosition appends samples and returns the absolute position of the
// first sample. Positions are monotonic for the lifetime of the buffer,
// including Clear and KeepLast operations.
func (rb *RingBuffer) WriteWithPosition(samples []float32) uint64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	start := rb.totalWritten
	if rb.size == 0 {
		rb.totalWritten += uint64(len(samples))
		return start
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
		rb.totalWritten++
	}
	return start
}

// Read returns up to n samples from the buffer.
// Returns nil if fewer than n samples are available.
func (rb *RingBuffer) Read(n int) []float32 {
	out, _ := rb.ReadWithPosition(n)
	return out
}

// ReadWithPosition reads a complete chunk and returns its absolute sample
// position. A short read returns nil and does not advance the buffer.
func (rb *RingBuffer) ReadWithPosition(n int) ([]float32, uint64) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	avail := rb.available()
	if n <= 0 || avail < n {
		return nil, 0
	}

	out := make([]float32, n)
	start := rb.totalWritten - uint64(avail)
	for i := range n {
		out[i] = rb.data[rb.tail]
		rb.tail = (rb.tail + 1) % rb.size
	}
	rb.full = false
	return out, start
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

// KeepFrom discards unread samples older than offset. Already-read samples
// cannot be recovered. An offset beyond the current end clamps to the end.
func (rb *RingBuffer) KeepFrom(offset uint64) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	oldest := rb.totalWritten - uint64(rb.available())
	if offset <= oldest {
		return
	}
	if offset >= rb.totalWritten {
		rb.tail = rb.head
		rb.full = false
		return
	}
	drop := int(offset - oldest)
	rb.tail = (rb.tail + drop) % rb.size
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
