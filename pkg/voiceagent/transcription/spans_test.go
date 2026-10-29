package transcription

import (
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// fakeVAD queues the segments a test wants the detector to see. Offsets are
// absolute sample indices, which is what the native detector reports for a
// stream it was never cleared during.
type fakeVAD struct {
	queued  []fakeSegment
	fed     int
	accepts int
	flushed bool
	onFlush []fakeSegment
}

type fakeSegment struct {
	start   int
	samples []float32
}

func (f *fakeVAD) AcceptWaveform(samples []float32) {
	f.accepts++
	if len(samples) == 0 {
		panic("AcceptWaveform called with no samples")
	}
	f.fed += len(samples)
}

func (f *fakeVAD) IsEmpty() bool { return len(f.queued) == 0 }

func (f *fakeVAD) FrontSegment() ([]float32, int) {
	return f.queued[0].samples, f.queued[0].start
}

func (f *fakeVAD) Pop() { f.queued = f.queued[1:] }

func (f *fakeVAD) Flush() {
	f.flushed = true
	f.queued = append(f.queued, f.onFlush...)
	f.onFlush = nil
}

// marked returns n samples whose value encodes their absolute position, so a
// test can prove which audio was spliced in front of a segment.
func marked(start, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(start + i)
	}
	return out
}

// feed pushes the file's samples through the detector in chunk-sized pieces,
// the way the runner feeds frames.
func feed(d SpanDetector, file []float32, chunk int) {
	for i := 0; i < len(file); i += chunk {
		d.Accept(file[i:min(i+chunk, len(file))])
	}
}

func TestSpanDetectorDropsEmptyAccept(t *testing.T) {
	vad := &fakeVAD{}
	d := NewVADSpanDetector(vad, 800, 16000)
	d.Accept(nil)
	d.Accept([]float32{})
	if vad.accepts != 0 {
		t.Fatalf("AcceptWaveform called %d time(s) for empty input", vad.accepts)
	}
	if d.Fed() != 0 {
		t.Fatalf("Fed = %d, want 0: empty Accept must not count as fed", d.Fed())
	}
}

func TestSpanDetectorPrependsPreRoll(t *testing.T) {
	vad := &fakeVAD{}
	d := NewVADSpanDetector(vad, 800, 16000)
	file := marked(0, 8000)
	feed(d, file, 1600)
	vad.queued = []fakeSegment{{start: 3200, samples: marked(3200, 1600)}}

	span, ok := d.Next()
	if !ok {
		t.Fatal("Next reported no span")
	}
	if span.StartSample != 2400 {
		t.Fatalf("StartSample = %d, want 2400 (3200 minus 800 of pre-roll)", span.StartSample)
	}
	if span.EndSample != 4800 {
		t.Fatalf("EndSample = %d, want 4800", span.EndSample)
	}
	if len(span.Samples) != 2400 {
		t.Fatalf("len(Samples) = %d, want 2400", len(span.Samples))
	}
	for i, got := range span.Samples {
		if want := float32(2400 + i); got != want {
			t.Fatalf("Samples[%d] = %v, want %v: the splice is misaligned", i, got, want)
		}
	}
	if _, ok := d.Next(); ok {
		t.Fatal("Next returned a second span")
	}
}

func TestSpanDetectorClipsPreRollAtFileStart(t *testing.T) {
	vad := &fakeVAD{}
	d := NewVADSpanDetector(vad, 800, 16000)
	feed(d, marked(0, 4000), 1600)
	vad.queued = []fakeSegment{{start: 300, samples: marked(300, 1000)}}

	span, ok := d.Next()
	if !ok {
		t.Fatal("Next reported no span")
	}
	if span.StartSample != 0 {
		t.Fatalf("StartSample = %d, want 0: pre-roll cannot reach before the file", span.StartSample)
	}
	if span.EndSample != 1300 {
		t.Fatalf("EndSample = %d, want 1300", span.EndSample)
	}
	if len(span.Samples) != 1300 {
		t.Fatalf("len(Samples) = %d, want 1300 (300 of pre-roll plus the segment)", len(span.Samples))
	}
	for i, got := range span.Samples {
		if want := float32(i); got != want {
			t.Fatalf("Samples[%d] = %v, want %v: file-start clip kept the length but the wrong audio", i, got, want)
		}
	}
}

