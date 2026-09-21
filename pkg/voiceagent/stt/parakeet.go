package stt

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

// ParakeetSTT implements utterance-final STT with an NVIDIA Parakeet TDT model
// run as a sherpa-onnx offline NeMo transducer. It shares the whisper path's
// VAD-gated offline loop; only the recognizer differs — a single-pass
// transducer decode instead of whisper's autoregressive one, which is why it
// is several times faster on the same CPU and does not hallucinate captions
// on non-speech.
type ParakeetSTT struct {
	cfg        *config.Config
	recognizer *sherpa.OfflineRecognizer
	vad        *audio.VAD
	capture    audioSource
	model      config.ParakeetAsset
}

// NewParakeetSTT creates a Parakeet STT provider from the configured
// parakeet_model, which must already be installed under the models dir.
func NewParakeetSTT(cfg *config.Config, capture audioSource, vad *audio.VAD) (*ParakeetSTT, error) {
	model, err := config.ParakeetModel(cfg.ParakeetModel)
	if err != nil {
		return nil, err
	}
	dir := model.ModelDir(config.ModelsDirFrom(cfg))
	join := func(name string) string { return filepath.Join(dir, name) }

	recognizerConfig := sherpa.OfflineRecognizerConfig{
		ModelConfig: sherpa.OfflineModelConfig{
			Transducer: sherpa.OfflineTransducerModelConfig{
				Encoder: join(model.Encoder()),
				Decoder: join(model.Decoder()),
				Joiner:  join(model.Joiner()),
			},
			Tokens:     join(model.Tokens()),
			NumThreads: min(runtime.NumCPU(), 4),
			Provider:   "cpu",
			// Skips sherpa's metadata probe; TDT decoding is still selected from
			// the encoder's own metadata.
			ModelType: "nemo_transducer",
		},
		DecodingMethod: "greedy_search",
	}

	recognizer := sherpa.NewOfflineRecognizer(&recognizerConfig)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create parakeet recognizer (model: %s); run 'obey-voice models ensure'", model.Name)
	}
	return &ParakeetSTT{cfg: cfg, recognizer: recognizer, vad: vad, capture: capture, model: model}, nil
}

// Start begins a VAD-gated utterance-final session over the shared offline loop.
func (s *ParakeetSTT) Start(ctx context.Context) (Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	preview, _ := ctx.Value(livePreviewKey{}).(bool)
	session := &sherpaSession{cancel: cancel, events: make(chan Event, 8), done: make(chan struct{}), pause: newPauseController("stt"), playback: !preview}

	finite := sourceKind(s.capture) != audio.SourceLive
	deps := offlineLoopDeps{
		frames:     asFrameSource(s.capture),
		seg:        newVADSegmenter(s.vad, preRollSamplesFromMS(s.cfg.VADPreRollMS)),
		policy:     endpoint.FromConfig(s.cfg, finite),
		transcribe: s.transcribe,
	}
	go func() {
		defer session.pause.stop()
		defer close(session.done)
		defer close(session.events)
		deps.pause = session.pause
		if preview {
			runPreviewLoop(sessionCtx, deps, session.events)
		} else {
			runOfflineLoop(sessionCtx, deps, session.events)
		}
	}()
	return session, nil
}

// transcribe decodes finalized speech samples to text. It is the production
// transcribeFunc seam for runOfflineLoop.
func (s *ParakeetSTT) transcribe(samples []float32) (string, error) {
	stream := sherpa.NewOfflineStream(s.recognizer)
	if stream == nil {
		return "", fmt.Errorf("failed to create parakeet stream")
	}
	defer sherpa.DeleteOfflineStream(stream)

	stream.AcceptWaveform(audio.SampleRate, samples)
	s.recognizer.Decode(stream)
	return normalizeTranscript(strings.TrimSpace(stream.GetResult().Text)), nil
}

// DecodeSegment decodes one finite span of speech. It is the SegmentDecoder
// seam: no session, no VAD, no capture, so a file runner can drive the same
// recognizer the live loop uses.
func (s *ParakeetSTT) DecodeSegment(ctx context.Context, samples []float32) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.transcribe(samples)
}

// Available returns true if the recognizer loaded.
func (s *ParakeetSTT) Available() bool { return s.recognizer != nil }

// Delete frees the recognizer.
func (s *ParakeetSTT) Delete() {
	if s.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(s.recognizer)
		s.recognizer = nil
	}
}
