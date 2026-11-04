package audio

import (
	"slices"
	"sync"
)

const (
	// StartupPreserveMinRMS is the NLMS residual level that distinguished
	// saved startup "Wait." from echo-only APM residuals on Studio Display.
	StartupPreserveMinRMS = 0.01
	startupPreserveRatio  = 2
	// nearEndHop is the residual scan stride. Studio Display capture callbacks
	// are 960 samples (60ms); this matches the live NearEnd decision.
	nearEndHop = 960
	// nearEndPreRoll / nearEndHold crop the first NearEnd hop to 300ms
	// (50ms before through 250ms after hop start). Live T172324Z NLMS
	// 130–430ms is exact "Wait."; 0–800ms raw/NLMS mix is not.
	nearEndPreRoll = 800  // 50ms
	nearEndHold    = 4000 // 250ms from hop start
	nearEndMinKeep = 3200 // 200ms; shorter crops decoded as fragments
)

// startupPreserveFrontend runs NLMS beside the primary (usually APM) front-end
// and yields the NLMS residual when APM has over-suppressed near-end speech.
type startupPreserveFrontend struct {
	mu           sync.Mutex
	primary      Frontend
	preserve     Frontend
	usePreserve  bool
	lastResidual []float32
}

// NewStartupPreserveFrontend wraps primary with an NLMS near-end preserve
// path. Playback reference and delay are forwarded to both.
func NewStartupPreserveFrontend(primary Frontend) Frontend {
	if primary == nil {
		return NewVoiceFrontend()
	}
	return newStartupPreserveFrontend(primary, NewVoiceFrontend())
}

func newStartupPreserveFrontend(primary, preserve Frontend) *startupPreserveFrontend {
	return &startupPreserveFrontend{primary: primary, preserve: preserve}
}

func (f *startupPreserveFrontend) ProcessCapture(samples []float32) []float32 {
	if f == nil {
		return samples
	}
	if len(samples) == 0 {
		return samples
	}
	preserved := f.preserve.ProcessCapture(slices.Clone(samples))
	residual := preserved
	if r, ok := f.preserve.(aecResidualReporter); ok {
		if res := r.LastAECResidual(); len(res) == len(preserved) {
			residual = res
		}
	}
	primary := f.primary.ProcessCapture(samples)
	use := frameRMS(residual) > StartupPreserveMinRMS && frameRMS(residual) > startupPreserveRatio*frameRMS(primary)
	f.mu.Lock()
	f.usePreserve = use
	f.lastResidual = slices.Clone(residual)
	f.mu.Unlock()
	return primary
}

// LastResidual returns the NLMS output of the most recent ProcessCapture.
func (f *startupPreserveFrontend) LastResidual() []float32 {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.lastResidual)
}

// NearEnd reports that the NLMS residual is a stronger near-end than the
// primary (APM) output. Barge-in uses this during the startup fence.
func (f *startupPreserveFrontend) NearEnd() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usePreserve
}

func (f *startupPreserveFrontend) PushPlaybackReference(samples []float32) {
	if f == nil {
		return
	}
	f.preserve.PushPlaybackReference(samples)
	f.primary.PushPlaybackReference(samples)
}

func (f *startupPreserveFrontend) SetReferenceDelay(samples int) {
	if f == nil {
		return
	}
	if delayed, ok := f.preserve.(ReferenceDelayer); ok {
		delayed.SetReferenceDelay(samples)
	}
	if delayed, ok := f.primary.(ReferenceDelayer); ok {
		delayed.SetReferenceDelay(samples)
	}
}

func (f *startupPreserveFrontend) InputGain() float64 {
	if f == nil {
		return 1
	}
	f.mu.Lock()
	use := f.usePreserve
	f.mu.Unlock()
	if use {
		if g, ok := f.preserve.(GainReporter); ok {
			return g.InputGain()
		}
		return 1
	}
	if g, ok := f.primary.(GainReporter); ok {
		return g.InputGain()
	}
	return 1
}

func (f *startupPreserveFrontend) Close() error {
	if f == nil {
		return nil
	}
	_ = f.preserve.Close()
	return f.primary.Close()
}

