package stt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

// transcribeCPPBackend is the compute backend the CLI selects per utterance.
// auto is safe here only because construction already refused a machine with no
// GPU lane, so the fallback auto would take is known to exist.
const transcribeCPPBackend = "auto"

// TranscribeCPPSTT implements utterance-final STT through the transcribe.cpp
// CLI (transcribe-cli) running Parakeet TDT as GGUF. It is optional: the
// default provider remains sherpa-parakeet.
type TranscribeCPPSTT struct {
	cfg       *config.Config
	binary    string
	modelPath string
	lanes     []config.ComputeLane
	vad       *audio.VAD
	capture   audioSource
}

// NewTranscribeCPPSTT creates a transcribe.cpp STT provider. The CLI, the GGUF
// and the machine's compute lanes are all resolved at construction so a
// misconfigured or unsuitable install fails before a turn starts rather than
// mid-conversation.
func NewTranscribeCPPSTT(cfg *config.Config, capture audioSource, vad *audio.VAD) (*TranscribeCPPSTT, error) {
	modelsDir := config.ModelsDirFrom(cfg)
	binaryPath, err := config.ResolveTranscribeCPPBinary(cfg, modelsDir, nil)
	if err != nil {
		return nil, err
	}

	asset, err := config.TranscribeCPPModel(cfg.TranscribeCPPModel)
	if err != nil {
		return nil, err
	}
	modelPath := asset.ModelPath(modelsDir)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("transcribe.cpp model not found at %s; run 'obey-voice models ensure' to download it", modelPath)
	}

	// Fail closed on the backend: --backend auto would fall back to CPU, where
	// this provider loses to the default it replaced.
	lanes, err := config.TranscribeCPPLanes(context.Background(), binaryPath)
	if err != nil {
		return nil, err
	}
	if len(config.GPULanes(lanes)) == 0 {
		return nil, config.TranscribeCPPCPUOnlyError(lanes)
	}

	return &TranscribeCPPSTT{
		cfg:       cfg,
		binary:    binaryPath,
		modelPath: modelPath,
		lanes:     lanes,
		vad:       vad,
		capture:   capture,
	}, nil
}

// Start begins a transcribe.cpp STT session on the shared utterance-final loop
// (runOfflineLoop) with the CLI as the transcribe seam.
func (t *TranscribeCPPSTT) Start(ctx context.Context) (Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &sherpaSession{
		cancel: cancel,
		events: make(chan Event, 8),
	}

	finite := sourceKind(t.capture) != audio.SourceLive
	deps := offlineLoopDeps{
		frames: asFrameSource(t.capture),
		seg:    newVADSegmenter(t.vad, preRollSamplesFromMS(t.cfg.VADPreRollMS)),
		policy: endpoint.FromConfig(t.cfg, finite),
		transcribe: func(samples []float32) (string, error) {
			return t.transcribe(sessionCtx, samples)
		},
	}

	go func() {
		defer close(session.events)
		runOfflineLoop(sessionCtx, deps, session.events)
	}()
	return session, nil
}

// transcribe writes one utterance to a temp 16 kHz wav — the only rate the CLI
// accepts — and reads the transcript back from the CLI's -o file rather than
// parsing its stdout segment lines.
func (t *TranscribeCPPSTT) transcribe(ctx context.Context, samples []float32) (string, error) {
	tempDir, err := os.MkdirTemp("", "obey-voice-transcribecpp-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)

	audioPath := filepath.Join(tempDir, "input.wav")
	outputPath := filepath.Join(tempDir, "transcript.txt")

	if err := audio.WriteWAVFloat32(audioPath, audio.SampleRate, samples); err != nil {
		return "", fmt.Errorf("write temp wav: %w", err)
	}

	args := []string{
		"-q",
		"-m", t.modelPath,
		"--backend", transcribeCPPBackend,
		"-o", outputPath,
		audioPath,
	}

	cmd := exec.CommandContext(ctx, t.binary, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("transcribe.cpp: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("read transcribe.cpp transcript: %w", err)
	}
	return normalizeTranscript(strings.TrimSpace(string(data))), nil
}

// Available returns true if the CLI and the GGUF are still in place.
func (t *TranscribeCPPSTT) Available() bool {
	_, err := os.Stat(t.modelPath)
	return err == nil && t.binary != ""
}
