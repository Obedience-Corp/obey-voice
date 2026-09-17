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
)

// startupPreserveFrontend runs NLMS beside the primary (usually APM) front-end
// and yields the NLMS residual when APM has over-suppressed near-end speech.
type startupPreserveFrontend struct {
	mu          sync.Mutex
	primary     Frontend
	preserve    Frontend
	usePreserve bool
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
	primary := f.primary.ProcessCapture(samples)
	use := frameRMS(preserved) > StartupPreserveMinRMS && frameRMS(preserved) > startupPreserveRatio*frameRMS(primary)
	f.mu.Lock()
	f.usePreserve = use
	f.mu.Unlock()
	return primary
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
	_ Frontend         = (*startupPreserveFrontend)(nil)
	_ ReferenceDelayer = (*startupPreserveFrontend)(nil)
	_ GainReporter     = (*startupPreserveFrontend)(nil)
)

// NearEndReporter is optionally implemented by front-ends that can distinguish
// over-suppressed near-end speech from echo residual.
type NearEndReporter interface {
	NearEnd() bool
}
