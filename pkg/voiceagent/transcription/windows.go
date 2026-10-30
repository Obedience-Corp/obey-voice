package transcription

// WindowSpanDetector sends every source sample to recognition. It does not
// decide that quiet speech or a transcript gap is silence. Windows stay bounded
// and nonoverlapping; a low-energy split near each limit reduces word clipping.
// These boundaries are batching decisions, never proposed edit boundaries.
type WindowSpanDetector struct {
	buffer     []float32
	ready      []Span
	start, fed int64
	limit      int
}

func NewWindowSpanDetector(sampleRate int) *WindowSpanDetector {
	return &WindowSpanDetector{limit: 20 * sampleRate}
}

func (d *WindowSpanDetector) Accept(samples []float32) {
	d.fed += int64(len(samples))
	d.buffer = append(d.buffer, samples...)
	for len(d.buffer) >= d.limit {
		// Choose an 80 ms valley within the last two seconds. Full coverage is
		// preserved even if there is no quiet valley; recognition remains an
		// estimate and does not qualify unattended cuts.
		window := d.limit / 250
		best, energy := d.limit, float64(1e300)
		for start := d.limit * 9 / 10; start+window <= d.limit; start += window / 2 {
			var sum float64
			for _, sample := range d.buffer[start : start+window] {
				sum += float64(sample) * float64(sample)
			}
			if sum < energy {
				best, energy = start+window/2, sum
			}
		}
		d.emit(best)
	}
}

func (d *WindowSpanDetector) emit(n int) {
	pcm := append([]float32(nil), d.buffer[:n]...)
	d.ready = append(d.ready, Span{StartSample: d.start, EndSample: d.start + int64(n), Samples: pcm})
	d.start += int64(n)
	d.buffer = append(d.buffer[:0], d.buffer[n:]...)
}
func (d *WindowSpanDetector) Next() (Span, bool) {
	if len(d.ready) == 0 {
		return Span{}, false
	}
	span := d.ready[0]
	d.ready[0] = Span{}
	d.ready = d.ready[1:]
	return span, true
}
func (d *WindowSpanDetector) Flush() {
	if len(d.buffer) > 0 {
		d.emit(len(d.buffer))
	}
}
func (d *WindowSpanDetector) Fed() int64 { return d.fed }
