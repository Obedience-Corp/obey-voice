package stt

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// SherpaOfflineSTT implements utterance-final STT using sherpa-onnx whisper.
type SherpaOfflineSTT struct {
	cfg        *config.Config
	recognizer *sherpa.OfflineRecognizer
	vad        *audio.VAD
	capture    audioSource
}

type sherpaSession struct {
	cancel   context.CancelFunc
	events   chan Event
	done     chan struct{}
	pause    *pauseController
	playback bool
}

func (s *sherpaSession) BindCompletionPlayback(ctx context.Context, token PauseToken, attempt audio.PlaybackAttempt, preRoll time.Duration) error {
	if !s.playback || s.pause == nil {
		return ErrCompletionPlaybackUnsupported
	}
	return s.pause.playbackCommand(ctx, &playbackCommand{kind: playbackBind, token: token, attempt: attempt, preRoll: preRoll})
}
func (s *sherpaSession) ConfirmCompletionPlayback(ctx context.Context, token PauseToken, attemptID string) error {
	if !s.playback || s.pause == nil {
		return ErrCompletionPlaybackUnsupported
	}
	return s.pause.playbackCommand(ctx, &playbackCommand{kind: playbackConfirm, token: token, attempt: audio.PlaybackAttempt{ID: attemptID}})
}
func (s *sherpaSession) FinishCompletionPlayback(ctx context.Context, token PauseToken, attemptID string, retain bool) error {
	if !s.playback || s.pause == nil {
		return ErrCompletionPlaybackUnsupported
	}
	return s.pause.playbackCommand(ctx, &playbackCommand{kind: playbackFinish, token: token, attempt: audio.PlaybackAttempt{ID: attemptID}, retain: retain})
}

func (s *sherpaSession) Events() <-chan Event {
	return s.events
}

func (s *sherpaSession) BindCompletionCoordinator(c *work.Coordinator) {
	if s.pause != nil {
		s.pause.BindCompletionCoordinator(c)
	}
}

func (s *sherpaSession) PauseForCompletion(ctx context.Context, scope work.WorkScope, requestID string) (PauseResult, error) {
	if s.pause == nil {
		return PauseResult{}, ErrCompletionPauseUnsupported
	}
	return s.pause.request(ctx, scope, requestID)
}

func (s *sherpaSession) ResumeCapture(ctx context.Context, token PauseToken) error {
	if s.pause == nil {
		return ErrCompletionPauseUnsupported
	}
	return s.pause.resume(ctx, token)
}

func (s *sherpaSession) HasCapturedInput() bool {
	return s.pause != nil && s.pause.hasCapturedInput()
}

func (s *sherpaSession) Close() error {
	if s.pause != nil {
		s.pause.stop()
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.done != nil {
		<-s.done
	}
	return nil
}

// NewSherpaOfflineSTT creates a new sherpa-onnx whisper STT provider.
func NewSherpaOfflineSTT(cfg *config.Config, capture audioSource, vad *audio.VAD) (*SherpaOfflineSTT, error) {
	modelsDir := config.ModelsDirFrom(cfg)
	model, err := config.SherpaOfflineWhisperModel(cfg.WhisperModel)
	if err != nil {
		return nil, err
	}

	suffix := ".onnx"
	if cfg.WhisperQuantized {
		suffix = ".int8.onnx"
	}

	whisperConfig := sherpa.OfflineWhisperModelConfig{
		Encoder:  filepath.Join(modelsDir, fmt.Sprintf("%s-encoder%s", model, suffix)),
		Decoder:  filepath.Join(modelsDir, fmt.Sprintf("%s-decoder%s", model, suffix)),
		Language: config.LanguageCode(cfg.Language), // "en-US" -> "en"
	}

	threads := min(runtime.NumCPU(), 4)
	modelConfig := sherpa.OfflineModelConfig{
		Whisper:    whisperConfig,
		Tokens:     filepath.Join(modelsDir, fmt.Sprintf("%s-tokens.txt", model)),
		NumThreads: threads,
	}

	recognizerConfig := sherpa.OfflineRecognizerConfig{
		ModelConfig: modelConfig,
	}

	recognizer := sherpa.NewOfflineRecognizer(&recognizerConfig)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create whisper recognizer (model: %s)", model)
	}

	return &SherpaOfflineSTT{
		cfg:        cfg,
		recognizer: recognizer,
		vad:        vad,
		capture:    capture,
	}, nil
}

// Start begins an STT session using utterance-final decoding over the typed
// frame contract and the endpoint policy.
func (s *SherpaOfflineSTT) Start(ctx context.Context) (Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &sherpaSession{
		cancel:   cancel,
		events:   make(chan Event, 8),
		done:     make(chan struct{}),
		pause:    newPauseController("stt"),
		playback: true,
	}

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
		runOfflineLoop(sessionCtx, deps, session.events)
	}()
	return session, nil
}

// transcribe decodes finalized speech samples to text via the cgo sherpa
// recognizer. It is the production transcribeFunc seam for runOfflineLoop.
func (s *SherpaOfflineSTT) transcribe(samples []float32) (string, error) {
	stream := sherpa.NewOfflineStream(s.recognizer)
	if stream == nil {
		return "", fmt.Errorf("failed to create whisper stream")
	}
	defer sherpa.DeleteOfflineStream(stream)

	stream.AcceptWaveform(audio.SampleRate, samples)
	s.recognizer.Decode(stream)
	return normalizeTranscript(strings.TrimSpace(stream.GetResult().Text)), nil
}

// Available returns true if the STT provider is ready.
func (s *SherpaOfflineSTT) Available() bool {
	return s.recognizer != nil
}

// Delete frees resources.
func (s *SherpaOfflineSTT) Delete() {
	if s.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(s.recognizer)
	}
}
