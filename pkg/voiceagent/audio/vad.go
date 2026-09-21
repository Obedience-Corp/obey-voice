package audio

import (
	"fmt"
	"path/filepath"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const (
	vadThreshold         = 0.6
	vadMinSpeechDuration = 0.25
	// vadWindowSize is the Silero v5 analysis window in samples (32ms at 16kHz).
	// The binding forwards a zero value straight to C, so set it explicitly
	// rather than relying on the model's default.
	vadWindowSize = 512
	// vadBufferSeconds is the detector's internal audio buffer. A segment can
	// never be longer than what the buffer holds.
	vadBufferSeconds = 30
)

// vadParams are the per-detector knobs newVAD forwards to sherpa. Zero values
// are forwarded as they are, which is what the native default means.
type vadParams struct {
	threshold  float32
	minSpeech  float32
	minSilence float32
	// maxSpeech raises the detector's threshold once a segment runs longer
	// than this many seconds, which splits continuous speech. Zero leaves the
	// native default, which is what the live capture paths use.
	maxSpeech float32
}

// VAD wraps sherpa-onnx Silero voice activity detection.
type VAD struct {
	detector *sherpa.VoiceActivityDetector
}

// NewVAD creates the capture VAD, honoring the configurable threshold and
// minimum speech duration.
func NewVAD(cfg *config.Config) (*VAD, error) {
	threshold := float32(vadThreshold)
	if cfg.VADThreshold > 0 {
		threshold = float32(cfg.VADThreshold)
	}
	minSpeech := float32(vadMinSpeechDuration)
	if cfg.VADMinSpeechDuration > 0 {
		minSpeech = float32(cfg.VADMinSpeechDuration)
	}
	return newVAD(cfg, vadParams{threshold: threshold, minSpeech: minSpeech, minSilence: float32(cfg.VADSilenceDuration)})
}

// NewBargeInVAD creates a VAD tuned for interrupt detection, stricter by
// default than the capture VAD so residual TTS echo is far less likely to
// register as the user interrupting. The threshold is config-driven
// (barge_in_vad_threshold, default 0.85, clamped 0.3-0.95 by
// config.BargeInVADThresholdOf) — droppable toward ~0.6 once
// voice_frontend=webrtc's AEC is doing the echo suppression instead. Minimum
// speech duration keeps the capture VAD's own tuning from bleeding into
// barge-in.
func NewBargeInVAD(cfg *config.Config) (*VAD, error) {
	// webrtc's unset default is 0.6; see config.WebRTCBargeInVADThreshold.
	threshold := float32(config.BargeInVADThresholdOf(cfg))
	return newVAD(cfg, vadParams{threshold: threshold, minSpeech: vadMinSpeechDuration, minSilence: float32(cfg.VADSilenceDuration)})
}

func newVAD(cfg *config.Config, params vadParams) (*VAD, error) {
	modelPath := filepath.Join(config.ModelsDirFrom(cfg), "silero_vad.onnx")

	sileroConfig := sherpa.SileroVadModelConfig{
		Model:              modelPath,
		MinSpeechDuration:  params.minSpeech,
		MinSilenceDuration: params.minSilence,
		Threshold:          params.threshold,
		WindowSize:         vadWindowSize,
		MaxSpeechDuration:  params.maxSpeech,
	}

	vadConfig := sherpa.VadModelConfig{
		SileroVad:  sileroConfig,
		SampleRate: SampleRate,
	}

	detector := sherpa.NewVoiceActivityDetector(&vadConfig, vadBufferSeconds)
	if detector == nil {
		return nil, fmt.Errorf("failed to create VAD detector (model: %s)", modelPath)
	}

	return &VAD{detector: detector}, nil
}

// AcceptWaveform feeds audio samples to the VAD.
func (v *VAD) AcceptWaveform(samples []float32) {
	v.detector.AcceptWaveform(samples)
}

// IsSpeechDetected returns true if speech has been detected and segments are available.
func (v *VAD) IsSpeechDetected() bool {
	return !v.detector.IsEmpty()
}

// IsEmpty returns true if no speech segments are available.
func (v *VAD) IsEmpty() bool {
	return v.detector.IsEmpty()
}

// IsSpeech returns true if current audio chunk contains speech.
func (v *VAD) IsSpeech() bool {
	return v.detector.IsSpeech()
}

// Front returns the first detected speech segment's samples.
func (v *VAD) Front() []float32 {
	seg := v.detector.Front()
	return seg.Samples
}

// FrontSegment returns the first detected speech segment's samples together
// with its start offset, measured in samples since the last Clear. Callers use
// the offset to prepend pre-trigger audio the
// VAD trimmed from the onset.
func (v *VAD) FrontSegment() (samples []float32, start int) {
	seg := v.detector.Front()
	return seg.Samples, seg.Start
}

// Pop removes the first speech segment.
func (v *VAD) Pop() {
	v.detector.Pop()
}

// Clear resets the detector for a new stream, including its sample offsets.
// Native Clear only removes queued speech segments; the wrapper's reset
// contract requires native Reset so detector state and pre-roll stay aligned.
func (v *VAD) Clear() {
	v.detector.Reset()
}

// Flush signals the VAD that the audio stream has ended.
func (v *VAD) Flush() {
	v.detector.Flush()
}

// Delete frees the VAD resources.
func (v *VAD) Delete() {
	sherpa.DeleteVoiceActivityDetector(v.detector)
}
