package playback

import (
	"encoding/binary"
	"slices"
	"testing"
	"time"
)

func referenceSamples(rec *recordingFrontend) []float32 {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []float32
	for _, block := range rec.refs {
		out = append(out, block...)
	}
	return out
}

func waitReferenceSamples(t *testing.T, rec *recordingFrontend, want int) []float32 {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got := referenceSamples(rec)
		if len(got) >= want {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("reference samples = %d, want at least %d", len(referenceSamples(rec)), want)
	return nil
}

func TestOnDataForwardsCompleteRenderTimelineIncludingIdleAndTail(t *testing.T) {
	rec := &recordingFrontend{}
	ingress := newAECRefIngress(rec)
	defer ingress.Close()
	p := &Player{channels: 1, sampleRate: 16000, aecIngress: ingress}
	p.volumeHundredths.Store(100)

	// An idle callback is still part of the render clock and must advance AEC
	// with explicit silence.
	p.onData(make([]byte, 8), nil, 4)

	seg := newPlaybackSegment()
	seg.append([]int16{1000, -1000, 2000, -2000})
	seg.finishInput(nil)
	p.current = seg
	out := make([]byte, 8)
	p.onData(out, nil, 4)
	got := make([]int16, 4)
	for i := range got {
		got[i] = int16(binary.LittleEndian.Uint16(out[i*2:]))
	}
	if !slices.Equal(got, []int16{1000, -1000, 2000, -2000}) {
		t.Fatalf("active callback output = %v", got)
	}

	// A short final segment still owns the whole device callback. Its cleared
	// tail must reach AEC as silence rather than shortening the timeline.
	tail := newPlaybackSegment()
	tail.append([]int16{3000, -3000})
	tail.finishInput(nil)
	p.current = tail
	p.onData(make([]byte, 8), nil, 4)

	refs := waitReferenceSamples(t, rec, 12)
	if len(refs) != 12 {
		t.Fatalf("render reference sample count = %d, want exactly 12", len(refs))
	}
	want := []float32{0, 0, 0, 0, 1000.0 / 32767, -1000.0 / 32767, 2000.0 / 32767, -2000.0 / 32767, 3000.0 / 32767, -3000.0 / 32767, 0, 0}
	if !slices.Equal(refs, want) {
		t.Fatalf("render reference timeline = %v, want %v", refs, want)
	}
}
