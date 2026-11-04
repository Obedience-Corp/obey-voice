//go:build apm

package apmfrontend

import (
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/apm"
)

// shim is the subset of *apm.APM this package drives, extracted so tests can
// substitute a fake instead of the real cgo/webrtc-audio-processing instance.
// It is apm-tagged (apm.Stats only exists in that build) rather than
// exported from the parent audio package, which must stay buildable without
// the library.
type shim interface {
	ProcessCapture(frame []int16) ([]int16, error)
	ProcessRender(frame []int16) error
	SetStreamDelayMs(ms int) error
	Stats() apm.Stats
	Close() error
}

// newAPM builds the real shim. Tests override this package variable to
// inject a fake, so New's wiring (framing, conversion, error propagation) can
// be checked without exercising the DSP itself.
var newAPM = func(nsLevel apm.NSLevel) (shim, error) {
	return apm.New(audio.SampleRate, true /* enableNS */, nsLevel, false /* exportLinearAEC: D3 uses the single suppressed stream */)
}

// Frontend wraps the APM shim as an audio.Frontend. D4: APM supplies AEC and
// noise suppression only — automatic gain control is not APM's, so this
// composes the engine's existing AGC stage after it, the same stage
// VoiceFrontend (nlms) uses.
//
// The underlying library is not safe for concurrent access (apm.APM's
// contract), but render frames arrive from the player's reference-ingress
// worker while capture frames arrive from the malgo capture callback — two
// different goroutines — so every call into the shim is serialized under mu,
// mirroring VoiceFrontend's own single-mutex design.
type Frontend struct {
	mu sync.Mutex

	apm      shim
	agc      audio.AGC
	capture  captureFramer
	render   renderFramer
	delayMs  int
	delaySet bool
}

var (
	_ audio.Frontend              = (*Frontend)(nil)
	_ audio.ReferenceDelayer      = (*Frontend)(nil)
	_ audio.FrontendStatsReporter = (*Frontend)(nil)
)

// New creates an APM-backed Frontend at the engine's fixed capture rate
// (audio.SampleRate, 16kHz) with noise suppression at nsLevel (0=low,
// 1=moderate, 2=high, 3=very high; clamped to that range).
func New(nsLevel int) (audio.Frontend, error) {
	level := clampNSLevel(nsLevel)
	inst, err := newAPM(apm.NSLevel(level))
	if err != nil {
		return nil, err
	}
	return &Frontend{
		apm: inst,
		agc: audio.NewAGC(audio.DefaultAGCTargetRMS, audio.DefaultAGCMinGain, audio.DefaultAGCMaxGain),
	}, nil
}

func clampNSLevel(level int) int {
	if level < 0 {
		return 0
	}
	if level > 3 {
		return 3
	}
	return level
}

// ProcessCapture reframes samples into APM's 10ms frames, runs each through
// AEC+NS, composes the existing AGC stage after it, and returns exactly
// len(samples) samples — see captureFramer for why a chunk size that is not
// an exact multiple of the 10ms frame can trail input by under one frame.
func (f *Frontend) ProcessCapture(samples []float32) []float32 {
	if len(samples) == 0 {
		return samples
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	out, err := f.capture.Process(samples, f.apm.ProcessCapture)
	if err != nil {
		// The shim only errors on a malformed frame (wrong length, which
		// captureFramer guarantees cannot happen) or an internal AudioProcessing
		// failure. Either way, raw passthrough is safer than dropping audio: STT
		// and barge-in still see something rather than silence.
		return samples
	}

	f.agc.Process(out)
	return out
}

// InputGain implements audio.GainReporter: the multiplier the composed AGC
// stage is currently applying to captured audio.
func (f *Frontend) InputGain() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.agc.Gain()
}

// PushPlaybackReference reframes samples into APM's 10ms frames and feeds
// each into the echo canceller's reverse stream. Errors are dropped rather
// than surfaced — PushPlaybackReference has no error return on the Frontend
// interface, and the aec_ref ingress worker that calls it is not on the
// capture path, so a bad reference frame degrading cancellation is
// preferable to it panicking.
func (f *Frontend) PushPlaybackReference(samples []float32) {
	if len(samples) == 0 {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.render.Push(samples, f.apm.ProcessRender)
}

// SetReferenceDelay converts the player's capture-rate delay offset to
// milliseconds and declares it to APM via SetStreamDelayMs. Precision barely
// matters here (see workflow/explore/barge-in-architecture/05-architecture.md):
// AEC3 refines the estimate itself from the reference/capture streams.
func (f *Frontend) SetReferenceDelay(samples int) {
	if samples < 0 {
		samples = 0
	}
	ms := (samples*1000 + audio.SampleRate/2) / audio.SampleRate

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delaySet && f.delayMs == ms {
		return
	}
	f.delayMs = ms
	f.delaySet = true
	_ = f.apm.SetStreamDelayMs(ms)
}

// FrontendStats maps APM's aggregate stats (updated roughly once a second)
// to the diagnostics-only snapshot doctor/logging consume. Never a barge-in
// trip signal — see audio.FrontendStats.
func (f *Frontend) FrontendStats() audio.FrontendStats {
	f.mu.Lock()
	s := f.apm.Stats()
	f.mu.Unlock()

	return audio.FrontendStats{
		ERLE:                      s.EchoReturnLossEnhancement,
		HasERLE:                   s.HasEchoReturnLossEnhancement,
		ResidualEchoLikelihood:    s.ResidualEchoLikelihood,
		HasResidualEchoLikelihood: s.HasResidualEchoLikelihood,
		VoiceDetected:             s.VoiceDetected,
		HasVoiceDetected:          s.HasVoiceDetected,
		DelayMs:                   s.DelayMs,
		HasDelayMs:                s.HasDelayMs,
	}
}

// Close releases the underlying APM instance.
func (f *Frontend) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apm.Close()
}

// ResetPlaybackReference discards the partial reverse-stream frame. Capture
// framing, gain and the running APM instance remain intact. The installed APM
// shim exposes no independent reset of its internal reverse-stream history.
func (f *Frontend) ResetPlaybackReference() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.render.Reset()
}
