package qwen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

type limitedBuffer struct {
	buf   bytes.Buffer
	mu    sync.Mutex
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	return written, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (q *Qwen3TTS) synthesize(ctx context.Context, req tts.SynthesisRequest, stream *audio.PCMStream) {
	runCtx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()
	if q.native {
		if err := q.synthesizeNativeStream(runCtx, req, stream); err != nil {
			stream.CloseWithError(managedWorkerError(err))
			return
		}
		stream.Close()
		return
	}
	if q.managed {
		// Managed path streams float32 PCM over JSONL (no temp WAV required).
		// Model generation is still whole-utterance at the engine layer (stage A).
		if err := q.synthesizeManagedStream(runCtx, req, stream); err != nil {
			stream.CloseWithError(managedWorkerError(err))
			return
		}
		stream.Close()
		return
	}

	tmpDir, err := os.MkdirTemp("", "samantha-qwen3-tts-")
	if err != nil {
		stream.CloseWithError(fmt.Errorf("qwen3-tts: create output directory: %w", err))
		return
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "speech.wav")
	textPath := filepath.Join(tmpDir, "text.txt")

	args, err := q.buildArgs(req, outputPath, textPath)
	if err != nil {
		stream.CloseWithError(err)
		return
	}
	cmd := q.command(runCtx, q.binary, args...)
	if cmd == nil {
		stream.CloseWithError(&tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "start worker", Kind: tts.ProviderErrorWorker, Err: errors.New("command factory returned nil")})
		return
	}
	managedqwen.ConfigureWorkerCommand(cmd)
	var stdout, stderr limitedBuffer
	stdout.limit = maxWorkerOutput
	stderr.limit = maxWorkerOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			stream.CloseWithError(&tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "cancel worker", Kind: tts.ProviderErrorCanceled, Err: ctx.Err()})
			return
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			stream.CloseWithError(&tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "run worker", Kind: tts.ProviderErrorWorker, Err: fmt.Errorf("worker timed out after %s", q.timeout)})
			return
		}
		stream.CloseWithError(&tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "run worker", Kind: tts.ProviderErrorWorker, Err: fmt.Errorf("worker failed: %w%s", err, workerOutputSuffix(stderr.String(), stdout.String()))})
		return
	}
	if err := runCtx.Err(); err != nil {
		kind := tts.ProviderErrorWorker
		if errors.Is(err, context.Canceled) {
			kind = tts.ProviderErrorCanceled
		}
		stream.CloseWithError(&tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "run worker", Kind: kind, Err: err})
		return
	}

	q.streamWorkerWAV(ctx, outputPath, stream)
	if stream.Err() == nil {
		q.speechCompleted.Store(true)
	}
}

func (q *Qwen3TTS) streamWorkerWAV(ctx context.Context, outputPath string, stream *audio.PCMStream) {
	samples, sampleRate, err := audio.ReadWAVFloat32(outputPath)
	if err != nil {
		stream.CloseWithError(&tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "read worker WAV", Kind: tts.ProviderErrorMalformed, Err: err})
		return
	}
	if len(samples) == 0 {
		stream.CloseWithError(&tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate worker WAV", Kind: tts.ProviderErrorMalformed, Err: errors.New("worker returned empty audio")})
		return
	}
	if sampleRate != qwen3TTSSampleRate {
		stream.CloseWithError(&tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate worker WAV", Kind: tts.ProviderErrorMalformed, Err: fmt.Errorf("worker returned %d Hz, want %d Hz", sampleRate, qwen3TTSSampleRate)})
		return
	}
	if duration := time.Duration(float64(len(samples)) / float64(sampleRate) * float64(time.Second)); duration > maxQwenAudioDuration {
		stream.CloseWithError(&tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate worker WAV", Kind: tts.ProviderErrorMalformed, Err: fmt.Errorf("worker returned %s of audio, maximum is %s", duration.Round(time.Second), maxQwenAudioDuration)})
		return
	}
	if err := stream.SetSampleRate(sampleRate); err != nil {
		stream.CloseWithError(err)
		return
	}

	const chunkSize = 2048
	for start := 0; start < len(samples); start += chunkSize {
		if err := ctx.Err(); err != nil {
			stream.CloseWithError(err)
			return
		}
		end := min(start+chunkSize, len(samples))
		if err := stream.Write(samples[start:end]); err != nil {
			stream.CloseWithError(err)
			return
		}
	}
	stream.Close()
}

// buildArgs is the one-shot argument seam used by the external compatibility
// adapter and managed-worker fixtures. Live managed synthesis uses the JSONL
// session so the model remains loaded.
func (q *Qwen3TTS) buildArgs(req tts.SynthesisRequest, outputPath string, textPaths ...string) ([]string, error) {
	if q.managed {
		textPath := ""
		if len(textPaths) > 0 {
			textPath = textPaths[0]
		}
		if strings.TrimSpace(textPath) == "" {
			return nil, errors.New("qwen3-tts: managed worker requires a text file")
		}
		if err := tts.ValidateRequest(q.Capabilities(), req); err != nil {
			return nil, err
		}
		args := []string{
			q.workerScript, "synthesize",
			"--model", q.model,
			"--text-file", textPath,
			"--output", outputPath,
			"--speaker", req.Voice,
			"--language", req.Language,
		}
		if strings.TrimSpace(req.Instruction) != "" {
			args = append(args, "--instruction", req.Instruction)
		}
		return args, nil
	}
	if req.Mode != "" && req.Mode != tts.VoiceModeStatic {
		return nil, qwenUnsupported("voice mode", fmt.Sprintf("mode %q is not verified by the native CLI", req.Mode))
	}
	if strings.TrimSpace(req.Voice) != "" && !strings.EqualFold(req.Voice, "default") {
		return nil, qwenUnsupported("voice selection", fmt.Sprintf("voice %q is unsupported by the native CLI", req.Voice))
	}
	if req.Language != "" || req.Instruction != "" || req.ReferenceAudio != "" || req.ReferenceTranscript != "" {
		return nil, qwenUnsupported("voice controls", "the verified native CLI exposes no voice-mode flags")
	}
	return []string{"-m", q.model, "-t", req.Text, "-o", outputPath}, nil
}

func workerOutputSuffix(stderr, stdout string) string {
	if detail := strings.TrimSpace(stderr); detail != "" {
		return ": " + detail
	}
	if detail := strings.TrimSpace(stdout); detail != "" {
		return ": " + detail
	}
	return ""
}
