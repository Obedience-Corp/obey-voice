package audio

import (
	"math"
	"testing"
)

// TestSegmentReportsSynthesizedDuration covers the denominator of the realtime
// factor. It is measured at the stream's own rate, before any resample, so a
// device that runs at a different rate cannot change the figure.
func TestSegmentReportsSynthesizedDuration(t *testing.T) {
	const inputRate = 24000
	samples := make([]float32, inputRate*2) // exactly 2 seconds

	segment := newPlaybackSegment()
	// 48000 output rate: the resample must not move the reported duration.
	finalizeSegment(segment, nil, samples, inputRate, 48000, nil)

	// Read the field rather than the terminal result: finishInput completes a
	// segment only once the device has drained it, and this test has no
	// device. The terminal paths are covered below.
	if math.Abs(segment.audioSeconds-2.0) > 1e-9 {
		t.Errorf("audioSeconds = %v, want 2.0 at the input rate regardless of the device rate", segment.audioSeconds)
	}
	if got := len(segment.samples); got != 48000*2 {
		t.Errorf("queued %d samples, want them resampled to the 48kHz device rate", got)
	}
}

// TestDrainedSegmentReportsDuration covers the ordinary path: the device
// consumes every sample, and the terminal result carries the duration.
func TestDrainedSegmentReportsDuration(t *testing.T) {
	const rate = 24000
	segment := newPlaybackSegment()
	finalizeSegment(segment, nil, make([]float32, rate), rate, rate, nil)

	buf := make([]byte, rate*2)
	for {
		n, done := segment.writeTo(buf, rate)
		if done {
			break
		}
		if n == 0 {
			t.Fatal("writeTo made no progress")
		}
	}
	segment.complete()

	result := <-segment.Done()
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if math.Abs(result.AudioSeconds-1.0) > 1e-9 {
		t.Errorf("AudioSeconds = %v, want 1.0", result.AudioSeconds)
	}
}

// TestInterruptedSegmentStillReportsWhatWasSynthesized is the case that would
// silently bias an aggregate: barge-in drops the queued samples, but the
// synthesis was still paid for, and a record that reported zero there would
// make every interrupted turn look infinitely fast.
func TestInterruptedSegmentStillReportsWhatWasSynthesized(t *testing.T) {
	const inputRate = 24000
	segment := newPlaybackSegment()
	segment.setAudioSeconds(make([]float32, inputRate*3), inputRate)

	segment.interrupt()

	result := <-segment.Done()
	if !result.Interrupted {
		t.Fatal("Interrupted = false, want true")
	}
	if math.Abs(result.AudioSeconds-3.0) > 1e-9 {
		t.Errorf("AudioSeconds = %v, want 3.0 to survive the interrupt", result.AudioSeconds)
	}
}

func TestFailedSegmentReportsDurationToo(t *testing.T) {
	const inputRate = 24000
	segment := newPlaybackSegment()
	segment.setAudioSeconds(make([]float32, inputRate/2), inputRate)

	segment.fail(errTestSynth)

	result := <-segment.Done()
	if result.Err != errTestSynth {
		t.Fatalf("Err = %v, want the synth error", result.Err)
	}
	if math.Abs(result.AudioSeconds-0.5) > 1e-9 {
		t.Errorf("AudioSeconds = %v, want 0.5", result.AudioSeconds)
	}
}

// TestSetAudioSecondsRejectsAnUnknownRate guards against reporting a duration
// derived from a zero rate, which would be an infinity in the artifact.
func TestSetAudioSecondsRejectsAnUnknownRate(t *testing.T) {
	segment := newPlaybackSegment()
	segment.setAudioSeconds(make([]float32, 1000), 0)
	if segment.audioSeconds != 0 {
		t.Errorf("audioSeconds = %v, want 0 when the rate is unknown", segment.audioSeconds)
	}
}

type testSynthError struct{}

func (testSynthError) Error() string { return "synth failed" }

var errTestSynth = testSynthError{}

// Done is a test-only accessor: the production path reads the result through
// Playback, which is built from the same channel.
func (s *playbackSegment) Done() <-chan PlaybackResult { return s.doneCh }