func TestSpanDetectorClipsPreRollToTheRing(t *testing.T) {
	vad := &fakeVAD{}
	// The ring holds the last 1000 samples of the 8000 fed, so a segment that
	// starts at 7500 can only be given the 500 that precede it.
	d := NewVADSpanDetector(vad, 800, 1000)
	feed(d, marked(0, 8000), 1600)
	vad.queued = []fakeSegment{{start: 7500, samples: marked(7500, 500)}}

	span, ok := d.Next()
	if !ok {
		t.Fatal("Next reported no span")
	}
	if span.StartSample != 7000 {
		t.Fatalf("StartSample = %d, want 7000: only the ring's oldest retained sample is available", span.StartSample)
	}
	if span.Samples[0] != 7000 {
		t.Fatalf("Samples[0] = %v, want 7000", span.Samples[0])
	}
}

// A forced split inside continuous speech hands back two segments that meet at
// a sample boundary. Pre-roll must not reach back into the first span, or the
// same audio would be decoded twice and the cues would overlap.
func TestSpanDetectorKeepsForcedSplitsAdjacent(t *testing.T) {
	vad := &fakeVAD{}
	d := NewVADSpanDetector(vad, 4800, 16000*30)
	feed(d, marked(0, 16000*25), 1600)
	vad.queued = []fakeSegment{
		{start: 16000, samples: marked(16000, 16000*10)},
		{start: 16000 * 11, samples: marked(16000*11, 16000*10)},
	}

	first, ok := d.Next()
	if !ok {
		t.Fatal("no first span")
	}
	second, ok := d.Next()
	if !ok {
		t.Fatal("no second span")
	}
	if first.StartSample != 16000-4800 {
		t.Fatalf("first StartSample = %d, want %d", first.StartSample, 16000-4800)
	}
	if second.StartSample != first.EndSample {
		t.Fatalf("second span starts at %d but the first ended at %d: spans must be adjacent", second.StartSample, first.EndSample)
	}
	if int64(len(second.Samples)) != second.EndSample-second.StartSample {
		t.Fatalf("second span carries %d samples for %d sample times", len(second.Samples), second.EndSample-second.StartSample)
	}
}

// TestSpanDetectorSpliceIdentityAfterRingWrap feeds chunks smaller than the
// ring so each write can wrap in two copies, then checks the spliced samples
// by value. A capacity that is a multiple of the chunk (1600 into 16000) never
// takes that split: the write lands exactly on the buffer boundary.
func TestSpanDetectorSpliceIdentityAfterRingWrap(t *testing.T) {
	const (
		preRoll = 800
		ringCap = 5000
		chunk   = 1600
		fed     = 8000
		onset   = 5600
		segLen  = 500
	)
	if ringCap%chunk == 0 {
		t.Fatal("ring capacity is a multiple of the chunk; every push would skip the split copy")
	}

	vad := &fakeVAD{}
	d := NewVADSpanDetector(vad, preRoll, ringCap)
	feed(d, marked(0, fed), chunk)
	vad.queued = []fakeSegment{{start: onset, samples: marked(onset, segLen)}}

	ring := d.(*vadSpanDetector).ring
	if ring.next == 0 {
		t.Fatal("ring write landed on a buffer boundary; the split copy did not run")
	}

	span, ok := d.Next()
	if !ok {
		t.Fatal("Next reported no span")
	}
	wantStart := int64(onset - preRoll)
	if span.StartSample != wantStart {
		t.Fatalf("StartSample = %d, want %d", span.StartSample, wantStart)
	}
	if span.EndSample != int64(onset+segLen) {
		t.Fatalf("EndSample = %d, want %d", span.EndSample, onset+segLen)
	}
	if len(span.Samples) != preRoll+segLen {
		t.Fatalf("len(Samples) = %d, want %d", len(span.Samples), preRoll+segLen)
	}
	for i, got := range span.Samples {
		if want := float32(int(wantStart) + i); got != want {
			t.Fatalf("Samples[%d] = %v, want %v: wrap splice is misaligned", i, got, want)
		}
	}
}

