package audio

// Frontend processes live microphone audio and can receive playback reference
// audio for echo cancellation or other front-end processing.
type Frontend interface {
	ProcessCapture(samples []float32) []float32
	PushPlaybackReference(samples []float32)
	Close() error
}

// ReferenceDelayer is optionally implemented by front-ends whose echo
// canceller needs to know how long after PushPlaybackReference the audio is
// actually audible. The player knows that number — it configured the device
// buffer — but most Frontend implementations (passthrough, test doubles) have
// no use for it, so it stays off the Frontend interface.
type ReferenceDelayer interface {
	SetReferenceDelay(samples int)
}

// GainReporter is optionally implemented by front-ends and capture sources
// that amplify what the microphone heard. Energy thresholds downstream (the
// not-silence floor that keeps decoder ghosts out of a transcript) are stated
// in microphone units, so they have to be scaled by whatever gain has already
// been applied: AGC lifts room tone and talkers alike, and a fixed post-gain
// threshold either admits the noise or rejects the far talker. Sources that do
// not amplify simply do not implement it.
type GainReporter interface {
	InputGain() float64
}

// FrontendStats is a diagnostics-only snapshot of a front-end's aggregate
// health metrics. APM-backed front-ends update these roughly once a second —
// far too coarse a signal to gate any per-frame decision, so nothing in the
// barge-in path may treat this as a trip signal (see
// workflow/design/obey-voice-barge-in/03-slices.md, S2): the suppressed
// capture stream feeding the existing VAD/streak logic IS the double-talk
// detector. FrontendStats exists for logs and `doctor`.
type FrontendStats struct {
	ERLE                      float64
	HasERLE                   bool
	ResidualEchoLikelihood    float64
	HasResidualEchoLikelihood bool
	VoiceDetected             bool
	HasVoiceDetected          bool
	DelayMs                   int32
	HasDelayMs                bool
}

// FrontendStatsReporter is optionally implemented by front-ends that can
// report FrontendStats. Passthrough and the NLMS front-end do not.
type FrontendStatsReporter interface {
	FrontendStats() FrontendStats
}

// PassthroughFrontend leaves audio unchanged while preserving the runtime hook
// points needed for a future AEC/NS/AGC implementation.
type PassthroughFrontend struct{}

// NewPassthroughFrontend creates a no-op audio front-end.
func NewPassthroughFrontend() *PassthroughFrontend {
	return &PassthroughFrontend{}
}

// ProcessCapture returns the input unchanged.
func (p *PassthroughFrontend) ProcessCapture(samples []float32) []float32 {
	return samples
}

// PushPlaybackReference records playback reference audio. The passthrough
// implementation intentionally ignores it.
func (p *PassthroughFrontend) PushPlaybackReference(samples []float32) {}

// Close releases front-end resources.
func (p *PassthroughFrontend) Close() error {
	return nil
}

// EchoCancellerTaps reports the adaptive filter length, in capture-rate
// samples. Exported for the hardware probe: a measured echo delay is only
// interpretable against the window the filter can actually span, and the probe
// lives outside this package.
func EchoCancellerTaps() int { return echoCancellerTaps }

// PlaybackReferenceResetter discards buffered playback reference PCM at a
// device discontinuity without restarting capture or closing the frontend.
// The output owner must first stop and join its old reference producer.
type PlaybackReferenceResetter interface {
	ResetPlaybackReference()
}
