// Package apmfrontend adapts the APM shim (pkg/voiceagent/audio/apm, apm
// build tag) into an audio.Frontend. This file has no build tag: it holds the
// pure-Go reframing/conversion logic shared by the real (tagged) and stub
// (untagged) halves of the package, so it — and its edge cases — can be unit
// tested without the webrtc-audio-processing library or a C++ toolchain.
package apmfrontend

import "math"

// frameSamples is APM's native frame size: 10ms of mono audio at the engine's
// fixed 16kHz capture rate (audio.SampleRate). It intentionally does not
// import pkg/voiceagent/audio to get that constant — this file must stay
// importable from the untagged stub without pulling in anything more than it
// needs — so it is asserted equal to audio.SampleRate/100 in the tagged
// implementation instead.
const frameSamples = 160

// captureFramer reframes an arbitrary-length stream of float32 capture
// samples into APM's fixed 160-sample frames, and reassembles the processed
// frames back into a stream whose length matches each call to Process — the
// contract every audio.Frontend.ProcessCapture implementation honors (see
// VoiceFrontend, the NLMS front-end: it also always returns len(input)
// samples).
//
// The engine's real capture chunks are 1600 samples (100ms @ 16kHz) — an
// exact multiple of frameSamples — so pending is always empty again by the
// end of a call in production. The partial-frame path below only exercises
// for a caller using a different chunk size (tests, or the probe's smaller
// playback writes): output can then lag input by less than one frame
// (<160 samples, <10ms), filled with silence, and drains once enough audio
// has accumulated to complete the pending frame. The backlog cannot grow
// without bound — pending never holds more than frameSamples-1 samples.
type captureFramer struct {
	pending []float32 // < frameSamples samples awaiting a full frame
	ready   []float32 // processed output not yet returned to a caller
}

// process runs one complete frameSamples-length int16 frame through APM
// (or a fake, in tests) and returns its output, also frameSamples long.
type captureProcessFunc func(frame []int16) ([]int16, error)

// Process feeds samples through process in frameSamples chunks and returns
// exactly len(samples) samples of output.
func (f *captureFramer) Process(samples []float32, process captureProcessFunc) ([]float32, error) {
	f.pending = append(f.pending, samples...)

	for len(f.pending) >= frameSamples {
		frame := f.pending[:frameSamples]
		out, err := process(float32ToInt16(frame))
		if err != nil {
			// The caller falls back to raw passthrough for this chunk. Buffered
			// state must go with it: a frame left in pending would be drained
			// into the NEXT chunk's output, shifting and duplicating audio.
			f.pending = f.pending[:0]
			f.ready = f.ready[:0]
			return nil, err
		}
		f.ready = append(f.ready, int16ToFloat32(out)...)
		f.pending = append(f.pending[:0], f.pending[frameSamples:]...)
	}

	return f.drain(len(samples)), nil
}

// drain returns exactly n samples from ready, oldest first, silence-padding
// the tail when fewer than n have finished processing so far.
func (f *captureFramer) drain(n int) []float32 {
	out := make([]float32, n)
	avail := min(n, len(f.ready))
	copy(out, f.ready[:avail])
	f.ready = append(f.ready[:0], f.ready[avail:]...)
	return out
}

// renderFramer reframes an arbitrary-length stream of float32 playback
// reference samples into APM's fixed 160-sample frames. Unlike captureFramer
// it has no return-length contract to honor — PushPlaybackReference is
// fire-and-forget — so leftover samples simply carry forward to the next
// call.
type renderFramer struct {
	pending []float32
}

type renderProcessFunc func(frame []int16) error

// Push feeds samples through process in frameSamples chunks, carrying any
// remainder forward.
func (f *renderFramer) Push(samples []float32, process renderProcessFunc) error {
	f.pending = append(f.pending, samples...)

	for len(f.pending) >= frameSamples {
		frame := f.pending[:frameSamples]
		if err := process(float32ToInt16(frame)); err != nil {
			return err
		}
		f.pending = append(f.pending[:0], f.pending[frameSamples:]...)
	}
	return nil
}

// float32ToInt16 converts samples in [-1, 1] to int16 PCM, clamping first so
// an out-of-range float cannot wrap instead of saturating.
func float32ToInt16(samples []float32) []int16 {
	out := make([]int16, len(samples))
	for i, s := range samples {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		out[i] = int16(s * math.MaxInt16)
	}
	return out
}

// int16ToFloat32 converts int16 PCM to float32 in [-1, 1], matching the
// conversion capture.go and aec_ref.go already use for the same PCM.
func int16ToFloat32(samples []int16) []float32 {
	out := make([]float32, len(samples))
	for i, s := range samples {
		out[i] = float32(s) / float32(math.MaxInt16)
	}
	return out
}

// Reset discards an incomplete reference frame across output discontinuities.
func (f *renderFramer) Reset() {
	clear(f.pending)
	f.pending = f.pending[:0]
}