var (
	_ Frontend            = (*startupPreserveFrontend)(nil)
	_ ReferenceDelayer    = (*startupPreserveFrontend)(nil)
	_ GainReporter        = (*startupPreserveFrontend)(nil)
	_ NearEndReporter     = (*startupPreserveFrontend)(nil)
	_ ResidualReporter    = (*startupPreserveFrontend)(nil)
	_ aecResidualReporter = (*VoiceFrontend)(nil)
)

// NearEndReporter is optionally implemented by front-ends that can distinguish
// over-suppressed near-end speech from echo residual.
type NearEndReporter interface {
	NearEnd() bool
}

// ResidualReporter exposes the parallel near-end residual for startup STT.
type ResidualReporter interface {
	LastResidual() []float32
}

// aecResidualReporter is the NLMS error before the preserve path's NS/AGC.
type aecResidualReporter interface {
	LastAECResidual() []float32
}

// CropNearEndResidual keeps the first NearEnd hop of residual plus a bounded
// pad. Echo-only Studio Display residuals do not contain a hop. A longer
// residual window mixes the same-speaker assistant onset back in.
func CropNearEndResidual(residual, primary []float32) []float32 {
	n := min(len(residual), len(primary))
	if n == 0 {
		return nil
	}
	residual = residual[:n]
	primary = primary[:n]
	first := -1
	for i := 0; i+nearEndHop <= n; i += nearEndHop {
		r := frameRMS(residual[i : i+nearEndHop])
		p := frameRMS(primary[i : i+nearEndHop])
		if r > StartupPreserveMinRMS && r > startupPreserveRatio*p {
			first = i
			break
		}
	}
	if first < 0 {
		return nil
	}
	start := max(0, first-nearEndPreRoll)
	end := min(n, first+nearEndHold)
	if end-start < nearEndMinKeep {
		return nil
	}
	return slices.Clone(residual[start:end])
}

// alignedKeepSamples trims a keep window to whole NearEnd hops counted from
// the newest sample, matching live callback decisions. A short remainder is
// left unaligned so a 200ms crop is not discarded.
func alignedKeepSamples(n int) int {
	if n < nearEndHop {
		return n
	}
	aligned := n - n%nearEndHop
	if aligned < nearEndMinKeep {
		return n
	}
	return aligned
}

// NormalizeSpeechRMS scales a NearEnd crop to the front-end target RMS so
// STT audibility (which divides by live AGC gain) still admits it.
func NormalizeSpeechRMS(samples []float32) []float32 {
	if len(samples) == 0 {
		return nil
	}
	r := frameRMS(samples)
	if r < 1e-6 || r >= frontendTargetRMS {
		return slices.Clone(samples)
	}
	g := float32(frontendTargetRMS / r)
	out := make([]float32, len(samples))
	for i, s := range samples {
		out[i] = s * g
	}
	return out
}

// PadToCaptureFrames extends a crop to a whole ReadFrame so STT does not
// mix the last Wait samples with later live APM in one chunk.
func PadToCaptureFrames(samples []float32) []float32 {
	if len(samples) == 0 {
		return nil
	}
	n := len(samples)
	if rem := n % ChunkSize; rem != 0 {
		n += ChunkSize - rem
	}
	if n == len(samples) {
		return slices.Clone(samples)
	}
	out := make([]float32, n)
	copy(out, samples)
	return out
}

// startupDecoderSilence is longer than the production VAD silence timeout
// (1.2s) so STT finalizes the NearEnd crop without the live APM tail.
const startupDecoderSilence = ChunkSize * 14 // 1.4s

// PadStartupListen aligns the crop to ReadFrame and appends trailing silence
// so the next listen ends on Wait instead of later echo.
func PadStartupListen(samples []float32) []float32 {
	aligned := PadToCaptureFrames(samples)
	if len(aligned) == 0 {
		return nil
	}
	out := make([]float32, len(aligned)+startupDecoderSilence)
	copy(out, aligned)
	return out
}

// ResetPlaybackReference resets both echo-reference paths without resetting
// the microphone preservation decision or captured residuals.
func (f *startupPreserveFrontend) ResetPlaybackReference() {
	if f == nil {
		return
	}
	if resetter, ok := f.preserve.(PlaybackReferenceResetter); ok {
		resetter.ResetPlaybackReference()
	}
	if resetter, ok := f.primary.(PlaybackReferenceResetter); ok {
		resetter.ResetPlaybackReference()
	}
}
