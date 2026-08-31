// Package apm binds webrtc-audio-processing's AEC3 acoustic echo canceller
// (plus its optional noise suppressor) into the engine over cgo.
//
// This is gated behind the "apm" build tag: the library is not vendored into
// this repo (see scripts/build-webrtc-apm.sh) and CI's default build,
// `go build ./...`, has neither the library nor a C++ toolchain guaranteed —
// it must keep working with this package entirely absent from the build.
// Nothing outside an apm-tagged build (this package and cmd/apmharness) may
// import it.
//
// The surface mirrors the four calls 03-candidates.md scoped for a thin
// shim: New, ProcessRender, ProcessCapture, SetStreamDelayMs, plus Close and
// the stats getters the library computes for free once AEC3 is enabled.
// LinearAecOutput was added afterward per 04-measurements.md finding 3, to
// retrieve AEC3's linear (pre-nonlinear-suppression) output alongside the
// suppressed one.
//
//go:build apm

package apm

/*
#cgo pkg-config: webrtc-audio-processing-1
#cgo CXXFLAGS: -std=c++17
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// NSLevel selects the noise suppressor's aggressiveness, matching
// webrtc::AudioProcessing::Config::NoiseSuppression::Level.
type NSLevel int

const (
	NSLevelLow NSLevel = iota
	NSLevelModerate
	NSLevelHigh
	NSLevelVeryHigh
)

// validSampleRate reports whether hz is one of the four rates the int16 APM
// interface accepts (webrtc::AudioProcessing::NativeRate). Any other rate is
// silently reinterpreted by the library as 48kHz, which would desync frame
// sizing here, so it is rejected up front instead.
func validSampleRate(hz int) bool {
	switch hz {
	case 8000, 16000, 32000, 48000:
		return true
	default:
		return false
	}
}

// APM is one webrtc AudioProcessing instance: AEC3 (desktop mode) is always
// on, with optional noise suppression. It is not safe for concurrent use —
// callers process render and capture frames from a single audio thread, the
// same assumption the underlying library documents.
type APM struct {
	handle       *C.ApmHandle
	sampleRate   int
	frameSamples int
}

// LinearAECFrameSamples is the fixed number of mono float32 samples
// GetLinearAecOutput (and LinearAecOutput below) produce per call: 10ms at
// 16kHz, always — regardless of an instance's own configured sample rate.
const LinearAECFrameSamples = 160

// New creates an APM instance for mono audio at sampleRateHz, which must be
// one of 8000, 16000, 32000, or 48000. AEC3 is always enabled; enableNS turns
// on the noise suppressor at level nsLevel. exportLinearAEC enables
// retrieving AEC3's linear (pre-nonlinear-suppression) output via
// LinearAecOutput after each ProcessCapture call — see 04-measurements.md
// finding 3: the default suppressed output destroys near-end speech during
// double-talk, but the linear output is documented to preserve it.
func New(sampleRateHz int, enableNS bool, nsLevel NSLevel, exportLinearAEC bool) (*APM, error) {
	if !validSampleRate(sampleRateHz) {
		return nil, fmt.Errorf("apm: sample rate %d is not a native rate (8000, 16000, 32000, 48000)", sampleRateHz)
	}

	ns := C.int(0)
	if enableNS {
		ns = 1
	}
	linear := C.int(0)
	if exportLinearAEC {
		linear = 1
	}
	handle := C.apm_new(C.int(sampleRateHz), ns, C.int(nsLevel), linear)
	if handle == nil {
		return nil, fmt.Errorf("apm: AudioProcessingBuilder rejected sample rate %d", sampleRateHz)
	}

	return &APM{
		handle:       handle,
		sampleRate:   sampleRateHz,
		frameSamples: int(C.apm_frame_samples(handle)),
	}, nil
}

// FrameSamples is the number of mono int16 samples one 10ms frame holds at
// this instance's sample rate. ProcessRender and ProcessCapture require
// frames of exactly this length.
func (a *APM) FrameSamples() int { return a.frameSamples }

// ProcessRender feeds one 10ms far-end (playback reference) frame into the
// echo canceller's reverse stream.
func (a *APM) ProcessRender(frame []int16) error {
	if len(frame) != a.frameSamples {
		return fmt.Errorf("apm: render frame has %d samples, want %d", len(frame), a.frameSamples)
	}
	code := C.apm_process_render(a.handle, (*C.int16_t)(unsafe.Pointer(&frame[0])), C.int(len(frame)))
	return apmError("process render", code)
}

// ProcessCapture runs one 10ms near-end (microphone) frame through AEC3 (and
// noise suppression, if enabled) and returns the processed frame.
func (a *APM) ProcessCapture(frame []int16) ([]int16, error) {
	if len(frame) != a.frameSamples {
		return nil, fmt.Errorf("apm: capture frame has %d samples, want %d", len(frame), a.frameSamples)
	}
	out := make([]int16, a.frameSamples)
	code := C.apm_process_capture(a.handle,
		(*C.int16_t)(unsafe.Pointer(&frame[0])), C.int(len(frame)),
		(*C.int16_t)(unsafe.Pointer(&out[0])))
	if err := apmError("process capture", code); err != nil {
		return nil, err
	}
	return out, nil
}

// LinearAecOutput returns the most recently produced 10ms chunk of AEC3's
// linear (pre-nonlinear-suppression) filter output, following a
// ProcessCapture call. The output is always mono float32 in [-1, 1] at a
// fixed 16kHz — LinearAECFrameSamples (160) samples — regardless of this
// instance's own sample rate; callers running at a different rate must
// account for that themselves when comparing it against their own signal.
//
// Only produces samples when New was called with exportLinearAEC true. ok is
// false when no new output was available for the most recent call (e.g.
// export not enabled, or called before the first ProcessCapture); that is
// not an error.
func (a *APM) LinearAecOutput() (samples []float32, ok bool, err error) {
	buf := make([]float32, LinearAECFrameSamples)
	n := int(C.apm_get_linear_aec_output(a.handle, (*C.float)(unsafe.Pointer(&buf[0])), C.int(len(buf))))
	switch {
	case n < 0:
		return nil, false, apmError("get linear aec output", C.int(n))
	case n == 0:
		return nil, false, nil
	default:
		return buf[:n], true, nil
	}
}

// SetStreamDelayMs declares the render-to-capture delay: how long after a
// frame reaches ProcessRender its echo reaches the microphone. Required
// whenever echo cancellation is enabled, which it always is here. Callers
// may set this once (or whenever a delay estimate updates) rather than every
// frame — the library reasserts the last value internally on every
// ProcessCapture call.
func (a *APM) SetStreamDelayMs(ms int) error {
	code := C.apm_set_stream_delay_ms(a.handle, C.int(ms))
	return apmError("set stream delay", code)
}

// Stats mirrors webrtc::AudioProcessingStats. Every field's presence is
// reported by the paired Has* field, since the library only starts producing
// a given value once enough frames have passed through its owning submodule.
type Stats struct {
	EchoReturnLoss                     float64
	HasEchoReturnLoss                  bool
	EchoReturnLossEnhancement          float64 // ERLE, in dB — the harness's headline metric.
	HasEchoReturnLossEnhancement       bool
	DivergentFilterFraction            float64
	HasDivergentFilterFraction         bool
	DelayMedianMs                      int32
	HasDelayMedianMs                   bool
	DelayStandardDeviationMs           int32
	HasDelayStandardDeviationMs        bool
	ResidualEchoLikelihood             float64
	HasResidualEchoLikelihood          bool
	ResidualEchoLikelihoodRecentMax    float64
	HasResidualEchoLikelihoodRecentMax bool
	DelayMs                            int32
	HasDelayMs                         bool
	VoiceDetected                      bool
	HasVoiceDetected                   bool
	OutputRMSDBFS                      int32
	HasOutputRMSDBFS                   bool
}

// Stats reports the library's current aggregated statistics. Safe to call
// every frame: the values only update roughly once a second internally.
func (a *APM) Stats() Stats {
	var raw C.ApmStats
	C.apm_get_stats(a.handle, &raw)

	return Stats{
		EchoReturnLoss:                     float64(raw.echo_return_loss),
		HasEchoReturnLoss:                  raw.has_echo_return_loss != 0,
		EchoReturnLossEnhancement:          float64(raw.echo_return_loss_enhancement),
		HasEchoReturnLossEnhancement:       raw.has_echo_return_loss_enhancement != 0,
		DivergentFilterFraction:            float64(raw.divergent_filter_fraction),
		HasDivergentFilterFraction:         raw.has_divergent_filter_fraction != 0,
		DelayMedianMs:                      int32(raw.delay_median_ms),
		HasDelayMedianMs:                   raw.has_delay_median_ms != 0,
		DelayStandardDeviationMs:           int32(raw.delay_standard_deviation_ms),
		HasDelayStandardDeviationMs:        raw.has_delay_standard_deviation_ms != 0,
		ResidualEchoLikelihood:             float64(raw.residual_echo_likelihood),
		HasResidualEchoLikelihood:          raw.has_residual_echo_likelihood != 0,
		ResidualEchoLikelihoodRecentMax:    float64(raw.residual_echo_likelihood_recent_max),
		HasResidualEchoLikelihoodRecentMax: raw.has_residual_echo_likelihood_recent_max != 0,
		DelayMs:                            int32(raw.delay_ms),
		HasDelayMs:                         raw.has_delay_ms != 0,
		VoiceDetected:                      raw.voice_detected != 0,
		HasVoiceDetected:                   raw.has_voice_detected != 0,
		OutputRMSDBFS:                      int32(raw.output_rms_dbfs),
		HasOutputRMSDBFS:                   raw.has_output_rms_dbfs != 0,
	}
}

// Close releases the underlying AudioProcessing instance. Safe to call once;
// the handle is nil'd so a repeat call is a no-op rather than a double free.
func (a *APM) Close() error {
	if a.handle == nil {
		return nil
	}
	C.apm_free(a.handle)
	a.handle = nil
	return nil
}

// apmError translates a webrtc::AudioProcessing::Error code (0 == success,
// negative == error) into a Go error, naming the call that produced it.
func apmError(op string, code C.int) error {
	if code == 0 {
		return nil
	}
	return fmt.Errorf("apm: %s: webrtc AudioProcessing error %d", op, int(code))
}
