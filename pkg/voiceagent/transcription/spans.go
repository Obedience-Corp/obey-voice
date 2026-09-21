package transcription

import (
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// Span is one stretch of speech, located on the source timeline. StartSample is
// the index of Samples[0] in the file, counted from the first sample of the
// file, and EndSample is StartSample + len(Samples).
type Span struct {
	StartSample int64
	EndSample   int64
	Samples     []float32
}

// SpanDetector finds speech spans in a stream of samples. Accept takes audio in
// the order it was read; Next drains whatever spans that made available; Flush
// finalizes trailing speech at end of input, after which Next drains the rest.
// Fed reports how many samples have been accepted, which the caller compares
// with its own read position so drift is caught instead of silently shifting
// every later timestamp.
type SpanDetector interface {
	Accept(samples []float32)
	Next() (Span, bool)
	Flush()
	Fed() int64
}

// vadDetector is the part of *audio.VAD the span detector drives. Keeping it an
// interface lets the offset and pre-roll logic be tested without the cgo Silero
// detector or its model files.
type vadDetector interface {
	AcceptWaveform(samples []float32)
	IsEmpty() bool
	FrontSegment() (samples []float32, start int)
	Pop()
	Flush()
}

var _ vadDetector = (*audio.VAD)(nil)

// PreRollRingSamples sizes the retained audio ring for a file run. A span is
// reported only once the detector has seen maxSpeech of speech plus minSilence
// of silence after it, so the pre-roll for that span is that far back by the
// time it can be spliced. One second of slack covers the chunk granularity the
// source reads in.
func PreRollRingSamples(maxSpeech, minSilence, preRoll time.Duration, rate int) int {
	if rate <= 0 {
		return 0
	}
	total := maxSpeech + minSilence + preRoll + time.Second
	if total <= 0 {
		return 0
	}
	return int(total.Seconds() * float64(rate))
}

// NewFileSpanDetector builds the span detector for one file run. opts are the
// options the file detector was built with, resolved so a zero field reads as
// the default the detector actually applies, and preRoll is vad_pre_roll_ms
// from config. The retained audio ring is sized from those resolved values.
func NewFileSpanDetector(vad vadDetector, opts audio.FileVADOptions, preRoll time.Duration) SpanDetector {
	if preRoll < 0 {
		preRoll = 0
	}
	resolved := opts.Resolved()
	preRollSamples := int(preRoll.Seconds() * float64(audio.SampleRate))
	ring := PreRollRingSamples(resolved.MaxSpeech, resolved.MinSilence, preRoll, audio.SampleRate)
	return NewVADSpanDetector(vad, preRollSamples, ring)
}

// vadSpanDetector adapts a Silero detector to SpanDetector. It widens the
// native int32 offset to int64 at the boundary, prepends the onset audio the
// detector trims, and never lets a span start before the previous span ended,
// so a forced split inside continuous speech produces adjacent spans instead of
// overlapping ones.
type vadSpanDetector struct {
	vad     vadDetector
	preRoll int
	ring    preRollRing
	lastEnd int64
}

// NewVADSpanDetector builds a detector over vad. preRollSamples is how much
// audio before a span's onset to prepend; ringSamples is how much recent audio
// to retain for that, which PreRollRingSamples sizes. The ring is the only
// audio the detector holds, so its memory does not depend on the file length.
func NewVADSpanDetector(vad vadDetector, preRollSamples, ringSamples int) SpanDetector {
	if preRollSamples < 0 {
		preRollSamples = 0
	}
	if ringSamples < preRollSamples {
		ringSamples = preRollSamples
	}
	return &vadSpanDetector{vad: vad, preRoll: preRollSamples, ring: newPreRollRing(ringSamples)}
}

// Accept feeds samples to the detector and retains them for pre-roll. An empty
// slice is dropped: the sherpa binding indexes samples[0] unconditionally.
func (d *vadSpanDetector) Accept(samples []float32) {
	if len(samples) == 0 {
		return
	}
	d.vad.AcceptWaveform(samples)
	d.ring.push(samples)
}

// Next pops the next finalized span, or reports that none is ready.
func (d *vadSpanDetector) Next() (Span, bool) {
	for !d.vad.IsEmpty() {
		seg, start := d.vad.FrontSegment()
		d.vad.Pop()
		if len(seg) == 0 {
			continue
		}
		startSample := int64(start)
		pre := d.preRollFor(startSample)
		samples := make([]float32, 0, len(pre)+len(seg))
		samples = append(samples, pre...)
		samples = append(samples, seg...)

		span := Span{
			StartSample: startSample - int64(len(pre)),
			Samples:     samples,
		}
		span.EndSample = span.StartSample + int64(len(samples))
		d.lastEnd = span.EndSample
		return span, true
	}
	return Span{}, false
}

// preRollFor returns the retained audio immediately before startSample, bounded
// by the ring's contents and by the end of the previous span so a forced split
// never decodes or reports the same audio twice.
func (d *vadSpanDetector) preRollFor(startSample int64) []float32 {
	if d.preRoll <= 0 {
		return nil
	}
	base := d.ring.base()
	if startSample < base {
		return nil
	}
	lo := max(startSample-int64(d.preRoll), base, d.lastEnd)
	hi := min(startSample, base+int64(d.ring.size))
	if hi <= lo {
		return nil
	}
	return d.ring.slice(lo, hi)
}

// Flush finalizes trailing speech. Spans become available through Next.
func (d *vadSpanDetector) Flush() { d.vad.Flush() }

// Fed reports how many samples have been accepted.
func (d *vadSpanDetector) Fed() int64 { return d.ring.fed }

// preRollRing retains the most recent samples in a fixed buffer. It is written
// in place, so retaining 20 s of audio costs one allocation for the whole run.
type preRollRing struct {
	buf  []float32
	size int   // samples retained, at most len(buf)
	next int   // write position
	fed  int64 // samples ever pushed
}

func newPreRollRing(capacity int) preRollRing {
	if capacity <= 0 {
		return preRollRing{}
	}
	return preRollRing{buf: make([]float32, capacity)}
}

func (r *preRollRing) push(samples []float32) {
	r.fed += int64(len(samples))
	if len(r.buf) == 0 || len(samples) == 0 {
		return
	}
	if len(samples) >= len(r.buf) {
		copy(r.buf, samples[len(samples)-len(r.buf):])
		r.next = 0
		r.size = len(r.buf)
		return
	}
	n := copy(r.buf[r.next:], samples)
	if n < len(samples) {
		copy(r.buf, samples[n:])
	}
	r.next = (r.next + len(samples)) % len(r.buf)
	r.size = min(r.size+len(samples), len(r.buf))
}

// base is the absolute index of the oldest retained sample.
func (r *preRollRing) base() int64 { return r.fed - int64(r.size) }

// slice copies the retained samples in the absolute range [lo, hi). The caller
// must have checked the range against base and size.
func (r *preRollRing) slice(lo, hi int64) []float32 {
	if hi <= lo || len(r.buf) == 0 {
		return nil
	}
	oldest := (r.next - r.size + len(r.buf)) % len(r.buf)
	idx := (oldest + int(lo-r.base())) % len(r.buf)
	out := make([]float32, 0, hi-lo)
	for remaining := int(hi - lo); remaining > 0; {
		n := min(remaining, len(r.buf)-idx)
		out = append(out, r.buf[idx:idx+n]...)
		idx = (idx + n) % len(r.buf)
		remaining -= n
	}
	return out
}