func TestSpanDetectorRingStaysBounded(t *testing.T) {
	vad := &fakeVAD{}
	d := NewVADSpanDetector(vad, 4800, 16000)
	feed(d, marked(0, 16000*60), 1600)

	ring := d.(*vadSpanDetector).ring
	if len(ring.buf) != 16000 {
		t.Fatalf("ring buffer is %d samples, want the 16000 it was built with", len(ring.buf))
	}
	if ring.size != 16000 {
		t.Fatalf("ring holds %d samples, want it capped at 16000", ring.size)
	}
	if d.Fed() != 16000*60 {
		t.Fatalf("Fed = %d, want %d", d.Fed(), 16000*60)
	}
}

func TestSpanDetectorFlushDrainsTrailingSpeech(t *testing.T) {
	vad := &fakeVAD{onFlush: []fakeSegment{{start: 1600, samples: marked(1600, 800)}}}
	d := NewVADSpanDetector(vad, 0, 16000)
	feed(d, marked(0, 2400), 1600)

	if _, ok := d.Next(); ok {
		t.Fatal("a span was available before Flush")
	}
	d.Flush()
	if !vad.flushed {
		t.Fatal("Flush did not reach the detector")
	}
	span, ok := d.Next()
	if !ok {
		t.Fatal("no span after Flush")
	}
	if span.StartSample != 1600 || span.EndSample != 2400 {
		t.Fatalf("span = [%d,%d), want [1600,2400)", span.StartSample, span.EndSample)
	}
}

func TestSpanDetectorSkipsEmptySegments(t *testing.T) {
	vad := &fakeVAD{}
	d := NewVADSpanDetector(vad, 800, 16000)
	feed(d, marked(0, 8000), 1600)
	vad.queued = []fakeSegment{
		{start: 1600},
		{start: 3200, samples: marked(3200, 800)},
	}

	span, ok := d.Next()
	if !ok {
		t.Fatal("Next reported no span")
	}
	if span.StartSample != 2400 {
		t.Fatalf("StartSample = %d, want the second segment's 2400", span.StartSample)
	}
}

func TestPreRollRingSamples(t *testing.T) {
	got := PreRollRingSamples(20*time.Second, 600*time.Millisecond, 300*time.Millisecond, 16000)
	want := int(21.9 * 16000)
	if got != want {
		t.Fatalf("PreRollRingSamples = %d, want %d", got, want)
	}
	if got := PreRollRingSamples(20*time.Second, 0, 0, 0); got != 0 {
		t.Fatalf("PreRollRingSamples with no rate = %d, want 0", got)
	}
}

// TestNewFileSpanDetectorSizesTheRing is the guard against a caller sizing the
// ring itself: zero options must read as the file detector's own defaults, and
// the ring has to be big enough to still hold a span's pre-roll when the span
// is reported a whole MaxSpeech plus MinSilence later.
func TestNewFileSpanDetectorSizesTheRing(t *testing.T) {
	d := NewFileSpanDetector(&fakeVAD{}, audio.FileVADOptions{}, 300*time.Millisecond)
	got := d.(*vadSpanDetector)
	if want := 300 * 16; got.preRoll != want {
		t.Fatalf("preRoll = %d samples, want %d", got.preRoll, want)
	}
	if want := PreRollRingSamples(20*time.Second, 600*time.Millisecond, 300*time.Millisecond, 16000); len(got.ring.buf) != want {
		t.Fatalf("ring = %d samples, want %d", len(got.ring.buf), want)
	}
}
