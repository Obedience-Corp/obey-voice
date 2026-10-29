package playback

import (
	"math"
	"testing"
)

func TestRecordingTapsRenderedVolumeAndExcludesInterruptedQueue(t *testing.T) {
	p := NewPlayer()
	p.channels = 2
	p.sampleRate = 48000
	p.SetVolume(.5)
	frames, stop := p.SubscribeRecording(4, nil)
	defer stop()
	segment := newPlaybackSegment()
	segment.append([]int16{20000, 20000, 20000, 20000, 30000, 30000, 30000, 30000})
	segment.finishInput(nil)
	p.current = segment
	out := make([]byte, 4*2*2)
	p.onData(out, nil, 4)
	first := <-frames
	p.Stop()
	p.onData(out, nil, 4)
	second := <-frames
	if first.SampleRate != 48000 || first.StartedAt.IsZero() || len(first.Samples) != 4 {
		t.Fatalf("frame=%+v", first)
	}
	for _, sample := range first.Samples {
		if math.Abs(float64(sample)-10000.0/32768) > .0001 {
			t.Fatalf("not actual post-volume PCM: %v", first.Samples)
		}
	}
	for _, sample := range second.Samples {
		if sample != 0 {
			t.Fatalf("recorded canceled queued speech: %v", second.Samples)
		}
	}
	// The next callback reused its scratch buffer; the first recording is intact.
	if first.Samples[0] == 0 {
		t.Fatal("callback scratch storage was retained")
	}
}
